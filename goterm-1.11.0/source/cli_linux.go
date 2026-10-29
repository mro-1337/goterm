//go:build cli && linux

package main

// goterm-cli: the same application (dialing directory, menus, transfers,
// scrollback...) drawn with ANSI escape sequences in a terminal.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type cliCell struct {
	r      rune
	fg, bg uint32
	attr   uint8 // 1 blink, 2 underline, 4 strike
}

type cliState struct {
	a       *App
	tty     ttyState
	out     *os.File
	mu      sync.Mutex
	prev    []cliCell
	pw, ph  int
	force   bool
	truecol bool
	quit    chan struct{}
	sound   chan []byte
	pasting bool
	paste   []byte
}

var cli cliState

func usage() {
	fmt.Fprintf(os.Stderr, `goterm-cli %s - terminal client for BBSes (telnet, ssh, rlogin)

usage: goterm-cli [options] [target]

  target   host[:port], telnet://host, ssh://user@host[:port], rlogin://host,
           or the name of a dialing directory entry. Without a target the
           dialing directory opens.

options:
  -mode NAME    screen mode (80x25, 80x28, 80x30, 80x43, 80x50, 80x60, ...)
  -term TYPE    terminal type to announce
  -user NAME    user name (ssh / rlogin)
  -256          use the 256-colour palette instead of 24-bit colour
  -version

Use a terminal at least as large as the BBS screen (80x25 by default).
Alt+Z or Ctrl+] opens the online menu. Hold Shift to select text with the
terminal's own mouse selection.
`, Version)
}

func main() {
	var target, modeName, termType, user string
	force256 := false
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			usage()
			os.Exit(2)
			return ""
		}
		switch a {
		case "-h", "-help", "--help":
			usage()
			return
		case "-version", "--version":
			fmt.Println("goterm-cli", Version)
			return
		case "-mode":
			modeName = next()
		case "-term":
			termType = next()
		case "-user":
			user = next()
		case "-256":
			force256 = true
		default:
			if strings.HasPrefix(a, "-") {
				usage()
				os.Exit(2)
			}
			target = a
		}
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) {
		fmt.Fprintln(os.Stderr, "goterm-cli needs an interactive terminal.")
		os.Exit(1)
	}
	cfg := LoadConfig()
	if modeName != "" {
		cfg.Mode = modeName
	}
	if termType != "" {
		cfg.Term = termType
	}
	a := NewApp(cfg)
	cli.a, cli.out = a, os.Stdout
	cli.quit = make(chan struct{})
	cli.sound = make(chan []byte, 32)
	cli.truecol = !force256 && (os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit")
	go cliSoundLoop()
	a.cellW, a.cellH = 1, 1
	plat = Platform{
		SetClipboard: func(s string) { cli.writeRaw("\x1b]52;c;" + b64(s) + "\x07") },
		Clipboard:    func() string { return "" },
		PlayWAV: func(w []byte) {
			select {
			case cli.sound <- w:
			default:
			}
		},
		Quit: func() { cliExit(0) },
	}
	if err := cli.tty.raw(); err != nil {
		fmt.Fprintln(os.Stderr, "cannot set terminal mode:", err)
		os.Exit(1)
	}
	// alternate screen, hide nothing, SGR mouse, bracketed paste
	cli.writeRaw("\x1b[?1049h\x1b[?1000h\x1b[?1003h\x1b[?1006h\x1b[?2004h\x1b[2J")
	defer cliExit(0)
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	go func() {
		for s := range sig {
			if s == syscall.SIGWINCH {
				cli.mu.Lock()
				cli.force = true
				cli.mu.Unlock()
				a.mu.Lock()
				a.dirty = true
				a.mu.Unlock()
				continue
			}
			cliExit(0)
		}
	}()

	if target != "" {
		e := entryFromTarget(cfg, target)
		if user != "" {
			e.User = user
		}
		a.mu.Lock()
		a.modals = nil
		a.connect(e, -1)
		a.mu.Unlock()
	}
	go cliInput()
	tick := time.NewTicker(40 * time.Millisecond)
	for range tick.C {
		redraw := a.Tick()
		a.mu.Lock()
		d := a.dirty
		a.mu.Unlock()
		if redraw || d {
			cliRender()
		}
	}
}

func cliExit(code int) {
	cli.writeRaw("\x1b[0m\x1b[?2004l\x1b[?1006l\x1b[?1003l\x1b[?1000l\x1b[?25h\x1b[?1049l")
	cli.tty.restore()
	if cli.a != nil {
		cli.a.cfg.Save()
	}
	os.Exit(code)
}

func (c *cliState) writeRaw(s string) {
	cli.out.WriteString(s)
}

func b64(s string) string {
	const t = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	in := []byte(s)
	var o strings.Builder
	for i := 0; i < len(in); i += 3 {
		var v uint32
		n := 0
		for j := 0; j < 3; j++ {
			v <<= 8
			if i+j < len(in) {
				v |= uint32(in[i+j])
				n++
			}
		}
		o.WriteByte(t[v>>18&63])
		o.WriteByte(t[v>>12&63])
		if n > 1 {
			o.WriteByte(t[v>>6&63])
		} else {
			o.WriteByte('=')
		}
		if n > 2 {
			o.WriteByte(t[v&63])
		} else {
			o.WriteByte('=')
		}
	}
	return o.String()
}

func cliSoundLoop() {
	var player []string
	for _, c := range [][]string{{"paplay"}, {"pw-play"}, {"aplay", "-q"}, {"play", "-q"}} {
		if p, err := exec.LookPath(c[0]); err == nil {
			player = append([]string{p}, c[1:]...)
			break
		}
	}
	for wav := range cli.sound {
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

// ------------------------------------------------------------ rendering

func rgb256(c uint32) int {
	r, g, b := int(c>>16&255), int(c>>8&255), int(c&255)
	if r == g && g == b {
		if r < 8 {
			return 16
		}
		if r > 248 {
			return 231
		}
		return 232 + (r-8)*24/247
	}
	q := func(v int) int { return (v*5 + 127) / 255 }
	return 16 + 36*q(r) + 6*q(g) + q(b)
}

// grid composes what GoTerm would draw: the terminal (with scrollback view
// and selection) plus any menus drawn over it.
func (a *App) cellGrid() (cols, rows int, cells []cliCell, cx, cy int, showCur bool) {
	t := a.term
	cols, rows = t.Cols, t.Rows
	cells = make([]cliCell, cols*rows)
	ice := a.cfg.ICE || t.ICE
	for y := 0; y < rows; y++ {
		row := t.rowAtView(y)
		abs := t.absLineAtView(y)
		for x := 0; x < cols; x++ {
			c := Cell{Ch: ' ', Fg: 7, Bg: 0}
			if row != nil && x < len(row) {
				c = row[x]
			}
			fgC, bgC := c.Fg, c.Bg
			if ice && c.Attr&aBlink != 0 && !bgC.isRGB() && bgC < 8 {
				bgC += 8
			}
			fg, bg := a.pal.resolve(fgC), a.pal.resolve(bgC)
			if t.inSelection(abs, x) {
				fg, bg = bg, fg
			}
			var at uint8
			if c.Attr&aBlink != 0 && !ice {
				at |= 1
			}
			if c.Attr&aUnder != 0 {
				at |= 2
			}
			if c.Attr&aStrike != 0 {
				at |= 4
			}
			cells[y*cols+x] = cliCell{cp437ToRune(c.Ch), fg, bg, at}
		}
	}
	viewing := t.ViewOffset > 0 && a.sess != nil
	if len(a.modals) > 0 || viewing {
		cv := newCanvas(cols, rows, t.CW, t.CH, &a.cfg.Roles, a.shadowBase())
		cv.Border = a.cfg.Border
		for _, m := range a.modals {
			m.Draw(cv)
		}
		if viewing && len(a.modals) == 0 {
			a.drawScrollbackBar(cv)
		}
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				if !cv.Mask[y*cols+x] {
					continue
				}
				c := cv.S.Cells[cv.S.idx(x, y)]
				cells[y*cols+x] = cliCell{cp437ToRune(c.Ch), a.uiPal.resolve(c.Fg), a.uiPal.resolve(c.Bg), 0}
			}
		}
	}
	if a.sess != nil && len(a.modals) == 0 && t.CursorVisible && t.ViewOffset == 0 {
		showCur, cx, cy = true, t.CX, t.CY
	}
	if a.barOn() {
		bc := newCanvas(cols, 1, t.CW, t.CH, &a.cfg.Roles, nil)
		a.drawTabBar(bc)
		bar := make([]cliCell, cols)
		for x := 0; x < cols; x++ {
			c := bc.S.Cells[bc.S.idx(x, 0)]
			bar[x] = cliCell{cp437ToRune(c.Ch), a.uiPal.resolve(c.Fg), a.uiPal.resolve(c.Bg), 0}
		}
		cells = append(bar, cells...)
		rows++
		cy++
	}
	return
}

func cliRender() {
	a := cli.a
	a.mu.Lock()
	cols, rows, cells, cx, cy, showCur := a.cellGrid()
	a.dirty = false
	a.mu.Unlock()
	tw, th := ttySize()
	cli.mu.Lock()
	defer cli.mu.Unlock()
	full := cli.force || cli.pw != cols || cli.ph != rows || len(cli.prev) != len(cells)
	cli.force = false
	var b bytes.Buffer
	if full {
		b.WriteString("\x1b[0m\x1b[2J")
	}
	var cur cliCell
	haveCur := false
	sgr := func(c cliCell) {
		if haveCur && cur == c {
			return
		}
		if haveCur && cur.attr == c.attr {
			var p []string
			if cur.fg != c.fg {
				p = append(p, colorSGR(38, c.fg))
			}
			if cur.bg != c.bg {
				p = append(p, colorSGR(48, c.bg))
			}
			if len(p) > 0 {
				b.WriteString("\x1b[" + strings.Join(p, ";") + "m")
			}
			cur = c
			return
		}
		p := []string{"0"}
		if c.attr&1 != 0 {
			p = append(p, "5")
		}
		if c.attr&2 != 0 {
			p = append(p, "4")
		}
		if c.attr&4 != 0 {
			p = append(p, "9")
		}
		p = append(p, colorSGR(38, c.fg), colorSGR(48, c.bg))
		b.WriteString("\x1b[" + strings.Join(p, ";") + "m")
		cur, haveCur = c, true
	}
	for y := 0; y < rows && y < th; y++ {
		x := 0
		for x < cols && x < tw {
			i := y*cols + x
			if !full && i < len(cli.prev) && cli.prev[i] == cells[i] {
				x++
				continue
			}
			fmt.Fprintf(&b, "\x1b[%d;%dH", y+1, x+1)
			for x < cols && x < tw {
				i = y*cols + x
				if !full && cli.prev[i] == cells[i] {
					break
				}
				sgr(cells[i])
				r := cells[i].r
				if r < 0x20 && r != 0 {
					r = ' '
				}
				if r == 0 {
					r = ' '
				}
				var buf [4]byte
				n := utf8.EncodeRune(buf[:], r)
				b.Write(buf[:n])
				x++
			}
		}
	}
	b.WriteString("\x1b[0m")
	if showCur && cx < tw && cy < th {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", cy+1, cx+1)
	} else {
		b.WriteString("\x1b[?25l")
	}
	cli.prev, cli.pw, cli.ph = cells, cols, rows
	cli.out.Write(b.Bytes())
}

func colorSGR(base int, c uint32) string {
	if cli.truecol {
		return fmt.Sprintf("%d;2;%d;%d;%d", base, c>>16&255, c>>8&255, c&255)
	}
	return fmt.Sprintf("%d;5;%d", base, rgb256(c))
}

// ------------------------------------------------------------ input

func cliInput() {
	chunks := make(chan []byte, 16)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil || n == 0 {
				close(chunks)
				return
			}
			chunks <- append([]byte(nil), buf[:n]...)
		}
	}()
	var pend []byte
	for {
		var timeout <-chan time.Time
		if len(pend) > 0 {
			timeout = time.After(40 * time.Millisecond)
		}
		select {
		case c, ok := <-chunks:
			if !ok {
				cliExit(0)
			}
			pend = append(pend, c...)
		case <-timeout:
			// an incomplete sequence that never finished: a lone ESC is the Esc key
			if pend[0] == 0x1b {
				cli.a.OnKey(KeyEvent{Key: KeyEsc})
			}
			pend = pend[1:]
			continue
		}
		for len(pend) > 0 {
			used, done := cliParse(pend)
			if !done {
				break
			}
			pend = pend[used:]
		}
	}
}

var csiKeys = map[string]Key{"A": KeyUp, "B": KeyDown, "C": KeyRight, "D": KeyLeft, "H": KeyHome, "F": KeyEnd}
var tildeKeys = map[int]Key{1: KeyHome, 2: KeyIns, 3: KeyDel, 4: KeyEnd, 5: KeyPgUp, 6: KeyPgDn, 7: KeyHome, 8: KeyEnd,
	11: KeyF1, 12: KeyF2, 13: KeyF3, 14: KeyF4, 15: KeyF5, 17: KeyF6, 18: KeyF7, 19: KeyF8, 20: KeyF9, 21: KeyF10, 23: KeyF11, 24: KeyF12}

func modsOf(m int) (ctrl, alt, shift bool) {
	m--
	return m&4 != 0, m&2 != 0, m&1 != 0
}

// cliParse consumes one input unit from b. done=false means more bytes are
// needed.
func cliParse(b []byte) (int, bool) {
	a := cli.a
	if cli.pasting {
		if i := bytes.Index(b, []byte("\x1b[201~")); i >= 0 {
			cli.paste = append(cli.paste, b[:i]...)
			cli.pasting = false
			txt := string(cli.paste)
			cli.paste = nil
			a.mu.Lock()
			if a.sess != nil && !a.sess.local && len(a.modals) == 0 {
				txt = strings.ReplaceAll(strings.ReplaceAll(txt, "\r\n", "\r"), "\n", "\r")
				a.sess.send(a.encodeText(txt))
			}
			a.mu.Unlock()
			return i + 6, true
		}
		if len(b) > 6 {
			cli.paste = append(cli.paste, b[:len(b)-6]...)
			return len(b) - 6, true
		}
		return 0, false
	}
	c := b[0]
	switch {
	case c == 0x1b:
		if len(b) == 1 {
			return 0, false
		}
		switch b[1] {
		case '[':
			// find final byte
			i := 2
			for i < len(b) && (b[i] < 0x40 || b[i] > 0x7e) {
				i++
			}
			if i >= len(b) {
				return 0, false
			}
			params, final := string(b[2:i]), string(b[i])
			cliCSI(params, final)
			return i + 1, true
		case 'O':
			if len(b) < 3 {
				return 0, false
			}
			if b[2] >= 'P' && b[2] <= 'S' {
				a.OnKey(KeyEvent{Key: KeyF1 + Key(b[2]-'P')})
			} else if k, ok := csiKeys[string(b[2])]; ok {
				a.OnKey(KeyEvent{Key: k})
			}
			return 3, true
		default:
			// Alt+key
			r, n := utf8.DecodeRune(b[1:])
			if r == utf8.RuneError && n <= 1 && !utf8.FullRune(b[1:]) {
				return 0, false
			}
			switch {
			case b[1] == 0x7f:
				a.OnKey(KeyEvent{Key: KeyBS, Alt: true})
			case b[1] == '\r':
				a.OnKey(KeyEvent{Key: KeyEnter, Alt: true})
			case r >= 'A' && r <= 'Z':
				a.OnKey(KeyEvent{Ch: r + 32, Alt: true, Shift: true})
			default:
				a.OnKey(KeyEvent{Ch: r, Alt: true})
			}
			return 1 + n, true
		}
	case c == 0x1d: // Ctrl+] : online menu
		a.OnKey(KeyEvent{Ch: 'z', Alt: true})
	case c == 0x7f || c == 0x08:
		a.OnKey(KeyEvent{Key: KeyBS})
	case c == '\r' || c == '\n':
		a.OnKey(KeyEvent{Key: KeyEnter})
	case c == '\t':
		a.OnKey(KeyEvent{Key: KeyTab})
	case c < 0x20:
		a.OnKey(KeyEvent{Ch: rune(c), Ctrl: true})
	default:
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n <= 1 && !utf8.FullRune(b) {
			return 0, false
		}
		a.OnKey(KeyEvent{Ch: r})
		return n, true
	}
	return 1, true
}

func cliCSI(params, final string) {
	a := cli.a
	switch {
	case final == "~" && params == "200":
		cli.pasting = true
		return
	case final == "Z":
		a.OnKey(KeyEvent{Key: KeyTab, Shift: true})
		return
	case (final == "M" || final == "m") && strings.HasPrefix(params, "<"):
		f := strings.Split(params[1:], ";")
		if len(f) != 3 {
			return
		}
		btn, _ := strconv.Atoi(f[0])
		x, _ := strconv.Atoi(f[1])
		y, _ := strconv.Atoi(f[2])
		ev := MouseEvent{X: x - 1, Y: y - 1, Shift: btn&4 != 0, Alt: btn&8 != 0, Ctrl: btn&16 != 0}
		switch {
		case btn&64 != 0:
			ev.Wheel = 1
			if btn&1 != 0 {
				ev.Wheel = -1
			}
			if final == "m" {
				return
			}
		case btn&32 != 0:
			ev.Move = true
		default:
			ev.Button = btn & 3
			if ev.Button == 3 {
				return
			}
			if final == "M" {
				ev.Down = true
			} else {
				ev.Up = true
			}
		}
		a.OnMouse(ev)
		return
	}
	// key with optional ;modifier
	parts := strings.Split(params, ";")
	n, _ := strconv.Atoi(parts[0])
	mod := 1
	if len(parts) > 1 {
		mod, _ = strconv.Atoi(parts[1])
	}
	ctrl, alt, shift := modsOf(mod)
	var k Key
	if final == "~" {
		k = tildeKeys[n]
	} else {
		k = csiKeys[final]
	}
	if k == KeyNone {
		if final >= "P" && final <= "S" && len(final) == 1 { // CSI 1;5P style F1-F4
			k = KeyF1 + Key(final[0]-'P')
		} else {
			return
		}
	}
	a.OnKey(KeyEvent{Key: k, Ctrl: ctrl, Alt: alt, Shift: shift})
}

var _ = filepath.Join
