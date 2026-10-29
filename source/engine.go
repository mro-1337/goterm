package main

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// The engine is GoTerm without a window: the Android app (a thin Kotlin
// shell) starts it as a child process and talks to it over stdin/stdout
// with length-prefixed messages. The engine renders frames, handles the
// network, protocols, audio mixing and all menus; the shell only shows the
// frames, forwards touch/keyboard input and plays the audio it is sent.
//
// Message framing: type u8, length u32 (big endian), payload.
//
// Engine -> shell:
//   1 FRAME   w u32, h u32, then w*h pixels as little-endian ARGB uint32
//   2 TITLE   utf-8
//   3 CLIPSET utf-8 text to put on the Android clipboard
//   4 CLIPREQ (empty) shell answers with CLIPREPLY
//   5 WAV     a complete WAV file to play (ANSI music, bell)
//   6 PCM     S16LE stereo 44100 Hz samples (BBS audio stream)
//   7 QUIT
// Shell -> engine:
//   1 KEY     key u8 (0 = none, see engineKeys), ch u32 rune, flags u8 (1 ctrl, 2 alt, 4 shift)
//   2 MOUSE   flags u8 (1 down, 2 up, 4 move, 8 double, 16 shift, 32 ctrl, 64 alt),
//             button u8, wheel i8, x i32, y i32 (framebuffer pixels)
//   3 CLIPREPLY utf-8
//   4 TEXT    utf-8 text typed on the soft keyboard
//   5 PAUSE / 6 RESUME   app went to the background / foreground
//   7 CONNECT utf-8 target (host:port, ssh://user@host, directory entry name)

const (
	eFrame = 1 + iota
	eTitle
	eClipSet
	eClipReq
	eWAV
	ePCM
	eQuit
)

const (
	iKey = 1 + iota
	iMouse
	iClipReply
	iText
	iPause
	iResume
	iConnect
)

type engineIO struct {
	mu  sync.Mutex
	w   *bufio.Writer
	clk chan string
}

var engineOut *engineIO

func (e *engineIO) send(typ byte, parts ...[]byte) error {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var h [5]byte
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(n))
	e.w.Write(h[:])
	for _, p := range parts {
		e.w.Write(p)
	}
	return e.w.Flush()
}

// engineSink forwards the audio mixer's PCM to the shell, paced to real
// time so the shell's player never needs to apply back-pressure.
type engineSink struct {
	next time.Time
}

var sinkOverride func() audioSink

func newSinkFunc() func() audioSink {
	if sinkOverride != nil {
		return sinkOverride
	}
	return platformAudioSink()
}

func (s *engineSink) Write(p []byte) error {
	if engineOut == nil {
		return nil
	}
	if s.next.IsZero() || time.Since(s.next) > 200*time.Millisecond {
		s.next = time.Now()
	}
	if err := engineOut.send(ePCM, p); err != nil {
		return err
	}
	s.next = s.next.Add(time.Duration(len(p)/4) * time.Second / apRate)
	if d := time.Until(s.next) - 150*time.Millisecond; d > 0 {
		time.Sleep(d)
	}
	return nil
}
func (s *engineSink) Close() {}

var engineKeys = []Key{KeyNone, KeyUp, KeyDown, KeyLeft, KeyRight, KeyHome, KeyEnd, KeyPgUp, KeyPgDn,
	KeyIns, KeyDel, KeyEnter, KeyEsc, KeyBS, KeyTab, KeyF1, KeyF2, KeyF3, KeyF4, KeyF5, KeyF6, KeyF7,
	KeyF8, KeyF9, KeyF10, KeyF11, KeyF12}

// runEngine serves the shell protocol until in is closed or the app quits.
func runEngine(a *App, in io.Reader, out io.Writer) {
	e := &engineIO{w: bufio.NewWriterSize(out, 1<<20), clk: make(chan string, 1)}
	engineOut = e
	sinkOverride = func() audioSink { return &engineSink{} }
	done := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(done) }) }
	paused := false
	var pmu sync.Mutex

	plat = Platform{
		SetClipboard: func(s string) { e.send(eClipSet, []byte(s)) },
		Clipboard: func() string {
			select {
			case <-e.clk:
			default:
			}
			if e.send(eClipReq) != nil {
				return ""
			}
			select {
			case s := <-e.clk:
				return s
			case <-time.After(1500 * time.Millisecond):
				return ""
			}
		},
		PlayWAV: func(b []byte) { go e.send(eWAV, b) },
		Quit: func() {
			e.send(eQuit)
			finish()
		},
	}

	// Input.
	go func() {
		defer finish()
		r := bufio.NewReaderSize(in, 1<<16)
		var h [5]byte
		for {
			if _, err := io.ReadFull(r, h[:]); err != nil {
				return
			}
			n := binary.BigEndian.Uint32(h[1:])
			if n > 64<<20 {
				return
			}
			p := make([]byte, n)
			if _, err := io.ReadFull(r, p); err != nil {
				return
			}
			switch h[0] {
			case iKey:
				if len(p) < 6 {
					continue
				}
				var ev KeyEvent
				if int(p[0]) < len(engineKeys) {
					ev.Key = engineKeys[p[0]]
				}
				ev.Ch = rune(binary.BigEndian.Uint32(p[1:]))
				ev.Ctrl, ev.Alt, ev.Shift = p[5]&1 != 0, p[5]&2 != 0, p[5]&4 != 0
				a.OnKey(ev)
			case iText:
				for _, r := range string(p) {
					switch r {
					case '\n', '\r':
						a.OnKey(KeyEvent{Key: KeyEnter})
					case '\t':
						a.OnKey(KeyEvent{Key: KeyTab})
					case 0x7f, '\b':
						a.OnKey(KeyEvent{Key: KeyBS})
					default:
						a.OnKey(KeyEvent{Ch: r})
					}
				}
			case iMouse:
				if len(p) < 11 {
					continue
				}
				f := p[0]
				ev := MouseEvent{
					Down: f&1 != 0, Up: f&2 != 0, Move: f&4 != 0, Double: f&8 != 0,
					Shift: f&16 != 0, Ctrl: f&32 != 0, Alt: f&64 != 0,
					Button: int(p[1]), Wheel: int(int8(p[2])),
					X: int(int32(binary.BigEndian.Uint32(p[3:]))), Y: int(int32(binary.BigEndian.Uint32(p[7:]))),
				}
				a.OnMouse(ev)
			case iClipReply:
				select {
				case e.clk <- string(p):
				default:
				}
			case iPause:
				pmu.Lock()
				paused = true
				pmu.Unlock()
			case iConnect:
				a.mu.Lock()
				if t := strings.TrimSpace(string(p)); t != "" {
					e := entryFromTarget(a.cfg, t)
					if a.sess != nil {
						a.newTab()
					}
					a.connect(e, -1)
				}
				a.mu.Unlock()
			case iResume:
				pmu.Lock()
				paused = false
				pmu.Unlock()
				a.mu.Lock()
				a.dirty = true
				a.mu.Unlock()
			}
		}
	}()

	// Output: frames whenever something changed.
	lastTitle := "\x00"
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	var buf []byte
	for {
		select {
		case <-done:
			if a.cfg != nil {
				a.mu.Lock()
				a.cfg.Save()
				a.mu.Unlock()
			}
			return
		case <-tick.C:
		}
		pmu.Lock()
		p := paused
		pmu.Unlock()
		if p || !a.Tick() {
			continue
		}
		fb, w, h := a.Frame()
		if need := w * h * 4; len(buf) < need {
			buf = make([]byte, need)
		}
		for i, px := range fb {
			binary.LittleEndian.PutUint32(buf[i*4:], px|0xFF000000)
		}
		var hdr [8]byte
		binary.BigEndian.PutUint32(hdr[0:], uint32(w))
		binary.BigEndian.PutUint32(hdr[4:], uint32(h))
		if e.send(eFrame, hdr[:], buf[:w*h*4]) != nil {
			return
		}
		if t := a.Title(); t != lastTitle {
			lastTitle = t
			e.send(eTitle, []byte(t))
		}
	}
}

// engineDNS points Go's resolver at DNS servers given by the Android shell
// (Android has no /etc/resolv.conf). GOTERM_DNS="1.2.3.4,8.8.8.8".
func engineEnvSetup() {
	if d := os.Getenv("GOTERM_DOWNDIR"); d != "" {
		engineDownDir = d
	}
	setupDNS(os.Getenv("GOTERM_DNS"))
}

var engineDownDir string
