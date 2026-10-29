package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func sbTestTerm() (*Screen, *Parser) {
	s := NewScreen(80, 25, 8, 16)
	s.SbMax = 1000
	s.KeepOnClear = true
	return s, NewParser(s)
}

func TestScrollbackKeepOnClear(t *testing.T) {
	s, p := sbTestTerm()
	p.Feed([]byte("\x1b[2J\x1b[HLogin screen alpha\r\nsecond line"))
	p.Feed([]byte("\x1b[2J\x1b[Hmain menu"))
	if s.SbLen() != 2 {
		t.Fatalf("sb=%d want 2", s.SbLen())
	}
	// identical repeated redraw must not flood scrollback
	p.Feed([]byte("\x1b[2J\x1b[Hmain menu\x1b[2J\x1b[Hmain menu"))
	if s.SbLen() != 3 { // only "main menu" pushed once more
		t.Fatalf("sb=%d want 3", s.SbLen())
	}
	txt := s.ScrollbackText()
	if !strings.Contains(txt, "Login screen alpha") || !strings.Contains(txt, "second line") || !strings.HasSuffix(txt, "main menu\r\n") {
		t.Fatalf("text %q", txt)
	}
	s.KeepOnClear = false
	n := s.SbLen()
	p.Feed([]byte("\x1b[2Jx\x1b[2J"))
	if s.SbLen() != n {
		t.Fatal("pushed with KeepOnClear off")
	}
}

func TestScrollbackFind(t *testing.T) {
	s, p := sbTestTerm()
	for i := 0; i < 100; i++ {
		p.Feed([]byte("line number " + string(rune('A'+i%26)) + "\r\n"))
	}
	p.Feed([]byte("NEEDLE here\r\n"))
	for i := 0; i < 60; i++ {
		p.Feed([]byte("filler\r\n"))
	}
	abs, ok := s.Find("needle", s.absLineAtView(0)+int64(s.Rows), -1)
	if !ok || !s.SelActive || s.ViewOffset == 0 {
		t.Fatalf("not found ok=%v off=%d", ok, s.ViewOffset)
	}
	if got := s.SelectedText(); got != "NEEDLE" {
		t.Fatalf("selected %q", got)
	}
	if _, ok := s.Find("needle", abs, -1); ok {
		t.Fatal("second match should not exist")
	}
	if _, ok := s.Find("nonexistent", abs, -1); ok {
		t.Fatal("bogus match")
	}
}

func TestScrollbackUI(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	cfg.DownDir = t.TempDir()
	host, port := fakeBBS(t, func(c net.Conn) {
		var b strings.Builder
		for i := 0; i < 80; i++ {
			fmt.Fprintf(&b, "history line %d\r\n", i)
		}
		b.WriteString("\x1b[2J\x1b[HFRESH SCREEN\r\n")
		c.Write([]byte(b.String()))
		io.Copy(io.Discard, c)
	})
	cfg.Entries = []DialEntry{{Name: "SB test", Host: host, Port: port, Conn: "telnet", Mode: "80x25"}}
	a := NewApp(cfg)
	key(a, KeyEnter)
	waitFor(t, "screen", 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.sess != nil && !a.sess.dialing && strings.Contains(a.term.AllText(), "FRESH SCREEN")
	})
	time.Sleep(200 * time.Millisecond)
	live := shot(t, a, "sb0_live")
	a.OnKey(KeyEvent{Key: KeyPgUp, Ctrl: true})
	back := shot(t, a, "sb1_back")
	if equalFB(live, back) {
		t.Fatal("view did not change")
	}
	a.mu.Lock()
	if a.term.ViewOffset == 0 || !strings.Contains(a.term.ScrollbackText(), "history line 3") {
		t.Fatalf("offset %d", a.term.ViewOffset)
	}
	a.mu.Unlock()
	// search
	a.OnKey(KeyEvent{Ch: '/'})
	chr(a, "line 12")
	key(a, KeyEnter)
	shot(t, a, "sb2_found")
	a.mu.Lock()
	sel := a.term.SelectedText()
	a.mu.Unlock()
	if sel != "line 12" {
		t.Fatalf("selected %q", sel)
	}
	// save
	key(a, KeyF2)
	ents, _ := os.ReadDir(cfg.DownDir)
	if len(ents) != 1 {
		t.Fatalf("saved files: %d", len(ents))
	}
	key(a, KeyEnter) // dismiss message
	key(a, KeyEsc)
	a.mu.Lock()
	if a.term.ViewOffset != 0 {
		t.Fatal("Esc did not return to live")
	}
	a.mu.Unlock()
}

func equalFB(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDirectorySearch(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	cfg.Entries = []DialEntry{
		{Name: "Alpha BBS", Host: "alpha.example.com", Port: 23, Conn: "telnet", Mode: "80x25"},
		{Name: "Level 29", Host: "bbs.fozztexx.com", Port: 23, Conn: "telnet", Mode: "80x25", Notes: "Retro fun"},
		{Name: "Secure Box", Host: "box.example.org", Port: 22, Conn: "ssh", Mode: "80x25"},
	}
	a := NewApp(cfg)
	a.OnKey(KeyEvent{Ch: '/'})
	for _, r := range "EXAMPLE" {
		a.OnKey(KeyEvent{Ch: r})
	}
	a.mu.Lock()
	vis := a.dir.visible(a)
	a.mu.Unlock()
	if len(vis) != 2 || vis[0] != 0 || vis[1] != 2 {
		t.Fatalf("vis %v", vis)
	}
	a.OnKey(KeyEvent{Key: KeyDown})
	a.mu.Lock()
	if a.dir.Sel != 2 {
		t.Fatalf("sel %d", a.dir.Sel)
	}
	a.mu.Unlock()
	for _, r := range " ssh" {
		a.OnKey(KeyEvent{Ch: r})
	}
	a.mu.Lock()
	vis = a.dir.visible(a)
	a.mu.Unlock()
	if len(vis) != 1 || vis[0] != 2 {
		t.Fatalf("vis2 %v", vis)
	}
	shot(t, a, "dir_search")
	a.OnKey(KeyEvent{Key: KeyEsc})
	a.mu.Lock()
	if a.dir.Filter != "" || a.dir.Searching || len(a.dir.visible(a)) != 3 {
		t.Fatal("esc did not clear")
	}
	a.mu.Unlock()
	// notes and port are searchable
	a.OnKey(KeyEvent{Ch: '/'})
	for _, r := range "retro" {
		a.OnKey(KeyEvent{Ch: r})
	}
	a.mu.Lock()
	if v := a.dir.visible(a); len(v) != 1 || v[0] != 1 {
		t.Fatalf("notes %v", v)
	}
	a.mu.Unlock()
}

func TestDirectoryShadowIgnoresOldTerminal(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	a := NewApp(cfg)
	blank := shot(t, a, "dir_blank")
	a.mu.Lock()
	a.parser = a.newParser(a.term, nil)
	for i := 0; i < 25; i++ {
		a.parser.Feed([]byte("OLD SESSION TEXT OLD SESSION TEXT OLD SESSION TEXT OLD SESSION TEXT OLD SESSION TEXT\r\n"))
	}
	a.mu.Unlock()
	dirty := shot(t, a, "dir_after_session")
	if !equalFB(blank, dirty) {
		t.Fatal("old terminal contents show through the dialing directory")
	}
}

func TestParseStartArgs(t *testing.T) {
	cases := []struct {
		in     []string
		target string
		bad    bool
	}{
		{nil, "", false},
		{[]string{"bbs.example.com:2323"}, "bbs.example.com:2323", false},
		{[]string{"bbs.example.com", "2323"}, "bbs.example.com:2323", false},
		{[]string{"-mode", "80x50", "ssh://me@host"}, "ssh://me@host", false},
		{[]string{"-bogus"}, "", true},
		{[]string{"a", "b"}, "", true},
	}
	for _, c := range cases {
		s := parseStartArgs(c.in)
		if s.Target != c.target || s.Bad != c.bad {
			t.Errorf("%v: got %+v", c.in, s)
		}
	}
	cfg := newDefaultConfig()
	e := entryFromTarget(cfg, "bbs.example.com:2323")
	if e.Host != "bbs.example.com" || e.Port != 2323 || e.Conn != "telnet" {
		t.Fatalf("%+v", e)
	}
	e = entryFromTarget(cfg, "ssh://bob@box.org")
	if e.Host != "box.org" || e.Port != 22 || e.User != "bob" || e.Conn != "ssh" {
		t.Fatalf("%+v", e)
	}
}

func TestPasteAndSaveOnDisconnect(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	cfg.Entries = nil
	var mu sync.Mutex
	var got []byte
	host, port := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("hello\r\n"))
		b := make([]byte, 512)
		for {
			n, err := c.Read(b)
			mu.Lock()
			got = append(got, b[:n]...)
			mu.Unlock()
			if err != nil {
				return
			}
		}
	})
	a := NewApp(cfg)
	e := entryFromTarget(cfg, fmt.Sprintf("%s:%d", host, port))
	a.mu.Lock()
	a.modals = nil
	a.connect(e, -1)
	a.mu.Unlock()
	waitFor(t, "connected", 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.sess != nil && !a.sess.dialing && a.sess.conn != nil
	})
	clip = "pasted text\nsecond"
	a.OnKey(KeyEvent{Ch: 22, Ctrl: true}) // Ctrl+V
	waitFor(t, "paste arrives", 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(string(got), "pasted text\rsecond")
	})
	// Ctrl+C with a selection copies instead of sending ^C
	a.mu.Lock()
	a.term.SelA, a.term.SelB, a.term.SelActive = selPoint{a.term.absLineAtView(0), 0}, selPoint{a.term.absLineAtView(0), 4}, true
	a.mu.Unlock()
	clip = ""
	a.OnKey(KeyEvent{Ch: 3, Ctrl: true})
	if clip != "Conne" {
		t.Fatalf("copied %q", clip)
	}
	// hang up -> offer to save
	a.mu.Lock()
	a.hangupNow()
	top := a.modals[len(a.modals)-1]
	a.mu.Unlock()
	if _, ok := top.(*FormModal); !ok {
		t.Fatalf("expected save prompt, got %T", top)
	}
	shot(t, a, "save_prompt")
	chr(a, "My Test")
	a.OnKey(KeyEvent{Key: KeyEnter})
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cfg.Entries) != 1 || !strings.Contains(a.cfg.Entries[0].Name, "My Test") || a.cfg.Entries[0].Host != host {
		t.Fatalf("entries %+v", a.cfg.Entries)
	}
}
