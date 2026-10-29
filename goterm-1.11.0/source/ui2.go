package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func (c *Canvas) putRaw(x, y int, ch byte, fg, bg Color) {
	if x < 0 || y < 0 || x >= c.S.Cols || y >= c.S.Rows {
		return
	}
	c.S.Cells[y*c.S.Cols+x] = Cell{Ch: ch, Fg: fg, Bg: bg}
	c.Mask[y*c.S.Cols+x] = true
}

var colorNames = [16]string{"Black", "Red", "Green", "Brown", "Blue", "Magenta", "Cyan", "Light Gray",
	"Dark Gray", "Light Red", "Light Green", "Yellow", "Light Blue", "Light Magenta", "Light Cyan", "White"}

type palPreset struct {
	Name string
	Pal  [16]RGB
}

func hexPal(v [16]uint32) (p [16]RGB) {
	for i, c := range v {
		p[i] = RGB{uint8(c >> 16), uint8(c >> 8), uint8(c)}
	}
	return
}

var palPresets = []palPreset{
	{"SyncTERM VGA (default)", defaultPalette()},
	{"Classic VGA (AA/55/FF)", oldVGA},
	{"Windows console", hexPal([16]uint32{0x000000, 0x800000, 0x008000, 0x808000, 0x000080, 0x800080, 0x008080, 0xC0C0C0,
		0x808080, 0xFF0000, 0x00FF00, 0xFFFF00, 0x0000FF, 0xFF00FF, 0x00FFFF, 0xFFFFFF})},
	{"xterm", hexPal([16]uint32{0x000000, 0xCD0000, 0x00CD00, 0xCDCD00, 0x0000EE, 0xCD00CD, 0x00CDCD, 0xE5E5E5,
		0x7F7F7F, 0xFF0000, 0x00FF00, 0xFFFF00, 0x5C5CFF, 0xFF00FF, 0x00FFFF, 0xFFFFFF})},
	{"Tango", hexPal([16]uint32{0x2E3436, 0xCC0000, 0x4E9A06, 0xC4A000, 0x3465A4, 0x75507B, 0x06989A, 0xD3D7CF,
		0x555753, 0xEF2929, 0x8AE234, 0xFCE94F, 0x729FCF, 0xAD7FA8, 0x34E2E2, 0xEEEEEC})},
	{"Solarized dark", hexPal([16]uint32{0x073642, 0xDC322F, 0x859900, 0xB58900, 0x268BD2, 0xD33682, 0x2AA198, 0xEEE8D5,
		0x002B36, 0xCB4B16, 0x586E75, 0x657B83, 0x839496, 0x6C71C4, 0x93A1A1, 0xFDF6E3})},
	{"Amiga / Workbench", hexPal([16]uint32{0x000000, 0xFF0000, 0x00AA00, 0xFFAA00, 0x0055AA, 0xAA00AA, 0x00AAAA, 0xBBBBBB,
		0x555555, 0xFF5555, 0x55FF55, 0xFFFF55, 0x5599FF, 0xFF55FF, 0x55FFFF, 0xFFFFFF})},
}

// ---------------------------------------------------------------------
// Palette editor
// ---------------------------------------------------------------------

type PaletteModal struct {
	noMouse
	Sel, Chan int
	Preset    int
	UI        bool
}

func (m *PaletteModal) target(a *App) *[16]RGB {
	if m.UI {
		return &a.cfg.UIPalette
	}
	return &a.cfg.Palette
}

func (m *PaletteModal) Draw(c *Canvas) {
	a := theApp
	ttl := "Edit terminal ANSI palette"
	if m.UI {
		ttl = "Edit interface palette"
	}
	ix, iy, iw, _ := c.centered(66, 24, ttl, rFrame)
	c.strClip(ix, iy, iw, "Idx Name            Swatch   Hex      Red   Green  Blue", rHilite)
	for i := 0; i < 16; i++ {
		y := iy + 1 + i
		role := rText
		if i == m.Sel {
			role = rSelect
		}
		col := m.target(a)[i]
		c.fill(ix-1, y, iw+2, 1, ' ', role)
		c.strClip(ix, y, 4, fmt.Sprintf("%2d", i), role)
		c.strClip(ix+4, y, 16, colorNames[i], role)
		for k := 0; k < 6; k++ {
			c.putRaw(ix+20+k, y, ' ', 7, rgbColor(m.target(a)[i].R, m.target(a)[i].G, m.target(a)[i].B))
		}
		c.strClip(ix+28, y, 8, fmt.Sprintf("%02X%02X%02X", col.R, col.G, col.B), role)
		vals := [3]uint8{col.R, col.G, col.B}
		for ch := 0; ch < 3; ch++ {
			r := role
			if i == m.Sel && ch == m.Chan {
				r = rInput
			}
			c.strClip(ix+37+ch*7, y, 5, fmt.Sprintf(" %3d ", vals[ch]), r)
		}
	}
	c.str(ix, iy+18, "Preview: ", rText)
	for i := 0; i < 16; i++ {
		pc := m.target(a)[i]
		c.putRaw(ix+9+i*2, iy+18, 0xDB, rgbColor(pc.R, pc.G, pc.B), 0)
		c.putRaw(ix+10+i*2, iy+18, 0xDB, rgbColor(pc.R, pc.G, pc.B), 0)
	}
	c.strClip(ix, iy+19, iw, "Up/Dn color  Lt/Rt R,G,B  +/- 1  PgUp/PgDn 16  H=hex  I=import .reg", rText)
	c.strClip(ix, iy+20, iw, "P=preset ("+palPresets[m.Preset].Name+")  D=default color  Z=all defaults  Esc=done", rText)
}

func (m *PaletteModal) Key(a *App, ev KeyEvent) {
	p := &m.target(a)[m.Sel]
	ch := func() *uint8 {
		switch m.Chan {
		case 0:
			return &p.R
		case 1:
			return &p.G
		}
		return &p.B
	}
	adj := func(d int) {
		v := int(*ch()) + d
		*ch() = uint8(clampInt(v, 0, 255))
		a.paletteChanged()
	}
	switch ev.Key {
	case KeyEsc, KeyEnter:
		a.pop()
		return
	case KeyUp:
		m.Sel = (m.Sel + 15) % 16
	case KeyDown:
		m.Sel = (m.Sel + 1) % 16
	case KeyLeft:
		m.Chan = (m.Chan + 2) % 3
	case KeyRight, KeyTab:
		m.Chan = (m.Chan + 1) % 3
	case KeyPgUp:
		adj(16)
	case KeyPgDn:
		adj(-16)
	case KeyHome:
		*ch() = 0
		a.paletteChanged()
	case KeyEnd:
		*ch() = 255
		a.paletteChanged()
	default:
		switch ev.Ch {
		case '+', '=':
			adj(1)
		case '-', '_':
			adj(-1)
		case 'h', 'H':
			idx := m.Sel
			cur := m.target(a)[idx]
			a.inputBox("Hex color", "RRGGBB", fmt.Sprintf("%02X%02X%02X", cur.R, cur.G, cur.B), 6, func(a *App, s string) {
				s = strings.TrimPrefix(strings.TrimSpace(s), "#")
				if v, err := strconv.ParseUint(s, 16, 32); err == nil && len(s) == 6 {
					m.target(a)[idx] = RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}
					a.paletteChanged()
				}
			})
		case 'p', 'P':
			m.Preset = (m.Preset + 1) % len(palPresets)
			*m.target(a) = palPresets[m.Preset].Pal
			a.paletteChanged()
		case 'd', 'D':
			m.target(a)[m.Sel] = defaultPalette()[m.Sel]
			a.paletteChanged()
		case 'i', 'I':
			home, _ := os.UserHomeDir()
			a.push(newPicker("Import console colors (.reg)", orStr(a.cfg.LastDir, home), func(a *App, paths []string) {
				pal, err := ParseConsoleReg(paths[0])
				if err != nil {
					a.errorBox(err.Error())
					return
				}
				*m.target(a) = pal
				a.paletteChanged()
			}))
		case 'z', 'Z':
			*m.target(a) = defaultPalette()
			a.paletteChanged()
		}
	}
}

// ---------------------------------------------------------------------
// Transfer progress
// ---------------------------------------------------------------------

type ProgressModal struct{ noMouse }

func fmtBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (m *ProgressModal) Draw(c *Canvas) {
	x := theApp.xfer
	title := "ZMODEM download"
	if x.Sending {
		title = "ZMODEM upload"
	}
	ix, iy, iw, _ := c.centered(60, 11, title, rFrame)
	name := x.Name
	if name == "" {
		name = "(waiting for host)"
	}
	c.strClip(ix, iy+1, iw, "File: "+name, rText)
	cnt := fmt.Sprintf("%d", x.FileIndex)
	if x.FileCount > 0 {
		cnt += fmt.Sprintf(" of %d", x.FileCount)
	}
	c.strClip(ix, iy+2, iw, "File number: "+cnt, rText)
	pct := 0
	if x.Total > 0 {
		pct = int(x.Done * 100 / x.Total)
	}
	bw := iw - 8
	fillN := bw * pct / 100
	for i := 0; i < bw; i++ {
		if i < fillN {
			c.put(ix+i, iy+4, 0xDB, rHilite)
		} else {
			c.put(ix+i, iy+4, 0xB0, rText)
		}
	}
	c.str(ix+bw+1, iy+4, fmt.Sprintf("%3d%%", pct), rText)
	el := time.Since(theApp.xferStart).Seconds()
	rate := 0.0
	if el > 0.2 {
		rate = float64(x.Done-theApp.xferBase) / el
	}
	c.strClip(ix, iy+5, iw, fmt.Sprintf("%s of %s", fmtBytes(x.Done), fmtBytes(x.Total)), rText)
	eta := "--"
	if rate > 1 && x.Total > x.Done {
		eta = (time.Duration((float64(x.Total-x.Done) / rate) * float64(time.Second))).Round(time.Second).String()
	}
	c.strClip(ix, iy+6, iw, fmt.Sprintf("Rate: %s/s    ETA: %s", fmtBytes(int64(rate)), eta), rText)
	c.strClip(ix, iy+8, iw, "Esc or Ctrl+X = cancel transfer", rHilite)
}

func (m *ProgressModal) Key(a *App, ev KeyEvent) {
	if ev.Key == KeyEsc || (ev.Ctrl && ev.Ch == 24) {
		a.xferAbort = true
	}
}

// ---------------------------------------------------------------------
// Dialing directory (main screen)
// ---------------------------------------------------------------------

type DirModal struct {
	Sel, Top  int
	rect      [4]int
	lastClk   time.Time
	Filter    string
	Searching bool
}

func (m *DirModal) Draw(c *Canvas) {
	a := theApp
	c.fill(0, 0, c.W(), c.H(), c.Border[8], rBackdrop)
	title := "GoTerm " + Version + " - Dialing Directory"
	w, h := c.W()-6, c.H()-4
	if w > 100 {
		w = 100
	}
	ix, iy, iw, ih := c.window((c.W()-w)/2, 1, w, h, title, rFrame)
	nameW, portW, typeW, modeW := 26, 5, 6, 7
	hostW := iw - nameW - portW - typeW - modeW - 4
	if hostW < 10 {
		hostW = 10
	}
	hdr := fmt.Sprintf("%-*s %-*s %*s %-*s %-*s", nameW, "Name", hostW, "Address", portW, "Port", typeW, "Type", modeW, "Mode")
	c.strClip(ix, iy, iw, hdr, rHilite)
	for x := 0; x < iw; x++ {
		c.put(ix+x, iy+1, c.Border[1], rFrame)
	}
	listH := ih - 5
	m.rect = [4]int{ix, iy + 2, iw, listH}
	vis := m.visible(a)
	n := len(vis)
	pos := m.pos(vis)
	if pos < 0 && n > 0 {
		m.Sel, pos = vis[0], 0
	}
	if pos < m.Top {
		m.Top = maxInt(pos, 0)
	}
	if pos >= m.Top+listH {
		m.Top = pos - listH + 1
	}
	if m.Top > maxInt(n-listH, 0) {
		m.Top = maxInt(n-listH, 0)
	}
	for i := 0; i < listH; i++ {
		vi := m.Top + i
		if vi >= n {
			break
		}
		idx := vis[vi]
		e := a.cfg.Entries[idx]
		role := rText
		if idx == m.Sel {
			role = rSelect
		}
		c.fill(ix-1, iy+2+i, iw+2, 1, ' ', role)
		line := fmt.Sprintf("%-*s %-*s %*d %-6s %-7s", nameW, trunc(e.Name, nameW), hostW, trunc(e.Host, hostW), portW, e.Port, e.Conn, e.Mode)
		c.strClip(ix, iy+2+i, iw, line, role)
	}
	if n == 0 {
		if m.Filter != "" {
			c.strClip(ix, iy+3, iw, "No entries match \""+m.Filter+"\".  Backspace edits the search, Esc clears it.", rText)
		} else {
			c.strClip(ix, iy+3, iw, "The directory is empty.  Press INS to add an entry.", rText)
		}
	}
	for x := 0; x < iw; x++ {
		c.put(ix+x, iy+ih-3, c.Border[1], rFrame)
	}
	if n > 0 && pos >= 0 {
		e := a.cfg.Entries[m.Sel]
		last := "never"
		if e.Last != "" {
			last = e.Last
		}
		c.strClip(ix, iy+ih-2, iw, fmt.Sprintf("Last called: %s  (%d calls)  Term: %s", last, e.Count, orStr(e.Term, a.cfg.Term)), rText)
		c.strClip(ix, iy+ih-1, iw, e.Notes, rText)
	}
	if m.Searching {
		c.fill(0, c.H()-1, c.W(), 1, ' ', rStatus)
		msg := fmt.Sprintf("Search: %s_   %d of %d   Enter Dial  Up/Down Move  Esc Clear", m.Filter, n, len(a.cfg.Entries))
		c.strClip(1, c.H()-1, c.W()-2, msg, rStatus)
		return
	}
	c.fill(0, c.H()-1, c.W(), 1, ' ', rStatus)
	c.strClip(1, c.H()-1, c.W()-2, "Enter Dial  / Search  Ins Add  E Edit  Del Remove  F1 Help  F2 Setup  F3 Pal  F4 Themes", rStatus)
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// visible returns the indices of the entries that match the search filter.
func (m *DirModal) visible(a *App) []int {
	terms := strings.Fields(strings.ToLower(m.Filter))
	out := make([]int, 0, len(a.cfg.Entries))
	for i, e := range a.cfg.Entries {
		if len(terms) > 0 {
			hay := strings.ToLower(e.Name + " " + e.Host + " " + e.Notes + " " + e.Conn + " " + e.User + " " + strconv.Itoa(e.Port))
			ok := true
			for _, t := range terms {
				if !strings.Contains(hay, t) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, i)
	}
	return out
}

// pos is the position of the selected entry within vis, or -1.
func (m *DirModal) pos(vis []int) int {
	for i, v := range vis {
		if v == m.Sel {
			return i
		}
	}
	return -1
}

// move selects the entry delta places along the visible list.
func (m *DirModal) move(a *App, delta int) {
	vis := m.visible(a)
	if len(vis) == 0 {
		return
	}
	p := m.pos(vis)
	if p < 0 {
		p = 0
		if delta > 0 {
			delta--
		}
	}
	m.Sel = vis[clampInt(p+delta, 0, len(vis)-1)]
}

func (m *DirModal) searchKey(a *App, ev KeyEvent) bool {
	switch {
	case ev.Key == KeyEsc:
		m.Searching, m.Filter = false, ""
	case ev.Key == KeyEnter:
		if vis := m.visible(a); len(vis) > 0 {
			if m.pos(vis) < 0 {
				m.Sel = vis[0]
			}
			m.Searching = false
			a.connectEntry(m.Sel)
			m.Filter = ""
		}
	case ev.Key == KeyBS:
		if r := []rune(m.Filter); len(r) > 0 {
			m.Filter = string(r[:len(r)-1])
		}
		m.fixSel(a)
	case ev.Key == KeyUp:
		m.move(a, -1)
	case ev.Key == KeyDown:
		m.move(a, 1)
	case ev.Key == KeyPgUp:
		m.move(a, -10)
	case ev.Key == KeyPgDn:
		m.move(a, 10)
	case ev.Key == KeyHome:
		m.move(a, -1<<20)
	case ev.Key == KeyEnd:
		m.move(a, 1<<20)
	case ev.Ch == 21 && ev.Ctrl: // Ctrl+U clears the query
		m.Filter = ""
		m.fixSel(a)
	case ev.Ch >= 32 && !ev.Ctrl && !ev.Alt:
		m.Filter += string(ev.Ch)
		m.fixSel(a)
	default:
		return false
	}
	return true
}

// fixSel keeps the selection on a visible entry after the filter changed.
func (m *DirModal) fixSel(a *App) {
	if vis := m.visible(a); len(vis) > 0 && m.pos(vis) < 0 {
		m.Sel = vis[0]
		m.Top = 0
	}
}

func (m *DirModal) Key(a *App, ev KeyEvent) {
	if m.Searching && m.searchKey(a, ev) {
		return
	}
	n := len(a.cfg.Entries)
	if ev.Key == KeyF8 || (ev.Ch == 6 && ev.Ctrl) || (ev.Ch == '/' && !ev.Ctrl && !ev.Alt) {
		m.Searching = true
		return
	}
	switch ev.Key {
	case KeyUp:
		m.move(a, -1)
	case KeyDown:
		m.move(a, 1)
	case KeyPgUp:
		m.move(a, -10)
	case KeyPgDn:
		m.move(a, 10)
	case KeyHome:
		m.move(a, -1<<20)
	case KeyEnd:
		m.move(a, 1<<20)
	case KeyEnter:
		if n > 0 {
			a.connectEntry(m.Sel)
		}
	case KeyIns:
		a.editEntry(-1, m)
	case KeyDel:
		if n > 0 {
			i := m.Sel
			a.cfg.Entries = append(a.cfg.Entries[:i], a.cfg.Entries[i+1:]...)
			a.cfg.Save()
		}
	case KeyF1:
		a.showHelp()
	case KeyF2:
		a.settings()
	case KeyF3:
		a.push(&PaletteModal{})
	case KeyF4:
		a.push(newThemeModal(a))
	case KeyF5:
		a.quickConnect()
	case KeyF6:
		a.startLocal()
	case KeyF7:
		a.importLst()
	case KeyF10, KeyEsc:
		a.confirm("Quit", "Exit GoTerm?", func(a *App) { a.quit() })
	default:
		if ev.Ch != 0 && !ev.Ctrl && !ev.Alt {
			switch ev.Ch {
			case 'a', 'A':
				a.editEntry(-1, m)
			case 'e', 'E':
				if n > 0 {
					a.editEntry(m.Sel, m)
				}
			case 'c', 'C':
				if n > 0 {
					e := a.cfg.Entries[m.Sel]
					e.Name += " (copy)"
					a.cfg.Entries = append(a.cfg.Entries, e)
					a.cfg.Save()
				}
			case 'q', 'Q':
				a.confirm("Quit", "Exit GoTerm?", func(a *App) { a.quit() })
			}
		}
	}
}

func (m *DirModal) Mouse(a *App, ev MouseEvent) {
	vis := m.visible(a)
	n := len(vis)
	if ev.Wheel != 0 {
		m.move(a, -ev.Wheel*3)
		return
	}
	if ev.Down && ev.Button == 0 {
		r := m.rect
		if ev.X >= r[0]-1 && ev.X < r[0]+r[2]+1 && ev.Y >= r[1] && ev.Y < r[1]+r[3] {
			vi := m.Top + ev.Y - r[1]
			if vi < n {
				i := vis[vi]
				dbl := ev.Double || (i == m.Sel && time.Since(m.lastClk) < 500*time.Millisecond)
				m.Sel = i
				m.lastClk = time.Now()
				if dbl {
					m.Searching, m.Filter = false, ""
					a.connectEntry(i)
				}
			}
		}
	}
}
