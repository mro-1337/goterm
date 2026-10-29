package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestMouseReports(t *testing.T) {
	p := NewParser(NewScreen(80, 25, 8, 16))
	var out bytes.Buffer
	p.Reply = func(b []byte) { out.Write(b) }
	take := func() string { s := out.String(); out.Reset(); return s }

	p.Feed([]byte("\x1b[?1000$p"))
	if g := take(); g != "\x1b[?1000;2$y" {
		t.Fatalf("decrqm off %q", g)
	}
	p.Feed([]byte("\x1b[?1000h\x1b[?1000$p\x1b[?1006$p\x1b[<c"))
	if g := take(); g != "\x1b[?1000;1$y\x1b[?1006;2$y\x1b[<0;2;4;7c" {
		t.Fatalf("decrqm/ctda %q", g)
	}
	// legacy encoding: press left at col 10,row 5 (0-based); release
	if r := string(p.mouseReport(MouseEvent{Button: 0, Down: true}, 10, 5, 0, 0, 0)); r != "\x1b[M +&" {
		t.Fatalf("x10 press %q", r)
	}
	if r := string(p.mouseReport(MouseEvent{Button: 0, Up: true}, 10, 5, 0, 0, 1)); r != "\x1b[M#+&" {
		t.Fatalf("release %q", r)
	}
	if r := string(p.mouseReport(MouseEvent{Wheel: 1}, 0, 0, 0, 0, 0)); r != "\x1b[M`!!" {
		t.Fatalf("wheel %q", r)
	}
	if r := p.mouseReport(MouseEvent{Move: true}, 1, 1, 0, 0, 0); r != nil {
		t.Fatal("motion must not be reported in 1000")
	}
	// SGR + modifiers
	p.Feed([]byte("\x1b[?1006h\x1b[?1003h"))
	if r := string(p.mouseReport(MouseEvent{Button: 2, Down: true, Ctrl: true}, 79, 24, 0, 0, 0)); r != "\x1b[<18;80;25M" {
		t.Fatalf("sgr press %q", r)
	}
	if r := string(p.mouseReport(MouseEvent{Button: 2, Up: true}, 79, 24, 0, 0, 4)); r != "\x1b[<2;80;25m" {
		t.Fatalf("sgr release %q", r)
	}
	if r := string(p.mouseReport(MouseEvent{Move: true}, 3, 4, 0, 0, 0)); r != "\x1b[<35;4;5M" {
		t.Fatalf("hover %q", r)
	}
	if r := string(p.mouseReport(MouseEvent{Move: true}, 3, 4, 0, 0, 1)); r != "\x1b[<32;4;5M" {
		t.Fatalf("drag %q", r)
	}
	// pixel mode
	p.Feed([]byte("\x1b[?1016h"))
	if r := string(p.mouseReport(MouseEvent{Button: 0, Down: true}, 3, 4, 27, 70, 0)); r != "\x1b[<0;28;71M" {
		t.Fatalf("pixel %q", r)
	}
	// X10 mode: press only
	p.Feed([]byte("\x1b[?1016l\x1b[?1006l\x1b[?9h"))
	if p.MouseMode != 9 || p.mouseReport(MouseEvent{Button: 0, Up: true}, 0, 0, 0, 0, 1) != nil {
		t.Fatal("x10")
	}
	p.Feed([]byte("\x1b[?9l"))
	if p.MouseMode != 0 {
		t.Fatal("mouse not off")
	}
}

func TestMouseEndToEnd(t *testing.T) {
	setupPlat()
	a := NewApp(newDefaultConfig())
	var mu sync.Mutex
	var got []byte
	h, p := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("menu\r\n\x1b[?1000h\x1b[?1006h"))
		go func() {
			buf := make([]byte, 256)
			for {
				n, err := c.Read(buf)
				mu.Lock()
				got = append(got, buf[:n]...)
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		time.Sleep(1500 * time.Millisecond)
		io.Copy(io.Discard, c)
	})
	altQ(a)
	chr(a, fmt.Sprintf("%s %d", h, p))
	key(a, KeyEnter)
	waitFor(t, "mouse mode on", 3*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.parser != nil && a.parser.MouseMode == 1000
	})
	a.Frame()
	cw, ch := a.cellW, a.cellH
	a.OnMouse(MouseEvent{X: 10*cw + 1, Y: 5*ch + 1, Button: 0, Down: true})
	a.OnMouse(MouseEvent{X: 10*cw + 1, Y: 5*ch + 1, Button: 0, Up: true})
	waitFor(t, "reports", 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return string(got) == "\x1b[<0;11;6M\x1b[<0;11;6m"
	})
	// Shift bypasses the host: local selection, nothing sent.
	a.OnMouse(MouseEvent{X: 1, Y: 1, Button: 0, Down: true, Shift: true})
	a.OnMouse(MouseEvent{X: 40, Y: 1, Move: true, Shift: true})
	time.Sleep(100 * time.Millisecond)
	a.mu.Lock()
	sel := a.selecting
	a.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	if !sel || string(got) != "\x1b[<0;11;6M\x1b[<0;11;6m" {
		t.Fatalf("shift bypass: selecting=%v got=%q", sel, got)
	}
}
