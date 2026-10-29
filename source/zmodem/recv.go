package zmodem

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// uniquePath returns dir/name, adding " (n)" before the extension if the
// file already exists.
func uniquePath(dir, name string) string {
	p := filepath.Join(dir, name)
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(p); err != nil {
			return p
		}
	}
	return p
}

func cleanName(raw string) string {
	raw = strings.ReplaceAll(raw, "\\", "/")
	if i := strings.LastIndexByte(raw, '/'); i >= 0 {
		raw = raw[i+1:]
	}
	raw = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"|?*`, r) {
			return '_'
		}
		return r
	}, raw)
	raw = strings.Trim(raw, " .")
	if raw == "" {
		return ""
	}
	return raw
}

func parseFileInfo(data []byte) (name string, size int64, mtime int64) {
	nul := -1
	for i, b := range data {
		if b == 0 {
			nul = i
			break
		}
	}
	if nul < 0 {
		return string(data), 0, 0
	}
	name = string(data[:nul])
	fields := strings.Fields(string(data[nul+1:]))
	if len(fields) > 0 {
		size, _ = strconv.ParseInt(fields[0], 10, 64)
	}
	if len(fields) > 1 {
		mtime, _ = strconv.ParseInt(fields[1], 8, 64)
	}
	return
}

// Receive accepts files from the sender into dir. prefix holds bytes
// already pulled off the link (the sender's initial ZRQINIT).
func Receive(link Link, prefix []byte, dir string, hooks *Hooks) (Result, error) {
	rd := newReader(link, prefix, hooks)
	res := Result{}
	finish := func(err error) (Result, error) {
		res.Left = rd.leftover()
		return res, err
	}
	hex := func(typ int, d [4]byte) { link.Write(encodeHex(typ, d)) }
	zrinit := func() {
		hex(ZRINIT, [4]byte{0, 0, 0, CANFDX | CANOVIO | CANFC32})
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return finish(err)
	}

	zrinit()
	fileNo := 0
	idle := 0
	for {
		h, err := rd.header(hdrTimeout)
		if err == ErrCancelled {
			link.Write(cancelSeq)
			return finish(ErrCancelled)
		}
		if err != nil {
			if err == ErrTimeout && idle < maxAttempts {
				idle++
				zrinit()
				continue
			}
			return finish(err)
		}
		idle = 0
		switch h.Type {
		case ZRQINIT:
			zrinit()
		case ZSINIT:
			_, _, ok, e := rd.subpacket(h.Fmt == ZBIN32, time.Now().Add(hdrTimeout), 4096)
			if e != nil {
				return finish(e)
			}
			if ok {
				hex(ZACK, [4]byte{})
			} else {
				hex(ZNAK, [4]byte{})
			}
		case ZCHALLENGE:
			hex(ZACK, h.Data)
		case ZFREECNT:
			hex(ZACK, [4]byte{0xFF, 0xFF, 0xFF, 0x7F})
		case ZCOMMAND:
			rd.subpacket(h.Fmt == ZBIN32, time.Now().Add(hdrTimeout), 4096)
			hex(ZCOMPL, [4]byte{1})
		case ZFILE:
			fileNo++
			path, err := receiveFile(link, rd, h, dir, hooks, fileNo, hex, zrinit)
			if err != nil {
				if err == ErrCancelled {
					link.Write(cancelSeq)
				}
				return finish(err)
			}
			if path != "" {
				res.Files = append(res.Files, path)
			}
		case ZFIN:
			hex(ZFIN, [4]byte{})
			// swallow the sender's "OO"
			for i := 0; i < 2; i++ {
				link.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
				bs, e := rd.br.Peek(1)
				if e != nil || len(bs) == 0 || bs[0] != 'O' {
					break
				}
				rd.br.ReadByte()
			}
			link.SetReadDeadline(time.Time{})
			return finish(nil)
		case ZABORT, ZCAN:
			return finish(fmt.Errorf("%w: sender aborted", ErrProtocol))
		default:
			zrinit()
		}
	}
}

func receiveFile(link Link, rd *reader, zf Header, dir string, hooks *Hooks, fileNo int,
	hex func(int, [4]byte), zrinit func()) (string, error) {

	use32 := zf.Fmt == ZBIN32
	data, _, ok, err := rd.subpacket(use32, time.Now().Add(hdrTimeout), 4096)
	if err != nil {
		return "", err
	}
	if !ok {
		hex(ZNAK, [4]byte{})
		return "", nil
	}
	rawName, size, mtime := parseFileInfo(data)
	name := cleanName(rawName)
	if name == "" {
		hex(ZSKIP, [4]byte{})
		return "", nil
	}
	path := uniquePath(dir, name)
	out, err := os.Create(path)
	if err != nil {
		hex(ZSKIP, [4]byte{})
		return "", err
	}
	closed := false
	closeOut := func() {
		if !closed {
			out.Close()
			closed = true
		}
	}
	defer closeOut()

	var pos int64
	hex(ZRPOS, posData(0))
	retries := 0
	report := func() {
		hooks.progress(Progress{Name: filepath.Base(path), Done: pos, Total: size, FileIndex: fileNo})
	}
	report()

	for {
		h, err := rd.header(hdrTimeout)
		if err != nil {
			if err == ErrTimeout && retries < maxAttempts {
				retries++
				hex(ZRPOS, posData(pos))
				continue
			}
			return "", err
		}
		switch h.Type {
		case ZDATA:
			if h.Pos() != pos {
				hex(ZRPOS, posData(pos))
				continue
			}
			retries = 0
			d32 := h.Fmt == ZBIN32
		frame:
			for {
				chunk, end, ok, err := rd.subpacket(d32, time.Now().Add(hdrTimeout), 16384)
				if err != nil {
					if err == ErrTimeout {
						hex(ZRPOS, posData(pos))
						break frame
					}
					return "", err
				}
				if !ok {
					hex(ZRPOS, posData(pos))
					break frame
				}
				if _, werr := out.Write(chunk); werr != nil {
					hex(ZFERR, posData(pos))
					return "", werr
				}
				pos += int64(len(chunk))
				report()
				switch end {
				case ZCRCW:
					hex(ZACK, posData(pos))
					break frame
				case ZCRCQ:
					hex(ZACK, posData(pos))
				case ZCRCE:
					break frame
				}
			}
		case ZEOF:
			if h.Pos() != pos {
				continue
			}
			closeOut()
			if mtime > 0 {
				t := time.Unix(mtime, 0)
				os.Chtimes(path, t, t)
			}
			zrinit()
			return path, nil
		case ZFILE:
			// sender missed our ZRPOS: swallow its info packet and repeat
			rd.subpacket(h.Fmt == ZBIN32, time.Now().Add(hdrTimeout), 4096)
			hex(ZRPOS, posData(pos))
		case ZFIN:
			hex(ZFIN, [4]byte{})
			return "", fmt.Errorf("%w: sender ended the session early", ErrProtocol)
		case ZABORT, ZCAN:
			return "", fmt.Errorf("%w: sender aborted", ErrProtocol)
		}
	}
}
