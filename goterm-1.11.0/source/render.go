package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

// RenderOpts controls how a Screen is turned into pixels.
type RenderOpts struct {
	Pal         *Palette
	Font        *Font
	NineDot     bool // VGA-style 9th column (line-drawing glyphs extend)
	BlinkOff    bool // current blink phase hides blinking text
	ICE         bool // blink bit selects bright background instead of blinking
	ShowCursor  bool // cursor blink phase is "on"
	CursorBlock bool // block cursor instead of underline
}

// CellPx returns the pixel size of one text cell.
func (o *RenderOpts) CellPx() (int, int) {
	w := o.Font.W
	if o.NineDot {
		w++
	}
	return w, o.Font.H
}

// FBSize returns the framebuffer size for a screen.
func (o *RenderOpts) FBSize(cols, rows int) (int, int) {
	cw, ch := o.CellPx()
	return cols * cw, rows * ch
}

// Render draws the screen into fb (0x00RRGGBB, len >= w*h).
func (s *Screen) Render(fb []uint32, o *RenderOpts) {
	cw, ch := o.CellPx()
	stride := s.Cols * cw
	f := o.Font
	ice := o.ICE || s.ICE

	for y := 0; y < s.Rows; y++ {
		row := s.rowAtView(y)
		abs := s.absLineAtView(y)
		for x := 0; x < s.Cols; x++ {
			var c Cell
			if row != nil && x < len(row) {
				c = row[x]
			} else {
				c = Cell{Ch: ' ', Fg: 7, Bg: 0}
			}
			fgC, bgC := c.Fg, c.Bg
			if ice && c.Attr&aBlink != 0 && !bgC.isRGB() && bgC < 8 {
				bgC += 8
			}
			fg := o.Pal.resolve(fgC)
			bg := o.Pal.resolve(bgC)
			if !ice && c.Attr&aBlink != 0 && o.BlinkOff {
				fg = bg
			}
			if s.inSelection(abs, x) {
				fg, bg = bg, fg
			}
			glyph := f.Data[int(c.Ch)*f.H : int(c.Ch)*f.H+f.H]
			wide9 := o.NineDot && c.Ch >= 0xC0 && c.Ch <= 0xDF
			px0 := x * cw
			for r := 0; r < ch; r++ {
				bits := glyph[r]
				line := fb[(y*ch+r)*stride+px0 : (y*ch+r)*stride+px0+cw]
				underline := false
				if c.Attr&aUnder != 0 && ((ch >= 14 && r == ch-2) || (ch < 14 && r == ch-1)) {
					underline = true
				}
				if c.Attr&aStrike != 0 && r == ch/2 {
					underline = true
				}
				if underline && !(!ice && c.Attr&aBlink != 0 && o.BlinkOff) {
					for i := range line {
						line[i] = fg
					}
					continue
				}
				for b := 0; b < 8; b++ {
					if bits&(0x80>>uint(b)) != 0 {
						line[b] = fg
					} else {
						line[b] = bg
					}
				}
				if o.NineDot {
					if wide9 && bits&1 != 0 {
						line[8] = fg
					} else {
						line[8] = bg
					}
				}
			}
		}
	}

	// Sixel graphics layer (live screen only).
	if s.gfx != nil && s.ViewOffset == 0 {
		gstride := s.Cols * s.CW
		if s.CW == cw && s.CH == ch {
			for i, g := range s.gfx {
				if g != 0 {
					fb[i] = g & 0xFFFFFF
				}
			}
		} else {
			// cell size changed since the image was drawn; map by pixel
			rows := s.Rows * s.CH
			for py := 0; py < rows && py < s.Rows*ch; py++ {
				for px := 0; px < gstride && px < stride; px++ {
					if g := s.gfx[py*gstride+px]; g != 0 {
						fb[py*stride+px] = g & 0xFFFFFF
					}
				}
			}
		}
	}

	// Cursor
	if o.ShowCursor && s.CursorVisible && s.ViewOffset == 0 && s.CY < s.Rows && s.CX < s.Cols {
		c := s.Cells[s.idx(s.CX, s.CY)]
		col := o.Pal.resolve(c.Fg)
		if col == o.Pal.resolve(c.Bg) {
			col = o.Pal[7]
		}
		r0 := ch - 2
		if o.CursorBlock {
			r0 = 0
		}
		if r0 < 0 {
			r0 = 0
		}
		for r := r0; r < ch; r++ {
			for px := 0; px < cw; px++ {
				i := (s.CY*ch+r)*stride + s.CX*cw + px
				if o.CursorBlock {
					fb[i] ^= 0xFFFFFF
				} else {
					fb[i] = col
				}
			}
		}
	}
}

// ToImage converts a framebuffer to an image.RGBA (for screenshots).
func fbToImage(fb []uint32, w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w*h; i++ {
		v := fb[i]
		img.SetRGBA(i%w, i/w, color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255})
	}
	return img
}

func savePNG(path string, fb []uint32, w, h int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, fbToImage(fb, w, h))
}
