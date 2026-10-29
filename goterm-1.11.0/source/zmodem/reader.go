package zmodem

import (
	"bufio"
	"bytes"
	"io"
	"time"
)

// reader decodes the incoming ZMODEM byte stream.
type reader struct {
	link  Link
	br    *bufio.Reader
	hooks *Hooks
	can   int
}

func newReader(link Link, prefix []byte, hooks *Hooks) *reader {
	var src io.Reader = link
	if len(prefix) > 0 {
		src = io.MultiReader(bytes.NewReader(prefix), link)
	}
	return &reader{link: link, br: bufio.NewReaderSize(src, 64*1024), hooks: hooks}
}

// leftover returns bytes already read from the link but not consumed.
func (r *reader) leftover() []byte {
	n := r.br.Buffered()
	if n == 0 {
		return nil
	}
	b, _ := r.br.Peek(n)
	return append([]byte(nil), b...)
}

// raw reads one byte from the link, honoring an overall deadline and the
// abort hook, skipping XON/XOFF flow-control bytes, and detecting a
// five-CAN cancel burst.
func (r *reader) raw(deadline time.Time) (byte, error) {
	for {
		if r.hooks.aborted() {
			return 0, ErrCancelled
		}
		slice := time.Now().Add(400 * time.Millisecond)
		if slice.After(deadline) {
			slice = deadline
		}
		r.link.SetReadDeadline(slice)
		b, err := r.br.ReadByte()
		if err != nil {
			if isTimeout(err) {
				if !time.Now().Before(deadline) {
					return 0, ErrTimeout
				}
				continue
			}
			return 0, err
		}
		switch b {
		case XON, XOFF, 0x91, 0x93:
			continue
		}
		if b == ZDLE {
			r.can++
			if r.can >= 5 {
				r.can = 0
				return 0, ErrCancelled
			}
		} else {
			r.can = 0
		}
		return b, nil
	}
}

// esc reads one (un-escaped) data byte. If a subpacket terminator is hit
// it returns it with marker=true.
func (r *reader) esc(deadline time.Time) (b byte, marker bool, err error) {
	c, err := r.raw(deadline)
	if err != nil {
		return 0, false, err
	}
	if c != ZDLE {
		return c, false, nil
	}
	c, err = r.raw(deadline)
	if err != nil {
		return 0, false, err
	}
	switch c {
	case ZCRCE, ZCRCG, ZCRCQ, ZCRCW:
		return c, true, nil
	case ZRUB0:
		return 0x7F, false, nil
	case ZRUB1:
		return 0xFF, false, nil
	}
	if c&0x60 == 0x40 {
		return c ^ 0x40, false, nil
	}
	return 0, false, ErrProtocol
}

// header hunts for the next valid header.
func (r *reader) header(timeout time.Duration) (Header, error) {
	deadline := time.Now().Add(timeout)
	for {
		b, err := r.raw(deadline)
		if err != nil {
			return Header{}, err
		}
		if b != ZPAD {
			continue
		}
		b, err = r.raw(deadline)
		if err != nil {
			return Header{}, err
		}
		if b == ZPAD {
			b, err = r.raw(deadline)
			if err != nil {
				return Header{}, err
			}
		}
		if b != ZDLE {
			continue
		}
		f, err := r.raw(deadline)
		if err != nil {
			return Header{}, err
		}
		var h Header
		var ok bool
		switch f {
		case ZHEX:
			h, ok, err = r.hexHeader(deadline)
		case ZBIN:
			h, ok, err = r.binHeader(deadline, false)
		case ZBIN32:
			h, ok, err = r.binHeader(deadline, true)
		default:
			continue
		}
		if err != nil {
			if err == ErrProtocol {
				continue
			}
			return Header{}, err
		}
		if ok {
			h.Fmt = f
			r.hooks.logf("rx %s fmt=%c pos=%d", FrameName(h.Type), f, h.Pos())
			return h, nil
		}
		r.hooks.logf("rx bad %c header skipped", f)
	}
}

func (r *reader) hexHeader(deadline time.Time) (Header, bool, error) {
	var raw [5]byte
	var crc [2]byte
	nib := func() (byte, error) {
		c, err := r.raw(deadline)
		if err != nil {
			return 0, err
		}
		v, ok := fromHex(c)
		if !ok {
			return 0, ErrProtocol
		}
		return v, nil
	}
	hb := func() (byte, error) {
		hi, err := nib()
		if err != nil {
			return 0, err
		}
		lo, err := nib()
		if err != nil {
			return 0, err
		}
		return hi<<4 | lo, nil
	}
	for i := range raw {
		v, err := hb()
		if err != nil {
			return Header{}, false, err
		}
		raw[i] = v
	}
	for i := range crc {
		v, err := hb()
		if err != nil {
			return Header{}, false, err
		}
		crc[i] = v
	}
	if crc16(raw[:]) != uint16(crc[0])<<8|uint16(crc[1]) {
		return Header{}, false, nil
	}
	// swallow the CR LF (XON) that trails a hex header, without blocking
	for i := 0; i < 3; i++ {
		r.link.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
		bs, err := r.br.Peek(1)
		if err != nil || len(bs) == 0 {
			break
		}
		if bs[0] == 0x0D || bs[0] == 0x0A || bs[0] == 0x8A || bs[0] == 0x8D {
			r.br.ReadByte()
			continue
		}
		break
	}
	h := Header{Type: int(raw[0])}
	copy(h.Data[:], raw[1:])
	return h, true, nil
}

func (r *reader) binHeader(deadline time.Time, use32 bool) (Header, bool, error) {
	n := 7
	if use32 {
		n = 9
	}
	buf := make([]byte, n)
	for i := range buf {
		b, marker, err := r.esc(deadline)
		if err != nil {
			return Header{}, false, err
		}
		if marker {
			return Header{}, false, ErrProtocol
		}
		buf[i] = b
	}
	if use32 {
		got := uint32(buf[5]) | uint32(buf[6])<<8 | uint32(buf[7])<<16 | uint32(buf[8])<<24
		if crc32sum(buf[:5]) != got {
			return Header{}, false, nil
		}
	} else if crc16(buf[:5]) != uint16(buf[5])<<8|uint16(buf[6]) {
		return Header{}, false, nil
	}
	h := Header{Type: int(buf[0])}
	copy(h.Data[:], buf[1:5])
	return h, true, nil
}

// subpacket reads one data subpacket. ok reports whether the CRC matched.
func (r *reader) subpacket(use32 bool, deadline time.Time, maxLen int) (data []byte, end byte, ok bool, err error) {
	for {
		b, marker, e := r.esc(deadline)
		if e != nil {
			if e == ErrProtocol {
				return data, 0, false, nil
			}
			return data, 0, false, e
		}
		if marker {
			end = b
			break
		}
		data = append(data, b)
		if len(data) > maxLen {
			return data, 0, false, nil
		}
	}
	if use32 {
		var c [4]byte
		for i := range c {
			b, marker, e := r.esc(deadline)
			if e != nil {
				if e == ErrProtocol {
					return data, end, false, nil
				}
				return data, end, false, e
			}
			if marker {
				return data, end, false, nil
			}
			c[i] = b
		}
		got := uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16 | uint32(c[3])<<24
		return data, end, crc32sum(data, []byte{end}) == got, nil
	}
	var c [2]byte
	for i := range c {
		b, marker, e := r.esc(deadline)
		if e != nil {
			if e == ErrProtocol {
				return data, end, false, nil
			}
			return data, end, false, e
		}
		if marker {
			return data, end, false, nil
		}
		c[i] = b
	}
	return data, end, crc16(data, []byte{end}) == uint16(c[0])<<8|uint16(c[1]), nil
}
