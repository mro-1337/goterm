package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Platform hooks, filled in by the OS-specific file.
type Platform struct {
	Clipboard     func() string
	SetClipboard  func(string)
	PlayWAV       func([]byte) // must not block
	Drives        func() []string
	SetFullscreen func(bool)
	Quit          func()
	SetTitle      func(string)
}

var plat Platform

var theApp *App

type App struct {
	mu     sync.Mutex
	cfg    *Config
	pal    *Palette
	uiPal  *Palette
	term   *Screen
	parser *Parser
	audio  *AudioAPC
	tabs   []*Tab
	cur    int
	modals []Modal
	dir    *DirModal
	sess   *Session

	dirty    bool
	blinkOff bool
	curOn    bool
	lastBlk  time.Time
	cfgDirty time.Time

	xfer      xferState
	sbQuery   string
	sbMatch   int64
	xferStart time.Time
	xferBase  int64
	xferAbort bool

	fb       []uint32
	ufb      []uint32
	fbW, fbH int
	cellW    int
	cellH    int

	fullscreen bool
	selecting  bool
	mouseBtns  int    // buttons held, for host mouse reporting
	mouseLast  [2]int // last reported motion cell/pixel
	selMoved   bool
}

type xferState struct {
	Name                 string
	Done, Total          int64
	FileIndex, FileCount int
	Sending              bool
}

func NewApp(cfg *Config) *App {
	a := &App{cfg: cfg, curOn: true, dirty: true}
	a.initTabs()
	theApp = a
	a.pal = BuildPalette(&cfg.Palette)
	a.uiPal = BuildPalette(&cfg.UIPalette)
	a.term = a.newTerm(screenModeByName(cfg.Mode))
	a.dir = &DirModal{}
	a.modals = []Modal{a.dir}
	a.fullscreen = cfg.Fullscreen
	return a
}

func (a *App) newTerm(m ScreenMode) *Screen {
	s := NewScreen(m.Cols, m.Rows, 8, m.FontH)
	s.SbMax = a.cfg.Scrollback
	s.KeepOnClear = a.cfg.KeepOnClear
	return s
}

func (a *App) newParser(s *Screen, e *DialEntry) *Parser {
	p := NewParser(s)
	p.Audio = a.audio
	p.ResolveBg = func(c Color) uint32 { return a.pal.resolve(c) }
	p.PlaySound = func(wav []byte) {
		if plat.PlayWAV != nil {
			plat.PlayWAV(wav)
		}
	}
	if e != nil {
		p.MusicMode = e.Music
		p.UTF8 = e.UTF8
	}
	a.applyParserCfg(p, e)
	return p
}

func (a *App) applyParserCfg(p *Parser, e *DialEntry) {
	p.SoundOn = a.cfg.Sound && (e == nil || !e.NoSound)
	p.BellOn = a.cfg.Bell
}

func (a *App) touch()       { a.dirty = true; a.cfgDirty = time.Now() }
func (a *App) push(m Modal) { a.modals = append(a.modals, m); a.dirty = true }
func (a *App) pop() {
	if len(a.modals) > 0 {
		a.modals = a.modals[:len(a.modals)-1]
	}
	a.dirty = true
}

func (a *App) removeModal(m Modal) {
	for i := range a.modals {
		if a.modals[i] == m {
			a.modals = append(a.modals[:i], a.modals[i+1:]...)
			break
		}
	}
	a.dirty = true
}

func (a *App) quit() {
	a.cfg.Save()
	a.saveTab()
	for _, t := range a.tabs {
		if t.sess != nil {
			t.sess.close()
		}
	}
	if plat.Quit != nil {
		plat.Quit()
	}
}

func (a *App) paletteChanged() {
	a.pal = BuildPalette(&a.cfg.Palette)
	a.uiPal = BuildPalette(&a.cfg.UIPalette)
	a.touch()
}

func (a *App) opts() *RenderOpts {
	return &RenderOpts{
		Pal: a.pal, Font: fontForHeight(a.term.CH), NineDot: a.cfg.NineDot,
		BlinkOff: a.blinkOff, ICE: a.cfg.ICE, ShowCursor: a.curOn, CursorBlock: a.cfg.BlockCur,
	}
}

// ---------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------

// Frame renders the current state. Called only from the window thread.
func (a *App) Frame() (fb []uint32, w, h int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	o := a.opts()
	w, h = o.FBSize(a.term.Cols, a.term.Rows)
	cw, ch := o.CellPx()
	a.cellW, a.cellH = cw, ch
	barH := 0
	if a.barOn() {
		barH = ch
	}
	if len(a.fb) < w*(h+barH) {
		a.fb = make([]uint32, w*(h+barH))
		a.ufb = make([]uint32, w*(h+barH))
	}
	a.fbW, a.fbH = w, h+barH
	body := a.fb[w*barH : w*(h+barH)]
	a.term.Render(body, o)
	viewing := a.term.ViewOffset > 0 && a.sess != nil
	if viewing {
		a.drawScrollbar(body, w, h)
	}
	if len(a.modals) > 0 || viewing {
		c := newCanvas(a.term.Cols, a.term.Rows, a.term.CW, a.term.CH, &a.cfg.Roles, a.shadowBase())
		c.Border = a.cfg.Border
		for _, m := range a.modals {
			m.Draw(c)
		}
		if viewing && len(a.modals) == 0 {
			a.drawScrollbackBar(c)
		}
		uo := *o
		uo.Pal = a.uiPal
		uo.ShowCursor = false
		uo.BlinkOff = false
		uo.ICE = false
		c.S.CursorVisible = false
		c.S.Render(a.ufb[:w*h], &uo)
		for y := 0; y < c.S.Rows; y++ {
			for x := 0; x < c.S.Cols; x++ {
				if !c.Mask[y*c.S.Cols+x] {
					continue
				}
				for r := 0; r < ch; r++ {
					o0 := (y*ch+r)*w + x*cw
					copy(body[o0:o0+cw], a.ufb[o0:o0+cw])
				}
			}
		}
	}
	if barH > 0 {
		bc := newCanvas(a.term.Cols, 1, a.term.CW, a.term.CH, &a.cfg.Roles, nil)
		a.drawTabBar(bc)
		uo := *o
		uo.Pal = a.uiPal
		uo.ShowCursor = false
		uo.BlinkOff = false
		uo.ICE = false
		bc.S.CursorVisible = false
		bc.S.Render(a.ufb[:w*barH], &uo)
		copy(a.fb[:w*barH], a.ufb[:w*barH])
	}
	a.dirty = false
	return a.fb[:w*(h+barH)], w, h + barH
}

// Tick is called ~every 50ms from the window thread; it reports whether a
// redraw is needed.
func (a *App) Tick() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if now.Sub(a.lastBlk) >= 500*time.Millisecond {
		a.lastBlk = now
		a.curOn = !a.curOn
		blinking := a.term.HasBlink()
		if blinking && !a.cfg.ICE && !a.term.ICE {
			a.blinkOff = !a.blinkOff
		}
		if (blinking && !a.cfg.ICE && !a.term.ICE) || (a.term.CursorVisible && len(a.modals) == 0) {
			a.dirty = true
		}
	}
	if !a.cfgDirty.IsZero() && now.Sub(a.cfgDirty) > time.Second {
		a.cfgDirty = time.Time{}
		a.cfg.Save()
	}
	return a.dirty
}

func (a *App) Title() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sess != nil && a.sess.entry.Name != "" {
		return "GoTerm - " + a.sess.entry.Name
	}
	return "GoTerm"
}

func (a *App) WantFullscreen() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.cfg.Fullscreen }

// ---------------------------------------------------------------------
// Keyboard
// ---------------------------------------------------------------------

func (a *App) OnKey(ev KeyEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dirty = true
	if ev.Alt && !ev.Ctrl && (ev.Ch == 'q' || ev.Ch == 'Q') && (len(a.modals) == 0 || a.modals[len(a.modals)-1] == Modal(a.dir)) {
		a.quickConnect() // Alt+Q: type an address and port, from anywhere
		return
	}
	if a.tabKey(ev) {
		return
	}
	if len(a.modals) > 0 {
		a.modals[len(a.modals)-1].Key(a, ev)
		return
	}
	if a.sess == nil {
		a.push(a.dir)
		return
	}
	s := a.sess
	if s.dialing && ev.Key == KeyEsc {
		a.hangupNow()
		return
	}
	if ev.Alt && !ev.Ctrl {
		switch ev.Ch {
		case 'z', 'Z':
			a.onlineMenu()
			return
		case 'v', 'V':
			a.paste()
			return
		case 'h', 'H':
			a.confirm("Hang up", "Disconnect from "+s.entry.Name+"?", func(a *App) { a.hangupNow() })
			return
		case 'u', 'U':
			a.uploadPick()
			return
		case 'd', 'D':
			a.downloadNow()
			return
		case 'c', 'C':
			a.copySel()
			return
		}
	}
	if (ev.Key == KeyIns && ev.Shift) || (ev.Ch == 22 && ev.Ctrl) { // Shift+Ins, Ctrl+V
		a.paste()
		return
	}
	if ev.Key == KeyIns && ev.Ctrl { // Ctrl+Ins copies
		a.copySel()
		return
	}
	if ev.Ch == 3 && ev.Ctrl && a.term.SelActive { // Ctrl+C copies when text is selected
		a.copySel()
		a.term.SelClear()
		return
	}
	if a.term.ViewOffset > 0 && a.scrollbackKey(ev) {
		return
	}
	if (ev.Shift || ev.Ctrl) && (ev.Key == KeyPgUp || ev.Key == KeyPgDn) {
		d := a.term.Rows - 1
		if ev.Key == KeyPgDn {
			d = -d
		}
		a.term.ScrollView(d)
		return
	}
	if s.local {
		if ev.Key == KeyEsc {
			a.hangupNow()
		}
		return
	}
	if a.term.ViewOffset > 0 {
		a.term.ViewOffset = 0
	}
	if a.term.SelActive {
		a.term.SelClear()
	}
	b := a.encodeKey(ev)
	if len(b) > 0 {
		s.send(b)
	}
}

func (a *App) encodeKey(ev KeyEvent) []byte {
	e := &a.sess.entry
	term := strings.ToLower(orStr(e.Term, a.cfg.Term))
	xt := strings.Contains(term, "xterm") || strings.Contains(term, "linux") || strings.Contains(term, "vt")
	csi := func(s string) []byte { return []byte("\x1b" + s) }
	switch ev.Key {
	case KeyUp:
		return csi("[A")
	case KeyDown:
		return csi("[B")
	case KeyRight:
		return csi("[C")
	case KeyLeft:
		return csi("[D")
	case KeyHome:
		return csi("[H")
	case KeyEnd:
		if xt {
			return csi("[F")
		}
		return csi("[K")
	case KeyPgUp:
		if xt {
			return csi("[5~")
		}
		return csi("[V")
	case KeyPgDn:
		if xt {
			return csi("[6~")
		}
		return csi("[U")
	case KeyIns:
		if xt {
			return csi("[2~")
		}
		return csi("[@")
	case KeyDel:
		if xt {
			return csi("[3~")
		}
		return []byte{0x7f}
	case KeyEnter:
		return []byte{'\r'}
	case KeyEsc:
		return []byte{0x1b}
	case KeyTab:
		if ev.Shift {
			return csi("[Z")
		}
		return []byte{'\t'}
	case KeyBS:
		if e.BSDel {
			return []byte{0x7f}
		}
		return []byte{8}
	case KeyF1, KeyF2, KeyF3, KeyF4:
		return csi("O" + string(rune('P'+int(ev.Key-KeyF1))))
	case KeyF5:
		return csi("[15~")
	case KeyF6:
		return csi("[17~")
	case KeyF7:
		return csi("[18~")
	case KeyF8:
		return csi("[19~")
	case KeyF9:
		return csi("[20~")
	case KeyF10:
		return csi("[21~")
	case KeyF11:
		return csi("[23~")
	case KeyF12:
		return csi("[24~")
	}
	if ev.Ch == 0 {
		return nil
	}
	var out []byte
	if ev.Alt {
		out = append(out, 0x1b)
	}
	if ev.Ch < 32 {
		return append(out, byte(ev.Ch))
	}
	return append(out, a.encodeText(string(ev.Ch))...)
}

func (a *App) encodeText(s string) []byte {
	if a.sess != nil && a.sess.entry.UTF8 {
		return []byte(s)
	}
	return toCP437(s)
}

// ---------------------------------------------------------------------
// Mouse / clipboard
// ---------------------------------------------------------------------

func (a *App) OnMouse(ev MouseEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dirty = true
	cw, ch := a.cellW, a.cellH
	if cw == 0 || ch == 0 {
		return
	}
	px, py := ev.X, ev.Y
	ev.X, ev.Y = ev.X/cw, ev.Y/ch
	if a.barOn() {
		py -= ch
		if ev.Y == 0 {
			if ev.Down && ev.Button == 0 {
				if i := a.tabAt(ev.X, a.term.Cols); i >= 0 {
					closeBtn := ev.X == a.tabSpans(a.term.Cols)[i][1]-3
					a.switchTab(i)
					if closeBtn {
						a.closeTab()
					}
				}
			}
			return
		}
		ev.Y--
	}
	if len(a.modals) > 0 {
		a.modals[len(a.modals)-1].Mouse(a, ev)
		return
	}
	if a.sess == nil {
		return
	}
	if a.hostMouse(ev, px, py, cw, ch) {
		return
	}
	t := a.term
	col := clampInt(ev.X, 0, t.Cols-1)
	row := clampInt(ev.Y, 0, t.Rows-1)
	switch {
	case ev.Wheel != 0:
		t.ScrollView(ev.Wheel * 3)
	case ev.Down && ev.Button == 0:
		a.selecting, a.selMoved = true, false
		t.SelA = selPoint{t.absLineAtView(row), col}
		t.SelB = t.SelA
		t.SelClear()
	case ev.Move && a.selecting:
		t.SelB = selPoint{t.absLineAtView(row), col}
		if t.SelB != t.SelA || a.selMoved {
			a.selMoved = true
			t.SelActive = true
		}
	case ev.Up && ev.Button == 0 && a.selecting:
		a.selecting = false
		if t.SelActive && a.cfg.AutoCopy {
			a.copySel()
		}
	case ev.Down && (ev.Button == 2 || ev.Button == 1):
		a.paste()
	}
}

func (a *App) copySel() {
	txt := a.term.SelectedText()
	if txt == "" {
		txt = a.term.AllText()
	}
	if plat.SetClipboard != nil {
		plat.SetClipboard(txt)
	}
}

func (a *App) paste() {
	if a.sess == nil || a.sess.local || plat.Clipboard == nil {
		return
	}
	txt := plat.Clipboard()
	if txt == "" {
		return
	}
	txt = strings.ReplaceAll(txt, "\r\n", "\r")
	txt = strings.ReplaceAll(txt, "\n", "\r")
	if a.term.ViewOffset > 0 {
		a.term.ViewOffset = 0
	}
	a.term.SelClear()
	if a.parser != nil && a.parser.BracketPaste {
		txt = strings.ReplaceAll(txt, "\x1b[201~", "")
		a.sess.send([]byte("\x1b[200~"))
		a.sess.send(a.encodeText(txt))
		a.sess.send([]byte("\x1b[201~"))
		return
	}
	a.sess.send(a.encodeText(txt))
}

// ---------------------------------------------------------------------
// Menus and forms
// ---------------------------------------------------------------------

func (a *App) onlineMenu() {
	items := []string{
		"Disconnect (hang up)",
		"Upload file(s) - ZMODEM",
		"Download file(s) - ZMODEM",
		"Paste from clipboard",
		"Copy screen/selection to clipboard",
		"Edit terminal ANSI palette...",
		"Color schemes / themes...",
		"Interface colors...",
		"Settings...",
		"Toggle fullscreen",
		"Toggle ICE colors (blink = bright bg)",
		"Toggle 9-dot VGA cells",
		"Toggle sound",
		"Clear screen",
		"Show test pattern",
		"Scrollback: view",
		"Scrollback: search...",
		"Scrollback: save to file",
		"Scrollback: clear",
		"Close menu",
	}
	a.push(&ListModal{Title: "Online menu", Items: items, Width: 46, OnPick: func(a *App, i int) {
		switch i {
		case 0:
			a.confirm("Hang up", "Disconnect from "+a.sess.entry.Name+"?", func(a *App) { a.hangupNow() })
		case 1:
			a.uploadPick()
		case 2:
			a.downloadNow()
		case 3:
			a.paste()
		case 4:
			a.copySel()
		case 5:
			a.push(&PaletteModal{})
		case 6:
			a.push(newThemeModal(a))
		case 7:
			a.push(&RolesModal{})
		case 8:
			a.settings()
		case 9:
			a.toggleFullscreen()
		case 10:
			a.cfg.ICE = !a.cfg.ICE
		case 11:
			a.cfg.NineDot = !a.cfg.NineDot
		case 12:
			a.cfg.Sound = !a.cfg.Sound
			a.applyParserCfg(a.parser, &a.sess.entry)
		case 13:
			a.parser.Feed([]byte("\x1b[0m\x1b[2J\x1b[H"))
		case 14:
			a.parser.Feed(TestPatternStream())
		case 15:
			a.term.ScrollView(a.term.Rows - 1)
		case 16:
			a.term.ScrollView(a.term.Rows - 1)
			a.searchPrompt()
		case 17:
			a.saveScrollback()
		case 18:
			a.term.ClearScrollback()
		}
		a.touch()
	}})
}

func (a *App) toggleFullscreen() {
	a.cfg.Fullscreen = !a.cfg.Fullscreen
	if plat.SetFullscreen != nil {
		plat.SetFullscreen(a.cfg.Fullscreen)
	}
	a.touch()
}

// ToggleFullscreen is called by the window layer (Alt+Enter).
func (a *App) ToggleFullscreen() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toggleFullscreen()
}

func (a *App) showHelp() {
	items := []string{
		"DIALING DIRECTORY",
		"  Enter/double-click connect    Ins/A add    E edit    Del remove",
		"  F2 settings   F3 edit ANSI palette   F4 color schemes/themes",
		"  F5 quick connect   F6 local test pattern   F7 import syncterm.lst",
		"  / or Ctrl+F or F8 search the directory (name, address, notes, type)",
		"ONLINE",
		"  Alt+T new tab   Alt+W close tab   Alt+1..9 or Alt+Left/Right switch tabs",
		"  Alt+Q quick connect (opens in a new tab)   Alt+Z menu   Alt+H hang up   Alt+U upload   Alt+D download",
		"  Ctrl+V / Alt+V / Shift+Ins / right-click paste   Ctrl+C (selection) / Ctrl+Ins copy",
		"  Mouse drag selects and copies   Ctrl+PgUp or wheel = scrollback (/ find, F3, F2 save)",
		"  Alt+Enter fullscreen",
		"  ZMODEM downloads start automatically; uploads prompt for files",
		"  when the host asks to receive.",
	}
	a.push(&ListModal{Title: "Help - GoTerm " + Version, Items: items, Width: 76})
}

var modeNames = func() []string {
	var n []string
	for _, m := range screenModes {
		n = append(n, m.Name)
	}
	return n
}()

var musicChoices = []string{"ANSI music: CSI | only", "+ BananaCom (CSI N)", "+ CSI M (no params)"}
// Terminal types offered in the entry editor and settings.
var termTypes = []string{"ansi-bbs", "ansi", "syncterm", "xterm-256color", "xterm", "xterm-color", "linux", "vt100", "vt102", "vt220", "rxvt", "pcansi", "cygwin", "dumb"}

// termChoiceField builds a terminal-type picker; a custom value already in
// use is kept as an extra choice. If withDefault, the first choice means
// "use the default" and is stored as "".
func termChoiceField(label, cur string, withDefault bool) *Field {
	ch := append([]string(nil), termTypes...)
	if cur != "" {
		found := false
		for _, c := range ch {
			found = found || c == cur
		}
		if !found {
			ch = append(ch, cur)
		}
	}
	if withDefault {
		ch = append([]string{"(default)"}, ch...)
		if cur == "" {
			cur = "(default)"
		}
	}
	return choiceField(label, ch, cur)
}

func termChoiceValue(f *Field) string {
	if v := f.Choice(); v != "(default)" {
		return v
	}
	return ""
}

var connChoices = []string{"telnet", "ssh", "rlogin", "raw"}

func (a *App) editEntry(idx int, d *DirModal) {
	var e DialEntry
	if idx >= 0 {
		e = a.cfg.Entries[idx]
	} else {
		e = DialEntry{Port: 23, Conn: "telnet", Mode: a.cfg.Mode}
	}
	name := textField("Name", e.Name, 40)
	host := textField("Address", e.Host, 120)
	port := numField("Port", e.Port, 5)
	conn := choiceField("Connection", connChoices, orStr(e.Conn, "telnet"))
	user := textField("User name (ssh/rlogin)", e.User, 40)
	pass := textField("Password (ssh, blank = ask)", e.Password, 60)
	pass.Secret = true
	term := termChoiceField("Terminal type", e.Term, true)
	mode := choiceField("Screen mode", modeNames, orStr(e.Mode, "80x25"))
	music := &Field{Label: "Music", Kind: fChoice, Choices: musicChoices, Idx: clampInt(e.Music, 0, 2)}
	utf := boolField("UTF-8 text (else CP437)", e.UTF8)
	bs := boolField("Backspace sends DEL", e.BSDel)
	nosnd := boolField("Mute sound", e.NoSound)
	notes := textField("Notes", e.Notes, 100)
	save := func(a *App) bool {
		if strings.TrimSpace(host.Text) == "" {
			a.errorBox("An address is required.")
			return false
		}
		e.Name, e.Host, e.Port = name.Text, strings.TrimSpace(host.Text), port.Int()
		if e.Name == "" {
			e.Name = e.Host
		}
		newConn := conn.Choice()
		if e.Port == 0 || (newConn != orStr(e.Conn, "telnet") && e.Port == defaultPort(orStr(e.Conn, "telnet"))) {
			e.Port = defaultPort(newConn)
		}
		e.User, e.Password = strings.TrimSpace(user.Text), pass.Text
		e.Conn, e.Term, e.Mode = newConn, termChoiceValue(term), mode.Choice()
		e.Music, e.UTF8, e.BSDel, e.NoSound, e.Notes = music.Idx, utf.Bool, bs.Bool, nosnd.Bool, notes.Text
		if idx >= 0 {
			a.cfg.Entries[idx] = e
		} else {
			a.cfg.Entries = append(a.cfg.Entries, e)
			d.Sel = len(a.cfg.Entries) - 1
		}
		a.cfg.Save()
		return true
	}
	m := &FormModal{Title: "Edit directory entry", Width: 70, OnSave: save,
		Help: "Tab/Up/Down move   Left/Right change   F10 = save   Esc = cancel"}
	m.Fields = []*Field{name, host, port, conn, user, pass, term, mode, music, utf, bs, nosnd, notes,
		buttonField("Save", func(a *App) {
			if save(a) {
				a.removeModal(m)
			}
		}),
		buttonField("Cancel", func(a *App) { a.removeModal(m) })}
	a.push(m)
}

func (a *App) settings() {
	c := a.cfg
	ice := boolField("ICE colors (blink bit = bright bg)", c.ICE)
	nine := boolField("9-dot VGA cells", c.NineDot)
	scale := choiceField("Window scaling", []string{"fit", "integer", "stretch"}, c.Scale)
	smooth := boolField("Smooth scaling", c.Smooth)
	block := boolField("Block cursor", c.BlockCur)
	sb := numField("Scrollback lines", c.Scrollback, 6)
	keep := boolField("Keep screen in scrollback on clear", c.KeepOnClear)
	dl := textField("Download directory", c.DownDir, 200)
	snd := boolField("Sound / ANSI music", c.Sound)
	bell := boolField("Bell beep", c.Bell)
	ac := boolField("Copy selection on release", c.AutoCopy)
	term := termChoiceField("Default terminal type", c.Term, false)
	mode := choiceField("Default screen mode", modeNames, c.Mode)
	fs := boolField("Start fullscreen", c.Fullscreen)
	save := func(a *App) bool {
		c.ICE, c.NineDot, c.Scale, c.Smooth, c.BlockCur = ice.Bool, nine.Bool, scale.Choice(), smooth.Bool, block.Bool
		c.Scrollback, c.DownDir, c.Sound, c.Bell, c.AutoCopy = maxInt(sb.Int(), 100), dl.Text, snd.Bool, bell.Bool, ac.Bool
		c.Term, c.Mode, c.Fullscreen = term.Choice(), mode.Choice(), fs.Bool
		a.term.SbMax = c.Scrollback
		c.KeepOnClear = keep.Bool
		a.term.KeepOnClear = keep.Bool
		if a.parser != nil && a.sess != nil {
			a.applyParserCfg(a.parser, &a.sess.entry)
		}
		a.cfg.Save()
		a.dirty = true
		return true
	}
	m := &FormModal{Title: "Settings", Width: 70, OnSave: save, Help: "F10 = save   Esc = cancel"}
	m.Fields = []*Field{ice, nine, scale, smooth, block, sb, keep, dl, snd, bell, ac, term, mode, fs,
		buttonField("Edit terminal ANSI palette...", func(a *App) { a.push(&PaletteModal{}) }),
		buttonField("Color schemes / themes...", func(a *App) { a.push(newThemeModal(a)) }),
		buttonField("Interface colors...", func(a *App) { a.push(&RolesModal{}) }),
		buttonField("Border characters...", func(a *App) { a.push(newBorderModal(a)) }),
		buttonField("Save", func(a *App) { save(a); a.removeModal(m) })}
	a.push(m)
}

func (a *App) quickConnect() {
	a.inputBox("Quick connect", "Address & port  (host:port, \"host port\", ssh://user@host)", "", 120, func(a *App, s string) {
		sa := parseStartArgs(strings.Fields(s))
		if sa.Bad || sa.Target == "" {
			if strings.TrimSpace(s) != "" {
				a.errorBox("Can't understand that address. Try host:port or \"host port\".")
			}
			return
		}
		e := entryFromTarget(a.cfg, sa.Target)
		if sa.User != "" {
			e.User = sa.User
		}
		if a.sess != nil { // keep the current call; open the new one in a new tab
			a.newTab()
		}
		a.connect(e, -1)
	})
}

func (a *App) importLst() {
	home, _ := os.UserHomeDir()
	p := newPicker("Select syncterm.lst to import", orStr(a.cfg.LastDir, home), func(a *App, paths []string) {
		ents, err := ImportSyncTermLst(paths[0])
		if err != nil {
			a.errorBox(err.Error())
			return
		}
		a.cfg.Entries = append(a.cfg.Entries, ents...)
		a.cfg.Save()
		a.message("Import", fmt.Sprintf("Imported %d entries.", len(ents)))
	})
	a.push(p)
}

func (a *App) connectEntry(i int) { a.connect(a.cfg.Entries[i], i) }

func (a *App) downDir() string {
	d := a.cfg.DownDir
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, "Downloads")
	}
	os.MkdirAll(d, 0o755)
	return d
}

var _ = utf8.RuneLen

func (a *App) top() Modal {
	if len(a.modals) == 0 {
		return nil
	}
	return a.modals[len(a.modals)-1]
}

// ---------------------------------------------------------------------
// Scrollback viewer
// ---------------------------------------------------------------------

// scrollbackKey handles keys while the view is scrolled back. It returns
// true when the key was consumed; other keys fall through (and jump the
// view back to the live screen, as before).
func (a *App) scrollbackKey(ev KeyEvent) bool {
	t := a.term
	page := t.Rows - 1
	switch {
	case ev.Key == KeyUp && !ev.Alt:
		t.ScrollView(1)
	case ev.Key == KeyDown && !ev.Alt:
		t.ScrollView(-1)
	case ev.Key == KeyPgUp:
		t.ScrollView(page)
	case ev.Key == KeyPgDn:
		t.ScrollView(-page)
	case ev.Key == KeyHome:
		t.ScrollView(1 << 30)
	case ev.Key == KeyEnd, ev.Key == KeyEsc:
		t.ViewOffset = 0
		t.SelClear()
	case ev.Ch == 6 && ev.Ctrl, ev.Ch == '/' && !ev.Ctrl && !ev.Alt:
		a.searchPrompt()
	case ev.Key == KeyF3 && !ev.Shift:
		a.searchNext(-1)
	case ev.Key == KeyF3 && ev.Shift:
		a.searchNext(1)
	case ev.Key == KeyF2, ev.Ch == 19 && ev.Ctrl:
		a.saveScrollback()
	default:
		return false
	}
	a.dirty = true
	return true
}

func (a *App) searchPrompt() {
	a.inputBox("Search scrollback", "Find:", a.sbQuery, 80, func(a *App, q string) {
		if q == "" {
			return
		}
		a.sbQuery = q
		a.sbMatch = a.term.absLineAtView(0) + int64(a.term.Rows) // start below the visible area -> finds nearest older
		a.searchNext(-1)
	})
}

// searchNext finds the next match; dir<0 older, dir>0 newer.
func (a *App) searchNext(dir int) {
	t := a.term
	if a.sbQuery == "" {
		a.searchPrompt()
		return
	}
	from := a.sbMatch
	if t.SelActive && t.SelA.line == t.SelB.line {
		from = t.SelA.line
	}
	if m, ok := t.Find(a.sbQuery, from, dir); ok {
		a.sbMatch = m
		return
	}
	// wrap once
	start := t.linesPushed - int64(t.SbLen()) - 1
	if dir < 0 {
		start = t.linesPushed + int64(t.Rows)
	}
	if m, ok := t.Find(a.sbQuery, start, dir); ok {
		a.sbMatch = m
		return
	}
	a.message("Search scrollback", "Not found: "+a.sbQuery)
}

func (a *App) saveScrollback() {
	txt := a.term.ScrollbackText()
	name := "scrollback"
	if a.sess != nil {
		name += "-" + strings.Map(func(r rune) rune {
			if r == ' ' || strings.ContainsRune(`<>:"/\|?*`, r) {
				return '_'
			}
			return r
		}, a.sess.entry.Name)
	}
	p := filepath.Join(a.downDir(), name+"-"+time.Now().Format("20060102-150405")+".txt")
	if err := os.WriteFile(p, []byte(txt), 0o644); err != nil {
		a.errorBox("Could not save scrollback: " + err.Error())
		return
	}
	a.message("Scrollback saved", p)
}

func (a *App) drawScrollbackBar(c *Canvas) {
	t := a.term
	txt := fmt.Sprintf(" SCROLLBACK %d/%d  PgUp/PgDn  / find  F3 older  Sh+F3 newer  F2 save  Esc live ", t.ViewOffset, t.SbLen())
	if len(txt) > t.Cols {
		txt = fmt.Sprintf(" SB %d/%d  / F3 F2 Esc ", t.ViewOffset, t.SbLen())
	}
	c.str(t.Cols-len(txt), 0, txt, rStatus)
}

// drawScrollbar paints a thin position bar down the right edge.
func (a *App) drawScrollbar(fb []uint32, w, h int) {
	t := a.term
	n := t.SbLen()
	if n == 0 || w < 16 || h < 16 {
		return
	}
	const bw = 6
	total := n + t.Rows
	th := h * t.Rows / total
	if th < 10 {
		th = 10
	}
	y0 := int(float64(h-th) * (1 - float64(t.ViewOffset)/float64(n)))
	for y := 0; y < h; y++ {
		col := uint32(0x303030)
		if y >= y0 && y < y0+th {
			col = 0xC0C0C0
		}
		for x := w - bw; x < w; x++ {
			fb[y*w+x] = col
		}
	}
}

// shadowBase is the screen that window shadows darken: the live terminal
// while connected, nothing at the dialing directory (so the previous
// session's text does not show through).
func (a *App) shadowBase() *Screen {
	if a.sess == nil {
		return nil
	}
	return a.term
}
