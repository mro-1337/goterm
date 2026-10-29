package main

import (
	"os"
	"testing"
)

func defaultPal() *Palette { var b [16]RGB; b = defaultPalette(); return BuildPalette(&b) }

func newTestTerm(cols, rows int) (*Screen, *Parser, *RenderOpts) {
	s := NewScreen(cols, rows, 8, 16)
	p := NewParser(s)
	pal := defaultPal()
	p.ResolveBg = func(c Color) uint32 { return pal.resolve(c) }
	o := &RenderOpts{Pal: pal, Font: fontForHeight(16)}
	return s, p, o
}

func TestPatternRender(t *testing.T) {
	s, p, o := newTestTerm(80, 25)
	var sounds int
	p.PlaySound = func(w []byte) { sounds++ }
	p.Feed(TestPatternStream())
	if sounds == 0 {
		t.Errorf("expected music to play")
	}
	w, h := o.FBSize(s.Cols, s.Rows)
	fb := make([]uint32, w*h)
	s.Render(fb, o)
	out := os.Getenv("GT_PNG")
	if out != "" {
		if err := savePNG(out, fb, w, h); err != nil {
			t.Fatal(err)
		}
	}
	if s.gfx == nil {
		t.Fatalf("sixel did not allocate gfx layer")
	}
}
