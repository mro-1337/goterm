package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Telnet command bytes (RFC 854).
const (
	tnSE   = 240
	tnNOP  = 241
	tnDM   = 242
	tnBRK  = 243
	tnIP   = 244
	tnAO   = 245
	tnAYT  = 246
	tnEC   = 247
	tnEL   = 248
	tnGA   = 249
	tnSB   = 250
	tnWILL = 251
	tnWONT = 252
	tnDO   = 253
	tnDONT = 254
	tnIAC  = 255
)

// Telnet option codes (RFC 855 / assorted RFCs) that we care about.
const (
	optBinary   = 0
	optEcho     = 1
	optSGA      = 3 // suppress go-ahead
	optTermType = 24
	optNAWS     = 31 // negotiate about window size
	optLineMode = 34
	optNewEnv   = 39
)

// TelnetConn wraps a raw net.Conn, performing telnet IAC negotiation and
// presenting a clean binary-safe io.Reader/io.Writer to the caller (all
// telnet commands/negotiation are consumed here and never leak through
// to the ANSI layer above).
type TelnetConn struct {
	nc       net.Conn
	br       *bufio.Reader
	termType string

	Raw        bool // plain TCP: no IAC processing
	RemoteEcho bool // true once server has told us it will echo (WILL ECHO)

	width, height int

	sbBuf []byte
	inSB  bool

	writeMu sync.Mutex
}

func DialTelnet(addr string, timeout time.Duration) (*TelnetConn, error) {
	nc, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	tc := &TelnetConn{
		nc:       nc,
		br:       bufio.NewReaderSize(nc, 8192),
		termType: "ANSI-BBS",
		width:    80,
		height:   25,
	}
	return tc, nil
}

func (t *TelnetConn) SetTermType(tt string) { t.termType = tt }
func (t *TelnetConn) SetSize(w, h int)      { t.width, t.height = w, h }

func (t *TelnetConn) Close() error { return t.nc.Close() }

// sendCmd writes IAC <cmd> <opt>.
func (t *TelnetConn) sendCmd(cmd, opt byte) {
	t.nc.Write([]byte{tnIAC, cmd, opt})
}

// SendNAWS informs the server of the current terminal size.
func (t *TelnetConn) SendNAWS(w, h int) {
	t.width, t.height = w, h
	buf := []byte{tnIAC, tnSB, optNAWS,
		byte(w >> 8), byte(w & 0xff),
		byte(h >> 8), byte(h & 0xff),
		tnIAC, tnSE,
	}
	// Escape any accidental 0xFF inside the size bytes (rare, w/h < 65536
	// with 0xFF byte value possible if width/height component == 255).
	t.nc.Write(escapeNAWS(buf))
}

func escapeNAWS(b []byte) []byte {
	// Only escape the 4 size data bytes (positions 3..6), not the IAC
	// framing bytes themselves.
	out := make([]byte, 0, len(b)+4)
	for i, c := range b {
		out = append(out, c)
		if i >= 3 && i <= 6 && c == tnIAC {
			out = append(out, tnIAC)
		}
	}
	return out
}

// Read implements io.Reader. It returns only "payload" bytes (terminal
// data), having consumed and acted on any telnet IAC command sequences
// found along the way. It may return 0 bytes read with a nil error if an
// entire read consisted of protocol negotiation.
func (t *TelnetConn) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if n > 0 && t.br.Buffered() == 0 {
			return n, nil
		}
		b, err := t.br.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		if b != tnIAC || t.Raw {
			p[n] = b
			n++
			continue
		}
		// We saw IAC; read the command byte.
		cmd, err := t.br.ReadByte()
		if err != nil {
			return n, err
		}
		switch cmd {
		case tnIAC:
			// Escaped 0xFF literal data byte.
			p[n] = 0xFF
			n++
		case tnWILL, tnWONT, tnDO, tnDONT:
			opt, err := t.br.ReadByte()
			if err != nil {
				return n, err
			}
			t.handleNegotiation(cmd, opt)
		case tnSB:
			t.readSubnegotiation()
		case tnGA, tnNOP, tnDM, tnBRK, tnIP, tnAO, tnAYT, tnEC, tnEL:
			// Ignore, no payload.
		default:
			// Unknown command, ignore.
		}
		if n > 0 {
			// Return what we have so far so the caller doesn't block
			// waiting for more when negotiation bytes interleave with data.
			// Keep looping only opportunistically via bufio.Buffered.
			if t.br.Buffered() == 0 {
				return n, nil
			}
		}
	}
	return n, nil
}

func (t *TelnetConn) readSubnegotiation() {
	t.sbBuf = t.sbBuf[:0]
	for {
		b, err := t.br.ReadByte()
		if err != nil {
			return
		}
		if b == tnIAC {
			b2, err := t.br.ReadByte()
			if err != nil {
				return
			}
			if b2 == tnSE {
				break
			}
			if b2 == tnIAC {
				t.sbBuf = append(t.sbBuf, 0xFF)
				continue
			}
			// Unexpected; treat as end.
			break
		}
		t.sbBuf = append(t.sbBuf, b)
	}
	if len(t.sbBuf) == 0 {
		return
	}
	switch t.sbBuf[0] {
	case optTermType:
		if len(t.sbBuf) >= 2 && t.sbBuf[1] == 1 { // SEND
			resp := []byte{tnIAC, tnSB, optTermType, 0} // IS
			resp = append(resp, []byte(t.termType)...)
			resp = append(resp, tnIAC, tnSE)
			t.nc.Write(resp)
		}
	}
}

func (t *TelnetConn) handleNegotiation(cmd, opt byte) {
	switch cmd {
	case tnWILL:
		switch opt {
		case optEcho:
			t.RemoteEcho = true
			t.sendCmd(tnDO, opt)
		case optSGA, optBinary:
			t.sendCmd(tnDO, opt)
		default:
			t.sendCmd(tnDONT, opt)
		}
	case tnWONT:
		if opt == optEcho {
			t.RemoteEcho = false
		}
		t.sendCmd(tnDONT, opt)
	case tnDO:
		switch opt {
		case optTermType, optNAWS, optSGA, optBinary:
			t.sendCmd(tnWILL, opt)
			if opt == optNAWS {
				t.SendNAWS(t.width, t.height)
			}
		default:
			t.sendCmd(tnWONT, opt)
		}
	case tnDONT:
		t.sendCmd(tnWONT, opt)
	}
}

// Write implements io.Writer, transparently doubling any literal 0xFF
// bytes in the outgoing payload per the telnet protocol.
func (t *TelnetConn) Write(p []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.Raw {
		return t.nc.Write(p)
	}
	if indexByte(p, 0xFF) == -1 {
		return t.nc.Write(p)
	}
	out := make([]byte, 0, len(p)+8)
	for _, b := range p {
		out = append(out, b)
		if b == tnIAC {
			out = append(out, tnIAC)
		}
	}
	_, err := t.nc.Write(out)
	return len(p), err
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// SetReadDeadline satisfies the zmodem.Link interface so the transfer
// code can time out cleanly waiting on the peer.
func (t *TelnetConn) SetReadDeadline(tm time.Time) error {
	return t.nc.SetReadDeadline(tm)
}

// RawConn exposes the underlying net.Conn for the ZMODEM layer, which
// needs an unbuffered, un-telnet-filtered read once a transfer starts
// (telnet IAC escaping of 0xFF must still apply, so we wrap it).
func (t *TelnetConn) RawConn() net.Conn { return t.nc }

// Reader returns an io.Reader that still drains anything already
// buffered in the bufio.Reader before falling through to raw socket
// reads. ZMODEM uses this so no bytes are lost mid-negotiation.
func (t *TelnetConn) Reader() io.Reader { return t }

var _ = fmt.Sprintf // keep fmt imported for future debug use
