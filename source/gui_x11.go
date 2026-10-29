//go:build linux && !cli && !engine

package main

// A minimal native X11 front end written against the X11 wire protocol
// (no Xlib, no cgo). It maps the same App interface the Win32 window uses.

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

//go:embed goterm.png
var iconPNG []byte

var le = binary.LittleEndian

type x11 struct {
	app *App
	nc  net.Conn
	wmu sync.Mutex // serialises writes

	root, win, gc, rid uint32
	ridBase, ridMask   uint32
	nextID             uint32
	maxReq             int // bytes
	scrW, scrH         int
	minKC, maxKC       int
	syms               []uint32
	perKC              int

	cw, ch     int // client size
	dx, dy     int
	dw, dh     int
	fbW, fbH   int
	fullscreen bool
	title      string
	out        []uint32

	atoms struct {
		wmProtocols, wmDelete, netWMName, utf8, netWMState, netFull, clipboard, targets, prop, netWMIcon, text uint32
	}

	evCh    chan []byte
	replyCh chan []byte
	selCh   chan uint32

	ownText   string
	owning    bool
	lastClick time.Time
	lastX     int
	lastY     int
	quit      bool
	sound     chan []byte
}

var X x11

func pad4(n int) int { return (n + 3) &^ 3 }

func (x *x11) write(b []byte) error {
	x.wmu.Lock()
	defer x.wmu.Unlock()
	_, err := x.nc.Write(b)
	return err
}

// readAuth finds an MIT-MAGIC-COOKIE-1 for the display.
func readAuth(dispNum string) (name string, data []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		h, _ := os.UserHomeDir()
		path = filepath.Join(h, ".Xauthority")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	host, _ := os.Hostname()
	for len(b) >= 2 {
		get := func() ([]byte, bool) {
			if len(b) < 2 {
				return nil, false
			}
			n := int(binary.BigEndian.Uint16(b))
			if len(b) < 2+n {
				return nil, false
			}
			v := b[2 : 2+n]
			b = b[2+n:]
			return v, true
		}
		if len(b) < 2 {
			break
		}
		fam := binary.BigEndian.Uint16(b)
		b = b[2:]
		addr, ok1 := get()
		num, ok2 := get()
		nm, ok3 := get()
		dat, ok4 := get()
		if !ok1 || !ok2 || !ok3 || !ok4 {
			break
		}
		if string(nm) != "MIT-MAGIC-COOKIE-1" {
			continue
		}
		if len(num) > 0 && string(num) != dispNum {
			continue
		}
		if fam == 256 && string(addr) != host && string(addr) != "localhost" {
			continue
		}
		return string(nm), append([]byte(nil), dat...)
	}
	return "", nil
}

func (x *x11) connect() error {
	d := os.Getenv("DISPLAY")
	if d == "" {
		return errors.New("DISPLAY is not set (an X11 session is required)")
	}
	host, rest, _ := strings.Cut(d, ":")
	numStr, _, _ := strings.Cut(rest, ".")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return fmt.Errorf("bad DISPLAY %q", d)
	}
	var nc net.Conn
	if host == "" || host == "unix" {
		nc, err = net.Dial("unix", "/tmp/.X11-unix/X"+numStr)
		if err != nil {
			nc, err = net.Dial("unix", "@/tmp/.X11-unix/X"+numStr)
		}
	} else {
		nc, err = net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(6000+n)))
	}
	if err != nil {
		return err
	}
	x.nc = nc
	an, ad := readAuth(numStr)
	req := make([]byte, 12)
	req[0] = 'l'
	le.PutUint16(req[2:], 11)
	le.PutUint16(req[6:], uint16(len(an)))
	le.PutUint16(req[8:], uint16(len(ad)))
	req = append(req, []byte(an)...)
	req = append(req, make([]byte, pad4(len(an))-len(an))...)
	req = append(req, ad...)
	req = append(req, make([]byte, pad4(len(ad))-len(ad))...)
	if _, err := nc.Write(req); err != nil {
		return err
	}
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(nc, hdr); err != nil {
		return err
	}
	body := make([]byte, int(le.Uint16(hdr[6:]))*4)
	if _, err := io.ReadFull(nc, body); err != nil {
		return err
	}
	if hdr[0] != 1 {
		return fmt.Errorf("X server refused connection: %s", string(body[:minInt(len(body), int(hdr[1]))]))
	}
	x.ridBase, x.ridMask = le.Uint32(body[4:]), le.Uint32(body[8:])
	vlen := int(le.Uint16(body[16:]))
	x.maxReq = int(le.Uint16(body[18:])) * 4
	nscreens, nformats := int(body[20]), int(body[21])
	x.minKC, x.maxKC = int(body[26]), int(body[27])
	off := 32 + pad4(vlen) + nformats*8
	if nscreens < 1 || off+40 > len(body) {
		return errors.New("bad X setup reply")
	}
	s := body[off:]
	x.root = le.Uint32(s[0:])
	x.scrW, x.scrH = int(le.Uint16(s[20:])), int(le.Uint16(s[22:]))
	x.nextID = 0
	return nil
}

func (x *x11) newID() uint32 {
	x.nextID++
	// lowest set bit of mask is the increment unit
	inc := x.ridMask & -x.ridMask
	return x.ridBase | (x.nextID * inc & x.ridMask)
}

// request sends a request and returns the 32+ byte reply (setup phase only,
// before the reader goroutine starts).
func (x *x11) syncReply() ([]byte, error) {
	for {
		h := make([]byte, 32)
		if _, err := io.ReadFull(x.nc, h); err != nil {
			return nil, err
		}
		switch h[0] {
		case 1:
			extra := int(le.Uint32(h[4:])) * 4
			if extra > 0 {
				e := make([]byte, extra)
				if _, err := io.ReadFull(x.nc, e); err != nil {
					return nil, err
				}
				h = append(h, e...)
			}
			return h, nil
		case 0:
			return nil, fmt.Errorf("X error code %d opcode %d", h[1], h[10])
		}
	}
}

func (x *x11) intern(name string) uint32 {
	req := make([]byte, 8, 8+pad4(len(name)))
	req[0] = 16
	le.PutUint16(req[2:], uint16(2+pad4(len(name))/4))
	le.PutUint16(req[4:], uint16(len(name)))
	req = append(req, []byte(name)...)
	req = append(req, make([]byte, pad4(len(name))-len(name))...)
	x.write(req)
	r, err := x.syncReply()
	if err != nil {
		return 0
	}
	return le.Uint32(r[8:])
}

func (x *x11) changeProp(prop, typ uint32, format int, data []byte) {
	units := len(data) / (format / 8)
	req := make([]byte, 24, 24+pad4(len(data)))
	req[0] = 18
	le.PutUint16(req[2:], uint16(6+pad4(len(data))/4))
	le.PutUint32(req[4:], x.win)
	le.PutUint32(req[8:], prop)
	le.PutUint32(req[12:], typ)
	req[16] = byte(format)
	le.PutUint32(req[20:], uint32(units))
	req = append(req, data...)
	req = append(req, make([]byte, pad4(len(data))-len(data))...)
	x.write(req)
}

func u32s(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, n := range v {
		le.PutUint32(b[4*i:], n)
	}
	return b
}

func (x *x11) setTitle(t string) {
	x.changeProp(39, 31, 8, []byte(t)) // WM_NAME STRING
	x.changeProp(x.atoms.netWMName, x.atoms.utf8, 8, []byte(t))
}

func (x *x11) loadKeymap() {
	cnt := x.maxKC - x.minKC + 1
	req := []byte{101, 0, 2, 0, byte(x.minKC), byte(cnt), 0, 0}
	x.write(req)
	r, err := x.syncReply()
	if err != nil {
		return
	}
	x.perKC = int(r[1])
	n := (len(r) - 32) / 4
	x.syms = make([]uint32, n)
	for i := range x.syms {
		x.syms[i] = le.Uint32(r[32+4*i:])
	}
}

func (x *x11) sym(kc, col int) uint32 {
	i := (kc-x.minKC)*x.perKC + col
	if x.perKC == 0 || i < 0 || i >= len(x.syms) || col >= x.perKC {
		return 0
	}
	return x.syms[i]
}

var x11Keys = map[uint32]Key{
	0xff08: KeyBS, 0xff09: KeyTab, 0xff0d: KeyEnter, 0xff8d: KeyEnter, 0xff1b: KeyEsc,
	0xff50: KeyHome, 0xff51: KeyLeft, 0xff52: KeyUp, 0xff53: KeyRight, 0xff54: KeyDown,
	0xff55: KeyPgUp, 0xff56: KeyPgDn, 0xff57: KeyEnd, 0xff63: KeyIns, 0xffff: KeyDel,
	0xff95: KeyHome, 0xff96: KeyLeft, 0xff97: KeyUp, 0xff98: KeyRight, 0xff99: KeyDown,
	0xff9a: KeyPgUp, 0xff9b: KeyPgDn, 0xff9c: KeyEnd, 0xff9e: KeyIns, 0xff9f: KeyDel,
}

// keyEvent translates an X key press into a KeyEvent.
func (x *x11) keyPress(kc int, state uint16) {
	shift := state&1 != 0
	caps := state&2 != 0
	ctrl := state&4 != 0
	alt := state&8 != 0
	numlock := state&0x10 != 0
	altgr := state&0x80 != 0
	base := x.sym(kc, 0)
	letter := base < 0x10000 && unicode.IsLetter(rune(base))
	eff := shift != (caps && letter)
	if base >= 0xff95 && base <= 0xff9f || base >= 0xffaa && base <= 0xffb9 {
		eff = shift != numlock
	}
	col := 0
	if altgr {
		col = 2
	}
	if eff {
		col++
	}
	s := x.sym(kc, col)
	if s == 0 {
		s = x.sym(kc, col&1)
	}
	if s == 0 {
		s = base
	}
	a := x.app
	switch {
	case s >= 0xffbe && s <= 0xffc9: // F1..F12
		a.OnKey(KeyEvent{Key: KeyF1 + Key(s-0xffbe), Ctrl: ctrl, Alt: alt, Shift: shift})
		return
	case s >= 0xffb0 && s <= 0xffb9:
		s = '0' + (s - 0xffb0)
	case s == 0xffaa:
		s = '*'
	case s == 0xffab:
		s = '+'
	case s == 0xffad:
		s = '-'
	case s == 0xffae:
		s = '.'
	case s == 0xffaf:
		s = '/'
	}
	if k, ok := x11Keys[s]; ok {
		if k == KeyEnter && alt && !ctrl {
			a.ToggleFullscreen()
			return
		}
		a.OnKey(KeyEvent{Key: k, Ctrl: ctrl, Alt: alt, Shift: shift})
		return
	}
	var r rune
	switch {
	case s >= 0x20 && s <= 0x7e, s >= 0xa0 && s <= 0xff:
		r = rune(s)
	case s >= 0x01000000 && s <= 0x0110ffff:
		r = rune(s - 0x01000000)
	default:
		return // modifiers and unknown keys
	}
	if alt && !ctrl && !altgr {
		r = unicode.ToLower(r)
		a.OnKey(KeyEvent{Ch: r, Alt: true, Shift: shift})
		return
	}
	if ctrl && !altgr {
		lr := unicode.ToLower(r)
		switch {
		case lr >= 'a' && lr <= 'z':
			a.OnKey(KeyEvent{Ch: lr - 'a' + 1, Ctrl: true, Shift: shift})
			return
		case r == ' ':
			a.OnKey(KeyEvent{Ch: 0, Ctrl: true})
			return
		case r >= '[' && r <= '_':
			a.OnKey(KeyEvent{Ch: r - '@', Ctrl: true})
			return
		}
	}
	a.OnKey(KeyEvent{Ch: r})
}

func (x *x11) mouse(ev MouseEvent, px, py int) {
	if x.dw > 0 && x.dh > 0 {
		ev.X = (px - x.dx) * x.fbW / x.dw
		ev.Y = (py - x.dy) * x.fbH / x.dh
	}
	x.app.OnMouse(ev)
}

// ---------------------------------------------------------------- drawing

func (x *x11) layout(fw, fh int) {
	cw, ch := x.cw, x.ch
	var dw, dh int
	switch x.app.scaleMode() {
	case "stretch":
		dw, dh = cw, ch
	case "integer":
		n := cw / fw
		if m := ch / fh; m < n {
			n = m
		}
		if n < 1 {
			dw, dh = fw*ch/fh, ch
			if dw > cw {
				dw, dh = cw, fh*cw/fw
			}
		} else {
			dw, dh = fw*n, fh*n
		}
	default:
		dw, dh = cw, fh*cw/fw
		if dh > ch {
			dw, dh = fw*ch/fh, ch
		}
	}
	x.dx, x.dy, x.dw, x.dh, x.fbW, x.fbH = (cw-dw)/2, (ch-dh)/2, dw, dh, fw, fh
}

func (x *x11) draw() {
	fb, fw, fh := x.app.Frame()
	if x.cw <= 0 || x.ch <= 0 || fw == 0 {
		return
	}
	x.layout(fw, fh)
	if len(x.out) < x.cw*x.ch {
		x.out = make([]uint32, x.cw*x.ch)
	}
	out := x.out[:x.cw*x.ch]
	for i := range out {
		out[i] = 0
	}
	dw, dh := x.dw, x.dh
	if dw <= 0 || dh <= 0 {
		return
	}
	smooth := x.app.smooth() && (dw != fw || dh != fh)
	xs := make([]int, dw)
	for i := range xs {
		xs[i] = i * fw / dw
	}
	for y := 0; y < dh; y++ {
		dst := out[(y+x.dy)*x.cw+x.dx : (y+x.dy)*x.cw+x.dx+dw]
		if !smooth {
			sy := y * fh / dh
			src := fb[sy*fw : sy*fw+fw]
			if dw == fw {
				copy(dst, src)
			} else {
				for i := range dst {
					dst[i] = src[xs[i]]
				}
			}
			continue
		}
		fy := (float32(y)+0.5)*float32(fh)/float32(dh) - 0.5
		y0 := int(fy)
		if fy < 0 {
			y0, fy = 0, 0
		}
		y1 := minInt(y0+1, fh-1)
		wy := int((fy - float32(y0)) * 256)
		for i := range dst {
			fx := (float32(i)+0.5)*float32(fw)/float32(dw) - 0.5
			x0 := int(fx)
			if fx < 0 {
				x0, fx = 0, 0
			}
			x1 := minInt(x0+1, fw-1)
			wx := int((fx - float32(x0)) * 256)
			dst[i] = blend4(fb[y0*fw+x0], fb[y0*fw+x1], fb[y1*fw+x0], fb[y1*fw+x1], wx, wy)
		}
	}
	x.putImage(out)
}

func blend4(a, b, c, d uint32, wx, wy int) uint32 {
	var res uint32
	for sh := uint(0); sh < 24; sh += 8 {
		ca, cb, cc, cd := int(a>>sh&255), int(b>>sh&255), int(c>>sh&255), int(d>>sh&255)
		top := ca*(256-wx) + cb*wx
		bot := cc*(256-wx) + cd*wx
		v := (top*(256-wy) + bot*wy) >> 16
		res |= uint32(v) << sh
	}
	return res
}

func (x *x11) putImage(px []uint32) {
	w, h := x.cw, x.ch
	maxRows := (x.maxReq - 24) / (w * 4)
	if maxRows < 1 {
		maxRows = 1
	}
	if x.maxReq == 0 {
		maxRows = 64
	}
	for y := 0; y < h; y += maxRows {
		rows := minInt(maxRows, h-y)
		n := rows * w * 4
		req := make([]byte, 24+n)
		req[0], req[1] = 72, 2
		le.PutUint16(req[2:], uint16(6+n/4))
		le.PutUint32(req[4:], x.win)
		le.PutUint32(req[8:], x.gc)
		le.PutUint16(req[12:], uint16(w))
		le.PutUint16(req[14:], uint16(rows))
		le.PutUint16(req[18:], uint16(y))
		req[21] = 24
		d := req[24:]
		for i, p := range px[y*w : (y+rows)*w] {
			le.PutUint32(d[4*i:], p&0xFFFFFF)
		}
		if x.write(req) != nil {
			return
		}
	}
}

// ---------------------------------------------------------------- window ops

func (x *x11) sendClient(dst uint32, mask uint32, typ uint32, data [5]uint32) {
	ev := make([]byte, 32)
	ev[0], ev[1] = 33, 32
	le.PutUint32(ev[4:], x.win)
	le.PutUint32(ev[8:], typ)
	for i, d := range data {
		le.PutUint32(ev[12+4*i:], d)
	}
	req := make([]byte, 12, 44)
	req[0], req[1] = 25, 0
	le.PutUint16(req[2:], 11)
	le.PutUint32(req[4:], dst)
	le.PutUint32(req[8:], mask)
	x.write(append(req, ev...))
}

func (x *x11) setFullscreen(on bool) {
	if on == x.fullscreen || x.win == 0 {
		return
	}
	v := uint32(0)
	if on {
		v = 1
	}
	x.sendClient(x.root, 0x180000, x.atoms.netWMState, [5]uint32{v, x.atoms.netFull, 0, 1, 0})
	x.fullscreen = on
}

// ---------------------------------------------------------------- clipboard

func (x *x11) setClipboard(s string) {
	x.ownText = s
	x.owning = true
	for _, sel := range []uint32{x.atoms.clipboard, 1} {
		req := make([]byte, 16)
		req[0] = 22
		le.PutUint16(req[2:], 4)
		le.PutUint32(req[4:], x.win)
		le.PutUint32(req[8:], sel)
		x.write(req)
	}
}

func (x *x11) getClipboard() string {
	if x.owning {
		return x.ownText
	}
	for len(x.selCh) > 0 {
		<-x.selCh
	}
	for len(x.replyCh) > 0 {
		<-x.replyCh
	}
	req := make([]byte, 24)
	req[0] = 24
	le.PutUint16(req[2:], 6)
	le.PutUint32(req[4:], x.win)
	le.PutUint32(req[8:], x.atoms.clipboard)
	le.PutUint32(req[12:], x.atoms.utf8)
	le.PutUint32(req[16:], x.atoms.prop)
	x.write(req)
	select {
	case p := <-x.selCh:
		if p == 0 {
			return ""
		}
	case <-time.After(1500 * time.Millisecond):
		return ""
	}
	gp := make([]byte, 24)
	gp[0], gp[1] = 20, 1 // delete
	le.PutUint16(gp[2:], 6)
	le.PutUint32(gp[4:], x.win)
	le.PutUint32(gp[8:], x.atoms.prop)
	le.PutUint32(gp[16:], 0)
	le.PutUint32(gp[20:], 1<<18)
	x.write(gp)
	select {
	case r := <-x.replyCh:
		n := int(le.Uint32(r[16:])) * int(r[1]) / 8
		if n < 0 || 32+n > len(r) {
			return ""
		}
		return string(r[32 : 32+n])
	case <-time.After(1500 * time.Millisecond):
		return ""
	}
}

func (x *x11) selectionRequest(ev []byte) {
	requestor := le.Uint32(ev[12:])
	sel := le.Uint32(ev[16:])
	target := le.Uint32(ev[20:])
	prop := le.Uint32(ev[24:])
	if prop == 0 {
		prop = target
	}
	ok := false
	if x.owning {
		switch target {
		case x.atoms.targets:
			ok = x.setProp(requestor, prop, 4, 32, u32s(x.atoms.targets, x.atoms.utf8, 31, x.atoms.text))
		case x.atoms.utf8, 31, x.atoms.text:
			typ := target
			if typ == x.atoms.text {
				typ = x.atoms.utf8
			}
			ok = x.setProp(requestor, prop, typ, 8, []byte(x.ownText))
		}
	}
	n := make([]byte, 32)
	n[0] = 31
	le.PutUint32(n[8:], requestor)
	le.PutUint32(n[12:], sel)
	le.PutUint32(n[16:], target)
	if ok {
		le.PutUint32(n[20:], prop)
	}
	req := make([]byte, 12, 44)
	req[0] = 25
	le.PutUint16(req[2:], 11)
	le.PutUint32(req[4:], requestor)
	x.write(append(req, n...))
}

func (x *x11) setProp(win, prop, typ uint32, format int, data []byte) bool {
	if 24+pad4(len(data)) > x.maxReq && x.maxReq > 0 {
		return false
	}
	units := len(data) / (format / 8)
	req := make([]byte, 24, 24+pad4(len(data)))
	req[0] = 18
	le.PutUint16(req[2:], uint16(6+pad4(len(data))/4))
	le.PutUint32(req[4:], win)
	le.PutUint32(req[8:], prop)
	le.PutUint32(req[12:], typ)
	req[16] = byte(format)
	le.PutUint32(req[20:], uint32(units))
	req = append(req, data...)
	req = append(req, make([]byte, pad4(len(data))-len(data))...)
	return x.write(req) == nil
}

// ---------------------------------------------------------------- reader

func (x *x11) reader() {
	defer close(x.evCh)
	for {
		h := make([]byte, 32)
		if _, err := io.ReadFull(x.nc, h); err != nil {
			return
		}
		t := h[0] & 0x7f
		switch t {
		case 1:
			extra := int(le.Uint32(h[4:])) * 4
			if extra > 0 {
				e := make([]byte, extra)
				if _, err := io.ReadFull(x.nc, e); err != nil {
					return
				}
				h = append(h, e...)
			}
			select {
			case x.replyCh <- h:
			default:
			}
		case 0:
			if os.Getenv("GT_X11DEBUG") != "" {
				fmt.Fprintf(os.Stderr, "X error code=%d major=%d minor=%d\n", h[1], h[10], le.Uint16(h[8:]))
			}
		case 29: // SelectionClear
			x.owning = false
		case 30:
			x.selectionRequest(h)
		case 31:
			select {
			case x.selCh <- le.Uint32(h[20:]):
			default:
			}
		default:
			x.evCh <- h
		}
	}
}

// ---------------------------------------------------------------- sound

func soundLoopX() {
	var player []string
	for _, c := range [][]string{{"paplay"}, {"pw-play"}, {"aplay", "-q"}, {"play", "-q"}} {
		if p, err := exec.LookPath(c[0]); err == nil {
			player = append([]string{p}, c[1:]...)
			break
		}
	}
	for wav := range X.sound {
		if len(wav) == 0 || player == nil {
			continue
		}
		f, err := os.CreateTemp("", "goterm-*.wav")
		if err != nil {
			continue
		}
		f.Write(wav)
		f.Close()
		exec.Command(player[0], append(player[1:], f.Name())...).Run()
		os.Remove(f.Name())
	}
}

func playWAVX(wav []byte) {
	select {
	case X.sound <- wav:
	default:
	}
}

// ---------------------------------------------------------------- main

func (a *App) scaleMode() string { a.mu.Lock(); defer a.mu.Unlock(); return a.cfg.Scale }
func (a *App) smooth() bool      { a.mu.Lock(); defer a.mu.Unlock(); return a.cfg.Smooth }

func runGUI(a *App) {
	x := &X
	x.app = a
	x.sound = make(chan []byte, 32)
	go soundLoopX()
	if err := x.connect(); err != nil {
		fmt.Fprintln(os.Stderr, "goterm:", err)
		os.Exit(1)
	}
	defer x.nc.Close()
	plat = Platform{
		Clipboard: x.getClipboard, SetClipboard: x.setClipboard, PlayWAV: playWAVX,
		SetFullscreen: x.setFullscreen,
		Quit:          func() { x.quit = true },
	}
	x.atoms.wmProtocols = x.intern("WM_PROTOCOLS")
	x.atoms.wmDelete = x.intern("WM_DELETE_WINDOW")
	x.atoms.netWMName = x.intern("_NET_WM_NAME")
	x.atoms.utf8 = x.intern("UTF8_STRING")
	x.atoms.netWMState = x.intern("_NET_WM_STATE")
	x.atoms.netFull = x.intern("_NET_WM_STATE_FULLSCREEN")
	x.atoms.clipboard = x.intern("CLIPBOARD")
	x.atoms.targets = x.intern("TARGETS")
	x.atoms.prop = x.intern("GOTERM_SEL")
	x.atoms.netWMIcon = x.intern("_NET_WM_ICON")
	x.atoms.text = x.intern("TEXT")
	x.loadKeymap()

	_, fw, fh := a.Frame()
	scale := 1
	if fw*2+40 < x.scrW && fh*2+80 < x.scrH {
		scale = 2
	}
	x.cw, x.ch = fw*scale, fh*scale
	x.win, x.gc = x.newID(), x.newID()
	// CreateWindow: BackPixel + EventMask
	const evMask = 1 | 4 | 8 | 0x40 | 0x8000 | 0x20000
	cw := make([]byte, 32)
	cw[0] = 1
	le.PutUint32(cw[4:], x.win)
	le.PutUint32(cw[8:], x.root)
	le.PutUint16(cw[16:], uint16(x.cw))
	le.PutUint16(cw[18:], uint16(x.ch))
	le.PutUint16(cw[22:], 1) // InputOutput
	le.PutUint32(cw[28:], 0x2|0x800)
	cw = append(cw, u32s(0, evMask)...)
	le.PutUint16(cw[2:], uint16(len(cw)/4))
	x.write(cw)
	gc := make([]byte, 16)
	gc[0] = 55
	le.PutUint16(gc[2:], 4)
	le.PutUint32(gc[4:], x.gc)
	le.PutUint32(gc[8:], x.win)
	x.write(gc)
	x.setTitle("GoTerm")
	x.changeProp(x.atoms.wmProtocols, 4, 32, u32s(x.atoms.wmDelete))
	x.changeProp(67, 31, 8, []byte("goterm\x00GoTerm\x00")) // WM_CLASS
	if img, err := png.Decode(bytes.NewReader(iconPNG)); err == nil {
		x.setIcon(img)
	}
	mw := []byte{8, 0, 2, 0, 0, 0, 0, 0}
	le.PutUint32(mw[4:], x.win)
	x.write(mw)

	x.evCh = make(chan []byte, 512)
	x.replyCh = make(chan []byte, 8)
	x.selCh = make(chan uint32, 4)
	go x.reader()

	if a.WantFullscreen() {
		x.setFullscreen(true)
	}
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for !x.quit {
		select {
		case ev, ok := <-x.evCh:
			if !ok {
				a.cfg.Save()
				return
			}
			x.handle(ev)
		case <-tick.C:
			if a.Tick() {
				x.draw()
			}
			if t := a.Title(); t != x.title {
				x.title = t
				x.setTitle(t)
			}
		}
	}
	a.cfg.Save()
}

func (x *x11) setIcon(img image.Image) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	data := make([]byte, 8+w*h*4)
	le.PutUint32(data[0:], uint32(w))
	le.PutUint32(data[4:], uint32(h))
	i := 8
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for xx := b.Min.X; xx < b.Max.X; xx++ {
			r, g, bl, al := img.At(xx, y).RGBA()
			// RGBA() is premultiplied; un-premultiply for ARGB32
			if al > 0 && al < 0xffff {
				r, g, bl = r*0xffff/al, g*0xffff/al, bl*0xffff/al
			}
			le.PutUint32(data[i:], al>>8<<24|r>>8<<16|g>>8<<8|bl>>8)
			i += 4
		}
	}
	x.changeProp(x.atoms.netWMIcon, 6, 32, data)
}

func (x *x11) handle(ev []byte) {
	if os.Getenv("GT_X11DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "event %d\n", ev[0]&0x7f)
	}
	a := x.app
	switch ev[0] & 0x7f {
	case 2: // KeyPress
		x.keyPress(int(ev[1]), le.Uint16(ev[28:]))
	case 4, 5, 6: // ButtonPress, ButtonRelease, MotionNotify
		px, py := int(int16(le.Uint16(ev[24:]))), int(int16(le.Uint16(ev[26:])))
		shift := le.Uint16(ev[28:])&1 != 0
		ctrl, alt := le.Uint16(ev[28:])&4 != 0, le.Uint16(ev[28:])&8 != 0
		t := ev[0] & 0x7f
		if t == 6 {
			x.mouse(MouseEvent{Move: true, Shift: shift, Ctrl: ctrl, Alt: alt}, px, py)
			return
		}
		btn := int(ev[1])
		switch btn {
		case 4, 5:
			if t == 4 {
				w := 1
				if btn == 5 {
					w = -1
				}
				x.mouse(MouseEvent{Wheel: w, Shift: shift, Ctrl: ctrl, Alt: alt}, px, py)
			}
		case 1, 2, 3:
			b := map[int]int{1: 0, 2: 1, 3: 2}[btn]
			if t == 4 {
				dbl := false
				now := time.Now()
				if btn == 1 && now.Sub(x.lastClick) < 400*time.Millisecond && abs(px-x.lastX) < 5 && abs(py-x.lastY) < 5 {
					dbl = true
					x.lastClick = time.Time{}
				} else if btn == 1 {
					x.lastClick, x.lastX, x.lastY = now, px, py
				}
				x.mouse(MouseEvent{Button: b, Down: true, Double: dbl, Shift: shift, Ctrl: ctrl, Alt: alt}, px, py)
			} else {
				x.mouse(MouseEvent{Button: b, Up: true, Shift: shift, Ctrl: ctrl, Alt: alt}, px, py)
			}
		}
	case 12: // Expose
		if le.Uint16(ev[16:]) == 0 {
			x.draw()
		}
	case 22: // ConfigureNotify
		w, h := int(le.Uint16(ev[20:])), int(le.Uint16(ev[22:]))
		if w != x.cw || h != x.ch {
			x.cw, x.ch = w, h
			x.draw()
		}
	case 33: // ClientMessage
		if le.Uint32(ev[8:]) == x.atoms.wmProtocols && le.Uint32(ev[12:]) == x.atoms.wmDelete {
			a.cfg.Save()
			x.quit = true
		}
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
