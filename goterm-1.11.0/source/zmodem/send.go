package zmodem

import (
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"time"
)

const (
	blockSize   = 1024
	syncEvery   = 16 // blocks between ACK sync points (ZCRCQ)
	hdrTimeout  = 10 * time.Second
	ackTimeout  = 60 * time.Second
	maxAttempts = 8
)

type sender struct {
	link  Link
	rd    *reader
	hooks *Hooks
	use32 bool
}

func (s *sender) hex(typ int, d [4]byte) { s.link.Write(encodeHex(typ, d)) }

func (s *sender) cancel() {
	s.link.Write(cancelSeq)
}

// Result of a transfer.
type Result struct {
	Files []string // paths written (receive) or sent (send)
	Left  []byte   // bytes read from the link but not part of the transfer
}

// Send transmits files. prefix contains bytes already read from the link
// (for example the ZRINIT header the caller used to detect the host being
// ready). Pass nil if nothing has been read yet.
func Send(link Link, prefix []byte, paths []string, hooks *Hooks) (Result, error) {
	s := &sender{link: link, rd: newReader(link, prefix, hooks), hooks: hooks}
	res := Result{}
	finish := func(err error) (Result, error) {
		res.Left = s.rd.leftover()
		return res, err
	}

	// Wait for the receiver's ZRINIT, poking it with ZRQINIT if needed.
	var h Header
	var err error
	for attempt := 0; ; attempt++ {
		wait := hdrTimeout
		if len(prefix) == 0 && attempt == 0 {
			wait = 3 * time.Second // let a ready receiver announce itself first
		}
		h, err = s.rd.header(wait)
		if err == nil && h.Type == ZRINIT {
			break
		}
		if err == ErrCancelled {
			s.cancel()
			return finish(ErrCancelled)
		}
		if err == nil && h.Type == ZCHALLENGE {
			s.hex(ZACK, h.Data)
			continue
		}
		if attempt >= maxAttempts {
			if err == nil {
				err = fmt.Errorf("%w: expected ZRINIT, got %s", ErrProtocol, FrameName(h.Type))
			}
			return finish(err)
		}
		if err != nil && err != ErrTimeout {
			return finish(err)
		}
		s.hex(ZRQINIT, [4]byte{})
	}
	s.use32 = h.Data[3]&CANFC32 != 0

	var totalBytes int64
	for _, p := range paths {
		if st, e := os.Stat(p); e == nil {
			totalBytes += st.Size()
		}
	}
	remaining := totalBytes
	for i, p := range paths {
		if hooks.aborted() {
			s.cancel()
			return finish(ErrCancelled)
		}
		sent, err := s.sendFile(p, i+1, len(paths), len(paths)-i, remaining)
		if err != nil {
			if err == ErrCancelled {
				s.cancel()
			}
			return finish(err)
		}
		if sent {
			res.Files = append(res.Files, p)
		}
		if st, e := os.Stat(p); e == nil {
			remaining -= st.Size()
		}
	}

	// Session end.
	fin := [4]byte{}
	for attempt := 0; attempt < 3; attempt++ {
		s.hex(ZFIN, fin)
		h, err = s.rd.header(5 * time.Second)
		if err == nil && h.Type == ZFIN {
			s.link.Write([]byte("OO"))
			break
		}
		if err == ErrCancelled {
			return finish(ErrCancelled)
		}
	}
	return finish(nil)
}

// sendFile sends one file; sent is false if the receiver skipped it.
func (s *sender) sendFile(path string, idx, count, remFiles int, remBytes int64) (sent bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := st.Size()
	name := filepath.Base(path)
	info := fmt.Sprintf("%s\x00%d %o 100644 0 %d %d", name, size, st.ModTime().Unix(), remFiles, remBytes)

	sendZFILE := func() {
		fh := [4]byte{0, 0, 0, 1} // ZF0 = ZCBIN
		s.link.Write(encodeBin(ZFILE, fh, s.use32))
		e := &escaper{}
		appendSubpacket(e, []byte(info), ZCRCW, s.use32)
		s.link.Write(e.out)
	}

	var pos int64
	started := false
	for attempt := 0; attempt < maxAttempts && !started; attempt++ {
		sendZFILE()
		for {
			h, e := s.rd.header(5 * time.Second)
			if e == ErrCancelled {
				return false, ErrCancelled
			}
			if e != nil {
				if e == ErrTimeout {
					break // resend ZFILE
				}
				return false, e
			}
			switch h.Type {
			case ZRPOS:
				pos = h.Pos()
				started = true
			case ZSKIP:
				return false, nil
			case ZCRC:
				var c uint32
				if _, e := f.Seek(0, io.SeekStart); e == nil {
					c = crc32FileLimit(f, h.Pos(), size)
				}
				s.hex(ZCRC, [4]byte{byte(c), byte(c >> 8), byte(c >> 16), byte(c >> 24)})
				continue
			case ZRINIT:
				continue // stale / repeated ZRINIT: keep waiting for the answer to ZFILE
			case ZNAK:
				// receiver wants the ZFILE again
			case ZABORT, ZFERR, ZCAN:
				return false, fmt.Errorf("%w: receiver aborted (%s)", ErrProtocol, FrameName(h.Type))
			default:
				continue
			}
			break
		}
	}
	if !started {
		return false, ErrTimeout
	}

	buf := make([]byte, blockSize)
	for { // one ZDATA frame per iteration
		if pos > size {
			pos = size
		}
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return false, err
		}
		s.link.Write(encodeBin(ZDATA, posData(pos), s.use32))
		sinceSync := 0
		reposition := false
		for {
			if s.hooks.aborted() {
				return false, ErrCancelled
			}
			n, rerr := io.ReadFull(f, buf)
			if rerr != nil && rerr != io.EOF && rerr != io.ErrUnexpectedEOF {
				return false, rerr
			}
			last := pos+int64(n) >= size
			end := byte(ZCRCG)
			switch {
			case last:
				end = ZCRCE
			case sinceSync+1 >= syncEvery:
				end = ZCRCQ
			}
			e := &escaper{}
			appendSubpacket(e, buf[:n], end, s.use32)
			if _, err := s.link.Write(e.out); err != nil {
				return false, err
			}
			pos += int64(n)
			sinceSync++
			s.hooks.progress(Progress{Name: filepath.Base(path), Done: pos, Total: size, FileIndex: idx, FileCount: count, Sending: true})
			if end == ZCRCQ {
				sinceSync = 0
				h, e := s.rd.header(ackTimeout)
				if e != nil {
					return false, e
				}
				switch h.Type {
				case ZACK:
				case ZRPOS:
					pos = h.Pos()
					reposition = true
				case ZSKIP:
					return false, nil
				case ZABORT, ZFERR, ZCAN:
					return false, fmt.Errorf("%w: receiver aborted (%s)", ErrProtocol, FrameName(h.Type))
				}
				if reposition {
					break
				}
			}
			if last {
				break
			}
		}
		if reposition {
			continue
		}
		// end of file
		for attempt := 0; attempt < maxAttempts; attempt++ {
			s.link.Write(encodeBin(ZEOF, posData(size), s.use32))
			h, e := s.rd.header(hdrTimeout)
			if e == ErrCancelled {
				return false, ErrCancelled
			}
			if e != nil {
				if e == ErrTimeout {
					continue
				}
				return false, e
			}
			switch h.Type {
			case ZRINIT:
				return true, nil
			case ZRPOS:
				pos = h.Pos()
				reposition = true
			case ZSKIP:
				return true, nil
			}
			if reposition {
				break
			}
		}
		if !reposition {
			return false, ErrTimeout
		}
	}
}

// crc32FileLimit computes the CRC-32 of the first n bytes of f (the
// receiver uses this to decide whether to resume).
func crc32FileLimit(f *os.File, n, size int64) uint32 {
	if n <= 0 || n > size {
		n = size
	}
	h := crc32.NewIEEE()
	io.Copy(h, io.LimitReader(f, n))
	return h.Sum32()
}
