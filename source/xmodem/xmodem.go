// Package xmodem implements XMODEM, XMODEM-CRC, XMODEM-1K, YMODEM and
// YMODEM-G transfers. It is a Go port of SyncTERM's sbbs3/xmodem.c
// (get_block/put_block/get_ack/get_mode/put_eot/send_file) plus the
// receive loop that SyncTERM drives from its transfer worker.
package xmodem

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SOH = 0x01
	STX = 0x02
	EOT = 0x04
	ACK = 0x06
	NAK = 0x15
	CAN = 0x18
	CPM = 0x1A

	minBlock = 128
	maxBlock = 1024

	maxErrors   = 9
	byteTimeout = 3 * time.Second
	ackTimeout  = 10 * time.Second
	recvTimeout = 10 * time.Second
)

// Mode selects the protocol variant.
type Mode int

const (
	XmodemChecksum Mode = iota // 128-byte blocks, 8-bit checksum
	XmodemCRC                  // 128-byte blocks, CRC-16
	Xmodem1K                   // 1024-byte blocks, CRC-16
	Ymodem                     // batch, 1024-byte blocks, CRC-16
	YmodemG                    // batch, streaming, no per-block ACK
)

func (m Mode) String() string {
	return [...]string{"XMODEM", "XMODEM-CRC", "XMODEM-1K", "YMODEM", "YMODEM-G"}[m]
}

func (m Mode) Batch() bool { return m.batch() }

func (m Mode) batch() bool { return m == Ymodem || m == YmodemG }

var (
	ErrCancelled = errors.New("xmodem: transfer cancelled")
	ErrTimeout   = errors.New("xmodem: timed out waiting for peer")
	ErrProtocol  = errors.New("xmodem: protocol error")
)

type Link interface {
	io.Reader
	io.Writer
	SetReadDeadline(t time.Time) error
}

type Progress struct {
	Name      string
	Done      int64
	Total     int64
	FileIndex int
	FileCount int
	Sending   bool
}

type Hooks struct {
	Abort    func() bool
	Progress func(Progress)
	Log      func(string)
}

func (h *Hooks) aborted() bool { return h != nil && h.Abort != nil && h.Abort() }
func (h *Hooks) logf(f string, a ...interface{}) {
	if h != nil && h.Log != nil {
		h.Log("xmodem " + fmt.Sprintf(f, a...))
	}
}

type Result struct {
	Files []string
	Left  []byte
}

// ---------------------------------------------------------------- CRC

func crc16(crc uint16, b byte) uint16 {
	crc ^= uint16(b) << 8
	for i := 0; i < 8; i++ {
		if crc&0x8000 != 0 {
			crc = crc<<1 ^ 0x1021
		} else {
			crc <<= 1
		}
	}
	return crc
}

// ---------------------------------------------------------------- engine

type xm struct {
	link      Link
	br        *bufio.Reader
	hooks     *Hooks
	mode      Mode
	crc       bool // CRC in use for the current file
	g         bool
	blockSize int
	cancelled bool
}

func newXM(l Link, mode Mode, h *Hooks) *xm {
	return &xm{link: l, br: bufio.NewReaderSize(l, 8192), hooks: h, mode: mode, blockSize: maxBlock}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// getcom returns a byte, or -1 on timeout. A non-timeout error or an abort
// is returned as err.
func (x *xm) getcom(d time.Duration) (int, error) {
	deadline := time.Now().Add(d)
	for {
		if x.hooks.aborted() {
			return -1, ErrCancelled
		}
		slice := time.Now().Add(250 * time.Millisecond)
		if slice.After(deadline) {
			slice = deadline
		}
		x.link.SetReadDeadline(slice)
		b, err := x.br.ReadByte()
		if err == nil {
			return int(b), nil
		}
		if isTimeout(err) {
			if !time.Now().Before(deadline) {
				return -1, nil
			}
			continue
		}
		return -1, err
	}
}

func (x *xm) putcom(b ...byte) error {
	_, err := x.link.Write(b)
	return err
}

func (x *xm) flushIn() {
	for x.br.Buffered() > 0 {
		x.br.ReadByte()
	}
	for {
		c, err := x.getcom(0)
		if c < 0 || err != nil {
			return
		}
	}
}

func (x *xm) cancel() {
	if x.cancelled {
		return
	}
	x.cancelled = true
	x.putcom(CAN, CAN, CAN, CAN, CAN, CAN, CAN, CAN, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8)
}

func (x *xm) putBlock(block []byte, size int, num int) error {
	out := make([]byte, 0, size+5)
	if size == minBlock {
		out = append(out, SOH)
	} else {
		out = append(out, STX)
	}
	out = append(out, byte(num), ^byte(num))
	out = append(out, block[:size]...)
	if x.crc {
		var c uint16
		for _, b := range block[:size] {
			c = crc16(c, b)
		}
		out = append(out, byte(c>>8), byte(c))
	} else {
		var s byte
		for _, b := range block[:size] {
			s += b
		}
		out = append(out, s)
	}
	return x.putcom(out...)
}

// getAck follows xmodem_get_ack. It returns the byte that ended the wait.
func (x *xm) getAck(tries int) (int, error) {
	i := -1
	can := false
	for errs := 0; errs < tries; {
		if x.g {
			c, err := x.getcom(0)
			if err != nil {
				return -1, err
			}
			if c == CAN {
				x.cancel()
				return CAN, nil
			}
			return ACK, nil
		}
		var err error
		i, err = x.getcom(ackTimeout)
		if err != nil {
			return -1, err
		}
		if can && i != CAN {
			can = false
		}
		if i == ACK {
			break
		}
		if i == CAN {
			if can {
				x.cancel()
				return CAN, nil
			}
			can = true
		}
		if i >= 0 {
			if i != CAN {
				return i, nil
			}
		}
		if i != CAN {
			errs++
		}
	}
	return i, nil
}

// getMode follows xmodem_get_mode: wait for the receiver's NAK / 'C' / 'G'.
func (x *xm) getMode() error {
	can := false
	for errs := 0; errs <= maxErrors; errs++ {
		i, err := x.getcom(recvTimeout)
		if err != nil {
			return err
		}
		if can && i != CAN {
			can = false
		}
		switch i {
		case NAK:
			x.crc, x.g = false, false
			x.hooks.logf("receiver requested checksum")
			return nil
		case 'C':
			x.crc, x.g = true, false
			x.hooks.logf("receiver requested CRC")
			return nil
		case 'G':
			if x.mode != YmodemG {
				continue
			}
			x.crc, x.g = true, true
			x.hooks.logf("receiver requested streaming")
			return nil
		case CAN:
			if can {
				return fmt.Errorf("%w: cancelled by receiver", ErrProtocol)
			}
			can = true
		}
	}
	return ErrTimeout
}

func (x *xm) putEOT() error {
	cans := 0
	for errs := 0; errs <= maxErrors; errs++ {
		x.flushIn()
		x.putcom(EOT)
		ch, err := x.getcom(recvTimeout)
		if err != nil {
			return err
		}
		if ch < 0 {
			continue
		}
		x.hooks.logf("EOT reply %02X", ch)
		if ch == ACK {
			return nil
		}
		if ch == CAN {
			cans++
			if cans > 1 {
				break
			}
		}
	}
	return fmt.Errorf("%w: EOT not acknowledged", ErrProtocol)
}

// ---------------------------------------------------------------- send

// Send transmits files. XMODEM modes send only the first file.
func Send(l Link, mode Mode, paths []string, hooks *Hooks) (Result, error) {
	x := newXM(l, mode, hooks)
	res := Result{}
	finish := func(err error) (Result, error) {
		if err != nil {
			x.cancel()
		}
		res.Left = leftover(x)
		return res, err
	}
	if len(paths) == 0 {
		return finish(fmt.Errorf("%w: no files", ErrProtocol))
	}
	if !mode.batch() {
		paths = paths[:1]
	}
	var totalBytes int64
	sizes := make([]int64, len(paths))
	for i, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return finish(err)
		}
		sizes[i] = st.Size()
		totalBytes += st.Size()
	}
	var sentBytes int64
	for fi, p := range paths {
		if err := x.sendFile(p, sizes[fi], fi, len(paths), len(paths)-fi, totalBytes-sentBytes); err != nil {
			return finish(err)
		}
		sentBytes += sizes[fi]
		res.Files = append(res.Files, p)
	}
	if mode.batch() {
		// empty header block ends the batch
		if err := x.getMode(); err != nil {
			return finish(err)
		}
		block := make([]byte, minBlock)
		for try := 0; try < 3; try++ {
			x.putBlock(block, minBlock, 0)
			a, err := x.getAck(1)
			if err != nil {
				return finish(err)
			}
			if a == ACK {
				break
			}
		}
	}
	res.Left = leftover(x)
	return res, nil
}

func leftover(x *xm) []byte {
	n := x.br.Buffered()
	if n == 0 {
		return nil
	}
	b, _ := x.br.Peek(n)
	return append([]byte(nil), b...)
}

func (x *xm) sendFile(path string, size int64, idx, count, filesLeft int, bytesLeft int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	name := filepath.Base(path)
	report := func(done int64) {
		if x.hooks != nil && x.hooks.Progress != nil {
			x.hooks.Progress(Progress{Name: name, Done: done, Total: size, FileIndex: idx + 1, FileCount: count, Sending: true})
		}
	}
	report(0)

	sentHeader := false
	if x.mode.batch() {
		if err := x.getMode(); err != nil {
			return err
		}
		block := make([]byte, maxBlock)
		n := copy(block, name)
		info := fmt.Sprintf("%d %o 0 0 %d %d", size, st.ModTime().Unix(), filesLeft, bytesLeft)
		n += 1 + copy(block[n+1:], info)
		bs := minBlock
		if n+1 > minBlock {
			bs = maxBlock
		}
		ok := false
		for errs := 0; errs <= maxErrors; errs++ {
			x.flushIn()
			x.putBlock(block, bs, 0)
			a, err := x.getAck(1)
			if err != nil {
				return err
			}
			if a == ACK {
				ok = true
				sentHeader = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: header block not acknowledged", ErrProtocol)
		}
	}
	if err := x.getMode(); err != nil {
		return err
	}
	switch x.mode {
	case XmodemChecksum, XmodemCRC:
		x.blockSize = minBlock
	default:
		x.blockSize = maxBlock
	}
	if x.mode == XmodemChecksum {
		x.crc = false
	}
	_ = sentHeader

	var sent int64
	num := 1
	errs := 0
	block := make([]byte, maxBlock)
	for sent < size && errs <= maxErrors {
		if x.hooks.aborted() {
			return ErrCancelled
		}
		for i := range block {
			block[i] = CPM
		}
		if x.blockSize > minBlock && x.mode != Xmodem1K && !x.mode.batch() {
			x.blockSize = minBlock
		}
		if x.blockSize > minBlock && sent+int64(x.blockSize)-minBlock >= size && !x.mode.batch() {
			x.blockSize = minBlock
		}
		f.Seek(sent, io.SeekStart)
		n, rerr := io.ReadFull(f, block[:x.blockSize])
		if rerr != nil && rerr != io.ErrUnexpectedEOF && rerr != io.EOF {
			return rerr
		}
		if err := x.putBlock(block, x.blockSize, num); err != nil {
			return err
		}
		a, err := x.getAck(5)
		if err != nil {
			return err
		}
		if a != ACK {
			errs++
			x.hooks.logf("block %d: error #%d (got %d)", num, errs, a)
			if a == CAN {
				return fmt.Errorf("%w: cancelled by receiver", ErrProtocol)
			}
			if errs == 3 && num == 1 && x.blockSize > minBlock {
				x.blockSize = minBlock
			}
			x.flushIn()
			continue
		}
		num++
		sent += int64(n)
		report(sent)
	}
	if sent < size {
		return fmt.Errorf("%w: too many errors", ErrProtocol)
	}
	return x.putEOT()
}

// ---------------------------------------------------------------- receive

const (
	blkOK = iota
	blkEOT
	blkCAN
	blkTimeout
	blkFail
	blkNotX
	blkNotY
)

func (x *xm) putNak(num int) {
	x.flushIn()
	if num <= 1 {
		switch {
		case x.g:
			x.putcom('G')
		case x.crc:
			x.putcom('C')
		default:
			x.putcom(NAK)
		}
		return
	}
	x.putcom(NAK)
}

// getBlock follows xmodem_get_block.
func (x *xm) getBlock(buf []byte, expected int) (int, int, error) {
	eot, can := false, false
	for errs := 0; errs <= maxErrors; errs++ {
		to := 10 * time.Second
		if expected <= 1 {
			to = 3 * time.Second
		}
		c, err := x.getcom(to)
		if err != nil {
			return blkFail, 0, err
		}
		if eot && c != EOT && c >= 0 {
			eot = false
		}
		if can && c != CAN {
			can = false
		}
		size := 0
		switch c {
		case SOH:
			size = minBlock
		case STX:
			size = maxBlock
		case EOT:
			if !x.g && !eot {
				eot = true
				x.putNak(expected)
				continue
			}
			return blkEOT, 0, nil
		case CAN:
			if !can {
				can = true
				continue
			}
			return blkCAN, 0, nil
		default:
			if eot {
				return blkEOT, 0, nil
			}
			return blkTimeout, 0, nil
		}
		hdr := make([]byte, 2)
		if err := x.readFull(hdr, byteTimeout); err != nil {
			if err == errShort {
				continue
			}
			return blkFail, 0, err
		}
		trailer := 1
		if x.crc {
			trailer = 2
		}
		body := make([]byte, size+trailer)
		if err := x.readFull(body, byteTimeout); err != nil {
			if err == errShort {
				continue
			}
			return blkFail, 0, err
		}
		if hdr[0] != ^hdr[1] {
			x.hooks.logf("block %d: number bit error", expected)
			continue
		}
		data := body[:size]
		if x.crc {
			var cc uint16
			for _, b := range data {
				cc = crc16(cc, b)
			}
			if cc != uint16(body[size])<<8|uint16(body[size+1]) {
				x.hooks.logf("block %d: CRC error", hdr[0])
				continue
			}
		} else {
			var s byte
			for _, b := range data {
				s += b
			}
			if s != body[size] {
				x.hooks.logf("block %d: checksum error", hdr[0])
				continue
			}
		}
		if hdr[0] != byte(expected) {
			if expected == 1 && hdr[0] == 0 && !x.mode.batch() {
				return blkNotX, 0, nil
			}
			if expected == 0 && hdr[0] == 1 {
				return blkNotY, 0, nil
			}
			if expected > 0 && hdr[0] == byte(expected-1) {
				continue // repeated packet
			}
			continue
		}
		copy(buf, data)
		return blkOK, size, nil
	}
	return blkFail, 0, nil
}

var errShort = errors.New("short")

func (x *xm) readFull(p []byte, per time.Duration) error {
	for i := range p {
		c, err := x.getcom(per)
		if err != nil {
			return err
		}
		if c < 0 {
			return errShort
		}
		p[i] = byte(c)
	}
	return nil
}

// Receive downloads files into dir. For plain XMODEM, name is the file name
// to store the data under.
func Receive(l Link, mode Mode, dir, name string, hooks *Hooks) (Result, error) {
	x := newXM(l, mode, hooks)
	res := Result{}
	finish := func(err error) (Result, error) {
		if err != nil {
			x.cancel()
		}
		res.Left = leftover(x)
		return res, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return finish(err)
	}
	x.crc = mode != XmodemChecksum
	x.g = mode == YmodemG
	buf := make([]byte, maxBlock)
	fileNo := 0

	for {
		var size int64 = -1
		var mtime int64
		fname := name
		if mode.batch() {
			// header block 0
			var got bool
			for tries := 0; tries <= maxErrors && !got; tries++ {
				if x.hooks.aborted() {
					return finish(ErrCancelled)
				}
				x.putNak(0)
				r, _, err := x.getBlock(buf, 0)
				if err != nil {
					return finish(err)
				}
				switch r {
				case blkOK:
					got = true
				case blkCAN:
					return finish(fmt.Errorf("%w: cancelled by sender", ErrProtocol))
				case blkEOT:
					x.putcom(ACK)
				}
			}
			if !got {
				return finish(ErrTimeout)
			}
			if buf[0] == 0 { // end of batch
				x.putcom(ACK)
				return finish(nil)
			}
			x.putcom(ACK)
			end := 0
			for end < len(buf) && buf[end] != 0 {
				end++
			}
			fname = string(buf[:end])
			rest := ""
			if end+1 < len(buf) {
				e2 := end + 1
				for e2 < len(buf) && buf[e2] != 0 {
					e2++
				}
				rest = string(buf[end+1 : e2])
			}
			fs := strings.Fields(rest)
			if len(fs) > 0 {
				if v, err := strconv.ParseInt(fs[0], 10, 64); err == nil {
					size = v
				}
			}
			if len(fs) > 1 {
				mtime, _ = strconv.ParseInt(fs[1], 8, 64)
			}
		}
		fname = cleanName(fname)
		if fname == "" {
			fname = "download.bin"
		}
		fileNo++
		path := uniquePath(dir, fname)
		out, err := os.Create(path)
		if err != nil {
			return finish(err)
		}
		report := func(done int64) {
			if hooks != nil && hooks.Progress != nil {
				hooks.Progress(Progress{Name: filepath.Base(path), Done: done, Total: size, FileIndex: fileNo})
			}
		}
		report(0)

		var got int64
		expected := 1
		errs := 0
		done := false
		started := false
		for !done {
			if hooks.aborted() {
				out.Close()
				return finish(ErrCancelled)
			}
			if !started {
				x.putNak(expected)
			}
			r, n, err := x.getBlock(buf, expected)
			if err != nil {
				out.Close()
				return finish(err)
			}
			switch r {
			case blkOK:
				started = true
				errs = 0
				w := int64(n)
				if size >= 0 && got+w > size {
					w = size - got
				}
				out.Write(buf[:w])
				got += w
				expected++
				report(got)
				if !x.g {
					x.putcom(ACK)
				}
			case blkEOT:
				x.putcom(ACK)
				done = true
			case blkCAN:
				out.Close()
				return finish(fmt.Errorf("%w: cancelled by sender", ErrProtocol))
			case blkNotX:
				out.Close()
				return finish(fmt.Errorf("%w: sender is using YMODEM", ErrProtocol))
			default:
				errs++
				if errs > maxErrors {
					out.Close()
					return finish(ErrTimeout)
				}
				if started {
					x.putNak(expected)
				}
			}
		}
		// plain XMODEM pads the last block with ^Z; trim trailing ^Z
		if size < 0 {
			if st, e := out.Stat(); e == nil {
				sz := st.Size()
				data := make([]byte, 128)
				if sz >= 128 {
					out.ReadAt(data, sz-128)
					trim := 0
					for i := len(data) - 1; i >= 0 && data[i] == CPM; i-- {
						trim++
					}
					out.Truncate(sz - int64(trim))
				}
			}
		}
		out.Close()
		if mtime > 0 {
			t := time.Unix(mtime, 0)
			os.Chtimes(path, t, t)
		}
		res.Files = append(res.Files, path)
		if !mode.batch() {
			return finish(nil)
		}
	}
}

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
	return strings.Trim(raw, " .")
}
