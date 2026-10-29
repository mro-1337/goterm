// Package zmodem implements the ZMODEM file transfer protocol (Chuck
// Forsberg's specification) for both sending and receiving over an
// 8-bit clean link such as a telnet connection in BINARY mode.
//
// It supports hex / binary-16 / binary-32 headers, CRC-16 and CRC-32 data
// subpackets, ZDLE escaping, streaming with periodic ACK sync points,
// ZRPOS error recovery, crash-recovery style ZSKIP, and CAN-CAN-CAN-CAN-CAN
// abort handling.
package zmodem

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Wire constants.
const (
	ZPAD   = '*'
	ZDLE   = 0x18
	ZDLEE  = 0x58
	ZBIN   = 'A'
	ZHEX   = 'B'
	ZBIN32 = 'C'

	ZRUB0 = 'l' // escaped DEL
	ZRUB1 = 'm' // escaped 0xFF

	XON  = 0x11
	XOFF = 0x13
)

// Frame types.
const (
	ZRQINIT    = 0
	ZRINIT     = 1
	ZSINIT     = 2
	ZACK       = 3
	ZFILE      = 4
	ZSKIP      = 5
	ZNAK       = 6
	ZABORT     = 7
	ZFIN       = 8
	ZRPOS      = 9
	ZDATA      = 10
	ZEOF       = 11
	ZFERR      = 12
	ZCRC       = 13
	ZCHALLENGE = 14
	ZCOMPL     = 15
	ZCAN       = 16
	ZFREECNT   = 17
	ZCOMMAND   = 18
	ZSTDERR    = 19
)

// Data subpacket terminators.
const (
	ZCRCE = 'h' // end of frame, no ack
	ZCRCG = 'i' // frame continues, no ack
	ZCRCQ = 'j' // frame continues, ack expected
	ZCRCW = 'k' // end of frame, ack expected
)

// ZRINIT capability flags (in ZF0, i.e. Header.Data[3]).
const (
	CANFDX  = 0x01
	CANOVIO = 0x02
	CANBRK  = 0x04
	CANCRY  = 0x08
	CANLZW  = 0x10
	CANFC32 = 0x20
	ESCCTL  = 0x40
	ESC8    = 0x80
)

var frameNames = [...]string{"ZRQINIT", "ZRINIT", "ZSINIT", "ZACK", "ZFILE", "ZSKIP", "ZNAK",
	"ZABORT", "ZFIN", "ZRPOS", "ZDATA", "ZEOF", "ZFERR", "ZCRC", "ZCHALLENGE", "ZCOMPL",
	"ZCAN", "ZFREECNT", "ZCOMMAND", "ZSTDERR"}

func FrameName(t int) string {
	if t >= 0 && t < len(frameNames) {
		return frameNames[t]
	}
	return fmt.Sprintf("frame#%d", t)
}

var (
	ErrCancelled = errors.New("zmodem: transfer cancelled")
	ErrTimeout   = errors.New("zmodem: timed out waiting for peer")
	ErrProtocol  = errors.New("zmodem: protocol error")
)

// Link is the connection a transfer runs over.
type Link interface {
	io.Reader
	io.Writer
	SetReadDeadline(t time.Time) error
}

// Progress describes transfer progress for the UI.
type Progress struct {
	Name      string
	Done      int64
	Total     int64
	FileIndex int // 1-based
	FileCount int // 0 if unknown (receiving)
	Sending   bool
}

// Hooks are optional callbacks.
type Hooks struct {
	Abort    func() bool // return true to cancel the transfer
	Progress func(Progress)
	Log      func(string)
}

func (h *Hooks) aborted() bool { return h != nil && h.Abort != nil && h.Abort() }
func (h *Hooks) progress(p Progress) {
	if h != nil && h.Progress != nil {
		h.Progress(p)
	}
}
func (h *Hooks) logf(f string, a ...interface{}) {
	if h != nil && h.Log != nil {
		h.Log(fmt.Sprintf(f, a...))
	}
}

// Header is a decoded ZMODEM header.
type Header struct {
	Type int
	Data [4]byte // ZP0..ZP3 (position LSB first) or ZF3..ZF0
	Fmt  byte    // ZHEX, ZBIN or ZBIN32 as received
}

func (h Header) Pos() int64 {
	return int64(uint32(h.Data[0]) | uint32(h.Data[1])<<8 | uint32(h.Data[2])<<16 | uint32(h.Data[3])<<24)
}

func posData(pos int64) [4]byte {
	p := uint32(pos)
	return [4]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// ---------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------

// escaper appends bytes to a buffer applying ZDLE escaping. Every control
// character (including CR) is escaped like "sz -e"; a receiver started with
// ESCCTL discards unescaped control bytes, and telnet links may mangle them.
type escaper struct {
	out  []byte
	last byte
}

func (e *escaper) put(c byte) {
	esc := false
	switch c {
	case ZDLE, 0x10, XON, XOFF, 0x90, 0x91, 0x93:
		esc = true
	default:
		esc = c&0x60 == 0 // 0x00-0x1F and 0x80-0x9F
	}
	if esc {
		e.out = append(e.out, ZDLE, c^0x40)
		e.last = c ^ 0x40
	} else {
		e.out = append(e.out, c)
		e.last = c
	}
}

func hexByte(out []byte, b byte) []byte {
	const hexd = "0123456789abcdef"
	return append(out, hexd[b>>4], hexd[b&15])
}

// encodeHex builds a hex header.
func encodeHex(typ int, data [4]byte) []byte {
	out := []byte{ZPAD, ZPAD, ZDLE, ZHEX}
	raw := []byte{byte(typ), data[0], data[1], data[2], data[3]}
	for _, b := range raw {
		out = hexByte(out, b)
	}
	c := crc16(raw)
	out = hexByte(out, byte(c>>8))
	out = hexByte(out, byte(c))
	out = append(out, 0x0D, 0x0A)
	if typ != ZFIN && typ != ZACK {
		out = append(out, XON)
	}
	return out
}

// encodeBin builds a binary header (CRC-16 or CRC-32).
func encodeBin(typ int, data [4]byte, use32 bool) []byte {
	e := &escaper{}
	raw := []byte{byte(typ), data[0], data[1], data[2], data[3]}
	var out []byte
	if use32 {
		out = []byte{ZPAD, ZDLE, ZBIN32}
	} else {
		out = []byte{ZPAD, ZDLE, ZBIN}
	}
	for _, b := range raw {
		e.put(b)
	}
	if use32 {
		c := crc32sum(raw)
		e.put(byte(c))
		e.put(byte(c >> 8))
		e.put(byte(c >> 16))
		e.put(byte(c >> 24))
	} else {
		c := crc16(raw)
		e.put(byte(c >> 8))
		e.put(byte(c))
	}
	return append(out, e.out...)
}

// appendSubpacket appends an escaped data subpacket (data + terminator +
// CRC) to e.
func appendSubpacket(e *escaper, data []byte, end byte, use32 bool) {
	for _, b := range data {
		e.put(b)
	}
	e.out = append(e.out, ZDLE, end)
	e.last = end
	if use32 {
		c := crc32sum(data, []byte{end})
		e.put(byte(c))
		e.put(byte(c >> 8))
		e.put(byte(c >> 16))
		e.put(byte(c >> 24))
	} else {
		c := crc16(data, []byte{end})
		e.put(byte(c >> 8))
		e.put(byte(c))
	}
	if end == ZCRCW {
		e.out = append(e.out, XON)
	}
}

var cancelSeq = []byte{0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 8, 8, 8, 8, 8, 8, 8, 8}

// ---------------------------------------------------------------------
// Detection helper for the terminal layer
// ---------------------------------------------------------------------

// Detect looks for the start of a ZMODEM hex header ("*" ZDLE "B") in buf.
// It returns the index of the header start (the position of the ZDLE minus
// the leading pads is not required; the returned index points at the first
// ZPAD of the run), the frame type if complete, and complete=false if the
// header was cut off at the end of buf.
func Detect(buf []byte) (idx int, frameType int, complete bool) {
	// A binary ZFILE header (sender that skips ZRQINIT): "*" ZDLE "A"|"C" 0x04
	for i := 0; i+3 < len(buf); i++ {
		if buf[i] == ZPAD && buf[i+1] == ZDLE && (buf[i+2] == ZBIN || buf[i+2] == ZBIN32) && buf[i+3] == ZFILE {
			start := i
			for start > 0 && buf[start-1] == ZPAD {
				start--
			}
			return start, ZFILE, true
		}
	}
	for i := 0; i+2 < len(buf); i++ {
		if buf[i] == ZPAD && buf[i+1] == ZDLE && buf[i+2] == ZHEX {
			start := i
			for start > 0 && buf[start-1] == ZPAD {
				start--
			}
			if i+5 > len(buf) {
				return start, 0, false
			}
			hi, ok1 := fromHex(buf[i+3])
			lo, ok2 := fromHex(buf[i+4])
			if !ok1 || !ok2 {
				continue
			}
			return start, int(hi<<4 | lo), true
		}
	}
	// A header cut off right at the end of buf: hold back "**", "*\x18"
	// or "**\x18" so the caller can wait for the rest.
	n := len(buf)
	if n >= 3 && buf[n-3] == ZPAD && buf[n-2] == ZPAD && buf[n-1] == ZDLE {
		return n - 3, 0, false
	}
	if n >= 2 && buf[n-2] == ZPAD && (buf[n-1] == ZPAD || buf[n-1] == ZDLE) {
		return n - 2, 0, false
	}
	return -1, 0, false
}

func fromHex(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}
