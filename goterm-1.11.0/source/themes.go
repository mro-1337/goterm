package main

import (
	"fmt"
	"strings"
)

// Theme is a complete color scheme: the 16 ANSI colors plus the
// interface role colors.
type Theme struct {
	Name    string              `json:"name"`
	Palette [16]RGB             `json:"palette"`
	Roles   [numRoles]RoleColor `json:"roles"`
	Border  [9]uint8            `json:"border"`
	Custom  bool                `json:"-"`
}

func rc(fg, bg uint8) RoleColor { return RoleColor{fg, bg} }

// roles order: backdrop, frame, title, text, hilite, select, input, status, shadow, error
func mkRoles(v [numRoles][2]uint8) (r [numRoles]RoleColor) {
	for i := range v {
		r[i] = RoleColor{v[i][0], v[i][1]}
	}
	return
}

var vga = defaultPalette()

// Border sets: TL, top, TR, left, right, BL, bottom, BR, backdrop pattern.
var (
	borderDouble  = [9]uint8{0xC9, 0xCD, 0xBB, 0xBA, 0xBA, 0xC8, 0xCD, 0xBC, 0xB0}
	borderSingle  = [9]uint8{0xDA, 0xC4, 0xBF, 0xB3, 0xB3, 0xC0, 0xC4, 0xD9, 0xB0}
	borderMixedH  = [9]uint8{0xD5, 0xCD, 0xB8, 0xB3, 0xB3, 0xD4, 0xCD, 0xBE, 0xB1}
	borderMixedV  = [9]uint8{0xD6, 0xC4, 0xB7, 0xBA, 0xBA, 0xD3, 0xC4, 0xBD, 0xB0}
	borderBlock   = [9]uint8{0xDB, 0xDF, 0xDB, 0xDB, 0xDB, 0xDB, 0xDC, 0xDB, 0xB1}
	borderHalf    = [9]uint8{0xDC, 0xDC, 0xDC, 0xDE, 0xDD, 0xDF, 0xDF, 0xDF, 0xB0}
	borderShade   = [9]uint8{0xB2, 0xB2, 0xB2, 0xB2, 0xB2, 0xB2, 0xB2, 0xB2, 0xB1}
	borderDiamond = [9]uint8{0x04, 0xC4, 0x04, 0xB3, 0xB3, 0x04, 0xC4, 0x04, 0xB0}
	borderASCII   = [9]uint8{'+', '-', '+', '|', '|', '+', '-', '+', '.'}
)

type borderPreset struct {
	Name string
	B    [9]uint8
}

var borderPresets = []borderPreset{
	{"Double line", borderDouble}, {"Single line", borderSingle},
	{"Double top/bottom", borderMixedH}, {"Double sides", borderMixedV},
	{"Solid block", borderBlock}, {"Half blocks", borderHalf}, {"Shade", borderShade},
	{"Diamond corners", borderDiamond}, {"Plain ASCII", borderASCII},
}

func mono(p [16]RGB) (o [16]RGB) {
	for i, c := range p {
		v := uint8((int(c.R)*30 + int(c.G)*59 + int(c.B)*11) / 100)
		o[i] = RGB{v, v, v}
	}
	o[15] = RGB{255, 255, 255}
	return
}

var builtinThemes = []Theme{
	{Name: "SyncTERM Blue", Palette: vga, Roles: defaultRoles()},
	{Name: "Black & White", Palette: mono(vga), Border: borderDouble, Roles: mkRoles([numRoles][2]uint8{{8, 0}, {15, 0}, {0, 15}, {7, 0}, {15, 0}, {0, 7}, {15, 8}, {0, 15}, {8, 0}, {0, 15}})},
	{Name: "White Paper", Palette: mono(vga), Border: borderSingle, Roles: mkRoles([numRoles][2]uint8{{7, 15}, {0, 15}, {15, 0}, {0, 15}, {8, 15}, {15, 0}, {0, 7}, {15, 0}, {8, 7}, {15, 0}})},
	{Name: "Classic Gray", Border: borderSingle, Palette: vga, Roles: mkRoles([numRoles][2]uint8{{8, 0}, {0, 7}, {15, 1}, {0, 7}, {1, 7}, {15, 0}, {15, 0}, {15, 4}, {8, 0}, {15, 1}})},
	{Name: "Light", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{7, 15}, {0, 7}, {15, 4}, {0, 7}, {1, 7}, {15, 4}, {0, 15}, {15, 4}, {8, 7}, {15, 1}})},
	{Name: "High Contrast", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{8, 0}, {15, 0}, {0, 15}, {15, 0}, {14, 0}, {0, 14}, {0, 15}, {0, 15}, {8, 0}, {15, 1}})},
	{Name: "Green Phosphor", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{2, 0}, {10, 0}, {0, 2}, {2, 0}, {10, 0}, {0, 10}, {10, 8}, {0, 2}, {8, 0}, {0, 10}})},
	{Name: "Amber Terminal", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{3, 0}, {14, 0}, {0, 3}, {3, 0}, {14, 0}, {0, 14}, {14, 8}, {0, 3}, {8, 0}, {0, 14}})},
	{Name: "Midnight Cyan", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{8, 0}, {7, 0}, {0, 6}, {7, 0}, {14, 0}, {0, 6}, {15, 8}, {0, 6}, {8, 0}, {15, 1}})},
	{Name: "Magenta", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{5, 0}, {15, 5}, {0, 7}, {15, 5}, {14, 5}, {0, 7}, {15, 0}, {0, 7}, {8, 0}, {15, 1}})},
	{Name: "Red Alert", Palette: vga, Roles: mkRoles([numRoles][2]uint8{{1, 0}, {15, 1}, {0, 7}, {7, 1}, {14, 1}, {0, 14}, {15, 0}, {0, 7}, {8, 0}, {0, 14}})},
	{Name: "Dracula", Palette: hexPal([16]uint32{0x282A36, 0xFF5555, 0x50FA7B, 0xFFB86C, 0xBD93F9, 0xFF79C6, 0x8BE9FD, 0xBFBFBF,
		0x4D4D4D, 0xFF6E6E, 0x69FF94, 0xF1FA8C, 0xD6ACFF, 0xFF92DF, 0xA4FFFF, 0xF8F8F2}),
		Roles: mkRoles([numRoles][2]uint8{{8, 0}, {15, 0}, {0, 4}, {7, 0}, {11, 0}, {0, 5}, {15, 8}, {0, 4}, {8, 0}, {15, 1}})},
	{Name: "Nord", Palette: hexPal([16]uint32{0x2E3440, 0xBF616A, 0xA3BE8C, 0xEBCB8B, 0x5E81AC, 0xB48EAD, 0x88C0D0, 0xD8DEE9,
		0x4C566A, 0xD08770, 0xA3BE8C, 0xEBCB8B, 0x81A1C1, 0xB48EAD, 0x8FBCBB, 0xECEFF4}),
		Roles: mkRoles([numRoles][2]uint8{{8, 0}, {7, 0}, {0, 6}, {7, 0}, {14, 0}, {15, 4}, {15, 8}, {0, 7}, {8, 0}, {15, 1}})},
	{Name: "Gruvbox", Palette: hexPal([16]uint32{0x282828, 0xCC241D, 0x98971A, 0xD79921, 0x458588, 0xB16286, 0x689D6A, 0xA89984,
		0x928374, 0xFB4934, 0xB8BB26, 0xFABD2F, 0x83A598, 0xD3869B, 0x8EC07C, 0xEBDBB2}),
		Roles: mkRoles([numRoles][2]uint8{{8, 0}, {15, 0}, {0, 3}, {7, 0}, {11, 0}, {0, 11}, {15, 8}, {0, 3}, {8, 0}, {15, 1}})},
	{Name: "Solarized Dark", Palette: hexPal([16]uint32{0x073642, 0xDC322F, 0x859900, 0xB58900, 0x268BD2, 0xD33682, 0x2AA198, 0xEEE8D5,
		0x002B36, 0xCB4B16, 0x586E75, 0x657B83, 0x839496, 0x6C71C4, 0x93A1A1, 0xFDF6E3}),
		Roles: mkRoles([numRoles][2]uint8{{10, 8}, {12, 8}, {8, 6}, {12, 8}, {3, 8}, {8, 6}, {15, 0}, {8, 6}, {10, 8}, {15, 1}})},
	{Name: "Tango", Palette: hexPal([16]uint32{0x2E3436, 0xCC0000, 0x4E9A06, 0xC4A000, 0x3465A4, 0x75507B, 0x06989A, 0xD3D7CF,
		0x555753, 0xEF2929, 0x8AE234, 0xFCE94F, 0x729FCF, 0xAD7FA8, 0x34E2E2, 0xEEEEEC}),
		Roles: defaultRoles()},
	{Name: "Windows Console", Palette: hexPal([16]uint32{0x000000, 0x800000, 0x008000, 0x808000, 0x000080, 0x800080, 0x008080, 0xC0C0C0,
		0x808080, 0xFF0000, 0x00FF00, 0xFFFF00, 0x0000FF, 0xFF00FF, 0x00FFFF, 0xFFFFFF}),
		Roles: defaultRoles()},
	{Name: "xterm", Palette: hexPal([16]uint32{0x000000, 0xCD0000, 0x00CD00, 0xCDCD00, 0x0000EE, 0xCD00CD, 0x00CDCD, 0xE5E5E5,
		0x7F7F7F, 0xFF0000, 0x00FF00, 0xFFFF00, 0x5C5CFF, 0xFF00FF, 0x00FFFF, 0xFFFFFF}),
		Roles: defaultRoles()},
}

func (a *App) allThemes() []Theme {
	out := append([]Theme(nil), builtinThemes...)
	for _, t := range a.cfg.Themes {
		t.Custom = true
		out = append(out, t)
	}
	return out
}

// applyTheme changes only the interface (dialing directory, menus, dialogs).
// The terminal's ANSI palette is never touched.
func (a *App) applyTheme(t Theme) {
	a.cfg.Roles = t.Roles
	a.cfg.UIPalette = t.Palette
	a.cfg.Border = t.Border
	if t.Border[0] == 0 && t.Border[1] == 0 {
		a.cfg.Border = borderDouble
	}
	a.cfg.Theme = t.Name
	a.paletteChanged()
}

// ---------------------------------------------------------------------
// Theme chooser (live preview)
// ---------------------------------------------------------------------

type ThemeModal struct {
	noMouse
	list    []Theme
	Sel     int
	Top     int
	origPal [16]RGB
	origBdr [9]uint8
	origRol [numRoles]RoleColor
	origNm  string
}

func newThemeModal(a *App) *ThemeModal {
	m := &ThemeModal{origPal: a.cfg.UIPalette, origBdr: a.cfg.Border, origRol: a.cfg.Roles, origNm: a.cfg.Theme}
	m.list = a.allThemes()
	for i, t := range m.list {
		if t.Name == a.cfg.Theme {
			m.Sel = i
		}
	}
	return m
}

func (m *ThemeModal) Draw(c *Canvas) {
	a := theApp
	ix, iy, iw, ih := c.centered(70, 23, "Color schemes", rFrame)
	listW := 28
	listH := ih - 4
	if m.Sel < m.Top {
		m.Top = m.Sel
	}
	if m.Sel >= m.Top+listH {
		m.Top = m.Sel - listH + 1
	}
	c.strClip(ix, iy, listW, "Theme", rHilite)
	for i := 0; i < listH; i++ {
		idx := m.Top + i
		if idx >= len(m.list) {
			break
		}
		role := rText
		if idx == m.Sel {
			role = rSelect
		}
		name := m.list[idx].Name
		if m.list[idx].Custom {
			name = "* " + name
		} else {
			name = "  " + name
		}
		c.fill(ix-1, iy+1+i, listW+1, 1, ' ', role)
		c.strClip(ix, iy+1+i, listW-1, name, role)
	}
	// right-hand preview
	px := ix + listW + 2
	pw := iw - listW - 2
	c.strClip(px, iy, pw, "Preview", rHilite)
	t := m.list[m.Sel]
	cur := a.cfg.Roles
	for i := 0; i < numRoles; i++ {
		r := cur[i]
		txt := fmt.Sprintf(" %-18s", roleNames[i])
		for k, b := range toCP437(txt) {
			if k < pw {
				c.putRaw(px+k, iy+1+i, b, Color(r.Fg), Color(r.Bg))
			}
		}
	}
	c.str(px, iy+numRoles+2, "Interface palette:", rText)
	for i := 0; i < 16; i++ {
		x := px + (i%8)*3
		y := iy + numRoles + 3 + i/8
		for k := 0; k < 3; k++ {
			c.putRaw(x+k, y, ' ', 7, Color(i))
		}
	}
	c.strClip(px, iy+numRoles+6, pw, "Terminal colors unaffected", rText)
	_ = t
	c.strClip(ix, iy+ih-3, iw, "Up/Dn try  Enter=keep  Esc=undo  (menus only, not terminal)", rText)
	c.strClip(ix, iy+ih-2, iw, "B=borders  P=palette  E=elements  S=save as  D=delete", rText)
}

func (m *ThemeModal) Key(a *App, ev KeyEvent) {
	n := len(m.list)
	move := func(d int) {
		m.Sel = clampInt(m.Sel+d, 0, n-1)
		a.applyTheme(m.list[m.Sel])
	}
	switch ev.Key {
	case KeyUp:
		move(-1)
	case KeyDown:
		move(1)
	case KeyPgUp:
		move(-8)
	case KeyPgDn:
		move(8)
	case KeyHome:
		move(-n)
	case KeyEnd:
		move(n)
	case KeyEnter:
		a.cfg.Save()
		a.pop()
	case KeyEsc:
		a.cfg.UIPalette, a.cfg.Roles, a.cfg.Theme, a.cfg.Border = m.origPal, m.origRol, m.origNm, m.origBdr
		a.paletteChanged()
		a.pop()
	default:
		switch ev.Ch {
		case 'b', 'B':
			a.push(newBorderModal(a))
		case 'p', 'P':
			a.push(&PaletteModal{UI: true})
		case 'e', 'E':
			a.push(&RolesModal{})
		case 's', 'S':
			a.inputBox("Save color scheme", "Name", orStr(a.cfg.Theme, "My theme"), 30, func(a *App, name string) {
				name = strings.TrimSpace(name)
				if name == "" {
					return
				}
				for _, b := range builtinThemes {
					if b.Name == name {
						a.errorBox("That name is used by a built-in theme.")
						return
					}
				}
				t := Theme{Name: name, Palette: a.cfg.UIPalette, Roles: a.cfg.Roles, Border: a.cfg.Border}
				replaced := false
				for i := range a.cfg.Themes {
					if a.cfg.Themes[i].Name == name {
						a.cfg.Themes[i], replaced = t, true
					}
				}
				if !replaced {
					a.cfg.Themes = append(a.cfg.Themes, t)
				}
				a.cfg.Theme = name
				a.cfg.Save()
				m.list = a.allThemes()
				for i, x := range m.list {
					if x.Name == name {
						m.Sel = i
					}
				}
				m.origPal, m.origRol, m.origNm, m.origBdr = a.cfg.UIPalette, a.cfg.Roles, name, a.cfg.Border
			})
		case 'd', 'D':
			t := m.list[m.Sel]
			if !t.Custom {
				a.message("Delete", "Built-in themes can't be deleted.")
				return
			}
			a.confirm("Delete", "Delete theme \""+t.Name+"\"?", func(a *App) {
				for i := range a.cfg.Themes {
					if a.cfg.Themes[i].Name == t.Name {
						a.cfg.Themes = append(a.cfg.Themes[:i], a.cfg.Themes[i+1:]...)
						break
					}
				}
				a.cfg.Save()
				m.list = a.allThemes()
				m.Sel = clampInt(m.Sel, 0, len(m.list)-1)
			})
		}
	}
}

// ---------------------------------------------------------------------
// 256-color picker
// ---------------------------------------------------------------------

type ColorPicker struct {
	noMouse
	Title string
	Sel   int
	orig  int
	apply func(a *App, idx int)
}

func (m *ColorPicker) Draw(c *Canvas) {
	ix, iy, iw, _ := c.centered(44, 24, m.Title, rFrame)
	for i := 0; i < 256; i++ {
		x := ix + (i%16)*2
		y := iy + i/16
		for k := 0; k < 2; k++ {
			ch := byte(' ')
			fg := Color(7)
			if i == m.Sel {
				ch = "[]"[k]
				fg = 15
				if i == 15 || i == 7 || (i >= 244 && i < 256) || (i >= 46 && i < 232 && ((i-16)%36)/6 >= 3) {
					fg = 0
				}
			}
			c.putRaw(x+k, y, ch, fg, Color(i))
		}
	}
	rgb := theApp.pal.resolve(Color(m.Sel))
	c.strClip(ix, iy+17, iw, fmt.Sprintf("Index %d   #%06X", m.Sel, rgb), rHilite)
	c.strClip(ix, iy+18, iw, "Arrows=move  Enter=pick  Esc=cancel", rText)
	c.strClip(ix, iy+19, iw, "0-15 = interface palette", rText)
}

func (m *ColorPicker) Key(a *App, ev KeyEvent) {
	move := func(d int) {
		m.Sel = clampInt(m.Sel+d, 0, 255)
		m.apply(a, m.Sel)
		a.touch()
	}
	switch ev.Key {
	case KeyLeft:
		move(-1)
	case KeyRight:
		move(1)
	case KeyUp:
		move(-16)
	case KeyDown:
		move(16)
	case KeyHome:
		move(-256)
	case KeyEnd:
		move(256)
	case KeyEnter:
		a.pop()
	case KeyEsc:
		m.apply(a, m.orig)
		a.touch()
		a.pop()
	}
}

// ---------------------------------------------------------------------
// Interface element colors
// ---------------------------------------------------------------------

type RolesModal struct {
	noMouse
	Sel int
	Col int // 0 = foreground, 1 = background
}

func (m *RolesModal) Draw(c *Canvas) {
	ix, iy, iw, _ := c.centered(66, 21, "Interface element colors", rFrame)
	a := theApp
	c.strClip(ix, iy, iw, "Element              Foreground   Background   Sample", rHilite)
	for i := 0; i < numRoles; i++ {
		y := iy + 1 + i
		r := a.cfg.Roles[i]
		lab := rText
		if i == m.Sel {
			lab = rSelect
		}
		c.fill(ix-1, y, 22, 1, ' ', lab)
		c.strClip(ix, y, 20, roleNames[i], lab)
		for col := 0; col < 2; col++ {
			idx := r.Fg
			if col == 1 {
				idx = r.Bg
			}
			x := ix + 22 + col*13
			role := rText
			if i == m.Sel && col == m.Col {
				role = rSelect
			}
			c.strClip(x, y, 12, "", role)
			c.putRaw(x, y, ' ', 7, Color(idx))
			c.putRaw(x+1, y, ' ', 7, Color(idx))
			c.strClip(x+3, y, 8, fmt.Sprintf("%3d", idx), role)
		}
		for k, b := range toCP437(" Sample text ") {
			c.putRaw(ix+48+k, y, b, Color(r.Fg), Color(r.Bg))
		}
	}
	y := iy + numRoles + 2
	c.strClip(ix, y, iw, "Up/Dn element  Lt/Rt fg/bg  Enter=pick from 256 colors", rText)
	c.strClip(ix, y+1, iw, "X=swap fg/bg  D=reset element  Z=reset all  Esc=done", rText)
	c.strClip(ix, y+2, iw, "Indexes 0-15 use the interface palette; 16-255 are fixed colors.", rText)
}

func (m *RolesModal) Key(a *App, ev KeyEvent) {
	r := &a.cfg.Roles[m.Sel]
	switch ev.Key {
	case KeyEsc:
		a.pop()
	case KeyUp:
		m.Sel = (m.Sel + numRoles - 1) % numRoles
	case KeyDown:
		m.Sel = (m.Sel + 1) % numRoles
	case KeyLeft, KeyRight, KeyTab:
		m.Col ^= 1
	case KeyEnter:
		sel, col := m.Sel, m.Col
		cur := int(r.Fg)
		if col == 1 {
			cur = int(r.Bg)
		}
		name := "foreground"
		if col == 1 {
			name = "background"
		}
		a.push(&ColorPicker{Title: roleNames[sel] + " " + name, Sel: cur, orig: cur, apply: func(a *App, idx int) {
			if col == 0 {
				a.cfg.Roles[sel].Fg = uint8(idx)
			} else {
				a.cfg.Roles[sel].Bg = uint8(idx)
			}
		}})
	default:
		switch ev.Ch {
		case 'x', 'X':
			r.Fg, r.Bg = r.Bg, r.Fg
		case 'd', 'D':
			a.cfg.Roles[m.Sel] = defaultRoles()[m.Sel]
		case 'z', 'Z':
			a.cfg.Roles = defaultRoles()
		}
	}
	a.touch()
}

// ---------------------------------------------------------------------
// Border characters
// ---------------------------------------------------------------------

var borderPosNames = [9]string{"Top-left corner", "Top edge", "Top-right corner", "Left edge", "Right edge",
	"Bottom-left corner", "Bottom edge", "Bottom-right corner", "Desktop backdrop"}

type BorderModal struct {
	noMouse
	Sel    int // 0 = preset, 1..9 = character positions
	Preset int
	orig   [9]uint8
}

func newBorderModal(a *App) *BorderModal {
	m := &BorderModal{orig: a.cfg.Border}
	for i, p := range borderPresets {
		if p.B == a.cfg.Border {
			m.Preset = i
		}
	}
	return m
}

func (m *BorderModal) Draw(c *Canvas) {
	a := theApp
	ix, iy, iw, _ := c.centered(60, 21, "Border characters", rFrame)
	role := rText
	if m.Sel == 0 {
		role = rSelect
	}
	c.fill(ix-1, iy, iw+2, 1, ' ', role)
	c.strClip(ix, iy, iw, "Preset: < "+borderPresets[m.Preset].Name+" >", role)
	for i := 0; i < 9; i++ {
		y := iy + 2 + i
		r := rText
		if m.Sel == i+1 {
			r = rSelect
		}
		ch := a.cfg.Border[i]
		c.fill(ix-1, y, 40, 1, ' ', r)
		c.strClip(ix, y, 22, borderPosNames[i], r)
		c.put(ix+23, y, ch, rInput)
		c.strClip(ix+25, y, 14, fmt.Sprintf("#%d (0x%02X)", ch, ch), r)
	}
	// sample window
	sx, sy := ix+42, iy+2
	for j := 0; j < 5; j++ {
		for i := 0; i < 14; i++ {
			c.put(sx+i, sy+j, a.cfg.Border[8], rBackdrop)
		}
	}
	c.window(sx, sy, 12, 4, "Title", rFrame)
	c.strClip(ix, iy+12, iw, "Up/Dn select   Lt/Rt preset or code -/+", rText)
	c.strClip(ix, iy+13, iw, "Enter = pick from the CP437 table", rText)
	c.strClip(ix, iy+14, iw, "Or just type a character to use it", rText)
	c.strClip(ix, iy+15, iw, "(#NNN codes: any of the 256 CP437 glyphs)", rText)
	c.strClip(ix, iy+17, iw, "F10 = keep   Esc = undo", rHilite)
}

func (m *BorderModal) Key(a *App, ev KeyEvent) {
	b := &a.cfg.Border
	switch ev.Key {
	case KeyEsc:
		*b = m.orig
		a.touch()
		a.pop()
		return
	case KeyF10:
		a.cfg.Save()
		a.pop()
		return
	case KeyUp:
		m.Sel = (m.Sel + 9) % 10
	case KeyDown:
		m.Sel = (m.Sel + 1) % 10
	case KeyLeft, KeyRight:
		d := 1
		if ev.Key == KeyLeft {
			d = -1
		}
		if m.Sel == 0 {
			m.Preset = (m.Preset + d + len(borderPresets)) % len(borderPresets)
			*b = borderPresets[m.Preset].B
		} else {
			b[m.Sel-1] += uint8(d)
		}
	case KeyEnter:
		if m.Sel == 0 {
			a.cfg.Save()
			a.pop()
			return
		}
		i := m.Sel - 1
		a.push(&CharPicker{Title: borderPosNames[i], Sel: int(b[i]), orig: int(b[i]), apply: func(a *App, v int) { a.cfg.Border[i] = uint8(v) }})
	default:
		if m.Sel > 0 && ev.Ch >= 32 && !ev.Ctrl && !ev.Alt {
			if cp, ok := runeToCP437(ev.Ch); ok {
				b[m.Sel-1] = cp
				m.Sel = clampInt(m.Sel+1, 0, 9)
			}
		}
	}
	a.touch()
}

// CharPicker shows all 256 CP437 glyphs.
type CharPicker struct {
	noMouse
	Title string
	Sel   int
	orig  int
	apply func(a *App, v int)
}

func (m *CharPicker) Draw(c *Canvas) {
	ix, iy, iw, _ := c.centered(40, 23, m.Title, rFrame)
	for i := 0; i < 256; i++ {
		x := ix + (i%16)*2
		y := iy + i/16
		r := rInput
		if i == m.Sel {
			r = rSelect
		}
		c.put(x, y, byte(i), r)
		c.put(x+1, y, ' ', r)
	}
	c.strClip(ix, iy+17, iw, fmt.Sprintf("Code %d  (0x%02X)", m.Sel, m.Sel), rHilite)
	c.strClip(ix, iy+18, iw, "Arrows=move  Enter=pick  Esc=cancel", rText)
}

func (m *CharPicker) Key(a *App, ev KeyEvent) {
	move := func(d int) {
		m.Sel = clampInt(m.Sel+d, 0, 255)
		m.apply(a, m.Sel)
		a.touch()
	}
	switch ev.Key {
	case KeyLeft:
		move(-1)
	case KeyRight:
		move(1)
	case KeyUp:
		move(-16)
	case KeyDown:
		move(16)
	case KeyEnter:
		a.pop()
	case KeyEsc:
		m.apply(a, m.orig)
		a.touch()
		a.pop()
	}
}
