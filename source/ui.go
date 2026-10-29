package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------------
// Input events
// ---------------------------------------------------------------------

type Key int

const (
	KeyNone Key = iota + 0x100
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPgUp
	KeyPgDn
	KeyIns
	KeyDel
	KeyEnter
	KeyEsc
	KeyBS
	KeyTab
	KeyF1 // F1..F12 are consecutive
	KeyF2
	KeyF3
	KeyF4
	KeyF5
	KeyF6
	KeyF7
	KeyF8
	KeyF9
	KeyF10
	KeyF11
	KeyF12
)

type KeyEvent struct {
	Key              Key  // special key, or KeyNone
	Ch               rune // character (printable, or control code 1..26 for Ctrl+letter)
	Ctrl, Alt, Shift bool
}

type MouseEvent struct {
	X, Y   int // cell coordinates (may be outside the grid)
	Button int // 0 left, 1 middle, 2 right
	Down   bool
	Up     bool
	Move   bool
	Wheel  int // +1 up, -1 down
	Shift  bool
	Ctrl   bool
	Alt    bool
	Double bool
}

// ---------------------------------------------------------------------
// Canvas: draws UI cells with palette-index colors on an overlay screen.
// ---------------------------------------------------------------------

type Canvas struct {
	S      *Screen
	Mask   []bool
	Base   *Screen // terminal underneath (for shadows); may be nil
	Roles  *[numRoles]RoleColor
	Border [9]uint8 // TL T TR L R BL B BR, backdrop
}

func newCanvas(cols, rows, cw, ch int, roles *[numRoles]RoleColor, base *Screen) *Canvas {
	s := NewScreen(cols, rows, cw, ch)
	return &Canvas{S: s, Mask: make([]bool, cols*rows), Base: base, Roles: roles, Border: borderDouble}
}

func (c *Canvas) W() int { return c.S.Cols }
func (c *Canvas) H() int { return c.S.Rows }

func (c *Canvas) put(x, y int, ch byte, role int) {
	if x < 0 || y < 0 || x >= c.S.Cols || y >= c.S.Rows {
		return
	}
	r := c.Roles[role]
	c.S.Cells[y*c.S.Cols+x] = Cell{Ch: ch, Fg: Color(r.Fg), Bg: Color(r.Bg)}
	c.Mask[y*c.S.Cols+x] = true
}

func toCP437(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		b, ok := runeToCP437(r)
		if !ok {
			b = '?'
		}
		out = append(out, b)
	}
	return out
}

func (c *Canvas) str(x, y int, s string, role int) int {
	for _, b := range toCP437(s) {
		c.put(x, y, b, role)
		x++
	}
	return x
}

// strClip draws s in a field w wide, padding with blanks.
func (c *Canvas) strClip(x, y, w int, s string, role int) {
	b := toCP437(s)
	for i := 0; i < w; i++ {
		ch := byte(' ')
		if i < len(b) {
			ch = b[i]
		}
		c.put(x+i, y, ch, role)
	}
}

func (c *Canvas) fill(x, y, w, h int, ch byte, role int) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			c.put(x+i, y+j, ch, role)
		}
	}
}

func (c *Canvas) shadowCell(x, y int) {
	if x < 0 || y < 0 || x >= c.S.Cols || y >= c.S.Rows {
		return
	}
	ch := c.Border[8] // no terminal underneath: shadow the desktop backdrop
	if c.Base != nil && x < c.Base.Cols && y < c.Base.Rows {
		ch = c.Base.Cells[y*c.Base.Cols+x].Ch
	}
	c.put(x, y, ch, rShadow)
}

// window draws a framed window and returns its interior rectangle.
func (c *Canvas) window(x, y, w, h int, title string, role int) (ix, iy, iw, ih int) {
	c.fill(x, y, w, h, ' ', role)
	b := c.Border
	c.put(x, y, b[0], role)
	c.put(x+w-1, y, b[2], role)
	c.put(x, y+h-1, b[5], role)
	c.put(x+w-1, y+h-1, b[7], role)
	for i := 1; i < w-1; i++ {
		c.put(x+i, y, b[1], role)
		c.put(x+i, y+h-1, b[6], role)
	}
	for j := 1; j < h-1; j++ {
		c.put(x, y+j, b[3], role)
		c.put(x+w-1, y+j, b[4], role)
	}
	if title != "" {
		t := " " + title + " "
		if len(t) > w-4 {
			t = t[:w-4]
		}
		tx := x + (w-len(t))/2
		c.str(tx, y, t, rTitle)
	}
	for j := 1; j <= h; j++ {
		c.shadowCell(x+w, y+j)
		c.shadowCell(x+w+1, y+j)
	}
	for i := 2; i < w+2; i++ {
		c.shadowCell(x+i, y+h)
	}
	return x + 2, y + 1, w - 4, h - 2
}

func (c *Canvas) centered(w, h int, title string, role int) (ix, iy, iw, ih int) {
	if w > c.W()-2 {
		w = c.W() - 2
	}
	if h > c.H()-1 {
		h = c.H() - 1
	}
	x, y := (c.W()-w)/2, (c.H()-h)/2
	return c.window(x, y, w, h, title, role)
}

// ---------------------------------------------------------------------
// Modal interface
// ---------------------------------------------------------------------

type Modal interface {
	Draw(c *Canvas)
	Key(a *App, ev KeyEvent)
	Mouse(a *App, ev MouseEvent)
}

type noMouse struct{}

func (noMouse) Mouse(*App, MouseEvent) {}

func clampInt(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

func trunc(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if n < 1 {
		return ""
	}
	return string(r[:n])
}

// ---------------------------------------------------------------------
// Message / confirm
// ---------------------------------------------------------------------

type MsgModal struct {
	noMouse
	Title   string
	Lines   []string
	Role    int
	Buttons []string
	Sel     int
	OnPick  func(a *App, i int)
}

func wrapLines(s string, w int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, wd := range words {
			if line == "" {
				line = wd
			} else if len(line)+1+len(wd) <= w {
				line += " " + wd
			} else {
				out = append(out, line)
				line = wd
			}
		}
		out = append(out, line)
	}
	return out
}

func (m *MsgModal) Draw(c *Canvas) {
	w := 50
	lines := m.Lines
	h := len(lines) + 5
	ix, iy, iw, _ := c.centered(w, h, m.Title, m.Role)
	for i, l := range lines {
		c.strClip(ix, iy+1+i, iw, l, m.Role)
	}
	total := 0
	for _, b := range m.Buttons {
		total += len(b) + 4
	}
	bx := ix + (iw-total)/2
	for i, b := range m.Buttons {
		role := m.Role
		if i == m.Sel {
			role = rSelect
		}
		c.str(bx, iy+len(lines)+2, "[ "+b+" ]", role)
		bx += len(b) + 4
	}
}

func (m *MsgModal) Key(a *App, ev KeyEvent) {
	switch {
	case ev.Key == KeyLeft || ev.Key == KeyUp || (ev.Key == KeyTab && ev.Shift):
		m.Sel = (m.Sel + len(m.Buttons) - 1) % len(m.Buttons)
	case ev.Key == KeyRight || ev.Key == KeyDown || ev.Key == KeyTab:
		m.Sel = (m.Sel + 1) % len(m.Buttons)
	case ev.Key == KeyEnter || ev.Ch == ' ':
		a.pop()
		if m.OnPick != nil {
			m.OnPick(a, m.Sel)
		}
	case ev.Key == KeyEsc:
		a.pop()
		if m.OnPick != nil && len(m.Buttons) > 1 {
			m.OnPick(a, len(m.Buttons)-1)
		}
	default:
		if ev.Ch != 0 && !ev.Ctrl && !ev.Alt {
			for i, b := range m.Buttons {
				if strings.EqualFold(b[:1], string(ev.Ch)) {
					a.pop()
					if m.OnPick != nil {
						m.OnPick(a, i)
					}
					return
				}
			}
		}
	}
}

func (a *App) message(title, text string) {
	a.push(&MsgModal{Title: title, Lines: wrapLines(text, 44), Role: rFrame, Buttons: []string{"OK"}})
}

func (a *App) errorBox(text string) {
	a.push(&MsgModal{Title: "Error", Lines: wrapLines(text, 44), Role: rError, Buttons: []string{"OK"}})
}

func (a *App) confirm(title, text string, yes func(a *App)) {
	a.push(&MsgModal{Title: title, Lines: wrapLines(text, 44), Role: rFrame, Buttons: []string{"Yes", "No"}, Sel: 1,
		OnPick: func(a *App, i int) {
			if i == 0 {
				yes(a)
			}
		}})
}

// ---------------------------------------------------------------------
// List menu
// ---------------------------------------------------------------------

type ListModal struct {
	Title  string
	Items  []string
	Sel    int
	Top    int
	Width  int
	OnPick func(a *App, i int)
	rect   [4]int
}

func (m *ListModal) Draw(c *Canvas) {
	w := m.Width
	if w == 0 {
		w = 40
		for _, it := range m.Items {
			if len(it)+6 > w {
				w = len(it) + 6
			}
		}
	}
	h := len(m.Items) + 2
	if h > c.H()-2 {
		h = c.H() - 2
	}
	ix, iy, iw, ih := c.centered(w, h, m.Title, rFrame)
	m.rect = [4]int{ix, iy, iw, ih}
	if m.Sel < m.Top {
		m.Top = m.Sel
	}
	if m.Sel >= m.Top+ih {
		m.Top = m.Sel - ih + 1
	}
	for i := 0; i < ih && m.Top+i < len(m.Items); i++ {
		role := rText
		if m.Top+i == m.Sel {
			role = rSelect
		}
		c.fill(ix-1, iy+i, iw+2, 1, ' ', role)
		c.strClip(ix, iy+i, iw, m.Items[m.Top+i], role)
	}
}

func (m *ListModal) Key(a *App, ev KeyEvent) {
	n := len(m.Items)
	switch ev.Key {
	case KeyUp:
		m.Sel = (m.Sel + n - 1) % n
	case KeyDown:
		m.Sel = (m.Sel + 1) % n
	case KeyHome:
		m.Sel = 0
	case KeyEnd:
		m.Sel = n - 1
	case KeyPgUp:
		m.Sel = clampInt(m.Sel-8, 0, n-1)
	case KeyPgDn:
		m.Sel = clampInt(m.Sel+8, 0, n-1)
	case KeyEsc:
		a.pop()
	case KeyEnter:
		a.pop()
		if m.OnPick != nil {
			m.OnPick(a, m.Sel)
		}
	}
}

func (m *ListModal) Mouse(a *App, ev MouseEvent) {
	if ev.Wheel != 0 {
		m.Sel = clampInt(m.Sel-ev.Wheel, 0, len(m.Items)-1)
		return
	}
	if ev.Down && ev.Button == 0 {
		r := m.rect
		if ev.X >= r[0]-1 && ev.X < r[0]+r[2]+1 && ev.Y >= r[1] && ev.Y < r[1]+r[3] {
			i := m.Top + ev.Y - r[1]
			if i < len(m.Items) {
				m.Sel = i
				a.pop()
				if m.OnPick != nil {
					m.OnPick(a, i)
				}
			}
		} else {
			a.pop()
		}
	}
}

// ---------------------------------------------------------------------
// Form
// ---------------------------------------------------------------------

const (
	fText = iota
	fNum
	fChoice
	fBool
	fButton
	fLabel
)

type Field struct {
	Label   string
	Kind    int
	Text    string
	Max     int
	Pos     int
	Choices []string
	Idx     int
	Bool    bool
	Action  func(a *App)
	OnChg   func()
	Secret  bool // draw as asterisks
}

func textField(label, val string, max int) *Field {
	return &Field{Label: label, Kind: fText, Text: val, Max: max, Pos: utf8.RuneCountInString(val)}
}
func numField(label string, val int, max int) *Field {
	s := strconv.Itoa(val)
	return &Field{Label: label, Kind: fNum, Text: s, Max: max, Pos: len(s)}
}
func choiceField(label string, choices []string, cur string) *Field {
	f := &Field{Label: label, Kind: fChoice, Choices: choices}
	for i, c := range choices {
		if c == cur {
			f.Idx = i
		}
	}
	return f
}
func boolField(label string, v bool) *Field { return &Field{Label: label, Kind: fBool, Bool: v} }
func buttonField(label string, act func(a *App)) *Field {
	return &Field{Label: label, Kind: fButton, Action: act}
}

func (f *Field) Int() int { n, _ := strconv.Atoi(f.Text); return n }
func (f *Field) Choice() string {
	if len(f.Choices) == 0 {
		return ""
	}
	return f.Choices[f.Idx]
}

type FormModal struct {
	noMouse
	Title      string
	Fields     []*Field
	Sel        int
	Width      int
	Help       string
	OnSave     func(a *App) bool // return true to close
	EnterSaves bool
	OnCancel   func(a *App)
}

func (m *FormModal) Draw(c *Canvas) {
	w := m.Width
	if w == 0 {
		w = 66
	}
	h := len(m.Fields) + 4
	if m.Help != "" {
		h++
	}
	ix, iy, iw, _ := c.centered(w, h, m.Title, rFrame)
	lw := 0
	for _, f := range m.Fields {
		if len(f.Label) > lw {
			lw = len(f.Label)
		}
	}
	lw += 2
	for i, f := range m.Fields {
		y := iy + 1 + i
		lrole := rText
		if i == m.Sel {
			lrole = rHilite
		}
		switch f.Kind {
		case fButton:
			role := rText
			if i == m.Sel {
				role = rSelect
			}
			c.str(ix+lw, y, "[ "+f.Label+" ]", role)
			continue
		case fLabel:
			c.str(ix, y, f.Label, rHilite)
			continue
		}
		c.strClip(ix, y, lw-1, f.Label+" "+strings.Repeat(".", 40), lrole)
		fw := iw - lw
		role := rInput
		if i == m.Sel {
			role = rInput
		}
		switch f.Kind {
		case fText, fNum:
			shown := f.Text
			if f.Secret {
				shown = strings.Repeat("*", utf8.RuneCountInString(shown))
			}
			c.strClip(ix+lw, y, fw, shown, role)
			if i == m.Sel {
				r := []rune(shown)
				ch := byte(' ')
				if f.Pos < len(r) {
					if b, ok := runeToCP437(r[f.Pos]); ok {
						ch = b
					}
				}
				c.put(ix+lw+clampInt(f.Pos, 0, fw-1), y, ch, rSelect)
			}
		case fChoice:
			s := "< " + f.Choice() + " >"
			if i != m.Sel {
				s = "  " + f.Choice()
			}
			c.strClip(ix+lw, y, fw, s, role)
		case fBool:
			s := "[ ] no"
			if f.Bool {
				s = "[X] yes"
			}
			c.strClip(ix+lw, y, fw, s, role)
		}
	}
	hy := iy + len(m.Fields) + 1
	if m.Help != "" {
		c.strClip(ix, hy, iw, m.Help, rText)
	}
}

func (m *FormModal) Key(a *App, ev KeyEvent) {
	f := m.Fields[m.Sel]
	move := func(d int) {
		for k := 0; k < len(m.Fields); k++ {
			m.Sel = (m.Sel + d + len(m.Fields)) % len(m.Fields)
			if m.Fields[m.Sel].Kind != fLabel {
				return
			}
		}
	}
	switch ev.Key {
	case KeyEsc:
		a.pop()
		if m.OnCancel != nil {
			m.OnCancel(a)
		}
		return
	case KeyF10:
		if m.OnSave != nil && m.OnSave(a) {
			a.pop()
		}
		return
	case KeyUp:
		move(-1)
		return
	case KeyDown:
		move(1)
		return
	case KeyTab:
		if ev.Shift {
			move(-1)
		} else {
			move(1)
		}
		return
	}
	switch f.Kind {
	case fText, fNum:
		r := []rune(f.Text)
		if (ev.Ch == 22 && ev.Ctrl) || (ev.Key == KeyIns && ev.Shift) { // paste
			if plat.Clipboard != nil {
				txt := plat.Clipboard()
				if i := strings.IndexAny(txt, "\r\n"); i >= 0 {
					txt = txt[:i]
				}
				for _, c := range txt {
					if c < 32 || (f.Max > 0 && len(r) >= f.Max) || (f.Kind == fNum && (c < '0' || c > '9')) {
						continue
					}
					r = append(r[:f.Pos], append([]rune{c}, r[f.Pos:]...)...)
					f.Pos++
				}
				f.Text = string(r)
			}
			return
		}
		if (ev.Ch == 3 || ev.Ch == 24) && ev.Ctrl && !f.Secret { // copy / cut the field
			if plat.SetClipboard != nil {
				plat.SetClipboard(f.Text)
			}
			if ev.Ch == 24 {
				f.Text, f.Pos = "", 0
			}
			return
		}
		switch ev.Key {
		case KeyLeft:
			f.Pos = clampInt(f.Pos-1, 0, len(r))
		case KeyRight:
			f.Pos = clampInt(f.Pos+1, 0, len(r))
		case KeyHome:
			f.Pos = 0
		case KeyEnd:
			f.Pos = len(r)
		case KeyBS:
			if f.Pos > 0 {
				r = append(r[:f.Pos-1], r[f.Pos:]...)
				f.Pos--
			}
		case KeyDel:
			if f.Pos < len(r) {
				r = append(r[:f.Pos], r[f.Pos+1:]...)
			}
		case KeyEnter:
			if m.EnterSaves && m.OnSave != nil {
				if m.OnSave(a) {
					a.removeModal(m)
				}
				return
			}
			move(1)
		default:
			if ev.Ch >= 32 && !ev.Ctrl && !ev.Alt && (f.Max == 0 || len(r) < f.Max) {
				if f.Kind == fNum && (ev.Ch < '0' || ev.Ch > '9') {
					return
				}
				r = append(r[:f.Pos], append([]rune{ev.Ch}, r[f.Pos:]...)...)
				f.Pos++
			}
		}
		f.Text = string(r)
	case fChoice:
		switch {
		case ev.Key == KeyLeft:
			f.Idx = (f.Idx + len(f.Choices) - 1) % len(f.Choices)
		case ev.Key == KeyRight || ev.Key == KeyEnter || ev.Ch == ' ':
			f.Idx = (f.Idx + 1) % len(f.Choices)
		}
		if f.OnChg != nil && (ev.Key == KeyLeft || ev.Key == KeyRight || ev.Key == KeyEnter || ev.Ch == ' ') {
			f.OnChg()
		}
	case fBool:
		if ev.Key == KeyEnter || ev.Ch == ' ' || ev.Key == KeyLeft || ev.Key == KeyRight {
			f.Bool = !f.Bool
			if f.OnChg != nil {
				f.OnChg()
			}
		}
	case fButton:
		if ev.Key == KeyEnter || ev.Ch == ' ' {
			if f.Action != nil {
				f.Action(a)
			}
		}
	}
}

// inputBox asks for a single line of text.
// passwordBox is inputBox with the text masked.
func (a *App) passwordBox(title, label string, ok func(a *App, s string), cancel func(a *App)) {
	f := textField(label, "", 128)
	f.Secret = true
	m := &FormModal{Title: title, Fields: []*Field{f}, Width: 60, Help: "Enter = OK   Esc = cancel", EnterSaves: true}
	m.OnSave = func(a *App) bool { a.removeModal(m); ok(a, f.Text); return false }
	m.Fields = append(m.Fields, buttonField("OK", func(a *App) { a.removeModal(m); ok(a, f.Text) }))
	m.OnCancel = cancel
	a.push(m)
}

func (a *App) inputBox(title, label, def string, max int, ok func(a *App, s string)) {
	f := textField(label, def, max)
	m := &FormModal{Title: title, Fields: []*Field{f}, Width: 60, Help: "Enter = OK   Esc = cancel", EnterSaves: true}
	m.OnSave = func(a *App) bool { a.removeModal(m); ok(a, f.Text); return false }
	m.Fields = append(m.Fields, buttonField("OK", func(a *App) { a.removeModal(m); ok(a, f.Text) }))
	a.push(m)
}

// ---------------------------------------------------------------------
// File picker
// ---------------------------------------------------------------------

type PickEntry struct {
	Name  string
	Path  string
	Dir   bool
	Size  int64
	Drive bool
}

type PickerModal struct {
	Title    string
	Dir      string
	Ents     []PickEntry
	Sel      int
	Top      int
	Marked   map[string]bool
	OnDone   func(a *App, paths []string)
	OnCancel func(a *App)
	Err      string
	rect     [4]int
}

func newPicker(title, dir string, onDone func(a *App, paths []string)) *PickerModal {
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	p := &PickerModal{Title: title, Marked: map[string]bool{}, OnDone: onDone}
	p.cd(dir)
	return p
}

func (p *PickerModal) cd(dir string) {
	p.Err = ""
	var ents []PickEntry
	if dir == "" && plat.Drives != nil {
		for _, d := range plat.Drives() {
			ents = append(ents, PickEntry{Name: d, Path: d, Dir: true, Drive: true})
		}
		p.Dir, p.Ents, p.Sel, p.Top = "", ents, 0, 0
		return
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		p.Err = err.Error()
		return
	}
	p.Dir = dir
	parent := filepath.Dir(dir)
	if parent == dir {
		if plat.Drives != nil {
			ents = append(ents, PickEntry{Name: "..", Path: "", Dir: true})
		}
	} else {
		ents = append(ents, PickEntry{Name: "..", Path: parent, Dir: true})
	}
	var dirs, files []PickEntry
	for _, d := range des {
		e := PickEntry{Name: d.Name(), Path: filepath.Join(dir, d.Name()), Dir: d.IsDir()}
		if info, err := d.Info(); err == nil {
			e.Size = info.Size()
		}
		if e.Dir {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	less := func(s []PickEntry) {
		sort.Slice(s, func(i, j int) bool { return strings.ToLower(s[i].Name) < strings.ToLower(s[j].Name) })
	}
	less(dirs)
	less(files)
	ents = append(ents, dirs...)
	ents = append(ents, files...)
	p.Ents, p.Sel, p.Top = ents, 0, 0
}

func (p *PickerModal) Draw(c *Canvas) {
	h := c.H() - 2
	if h > 22 {
		h = 22
	}
	ix, iy, iw, ih := c.centered(70, h, p.Title, rFrame)
	d := p.Dir
	if d == "" {
		d = "(drives)"
	}
	c.strClip(ix, iy, iw, d, rHilite)
	if p.Err != "" {
		c.strClip(ix, iy+1, iw, p.Err, rError)
	}
	listH := ih - 3
	p.rect = [4]int{ix, iy + 2, iw, listH}
	if p.Sel < p.Top {
		p.Top = p.Sel
	}
	if p.Sel >= p.Top+listH {
		p.Top = p.Sel - listH + 1
	}
	for i := 0; i < listH && p.Top+i < len(p.Ents); i++ {
		e := p.Ents[p.Top+i]
		role := rText
		if p.Top+i == p.Sel {
			role = rSelect
		}
		mark := "   "
		if p.Marked[e.Path] {
			mark = "[x]"
		}
		sz := ""
		name := e.Name
		if e.Dir && e.Name != ".." {
			name += string(filepath.Separator)
			sz = "<DIR>"
		} else if !e.Dir {
			sz = fmt.Sprintf("%d", e.Size)
		}
		c.fill(ix-1, iy+2+i, iw+2, 1, ' ', role)
		c.strClip(ix, iy+2+i, 3, mark, role)
		c.strClip(ix+4, iy+2+i, iw-18, name, role)
		c.strClip(ix+iw-13, iy+2+i, 13, fmt.Sprintf("%13s", sz), role)
	}
	c.strClip(ix, iy+ih-1, iw, "Enter=open/send  Space=mark  Bksp=up  F10=send marked  Esc=cancel", rText)
}

func (p *PickerModal) Key(a *App, ev KeyEvent) {
	n := len(p.Ents)
	switch ev.Key {
	case KeyEsc:
		a.pop()
		if p.OnCancel != nil {
			p.OnCancel(a)
		}
	case KeyUp:
		p.Sel = clampInt(p.Sel-1, 0, n-1)
	case KeyDown:
		p.Sel = clampInt(p.Sel+1, 0, n-1)
	case KeyPgUp:
		p.Sel = clampInt(p.Sel-10, 0, n-1)
	case KeyPgDn:
		p.Sel = clampInt(p.Sel+10, 0, n-1)
	case KeyHome:
		p.Sel = 0
	case KeyEnd:
		p.Sel = n - 1
	case KeyBS:
		if p.Dir != "" {
			par := filepath.Dir(p.Dir)
			if par == p.Dir {
				par = ""
			}
			p.cd(par)
		}
	case KeyIns:
		p.toggle()
		p.Sel = clampInt(p.Sel+1, 0, n-1)
	case KeyF10:
		p.finish(a, false)
	case KeyEnter:
		if n == 0 {
			return
		}
		e := p.Ents[p.Sel]
		if e.Dir {
			p.cd(e.Path)
		} else {
			p.finish(a, true)
		}
	default:
		if ev.Ch == ' ' {
			p.toggle()
			p.Sel = clampInt(p.Sel+1, 0, n-1)
		}
	}
}

func (p *PickerModal) toggle() {
	if len(p.Ents) == 0 {
		return
	}
	e := p.Ents[p.Sel]
	if e.Dir {
		return
	}
	if p.Marked[e.Path] {
		delete(p.Marked, e.Path)
	} else {
		p.Marked[e.Path] = true
	}
}

func (p *PickerModal) finish(a *App, useCurrent bool) {
	var paths []string
	for k := range p.Marked {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	if len(paths) == 0 && useCurrent && len(p.Ents) > 0 && !p.Ents[p.Sel].Dir {
		paths = []string{p.Ents[p.Sel].Path}
	}
	if len(paths) == 0 {
		return
	}
	a.pop()
	p.OnDone(a, paths)
}

func (p *PickerModal) Mouse(a *App, ev MouseEvent) {
	if ev.Wheel != 0 {
		p.Sel = clampInt(p.Sel-ev.Wheel*3, 0, len(p.Ents)-1)
		return
	}
	if ev.Down && ev.Button == 0 {
		r := p.rect
		if ev.X >= r[0]-1 && ev.X < r[0]+r[2]+1 && ev.Y >= r[1] && ev.Y < r[1]+r[3] {
			i := p.Top + ev.Y - r[1]
			if i < len(p.Ents) {
				if i == p.Sel || ev.Double {
					p.Key(a, KeyEvent{Key: KeyEnter})
				}
				p.Sel = clampInt(i, 0, len(p.Ents)-1)
			}
		}
	}
}
