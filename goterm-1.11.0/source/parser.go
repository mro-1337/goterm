package main

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	stGround = iota
	stEsc
	stEscInter
	stCSI
	stStr    // DCS / OSC / PM / APC / SOS payload
	stStrEsc // ESC seen inside a string
	stMusic
)

const maxStrLen = 32 << 20

// Parser interprets a byte stream (CP437 or UTF-8 text with ECMA-48 /
// ANSI-BBS / xterm escape sequences) and drives a Screen. It follows the
// command set of SyncTERM's cterm engine, plus xterm 256-color and
// truecolor SGR, DEC sixel graphics, and ANSI music.
type Parser struct {
	S *Screen

	Reply     func([]byte)       // bytes to send back to the host
	PlaySound func(wav []byte)   // audio sink (may be nil)
	ResolveBg func(Color) uint32 // palette lookup for sixel backgrounds

	MusicMode int  // 0 = CSI | only, 1 = + CSI N (BANSI), 2 = + CSI M
	SoundOn   bool // synthesize music at all
	BellOn    bool
	UTF8      bool

	// WaitMs is set when a foreground music string was queued; the caller
	// should pause feeding data for that long (SyncTERM blocks the
	// parser until foreground notes have played).
	WaitMs int

	// Audio is the SyncTERM audio APC engine (may be nil). WaitCh is set
	// when a SyncTERM:A;Wait asks the caller to pause until that channel
	// is idle.
	Audio  *AudioAPC
	WaitCh int

	// Mouse reporting requested by the host (xterm / SyncTERM modes).
	MouseMode    int  // 0 off, 9 X10, 1000 normal, 1002 button-event, 1003 any-event
	MouseSGR     bool // DECSET 1006 (also set by 1016)
	MousePix     bool // DECSET 1016: SGR with pixel coordinates
	BracketPaste bool // DECSET 2004

	mus MusicState

	st       int
	params   []byte
	inter    []byte
	str      []byte
	strKind  byte
	music    []byte
	musicKnd int
	lastCh   byte
	haveLast bool

	u8need int
	u8acc  rune
}

func NewParser(s *Screen) *Parser {
	return &Parser{S: s, mus: newMusicState(), SoundOn: true, BellOn: true, MusicMode: 1}
}

// Reset clears parser state (not the screen).
func (p *Parser) Reset() {
	p.st = stGround
	p.params, p.inter, p.str, p.music = nil, nil, nil, nil
	p.mus = newMusicState()
	p.u8need = 0
}

// Feed processes data and returns how many bytes were consumed. It
// returns early (consumed < len(data)) if a foreground music string asks
// the caller to wait (WaitMs > 0).
func (p *Parser) Feed(data []byte) int {
	for i, b := range data {
		p.feedByte(b)
		if p.WaitMs > 0 || p.WaitCh > 0 {
			return i + 1
		}
	}
	return len(data)
}

func (p *Parser) reply(s string) {
	if p.Reply != nil {
		p.Reply([]byte(s))
	}
}

func (p *Parser) feedByte(b byte) {
	switch p.st {
	case stGround:
		p.ground(b)
	case stEsc:
		p.escape(b)
	case stEscInter:
		if b >= 0x30 && b <= 0x7E {
			if len(p.inter) > 0 && p.inter[0] == '#' && b == '8' { // DECALN
				for i := range p.S.Cells {
					p.S.Cells[i] = Cell{Ch: 'E', Fg: 7, Bg: 0}
				}
				p.S.Dirty = true
			}
			p.st = stGround
		} else if b >= 0x20 && b <= 0x2F {
			p.inter = append(p.inter, b)
		} else {
			p.st = stGround
		}
	case stCSI:
		p.csiByte(b)
	case stStr:
		switch {
		case b == 0x1B:
			p.st = stStrEsc
		case b == 0x07 && p.strKind != 'P':
			p.endString()
		case b == 0x18 || b == 0x1A: // CAN / SUB cancel
			p.st = stGround
			p.str = nil
		default:
			if len(p.str) < maxStrLen {
				p.str = append(p.str, b)
			}
		}
	case stStrEsc:
		if b == '\\' {
			p.endString()
		} else {
			// ESC began a new sequence; abandon the string.
			p.str = nil
			p.st = stEsc
			p.escape(b)
		}
	case stMusic:
		p.musicByte(b)
	}
}

// ---------------------------------------------------------------------
// Ground state
// ---------------------------------------------------------------------

func (p *Parser) execC0(b byte) bool {
	s := p.S
	switch b {
	case 0x00:
	case 0x07:
		if p.BellOn && p.PlaySound != nil {
			p.PlaySound(pcmToWAV(beepPCM()))
		}
	case 0x08:
		s.Backspace()
	case 0x09:
		s.Tab()
	case 0x0A:
		s.LineFeed()
	case 0x0C: // form feed: clear screen and home (ANSI-BBS convention)
		s.EraseDisplay(2)
		s.CursorPos(0, 0)
	case 0x0D:
		s.CarriageReturn()
	default:
		return false
	}
	return true
}

func (p *Parser) ground(b byte) {
	if b == 0x1B {
		p.u8need = 0
		p.st = stEsc
		return
	}
	if b < 0x20 {
		if p.execC0(b) {
			return
		}
		p.print(b) // other control codes show their CP437 glyphs
		return
	}
	if p.UTF8 && b >= 0x80 {
		switch {
		case b >= 0xC0 && b < 0xE0:
			p.u8need, p.u8acc = 1, rune(b&0x1F)
		case b >= 0xE0 && b < 0xF0:
			p.u8need, p.u8acc = 2, rune(b&0x0F)
		case b >= 0xF0 && b < 0xF8:
			p.u8need, p.u8acc = 3, rune(b&0x07)
		case b >= 0x80 && b < 0xC0 && p.u8need > 0:
			p.u8acc = p.u8acc<<6 | rune(b&0x3F)
			p.u8need--
			if p.u8need == 0 {
				if g, ok := runeToCP437(p.u8acc); ok {
					p.print(g)
				} else {
					p.print('?')
				}
			}
		default:
			p.u8need = 0
		}
		return
	}
	p.u8need = 0
	p.print(b)
}

func (p *Parser) print(ch byte) {
	p.S.PutChar(ch)
	p.lastCh, p.haveLast = ch, true
}

// ---------------------------------------------------------------------
// ESC
// ---------------------------------------------------------------------

func (p *Parser) escape(b byte) {
	s := p.S
	p.st = stGround
	switch b {
	case '[':
		p.params, p.inter = p.params[:0], p.inter[:0]
		p.st = stCSI
	case 'P', ']', '^', '_', 'X':
		p.strKind = b
		p.str = p.str[:0]
		p.st = stStr
	case '7':
		s.SaveCursor()
	case '8':
		s.RestoreCursor()
	case 'c':
		s.Reset()
		p.mus = newMusicState()
		p.MouseMode, p.MouseSGR, p.MousePix, p.BracketPaste = 0, false, false, false
	case 'D':
		s.LineFeed()
	case 'E':
		s.CarriageReturn()
		s.LineFeed()
	case 'M':
		s.ReverseIndex()
	case 'H':
		s.SetTab()
	case 'Z':
		p.reply("\x1b[?62;4;22c")
	case 0x1B:
		p.st = stEsc
	default:
		if b >= 0x20 && b <= 0x2F {
			p.inter = append(p.inter[:0], b)
			p.st = stEscInter
		} else if b < 0x20 {
			p.execC0(b)
		}
	}
}

// ---------------------------------------------------------------------
// CSI
// ---------------------------------------------------------------------

func (p *Parser) csiByte(b byte) {
	switch {
	case b == 0x1B:
		p.st = stEsc
	case b >= 0x30 && b <= 0x3F:
		p.params = append(p.params, b)
		if len(p.params) > 512 {
			p.st = stGround
		}
	case b >= 0x20 && b <= 0x2F:
		p.inter = append(p.inter, b)
	case b >= 0x40 && b <= 0x7E:
		p.st = stGround
		p.dispatchCSI(b)
	case b < 0x20:
		p.execC0(b)
	default:
		p.st = stGround
	}
}

// splitParams splits the parameter bytes into ';'-separated items, each
// of which may contain ':' sub-parameters. A leading private-marker
// byte (< = > ?) is returned separately.
func (p *Parser) splitParams() (prefix byte, items [][]int, count int) {
	raw := p.params
	if len(raw) > 0 && strings.IndexByte("<=>?", raw[0]) >= 0 {
		prefix = raw[0]
		raw = raw[1:]
	}
	if len(raw) == 0 {
		return prefix, nil, 0
	}
	for _, tok := range strings.Split(string(raw), ";") {
		var sub []int
		for _, t := range strings.Split(tok, ":") {
			if t == "" {
				sub = append(sub, -1)
				continue
			}
			v, err := strconv.Atoi(t)
			if err != nil || v < 0 {
				v = 0
			}
			if v > 1<<20 {
				v = 1 << 20
			}
			sub = append(sub, v)
		}
		items = append(items, sub)
	}
	return prefix, items, len(items)
}

func argAt(items [][]int, i, def int) int {
	if i >= len(items) || len(items[i]) == 0 || items[i][0] < 1 {
		return def
	}
	return items[i][0]
}

func argZero(items [][]int, i int) int {
	if i >= len(items) || len(items[i]) == 0 || items[i][0] < 0 {
		return 0
	}
	return items[i][0]
}

func (p *Parser) dispatchCSI(final byte) {
	s := p.S
	prefix, items, count := p.splitParams()

	if len(p.inter) == 1 && p.inter[0] == '$' && final == 'p' && prefix == '?' {
		p.decrqm(argZero(items, 0))
		return
	}
	if prefix == '<' && final == 'c' && len(p.inter) == 0 {
		if argZero(items, 0) == 0 { // CTerm extended device attributes
			p.reply("\x1b[<0;2;4;7c") // bright bg, pixel (sixel) ops, mouse
		}
		return
	}
	if len(p.inter) > 0 {
		// CSI SP q (DECSCUSR) etc. -- nothing to do.
		return
	}
	switch prefix {
	case '?':
		p.dispatchPrivate(final, items)
		return
	case '=':
		if final == 'M' { // CTerm: set ANSI music mode
			if v := argZero(items, 0); v >= 0 && v <= 2 {
				p.MusicMode = v
			}
		} else if final == 'n' && argZero(items, 0) == 7 { // audio state DSR
			target := -1
			if len(items) >= 2 {
				target = argZero(items, 1)
			}
			if p.Audio != nil {
				p.reply(p.Audio.StateReport(target))
			} else {
				p.reply("\x1b[=7n")
			}
		}
		return
	case '>', '<':
		return
	}

	switch final {
	case 'A':
		s.CursorUp(argAt(items, 0, 1))
	case 'B', 'e':
		s.CursorDown(argAt(items, 0, 1))
	case 'C', 'a':
		s.CursorFwd(argAt(items, 0, 1))
	case 'D', 'j':
		s.CursorBack(argAt(items, 0, 1))
	case 'E':
		s.CursorDown(argAt(items, 0, 1))
		s.CursorCol(0)
	case 'F':
		s.CursorUp(argAt(items, 0, 1))
		s.CursorCol(0)
	case 'G', '`':
		s.CursorCol(argAt(items, 0, 1) - 1)
	case 'd':
		s.CursorRow(argAt(items, 0, 1) - 1)
	case 'k':
		s.CursorUp(argAt(items, 0, 1))
	case 'H', 'f':
		s.CursorPos(argAt(items, 0, 1)-1, argAt(items, 1, 1)-1)
	case 'I':
		s.ForwardTab(argAt(items, 0, 1))
	case 'Z':
		s.BackTab(argAt(items, 0, 1))
	case 'J':
		s.EraseDisplay(argZero(items, 0))
	case 'K':
		s.EraseLine(argZero(items, 0))
	case 'L':
		s.InsertLines(argAt(items, 0, 1))
	case 'M':
		if p.MusicMode == 2 && count == 0 {
			p.beginMusic(1)
		} else {
			s.DeleteLines(argAt(items, 0, 1))
		}
	case 'N':
		if p.MusicMode >= 1 {
			p.beginMusic(2)
		}
	case '|':
		p.beginMusic(1)
	case '@':
		s.InsertChars(argAt(items, 0, 1))
	case 'P':
		s.DeleteChars(argAt(items, 0, 1))
	case 'X':
		s.EraseChars(argAt(items, 0, 1))
	case 'S':
		s.ScrollUp(argAt(items, 0, 1))
	case 'T':
		s.ScrollDown(argAt(items, 0, 1))
	case 'b':
		if p.haveLast {
			n := argAt(items, 0, 1)
			if n > 65535 {
				n = 65535
			}
			for i := 0; i < n; i++ {
				s.PutChar(p.lastCh)
			}
		}
	case 'g':
		switch argZero(items, 0) {
		case 0:
			s.ClearTab(false)
		case 3, 5:
			s.ClearTab(true)
		}
	case 'r':
		s.SetScrollRegion(argAt(items, 0, 1)-1, argAt(items, 1, s.Rows)-1)
	case 's':
		s.SaveCursor()
	case 'u':
		s.RestoreCursor()
	case 'h', 'l':
		on := final == 'h'
		for i := range items {
			if argZero(items, i) == 4 {
				s.InsertMode = on
			}
		}
	case 'm':
		p.sgr(items, count)
	case 'n':
		switch argZero(items, 0) {
		case 5:
			p.reply("\x1b[0n")
		case 6:
			row := s.CY + 1
			if s.Origin {
				row -= s.top
			}
			p.reply(fmt.Sprintf("\x1b[%d;%dR", row, s.CX+1))
		}
	case 'c':
		if argZero(items, 0) == 0 {
			p.reply("\x1b[?62;4;22c")
		}
	case 't':
		switch argZero(items, 0) {
		case 14:
			p.reply(fmt.Sprintf("\x1b[4;%d;%dt", s.Rows*s.CH, s.Cols*s.CW))
		case 16:
			p.reply(fmt.Sprintf("\x1b[6;%d;%dt", s.CH, s.CW))
		case 18, 19:
			p.reply(fmt.Sprintf("\x1b[8;%d;%dt", s.Rows, s.Cols))
		}
	}
}

func (p *Parser) dispatchPrivate(final byte, items [][]int) {
	s := p.S
	switch final {
	case 'h', 'l':
		on := final == 'h'
		for i := range items {
			switch argZero(items, i) {
			case 6:
				s.Origin = on
				s.CursorPos(0, 0)
			case 7:
				s.AutoWrap = on
			case 25:
				s.CursorVisible = on
			case 32:
				s.NoBright = on
			case 33:
				s.ICE = on
			case 35:
				s.NoBlink = on
			case 80:
				s.SixelNoScroll = on
			case 9, 1000, 1002, 1003:
				v := argZero(items, i)
				if on {
					p.MouseMode = v
				} else if p.MouseMode == v {
					p.MouseMode = 0
				}
			case 1006:
				if on {
					p.MouseSGR, p.MousePix = true, false
				} else if !p.MousePix {
					p.MouseSGR = false
				}
			case 1016:
				if on {
					p.MouseSGR, p.MousePix = true, true
				} else if p.MousePix {
					p.MouseSGR, p.MousePix = false, false
				}
			case 2004:
				p.BracketPaste = on
			}
		}
	case 'S': // XTSMGRAPHICS
		item, action := argZero(items, 0), argZero(items, 1)
		switch {
		case item == 1 && action == 1:
			p.reply("\x1b[?1;0;256S")
		case item == 2 && action == 1:
			p.reply(fmt.Sprintf("\x1b[?2;0;%d;%dS", s.Cols*s.CW, s.Rows*s.CH))
		case item == 3 && action == 1:
			p.reply("\x1b[?3;0;256S")
		default:
			p.reply(fmt.Sprintf("\x1b[?%d;2;0S", item))
		}
	case 'n':
		if argZero(items, 0) == 6 {
			p.reply(fmt.Sprintf("\x1b[?%d;%dR", s.CY+1, s.CX+1))
		}
	}
}

// ---------------------------------------------------------------------
// SGR
// ---------------------------------------------------------------------

func (p *Parser) sgr(items [][]int, count int) {
	s := p.S
	if count == 0 {
		s.ResetRendition()
		return
	}
	for i := 0; i < len(items); i++ {
		it := items[i]
		n := 0
		if len(it) > 0 && it[0] >= 0 {
			n = it[0]
		}
		switch {
		case n == 0:
			s.ResetRendition()
		case n == 1:
			s.bold = true
		case n == 2, n == 22:
			s.bold = false
		case n == 4, n == 21:
			s.under = true
		case n == 24:
			s.under = false
		case n == 5, n == 6:
			s.blink = true
		case n == 25:
			s.blink = false
		case n == 7:
			s.rev = true
		case n == 27:
			s.rev = false
		case n == 8:
			s.conceal = true
		case n == 28:
			s.conceal = false
		case n == 9:
			s.strike = true
		case n == 29:
			s.strike = false
		case n >= 30 && n <= 37:
			s.fg = Color(n - 30)
		case n == 39:
			s.fg = 7
		case n >= 40 && n <= 47:
			s.bg = Color(n - 40)
		case n == 49:
			s.bg = 0
		case n >= 90 && n <= 97:
			s.fg = Color(n - 90 + 8)
		case n >= 100 && n <= 107:
			s.bg = Color(n - 100 + 8)
		case n == 38 || n == 48:
			col, used, ok := parseExtColor(items, i)
			if ok {
				if n == 38 {
					s.fg = col
				} else {
					s.bg = col
				}
			}
			i += used
		}
	}
}

// parseExtColor handles 38/48 in all common spellings:
//
//	38;5;N        38;2;R;G;B
//	38:5:N        38:2::R:G:B      38:2:R:G:B
//
// It returns the color, the number of *additional* items consumed, and
// whether a valid color was parsed.
func parseExtColor(items [][]int, i int) (Color, int, bool) {
	it := items[i]
	if len(it) > 1 { // colon form, all in one item
		sub := it[1:]
		switch sub[0] {
		case 5:
			if len(sub) >= 2 && sub[1] >= 0 {
				return Color(sub[1] & 0xFF), 0, true
			}
		case 2:
			rest := sub[1:]
			if len(rest) >= 4 { // colorspace id present
				rest = rest[1:]
			}
			if len(rest) >= 3 {
				return rgbColor(clamp8(rest[0]), clamp8(rest[1]), clamp8(rest[2])), 0, true
			}
		}
		return 0, 0, false
	}
	get := func(k int) int {
		if i+k < len(items) && len(items[i+k]) > 0 && items[i+k][0] >= 0 {
			return items[i+k][0]
		}
		return 0
	}
	if i+1 >= len(items) {
		return 0, 0, false
	}
	switch get(1) {
	case 5:
		if i+2 < len(items) {
			return Color(get(2) & 0xFF), 2, true
		}
		return 0, len(items) - i - 1, false
	case 2:
		if i+4 < len(items) {
			return rgbColor(clamp8(get(2)), clamp8(get(3)), clamp8(get(4))), 4, true
		}
		return 0, len(items) - i - 1, false
	}
	return 0, 0, false
}

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// ---------------------------------------------------------------------
// Strings: DCS (sixel), OSC
// ---------------------------------------------------------------------

func (p *Parser) endString() {
	p.st = stGround
	data := p.str
	p.str = nil
	if p.strKind == '_' {
		p.apc(string(data))
		return
	}
	if p.strKind != 'P' {
		return // OSC / PM / SOS ignored
	}
	// DCS params intermediates final data
	i := 0
	for i < len(data) && ((data[i] >= '0' && data[i] <= '9') || data[i] == ';' || data[i] == ':') {
		i++
	}
	params := string(data[:i])
	for i < len(data) && data[i] >= 0x20 && data[i] <= 0x2F {
		i++
	}
	if i >= len(data) {
		return
	}
	if data[i] != 'q' {
		return
	}
	var pv [2]int
	for k, tok := range strings.Split(params, ";") {
		if k > 1 {
			break
		}
		pv[k], _ = strconv.Atoi(tok)
	}
	bg := uint32(0)
	if p.ResolveBg != nil {
		_, bgc, _ := p.S.curAttr()
		bg = p.ResolveBg(bgc)
	}
	img := DecodeSixel(data[i+1:], pv[0], pv[1], bg)
	p.S.PlaceSixel(img)
}

// ---------------------------------------------------------------------
// ANSI music
// ---------------------------------------------------------------------

func (p *Parser) beginMusic(kind int) {
	p.music = p.music[:0]
	p.musicKnd = kind
	p.st = stMusic
}

func (p *Parser) musicByte(b byte) {
	if b == 0x0E {
		p.st = stGround
		str := p.music
		p.music = nil
		if !p.SoundOn || p.PlaySound == nil || len(str) == 0 {
			return
		}
		pcm, fore := p.mus.Render(str, p.musicKnd)
		if len(pcm) == 0 {
			return
		}
		p.PlaySound(pcmToWAV(pcm))
		if fore {
			p.WaitMs = pcmDurationMs(pcm)
		}
		return
	}
	if strings.IndexByte(musicChars, b) >= 0 {
		if len(p.music) < 4096 {
			p.music = append(p.music, b)
		}
		return
	}
	// Disallowed character: silently abandon the music string (as cterm does).
	p.st = stGround
	p.music = nil
}
