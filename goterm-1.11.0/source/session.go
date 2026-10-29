package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goterm/xmodem"
	"goterm/zmodem"
)

type Session struct {
	a       *App
	entry   DialEntry
	conn    Conn
	in      chan []byte
	jobs    chan func()
	stop    chan struct{}
	once    sync.Once
	stopped bool
	dialing bool
	local   bool
	busy    bool

	term   *Screen
	parser *Parser
	audio  *AudioAPC
	active bool // activity while this tab was in the background

	offerSave bool // connected to an address that is not in the directory

	carry    []byte
	upPrompt bool
	upIgnore time.Time
	pm       *ProgressModal
}

func newSession(a *App, e DialEntry) *Session {
	return &Session{a: a, entry: e, in: make(chan []byte, 64), jobs: make(chan func(), 8), stop: make(chan struct{})}
}

func (s *Session) close() {
	s.once.Do(func() {
		s.stopped = true
		close(s.stop)
		if s.conn != nil {
			s.conn.Close()
		}
	})
}

func (s *Session) send(b []byte) {
	if s.conn != nil && !s.stopped {
		s.conn.Write(b)
	}
}

// connect starts a new session (called with a.mu held).
func (a *App) connect(e DialEntry, idx int) {
	if a.sess != nil {
		return
	}
	m := screenModeByName(e.Mode)
	a.term = a.newTerm(m)
	a.audio = NewAudioAPC()
	a.audio.sinkOpen = newSinkFunc()
	a.parser = a.newParser(a.term, &e)
	s := newSession(a, e)
	s.term, s.parser, s.audio = a.term, a.parser, a.audio
	s.offerSave = idx < 0
	a.parser.Reply = func(b []byte) { s.send(b) }
	a.audio.Send = func(b []byte) { s.send(b) }
	a.audio.CacheDir = cacheDirFor(e.Host, e.Port)
	a.audio.Muted = !a.cfg.Sound || e.NoSound
	a.sess = s
	a.modals = nil
	s.dialing = true
	if idx >= 0 {
		a.cfg.Entries[idx].Count++
		a.cfg.Entries[idx].Last = time.Now().Format("2006-01-02 15:04")
		a.cfg.Save()
	}
	s.parser.Feed([]byte(fmt.Sprintf("\x1b[0;37mConnecting to \x1b[1;36m%s:%d\x1b[0;37m ...  (Esc cancels)\r\n", e.Host, e.Port)))
	a.dirty = true
	go s.dial()
}

// askPassword prompts for an SSH password (called off the UI goroutine).
func (s *Session) askPassword() (string, error) {
	a := s.a
	ch := make(chan string, 1)
	a.mu.Lock()
	s.onTab(func() {
		a.passwordBox("SSH password", "Password for "+orStr(s.entry.User, "guest")+"@"+s.entry.Host+":",
			func(a *App, p string) { ch <- p },
			func(a *App) { close(ch) })
	})
	a.dirty = true
	a.mu.Unlock()
	select {
	case p, ok := <-ch:
		if !ok {
			return "", errors.New("password entry cancelled")
		}
		return p, nil
	case <-s.stop:
		return "", errors.New("cancelled")
	}
}

func (s *Session) dial() {
	a := s.a
	addr := net.JoinHostPort(s.entry.Host, strconv.Itoa(s.entry.Port))
	term := orStr(s.entry.Term, a.cfg.Term)
	a.mu.Lock()
	cols, rows := s.term.Cols, s.term.Rows
	a.mu.Unlock()
	var tc Conn
	var err error
	switch s.entry.Conn {
	case "ssh":
		tc, err = DialSSH(addr, s.entry.User, s.entry.Password, term, cols, rows, 15*time.Second, s.askPassword)
	case "rlogin":
		tc, err = DialRLogin(addr, s.entry.User, term, 15*time.Second)
	default:
		var t *TelnetConn
		t, err = DialTelnet(addr, 15*time.Second)
		if err == nil {
			t.Raw = s.entry.Conn == "raw"
			t.SetTermType(term)
			t.SetSize(cols, rows)
			tc = t
		}
	}
	if err != nil {
		tc = nil
	}
	a.mu.Lock()
	if s.stopped {
		a.mu.Unlock()
		if tc != nil {
			tc.Close()
		}
		return
	}
	s.dialing = false
	if err != nil {
		s.parser.Feed([]byte("\r\n\x1b[1;31mConnection failed: " + err.Error() + "\x1b[0m\r\n"))
		s.onTab(func() { a.errorBoxThen("Could not connect: "+err.Error(), func(a *App) { a.hangupNow() }) })
		a.mu.Unlock()
		return
	}
	s.conn = tc
	s.parser.Feed([]byte("\x1b[1;32mConnected.\x1b[0m\r\n"))
	a.mu.Unlock()
	go s.reader()
	s.run()
}

func (a *App) errorBoxThen(text string, f func(a *App)) {
	a.push(&MsgModal{Title: "Error", Lines: wrapLines(text, 44), Role: rError, Buttons: []string{"OK"},
		OnPick: func(a *App, _ int) { f(a) }})
}

func (s *Session) reader() {
	buf := make([]byte, 16384)
	for {
		n, err := s.conn.Read(buf)
		if n > 0 {
			c := append([]byte(nil), buf[:n]...)
			select {
			case s.in <- c:
			case <-s.stop:
				return
			}
		}
		if err != nil {
			close(s.in)
			return
		}
	}
}

func (s *Session) run() {
	for {
		var flush <-chan time.Time
		if len(s.carry) > 0 {
			flush = time.After(300 * time.Millisecond)
		}
		select {
		case job := <-s.jobs:
			job()
		case b, ok := <-s.in:
			if !ok {
				s.onClosed()
				return
			}
			s.handle(b)
		case <-flush:
			c := s.carry
			s.carry = nil
			s.feed(c)
		case <-s.stop:
			return
		}
	}
}

func (s *Session) onClosed() {
	a := s.a
	a.mu.Lock()
	defer a.mu.Unlock()
	if s.stopped {
		return
	}
	s.parser.Feed([]byte("\r\n\x1b[0;1;31m[Connection closed]\x1b[0m\r\n"))
	s.active = true
	s.onTab(func() {
		a.push(&MsgModal{Title: "Disconnected", Lines: []string{"The connection was closed by the host."},
			Role: rFrame, Buttons: []string{"OK"}, OnPick: func(a *App, _ int) { a.hangupNow() }})
	})
}

// feed passes data to the terminal, honoring foreground-music waits.
func (s *Session) feed(data []byte) {
	a := s.a
	for len(data) > 0 {
		a.mu.Lock()
		if s.stopped {
			a.mu.Unlock()
			return
		}
		n := s.parser.Feed(data)
		wait := s.parser.WaitMs
		s.parser.WaitMs = 0
		waitCh := s.parser.WaitCh
		s.parser.WaitCh = 0
		if a.sess != s {
			s.active = true
		}
		a.dirty = true
		a.mu.Unlock()
		if n <= 0 {
			n = len(data)
		}
		data = data[n:]
		if waitCh > 0 {
			s.audio.WaitIdle(waitCh, s.stop)
		}
		if wait > 0 {
			select {
			case <-time.After(time.Duration(wait) * time.Millisecond):
			case <-s.stop:
				return
			}
		}
	}
}

// hexHeaderEnd returns the length of a complete ZMODEM hex header that
// starts at b[0] (the first '*'), or 0 if more data is needed.
func hexHeaderEnd(b []byte) int {
	i := 0
	for i < len(b) && b[i] == '*' {
		i++
	}
	if i+2 > len(b) || b[i] != 0x18 || b[i+1] != 'B' {
		return 0
	}
	i += 2 + 14
	if i > len(b) {
		return 0
	}
	for i < len(b) && (b[i] == 0x0d || b[i] == 0x8d || b[i] == 0x0a || b[i] == 0x8a || b[i] == 0x11 || b[i] == 0x91) {
		i++
	}
	return i
}

func (s *Session) handle(b []byte) {
	data := append(s.carry, b...)
	s.carry = nil
	for len(data) > 0 {
		idx, ft, complete := zmodem.Detect(data)
		if idx < 0 {
			s.feed(data)
			return
		}
		if idx > 0 {
			s.feed(data[:idx])
		}
		rest := data[idx:]
		if !complete {
			s.carry = append([]byte(nil), rest...)
			return
		}
		switch ft {
		case zmodem.ZRQINIT, zmodem.ZFILE:
			s.doReceive(rest)
			return
		case zmodem.ZRINIT:
			n := hexHeaderEnd(rest)
			if n == 0 {
				s.carry = append([]byte(nil), rest...)
				return
			}
			if !s.upPrompt && time.Now().After(s.upIgnore) {
				s.promptUpload(append([]byte(nil), rest[:n]...))
			}
			data = rest[n:]
			continue
		default:
			s.feed(rest)
			return
		}
	}
}

func (s *Session) promptUpload(hdr []byte) {
	a := s.a
	s.upPrompt = true
	a.mu.Lock()
	defer a.mu.Unlock()
	p := newPicker("Host is ready to receive - choose file(s) to upload", a.cfg.LastDir, func(a *App, paths []string) {
		a.cfg.LastDir = dirOf(paths[0])
		s.jobs <- func() { s.upPrompt = false; s.doSendPrefix(paths, hdr) }
	})
	p.OnCancel = func(a *App) {
		s.jobs <- func() {
			s.upPrompt = false
			s.upIgnore = time.Now().Add(5 * time.Second)
			s.send([]byte{0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 0x18, 8, 8, 8, 8, 8, 8, 8, 8})
		}
	}
	s.onTab(func() { a.push(p) })
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return p
	}
	return p[:i]
}

// ---------------------------------------------------------------------
// ZMODEM glue
// ---------------------------------------------------------------------

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

type chanLink struct {
	s        *Session
	buf      []byte
	deadline time.Time
	rxN, txN int
	rxS, txS []byte // first bytes in each direction, for the trace log
}

const traceBytes = 384

func (l *chanLink) note(dst *[]byte, b []byte) {
	if len(*dst) < traceBytes {
		n := traceBytes - len(*dst)
		if n > len(b) {
			n = len(b)
		}
		*dst = append(*dst, b[:n]...)
	}
}

func (l *chanLink) Read(p []byte) (int, error) {
	if len(l.buf) == 0 {
		var tmo <-chan time.Time
		if !l.deadline.IsZero() {
			d := time.Until(l.deadline)
			if d < 0 {
				d = 0
			}
			t := time.NewTimer(d)
			defer t.Stop()
			tmo = t.C
		}
		select {
		case b, ok := <-l.s.in:
			if !ok {
				return 0, io.EOF
			}
			l.buf = b
		case <-tmo:
			return 0, timeoutErr{}
		case <-l.s.stop:
			return 0, io.EOF
		}
	}
	n := copy(p, l.buf)
	l.note(&l.rxS, p[:n])
	l.rxN += n
	l.buf = l.buf[n:]
	return n, nil
}

func (l *chanLink) Write(p []byte) (int, error) {
	if l.s.conn == nil || l.s.stopped {
		return 0, io.ErrClosedPipe
	}
	l.note(&l.txS, p)
	l.txN += len(p)
	return l.s.conn.Write(p)
}

func (l *chanLink) SetReadDeadline(t time.Time) error { l.deadline = t; return nil }

func (s *Session) startXfer(sending bool) *chanLink {
	a := s.a
	a.mu.Lock()
	defer a.mu.Unlock()
	s.busy = true
	a.xfer = xferState{Sending: sending}
	a.xferStart, a.xferBase, a.xferAbort = time.Now(), 0, false
	s.pm = &ProgressModal{}
	s.onTab(func() { a.push(s.pm) })
	return &chanLink{s: s}
}

func (s *Session) hooks() *zmodem.Hooks {
	a := s.a
	return &zmodem.Hooks{
		Log:   func(m string) { s.tracef("  %s", m) },
		Abort: func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.xferAbort || s.stopped },
		Progress: func(p zmodem.Progress) {
			a.mu.Lock()
			if p.Name != a.xfer.Name || p.FileIndex != a.xfer.FileIndex {
				a.xferStart, a.xferBase = time.Now(), p.Done
			}
			a.xfer = xferState{p.Name, p.Done, p.Total, p.FileIndex, p.FileCount, p.Sending}
			a.dirty = true
			a.mu.Unlock()
		},
	}
}

func (s *Session) endXfer(l *chanLink, left []byte, title, okText string, files []string, err error) {
	a := s.a
	s.tracef("%s finished: err=%v files=%v rx=%d tx=%d", title, err, files, l.rxN, l.txN)
	s.tracef("  first bytes received: %s", dumpBytes(l.rxS))
	s.tracef("  first bytes sent:     %s", dumpBytes(l.txS))
	a.mu.Lock()
	s.busy = false
	if s.pm != nil {
		pm := s.pm
		s.onTab(func() { a.removeModal(pm) })
		s.pm = nil
	}
	stopped := s.stopped
	if !stopped {
		if err != nil {
			msg := title + " failed: " + err.Error()
			if p := a.logPath(); p != "" {
				msg += "\nDetails were written to " + p
			}
			s.onTab(func() { a.errorBox(msg) })
		} else {
			names := make([]string, len(files))
			for i, f := range files {
				names[i] = f
				if j := strings.LastIndexAny(f, `/\`); j >= 0 {
					names[i] = f[j+1:]
				}
			}
			txt := okText
			if len(names) > 0 {
				txt += "\n" + strings.Join(names, ", ")
			}
			s.onTab(func() { a.message(title, txt) })
		}
	}
	a.mu.Unlock()
	if stopped {
		return
	}
	rest := append(append([]byte(nil), left...), l.buf...)
	l.buf = nil
	s.feed(rest)
}

func (s *Session) doReceive(prefix []byte) {
	s.tracef("=== download from %s:%d (prefix %d bytes: %s)", s.entry.Host, s.entry.Port, len(prefix), dumpBytes(prefix[:minInt(len(prefix), 64)]))
	l := s.startXfer(false)
	res, err := zmodem.Receive(l, prefix, s.a.downDir(), s.hooks())
	s.endXfer(l, res.Left, "ZMODEM download", fmt.Sprintf("Received %d file(s) into %s", len(res.Files), s.a.cfg.DownDir), res.Files, err)
}

func (s *Session) doSend(paths []string) { s.doSendPrefix(paths, nil) }

func (s *Session) doSendPrefix(paths []string, prefix []byte) {
	s.tracef("=== upload to %s:%d files=%v (prefix %d bytes)", s.entry.Host, s.entry.Port, paths, len(prefix))
	l := s.startXfer(true)
	res, err := zmodem.Send(l, prefix, paths, s.hooks())
	s.endXfer(l, res.Left, "ZMODEM upload", fmt.Sprintf("Sent %d file(s)", len(res.Files)), res.Files, err)
}

// ---------------------------------------------------------------------
// App-level session actions (a.mu held)
// ---------------------------------------------------------------------

func (a *App) hangupNow() {
	s := a.sess
	if s != nil {
		s.close()
	}
	if s != nil {
		s.audio.Reset()
	}
	a.sess = nil
	a.audio = nil
	a.selecting = false
	a.modals = nil
	a.push(a.dir)
	if s != nil && s.offerSave && s.conn != nil && !s.local {
		a.offerSaveEntry(s.entry)
	}
}

// offerSaveEntry asks whether to add a quick-connected address to the directory.
func (a *App) offerSaveEntry(e DialEntry) {
	for _, x := range a.cfg.Entries {
		if strings.EqualFold(x.Host, e.Host) && x.Port == e.Port && x.Conn == e.Conn {
			return
		}
	}
	a.inputBox("Save to dialing directory?", "Name (Esc = don't save):", orStr(e.Name, e.Host), 40, func(a *App, name string) {
		e.Name = strings.TrimSpace(name)
		if e.Name == "" {
			e.Name = e.Host
		}
		e.Password = ""
		if e.Mode == "" {
			e.Mode = a.cfg.Mode
		}
		a.cfg.Entries = append(a.cfg.Entries, e)
		a.cfg.SortEntries()
		for i, x := range a.cfg.Entries {
			if x.Host == e.Host && x.Port == e.Port && x.Name == e.Name {
				a.dir.Sel = i
			}
		}
		a.cfg.Save()
	})
}

var protoNames = []string{"ZMODEM", "YMODEM", "YMODEM-G", "XMODEM-CRC", "XMODEM-1K", "XMODEM (checksum)"}
var protoModes = []xmodem.Mode{0, xmodem.Ymodem, xmodem.YmodemG, xmodem.XmodemCRC, xmodem.Xmodem1K, xmodem.XmodemChecksum}

func (a *App) protoPick(title string, pick func(a *App, i int)) {
	a.push(&ListModal{Title: title, Items: protoNames, Width: 40, OnPick: func(a *App, i int) { pick(a, i) }})
}

func (a *App) uploadPick() {
	s := a.sess
	if s == nil || s.local || s.dialing || s.busy {
		return
	}
	a.protoPick("Upload protocol", func(a *App, pi int) {
		a.push(newPicker("Upload ("+protoNames[pi]+") - choose file(s)", a.cfg.LastDir, func(a *App, paths []string) {
			a.cfg.LastDir = dirOf(paths[0])
			if pi == 0 {
				s.jobs <- func() { s.doSend(paths) }
			} else {
				s.jobs <- func() { s.doSendX(protoModes[pi], paths) }
			}
		}))
	})
}

func (a *App) downloadNow() {
	s := a.sess
	if s == nil || s.local || s.dialing || s.busy {
		return
	}
	a.protoPick("Download protocol", func(a *App, pi int) {
		switch {
		case pi == 0:
			s.jobs <- func() { s.doReceive(nil) }
		case protoModes[pi].Batch():
			s.jobs <- func() { s.doReceiveX(protoModes[pi], "") }
		default:
			a.inputBox("Download ("+protoNames[pi]+")", "Save as:", "download.bin", 100, func(a *App, name string) {
				s.jobs <- func() { s.doReceiveX(protoModes[pi], name) }
			})
		}
	})
}

func (s *Session) xhooks() *xmodem.Hooks {
	h := s.hooks()
	return &xmodem.Hooks{
		Log:   h.Log,
		Abort: h.Abort,
		Progress: func(p xmodem.Progress) {
			h.Progress(zmodem.Progress{Name: p.Name, Done: p.Done, Total: p.Total, FileIndex: p.FileIndex, FileCount: p.FileCount, Sending: p.Sending})
		},
	}
}

func (s *Session) doSendX(mode xmodem.Mode, paths []string) {
	s.tracef("=== %s upload files=%v", mode, paths)
	l := s.startXfer(true)
	res, err := xmodem.Send(l, mode, paths, s.xhooks())
	s.endXfer(l, res.Left, mode.String()+" upload", fmt.Sprintf("Sent %d file(s)", len(res.Files)), res.Files, err)
}

func (s *Session) doReceiveX(mode xmodem.Mode, name string) {
	s.tracef("=== %s download name=%q", mode, name)
	l := s.startXfer(false)
	res, err := xmodem.Receive(l, mode, s.a.downDir(), name, s.xhooks())
	s.endXfer(l, res.Left, mode.String()+" download", fmt.Sprintf("Received %d file(s) into %s", len(res.Files), s.a.cfg.DownDir), res.Files, err)
}

func (a *App) startLocal() {
	if a.sess != nil {
		return
	}
	m := screenModeByName("80x25")
	a.term = a.newTerm(m)
	e := DialEntry{Name: "Test pattern"}
	a.audio = NewAudioAPC()
	a.parser = a.newParser(a.term, &e)
	s := newSession(a, e)
	s.term, s.parser, s.audio = a.term, a.parser, a.audio
	s.local = true
	a.sess = s
	a.modals = nil
	go s.feed(TestPatternStream())
	a.dirty = true
}

// ---------------------------------------------------------------------
// Transfer trace log: <config dir>/zmodem.log
// ---------------------------------------------------------------------

func (a *App) logPath() string {
	if a.cfg.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(a.cfg.path), "zmodem.log")
}

func (s *Session) tracef(format string, args ...interface{}) {
	p := s.a.logPath()
	if p == "" {
		return
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() > 512<<10 {
		os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
}

func dumpBytes(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "<%02X>", c)
		}
	}
	return sb.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
