package main

import "strings"

// ---------------------------------------------------------------------
// Colors
// ---------------------------------------------------------------------

// Color is either a palette index (0-255) or a 24-bit RGB value tagged
// with rgbFlag. Cells store palette *indexes* for the classic 16 colors
// and the xterm 256-color cube, so editing the palette recolors text that
// is already on screen -- exactly how a DOS/SyncTERM palette behaves.
type Color uint32

const rgbFlag Color = 1 << 24

func rgbColor(r, g, b uint8) Color {
	return rgbFlag | Color(r)<<16 | Color(g)<<8 | Color(b)
}

func (c Color) isRGB() bool { return c&rgbFlag != 0 }

// Palette holds 256 resolved 0xRRGGBB colors.
type Palette [256]uint32

// RGB is a plain 24-bit color used in the config file.
type RGB struct {
	R uint8 `json:"r"`
	G uint8 `json:"g"`
	B uint8 `json:"b"`
}

func (c RGB) u32() uint32 { return uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B) }

// BuildPalette expands the 16 user-editable colors with the standard
// xterm 6x6x6 color cube (16-231) and 24-step grayscale ramp (232-255).
func BuildPalette(base *[16]RGB) *Palette {
	var p Palette
	for i := 0; i < 16; i++ {
		p[i] = base[i].u32()
	}
	lv := [6]uint32{0, 95, 135, 175, 215, 255}
	for i := 0; i < 216; i++ {
		r, g, b := lv[i/36], lv[(i/6)%6], lv[i%6]
		p[16+i] = r<<16 | g<<8 | b
	}
	for i := 0; i < 24; i++ {
		v := uint32(8 + i*10)
		p[232+i] = v<<16 | v<<8 | v
	}
	return &p
}

func (p *Palette) resolve(c Color) uint32 {
	if c.isRGB() {
		return uint32(c) & 0xFFFFFF
	}
	return p[c&0xFF]
}

// ---------------------------------------------------------------------
// Cells and Screen
// ---------------------------------------------------------------------

const (
	aBlink uint8 = 1 << iota
	aUnder
	aStrike
)

// Cell is one character position.
type Cell struct {
	Ch     uint8 // CP437 glyph index
	Attr   uint8
	Fg, Bg Color
}

type savedCursor struct {
	x, y                     int
	fg, bg                   Color
	bold, blink, under       bool
	strike, reverse, conceal bool
	wrap, origin             bool
}

type selPoint struct {
	line int64 // absolute line number (see Screen.linesPushed)
	col  int
}

// Screen is the emulated terminal display: a grid of cells, cursor and
// rendition state, scrollback, and an optional pixel layer for sixel
// graphics.
type Screen struct {
	Cols, Rows int
	CW, CH     int // cell pixel size (for the graphics layer)
	Cells      []Cell

	CX, CY        int
	wrapPending   bool
	CursorVisible bool

	// current rendition
	fg, bg                                   Color
	bold, blink, under, strike, rev, conceal bool

	// modes
	AutoWrap      bool
	Origin        bool
	InsertMode    bool
	ICE           bool // DECSET 33: blink bit means bright background
	NoBright      bool // DECSET 32
	NoBlink       bool // DECSET 35
	SixelNoScroll bool // DECSET 80

	top, bot int // scroll region (inclusive)
	tabs     []bool

	saved savedCursor

	// scrollback
	sb          [][]Cell
	SbMax       int
	linesPushed int64
	ViewOffset  int  // lines scrolled back from the live screen
	KeepOnClear bool // push the screen into scrollback before a full clear

	// selection (absolute line numbers)
	SelActive  bool
	SelA, SelB selPoint

	// sixel pixel layer: 0 = transparent, else 0xFF000000|rgb
	gfx []uint32

	Dirty bool
}

func NewScreen(cols, rows, cw, ch int) *Screen {
	s := &Screen{Cols: cols, Rows: rows, CW: cw, CH: ch, SbMax: 2000}
	s.Cells = make([]Cell, cols*rows)
	s.Reset()
	return s
}

func (s *Screen) Reset() {
	s.CX, s.CY = 0, 0
	s.wrapPending = false
	s.CursorVisible = true
	s.AutoWrap = true
	s.Origin = false
	s.InsertMode = false
	s.ICE, s.NoBright, s.NoBlink, s.SixelNoScroll = false, false, false, false
	s.ResetRendition()
	s.top, s.bot = 0, s.Rows-1
	s.tabs = make([]bool, s.Cols)
	for i := 0; i < s.Cols; i += 8 {
		s.tabs[i] = true
	}
	s.gfx = nil
	s.saved = savedCursor{fg: 7}
	s.ViewOffset = 0
	s.SelActive = false
	s.clearAll()
}

func (s *Screen) ResetRendition() {
	s.fg, s.bg = 7, 0
	s.bold, s.blink, s.under, s.strike, s.rev, s.conceal = false, false, false, false, false, false
}

func (s *Screen) idx(x, y int) int { return y*s.Cols + x }

// ---- rendition setters (called by the parser) ----

func (s *Screen) SetFg(c Color) { s.fg = c }
func (s *Screen) SetBg(c Color) { s.bg = c }

// curAttr resolves the current rendition into fg/bg/attr for a new cell.
func (s *Screen) curAttr() (fg, bg Color, attr uint8) {
	fg, bg = s.fg, s.bg
	if s.bold && !s.NoBright && !fg.isRGB() && fg < 8 {
		fg += 8
	}
	if s.blink && !s.NoBlink {
		attr |= aBlink
	}
	if s.under {
		attr |= aUnder
	}
	if s.strike {
		attr |= aStrike
	}
	if s.rev {
		fg, bg = bg, fg
	}
	if s.conceal {
		fg = bg
	}
	return
}

func (s *Screen) blankCell() Cell {
	fg, bg, _ := s.curAttr()
	return Cell{Ch: ' ', Fg: fg, Bg: bg}
}

func (s *Screen) clearAll() {
	b := s.blankCell()
	for i := range s.Cells {
		s.Cells[i] = b
	}
	s.gfx = nil
	s.Dirty = true
}

// ---------------------------------------------------------------------
// Graphics layer helpers
// ---------------------------------------------------------------------

func (s *Screen) gfxStride() int { return s.Cols * s.CW }

func (s *Screen) ensureGfx() {
	if s.gfx == nil {
		s.gfx = make([]uint32, s.Cols*s.CW*s.Rows*s.CH)
	}
}

// clearGfxCells erases the pixel layer under the given cell rectangle
// (inclusive coordinates).
func (s *Screen) clearGfxCells(x0, y0, x1, y1 int) {
	if s.gfx == nil {
		return
	}
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 >= s.Cols {
		x1 = s.Cols - 1
	}
	if y1 >= s.Rows {
		y1 = s.Rows - 1
	}
	if x1 < x0 || y1 < y0 {
		return
	}
	stride := s.gfxStride()
	for py := y0 * s.CH; py < (y1+1)*s.CH; py++ {
		row := s.gfx[py*stride+x0*s.CW : py*stride+(x1+1)*s.CW]
		for i := range row {
			row[i] = 0
		}
	}
}

// moveGfxRows moves whole cell rows [srcRow, srcRow+n) to dstRow.
func (s *Screen) moveGfxRows(srcRow, dstRow, n int) {
	if s.gfx == nil || n <= 0 {
		return
	}
	stride := s.gfxStride()
	copy(s.gfx[dstRow*s.CH*stride:(dstRow+n)*s.CH*stride], s.gfx[srcRow*s.CH*stride:(srcRow+n)*s.CH*stride])
}

// ---------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------

// PutChar writes a glyph at the cursor using the xterm "pending wrap"
// model that SyncTERM's cterm also uses (last-column flag).
func (s *Screen) PutChar(ch uint8) {
	if s.wrapPending {
		s.wrapPending = false
		if s.AutoWrap {
			s.CX = 0
			s.lineFeed()
		}
	}
	if s.InsertMode {
		row := s.idx(0, s.CY)
		copy(s.Cells[row+s.CX+1:row+s.Cols], s.Cells[row+s.CX:row+s.Cols-1])
		s.clearGfxCells(s.CX, s.CY, s.Cols-1, s.CY)
	}
	fg, bg, attr := s.curAttr()
	s.Cells[s.idx(s.CX, s.CY)] = Cell{Ch: ch, Attr: attr, Fg: fg, Bg: bg}
	if s.gfx != nil {
		s.clearGfxCells(s.CX, s.CY, s.CX, s.CY)
	}
	if s.CX >= s.Cols-1 {
		if s.AutoWrap {
			s.wrapPending = true
		}
	} else {
		s.CX++
	}
	s.Dirty = true
}

func (s *Screen) CarriageReturn() { s.CX = 0; s.wrapPending = false }

func (s *Screen) Backspace() {
	s.wrapPending = false
	if s.CX > 0 {
		s.CX--
	}
}

func (s *Screen) Tab() {
	s.wrapPending = false
	x := s.CX + 1
	for x < s.Cols-1 && !s.tabs[x] {
		x++
	}
	if x >= s.Cols {
		x = s.Cols - 1
	}
	s.CX = x
}

func (s *Screen) BackTab(n int) {
	s.wrapPending = false
	for ; n > 0; n-- {
		x := s.CX - 1
		for x > 0 && !s.tabs[x] {
			x--
		}
		if x < 0 {
			x = 0
		}
		s.CX = x
	}
}

func (s *Screen) ForwardTab(n int) {
	for ; n > 0; n-- {
		s.Tab()
	}
}

func (s *Screen) SetTab() { s.tabs[s.CX] = true }
func (s *Screen) ClearTab(all bool) {
	if all {
		for i := range s.tabs {
			s.tabs[i] = false
		}
	} else {
		s.tabs[s.CX] = false
	}
}

func (s *Screen) LineFeed() { s.wrapPending = false; s.lineFeed() }

func (s *Screen) lineFeed() {
	if s.CY == s.bot {
		s.scrollUp(1)
	} else if s.CY < s.Rows-1 {
		s.CY++
	}
	s.Dirty = true
}

func (s *Screen) ReverseIndex() {
	s.wrapPending = false
	if s.CY == s.top {
		s.scrollDown(1)
	} else if s.CY > 0 {
		s.CY--
	}
	s.Dirty = true
}

// scrollUp scrolls the scroll region up by n lines.
func (s *Screen) scrollUp(n int) {
	top, bot := s.top, s.bot
	if n <= 0 {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	if top == 0 && s.SbMax > 0 {
		for i := 0; i < n; i++ {
			line := make([]Cell, s.Cols)
			copy(line, s.Cells[s.idx(0, i):s.idx(0, i)+s.Cols])
			s.sb = append(s.sb, line)
			s.linesPushed++
		}
		if over := len(s.sb) - s.SbMax; over > 0 {
			s.sb = append(s.sb[:0], s.sb[over:]...)
		}
	}
	copy(s.Cells[s.idx(0, top):s.idx(0, bot-n+1)], s.Cells[s.idx(0, top+n):s.idx(0, bot+1)])
	b := s.blankCell()
	for y := bot - n + 1; y <= bot; y++ {
		for x := 0; x < s.Cols; x++ {
			s.Cells[s.idx(x, y)] = b
		}
	}
	if s.gfx != nil {
		s.moveGfxRows(top+n, top, bot-top+1-n)
		s.clearGfxCells(0, bot-n+1, s.Cols-1, bot)
	}
	s.Dirty = true
}

func (s *Screen) scrollDown(n int) {
	top, bot := s.top, s.bot
	if n <= 0 {
		return
	}
	if n > bot-top+1 {
		n = bot - top + 1
	}
	copy(s.Cells[s.idx(0, top+n):s.idx(0, bot+1)], s.Cells[s.idx(0, top):s.idx(0, bot-n+1)])
	b := s.blankCell()
	for y := top; y < top+n; y++ {
		for x := 0; x < s.Cols; x++ {
			s.Cells[s.idx(x, y)] = b
		}
	}
	if s.gfx != nil {
		// copy is memmove-safe for overlapping ranges
		s.moveGfxRows(top, top+n, bot-top+1-n)
		s.clearGfxCells(0, top, s.Cols-1, top+n-1)
	}
	s.Dirty = true
}

func (s *Screen) ScrollUp(n int)   { s.scrollUp(n) }
func (s *Screen) ScrollDown(n int) { s.scrollDown(n) }

func (s *Screen) clampCursor() {
	if s.CX < 0 {
		s.CX = 0
	}
	if s.CX >= s.Cols {
		s.CX = s.Cols - 1
	}
	if s.CY < 0 {
		s.CY = 0
	}
	if s.CY >= s.Rows {
		s.CY = s.Rows - 1
	}
}

func (s *Screen) CursorUp(n int) {
	s.wrapPending = false
	lim := 0
	if s.CY >= s.top {
		lim = s.top
	}
	s.CY -= n
	if s.CY < lim {
		s.CY = lim
	}
}

func (s *Screen) CursorDown(n int) {
	s.wrapPending = false
	lim := s.Rows - 1
	if s.CY <= s.bot {
		lim = s.bot
	}
	s.CY += n
	if s.CY > lim {
		s.CY = lim
	}
}

func (s *Screen) CursorFwd(n int)  { s.wrapPending = false; s.CX += n; s.clampCursor() }
func (s *Screen) CursorBack(n int) { s.wrapPending = false; s.CX -= n; s.clampCursor() }
func (s *Screen) CursorCol(c int)  { s.wrapPending = false; s.CX = c; s.clampCursor() }

// CursorRow sets the (1-based already converted to 0-based) row,
// honoring origin mode.
func (s *Screen) CursorRow(r int) {
	s.wrapPending = false
	if s.Origin {
		r += s.top
		if r > s.bot {
			r = s.bot
		}
	}
	s.CY = r
	s.clampCursor()
}

func (s *Screen) CursorPos(row, col int) {
	s.CursorRow(row)
	s.CursorCol(col)
}

func (s *Screen) SetScrollRegion(top, bot int) {
	if top < 0 {
		top = 0
	}
	if bot >= s.Rows || bot < 0 {
		bot = s.Rows - 1
	}
	if top >= bot {
		top, bot = 0, s.Rows-1
	}
	s.top, s.bot = top, bot
	s.CursorPos(0, 0)
}

func (s *Screen) SaveCursor() {
	s.saved = savedCursor{
		x: s.CX, y: s.CY, fg: s.fg, bg: s.bg,
		bold: s.bold, blink: s.blink, under: s.under, strike: s.strike,
		reverse: s.rev, conceal: s.conceal, wrap: s.wrapPending, origin: s.Origin,
	}
}

func (s *Screen) RestoreCursor() {
	v := s.saved
	s.CX, s.CY, s.fg, s.bg = v.x, v.y, v.fg, v.bg
	s.bold, s.blink, s.under, s.strike = v.bold, v.blink, v.under, v.strike
	s.rev, s.conceal, s.wrapPending, s.Origin = v.reverse, v.conceal, v.wrap, v.origin
	s.clampCursor()
}

// ---- erase / insert / delete ----

// EraseDisplay: 0 = cursor to end, 1 = start to cursor, 2/3 = all.
func (s *Screen) EraseDisplay(mode int) {
	b := s.blankCell()
	switch mode {
	case 0:
		if s.CX == 0 && s.CY == 0 {
			s.pushScreen()
		}
		for i := s.idx(s.CX, s.CY); i < len(s.Cells); i++ {
			s.Cells[i] = b
		}
		s.clearGfxCells(s.CX, s.CY, s.Cols-1, s.CY)
		s.clearGfxCells(0, s.CY+1, s.Cols-1, s.Rows-1)
	case 1:
		for i := 0; i <= s.idx(s.CX, s.CY); i++ {
			s.Cells[i] = b
		}
		s.clearGfxCells(0, 0, s.Cols-1, s.CY-1)
		s.clearGfxCells(0, s.CY, s.CX, s.CY)
	default:
		s.pushScreen()
		for i := range s.Cells {
			s.Cells[i] = b
		}
		s.gfx = nil
	}
	s.Dirty = true
}

// EraseLine: 0 = cursor to end, 1 = start to cursor, 2 = whole line.
func (s *Screen) EraseLine(mode int) {
	b := s.blankCell()
	x0, x1 := 0, s.Cols-1
	switch mode {
	case 0:
		x0 = s.CX
	case 1:
		x1 = s.CX
	}
	for x := x0; x <= x1; x++ {
		s.Cells[s.idx(x, s.CY)] = b
	}
	s.clearGfxCells(x0, s.CY, x1, s.CY)
	s.Dirty = true
}

func (s *Screen) EraseChars(n int) {
	b := s.blankCell()
	x1 := s.CX + n - 1
	if x1 >= s.Cols {
		x1 = s.Cols - 1
	}
	for x := s.CX; x <= x1; x++ {
		s.Cells[s.idx(x, s.CY)] = b
	}
	s.clearGfxCells(s.CX, s.CY, x1, s.CY)
	s.Dirty = true
}

func (s *Screen) DeleteChars(n int) {
	s.wrapPending = false
	if n > s.Cols-s.CX {
		n = s.Cols - s.CX
	}
	row := s.idx(0, s.CY)
	copy(s.Cells[row+s.CX:row+s.Cols-n], s.Cells[row+s.CX+n:row+s.Cols])
	b := s.blankCell()
	for x := s.Cols - n; x < s.Cols; x++ {
		s.Cells[row+x] = b
	}
	s.clearGfxCells(s.CX, s.CY, s.Cols-1, s.CY)
	s.Dirty = true
}

func (s *Screen) InsertChars(n int) {
	s.wrapPending = false
	if n > s.Cols-s.CX {
		n = s.Cols - s.CX
	}
	row := s.idx(0, s.CY)
	copy(s.Cells[row+s.CX+n:row+s.Cols], s.Cells[row+s.CX:row+s.Cols-n])
	b := s.blankCell()
	for x := s.CX; x < s.CX+n; x++ {
		s.Cells[row+x] = b
	}
	s.clearGfxCells(s.CX, s.CY, s.Cols-1, s.CY)
	s.Dirty = true
}

func (s *Screen) InsertLines(n int) {
	if s.CY < s.top || s.CY > s.bot {
		return
	}
	s.wrapPending = false
	s.CX = 0
	save := s.top
	s.top = s.CY
	s.scrollDown(n)
	s.top = save
}

func (s *Screen) DeleteLines(n int) {
	if s.CY < s.top || s.CY > s.bot {
		return
	}
	s.wrapPending = false
	s.CX = 0
	save, saveSb := s.top, s.SbMax
	s.top = s.CY
	s.SbMax = 0 // deleted lines are not scrollback
	s.scrollUp(n)
	s.top, s.SbMax = save, saveSb
}

// ---------------------------------------------------------------------
// Scrollback / selection / text export
// ---------------------------------------------------------------------

func (s *Screen) HasBlink() bool {
	for i := range s.Cells {
		if s.Cells[i].Attr&aBlink != 0 {
			return true
		}
	}
	return false
}

func rowBlank(r []Cell) bool {
	for _, c := range r {
		if c.Ch != 0 && c.Ch != ' ' {
			return false
		}
	}
	return true
}

func rowsEqual(a, b []Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Ch != b[i].Ch || a[i].Fg != b[i].Fg || a[i].Bg != b[i].Bg || a[i].Attr != b[i].Attr {
			return false
		}
	}
	return true
}

// pushScreen copies the visible screen (up to its last non-blank row) into
// scrollback so a full-screen clear does not lose it. An identical repeat of
// what was pushed last is skipped, so redraw loops do not flood scrollback.
func (s *Screen) pushScreen() {
	if !s.KeepOnClear || s.SbMax <= 0 {
		return
	}
	last := -1
	for y := s.Rows - 1; y >= 0; y-- {
		if !rowBlank(s.Cells[s.idx(0, y) : s.idx(0, y)+s.Cols]) {
			last = y
			break
		}
	}
	if last < 0 {
		return
	}
	n := last + 1
	if len(s.sb) >= n {
		same := true
		for i := 0; i < n && same; i++ {
			same = rowsEqual(s.sb[len(s.sb)-n+i], s.Cells[s.idx(0, i):s.idx(0, i)+s.Cols])
		}
		if same {
			return
		}
	}
	for i := 0; i < n; i++ {
		line := make([]Cell, s.Cols)
		copy(line, s.Cells[s.idx(0, i):s.idx(0, i)+s.Cols])
		s.sb = append(s.sb, line)
		s.linesPushed++
	}
	if over := len(s.sb) - s.SbMax; over > 0 {
		s.sb = append(s.sb[:0], s.sb[over:]...)
	}
}

// SbLen is the number of lines in scrollback.
func (s *Screen) SbLen() int { return len(s.sb) }

// ClearScrollback discards all scrollback.
func (s *Screen) ClearScrollback() {
	s.sb = nil
	s.ViewOffset = 0
	s.SelActive = false
	s.Dirty = true
}

func (s *Screen) lineText(cells []Cell) string {
	r := make([]rune, 0, len(cells))
	for _, c := range cells {
		r = append(r, cp437ToRune(c.Ch))
	}
	return strings.TrimRight(string(r), " \x00")
}

// ScrollbackText returns scrollback plus the live screen as text.
func (s *Screen) ScrollbackText() string {
	var sb strings.Builder
	for _, l := range s.sb {
		sb.WriteString(s.lineText(l))
		sb.WriteString("\r\n")
	}
	last := s.Rows - 1
	for last >= 0 && rowBlank(s.Cells[s.idx(0, last):s.idx(0, last)+s.Cols]) {
		last--
	}
	for y := 0; y <= last; y++ {
		sb.WriteString(s.lineText(s.Cells[s.idx(0, y) : s.idx(0, y)+s.Cols]))
		sb.WriteString("\r\n")
	}
	return sb.String()
}

// Find searches (case-insensitively) the scrollback and live screen for q,
// starting beside absolute line `from` (exclusive); dir<0 goes toward older
// lines, dir>0 toward newer. It scrolls the view to the match and selects it.
func (s *Screen) Find(q string, from int64, dir int) (int64, bool) {
	if q == "" {
		return 0, false
	}
	ql := strings.ToLower(q)
	base := s.linesPushed - int64(len(s.sb))
	total := int64(len(s.sb) + s.Rows)
	li := from - base + int64(dir)
	for ; li >= 0 && li < total; li += int64(dir) {
		var cells []Cell
		if li < int64(len(s.sb)) {
			cells = s.sb[li]
		} else {
			r := int(li) - len(s.sb)
			cells = s.Cells[s.idx(0, r) : s.idx(0, r)+s.Cols]
		}
		txt := []rune(strings.ToLower(s.lineText(cells)))
		qr := []rune(ql)
		for x := 0; x+len(qr) <= len(txt); x++ {
			if string(txt[x:x+len(qr)]) == ql {
				abs := base + li
				s.SelActive = true
				s.SelA = selPoint{abs, x}
				s.SelB = selPoint{abs, x + len(qr) - 1}
				off := len(s.sb) + s.Rows/3 - int(li)
				if off < 0 {
					off = 0
				}
				if off > len(s.sb) {
					off = len(s.sb)
				}
				s.ViewOffset = off
				s.Dirty = true
				return abs, true
			}
		}
	}
	return from, false
}

// ScrollView moves the scrollback view; positive = further back.
func (s *Screen) ScrollView(delta int) {
	s.ViewOffset += delta
	if s.ViewOffset > len(s.sb) {
		s.ViewOffset = len(s.sb)
	}
	if s.ViewOffset < 0 {
		s.ViewOffset = 0
	}
	s.Dirty = true
}

// rowAtView returns the cells shown on display row y given the view offset.
func (s *Screen) rowAtView(y int) []Cell {
	li := len(s.sb) - s.ViewOffset + y // index into sb ++ screen
	if li < 0 {
		return nil
	}
	if li < len(s.sb) {
		return s.sb[li]
	}
	r := li - len(s.sb)
	if r >= s.Rows {
		return nil
	}
	return s.Cells[s.idx(0, r) : s.idx(0, r)+s.Cols]
}

// absLineAtView returns the absolute line number shown on display row y.
func (s *Screen) absLineAtView(y int) int64 {
	return s.linesPushed - int64(s.ViewOffset) + int64(y)
}

func (s *Screen) lineByAbs(abs int64) []Cell {
	idx := abs - (s.linesPushed - int64(len(s.sb)))
	if idx < 0 {
		return nil
	}
	if idx < int64(len(s.sb)) {
		return s.sb[idx]
	}
	r := int(idx) - len(s.sb)
	if r >= s.Rows {
		return nil
	}
	return s.Cells[s.idx(0, r) : s.idx(0, r)+s.Cols]
}

func (s *Screen) SelClear() { s.SelActive = false; s.Dirty = true }

func (s *Screen) selOrdered() (a, b selPoint) {
	a, b = s.SelA, s.SelB
	if b.line < a.line || (b.line == a.line && b.col < a.col) {
		a, b = b, a
	}
	return
}

// inSelection reports whether the cell at absolute line/col is selected.
func (s *Screen) inSelection(line int64, col int) bool {
	if !s.SelActive {
		return false
	}
	a, b := s.selOrdered()
	if line < a.line || line > b.line {
		return false
	}
	if line == a.line && col < a.col {
		return false
	}
	if line == b.line && col > b.col {
		return false
	}
	return true
}

// SelectedText returns the selection as Unicode text (CRLF line breaks,
// trailing blanks trimmed).
func (s *Screen) SelectedText() string {
	if !s.SelActive {
		return ""
	}
	a, b := s.selOrdered()
	var sb strings.Builder
	for ln := a.line; ln <= b.line; ln++ {
		cells := s.lineByAbs(ln)
		if cells == nil {
			continue
		}
		c0, c1 := 0, s.Cols-1
		if ln == a.line {
			c0 = a.col
		}
		if ln == b.line {
			c1 = b.col
		}
		var line []rune
		for c := c0; c <= c1 && c < len(cells); c++ {
			line = append(line, cp437ToRune(cells[c].Ch))
		}
		txt := strings.TrimRight(string(line), " ")
		sb.WriteString(txt)
		if ln != b.line {
			sb.WriteString("\r\n")
		}
	}
	return sb.String()
}

// AllText returns the whole visible screen as text.
func (s *Screen) AllText() string {
	var sb strings.Builder
	for y := 0; y < s.Rows; y++ {
		var line []rune
		for x := 0; x < s.Cols; x++ {
			line = append(line, cp437ToRune(s.Cells[s.idx(x, y)].Ch))
		}
		sb.WriteString(strings.TrimRight(string(line), " "))
		sb.WriteString("\r\n")
	}
	return sb.String()
}

// Clone makes an independent copy of the visible screen (used for
// drawing menus on top of a live terminal without disturbing it).
func (s *Screen) Clone() *Screen {
	c := *s
	c.Cells = append([]Cell(nil), s.Cells...)
	if s.gfx != nil {
		c.gfx = append([]uint32(nil), s.gfx...)
	}
	c.sb = nil
	c.ViewOffset = 0
	c.SelActive = false
	return &c
}
