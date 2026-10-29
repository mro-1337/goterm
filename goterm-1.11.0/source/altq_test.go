package main

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func altQ(a *App) { a.OnKey(KeyEvent{Ch: 'q', Alt: true}) }

func TestAltQQuickConnectAndAudioReply(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	a := NewApp(cfg)

	var mu sync.Mutex
	var reply1 string
	closed1 := make(chan struct{})
	h1, p1 := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("Welcome to BBS one\r\n\x1b_SyncTERM:Q;libsndfile\x1b\\"))
		buf := make([]byte, 64)
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, _ := c.Read(buf)
		mu.Lock()
		reply1 = string(buf[:n])
		mu.Unlock()
		c.SetReadDeadline(time.Time{})
		io.Copy(io.Discard, c) // until the client hangs up
		close(closed1)
	})
	got2 := make(chan string, 1)
	h2, p2 := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("Welcome to BBS two\r\n"))
		time.Sleep(300 * time.Millisecond)
		got2 <- "connected"
	})

	altQ(a)
	chr(a, fmt.Sprintf("%s %d", h1, p1))
	key(a, KeyEnter)
	waitFor(t, "bbs1 reply", 4*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return reply1 != ""
	})
	mu.Lock()
	if reply1 != "\x1b[=7;100;1n" {
		t.Fatalf("libsndfile reply %q", reply1)
	}
	mu.Unlock()

	// Alt+Q while connected opens the new system in a second tab; the first stays up.
	altQ(a)
	chr(a, fmt.Sprintf("%s:%d", h2, p2))
	key(a, KeyEnter)
	select {
	case <-got2:
	case <-time.After(3 * time.Second):
		t.Fatal("second connection never made")
	}
	select {
	case <-closed1:
		t.Fatal("first connection must stay connected")
	case <-time.After(200 * time.Millisecond):
	}
	waitFor(t, "screen text", 2*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return strings.Contains(a.term.AllText(), "BBS two")
	})
	a.mu.Lock()
	if len(a.tabs) != 2 || a.cur != 1 {
		t.Fatalf("tabs=%d cur=%d", len(a.tabs), a.cur)
	}
	a.mu.Unlock()

	// Back to tab 1: still shows BBS one; frame grows by the tab bar.
	_, _, h2px := a.Frame()
	a.OnKey(KeyEvent{Ch: '1', Alt: true})
	a.mu.Lock()
	txt := a.term.AllText()
	a.mu.Unlock()
	if !strings.Contains(txt, "BBS one") || strings.Contains(txt, "BBS two") {
		t.Fatalf("tab 1 screen wrong: %q", txt)
	}
	_, _, h1px := a.Frame()
	if h1px != h2px {
		t.Fatal("frame height changed between tabs")
	}
	// Closing tab 1 hangs it up and leaves tab 2.
	a.OnKey(KeyEvent{Ch: 'w', Alt: true})
	key(a, KeyLeft) // Yes
	key(a, KeyEnter)
	select {
	case <-closed1:
	case <-time.After(3 * time.Second):
		t.Fatal("first connection not dropped on close")
	}
	// The address isn't in the directory, so a save prompt keeps the tab; decline, then close it.
	key(a, KeyEsc)
	a.OnKey(KeyEvent{Ch: 'w', Alt: true})
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tabs) != 1 || !strings.Contains(a.term.AllText(), "BBS two") {
		t.Fatalf("after close: tabs=%d", len(a.tabs))
	}
}

func TestTabCloseButton(t *testing.T) {
	setupPlat()
	a := NewApp(newDefaultConfig())
	a.mu.Lock()
	a.newTab()
	a.newTab()
	a.mu.Unlock()
	a.Frame()
	cw, ch := a.cellW, a.cellH
	a.mu.Lock()
	sp := a.tabSpans(a.term.Cols)
	a.mu.Unlock()
	// click the x of tab 2 (middle)
	a.OnMouse(MouseEvent{X: (sp[1][1]-3)*cw + 1, Y: ch / 2, Button: 0, Down: true})
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tabs) != 2 {
		t.Fatalf("tabs=%d after close click", len(a.tabs))
	}
}
