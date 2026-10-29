package main

import (
	"bytes"
	"fmt"
	"math"
)

// encodeSixel converts an RGB image (0xRRGGBB per pixel) into a DCS sixel
// string using the 6x6x6 color cube as its palette. It is used by the
// built-in test pattern so the user can verify sixel support without a
// BBS that sends graphics.
func encodeSixel(w, h int, pix []uint32) []byte {
	idxOf := func(v uint32) int {
		q := func(c uint32) int { return int((c*5 + 127) / 255) }
		return q(v>>16&0xFF)*36 + q(v>>8&0xFF)*6 + q(v&0xFF)
	}
	idx := make([]int, w*h)
	used := map[int]bool{}
	for i := range idx {
		idx[i] = idxOf(pix[i])
		used[idx[i]] = true
	}
	var out bytes.Buffer
	out.WriteString("\x1bPq")
	fmt.Fprintf(&out, "\"1;1;%d;%d", w, h)
	for n := 0; n < 216; n++ {
		if !used[n] {
			continue
		}
		r, g, b := n/36, (n/6)%6, n%6
		fmt.Fprintf(&out, "#%d;2;%d;%d;%d", n, r*20, g*20, b*20)
	}
	for band := 0; band*6 < h; band++ {
		colors := map[int]bool{}
		for y := band * 6; y < band*6+6 && y < h; y++ {
			for x := 0; x < w; x++ {
				colors[idx[y*w+x]] = true
			}
		}
		first := true
		for n := 0; n < 216; n++ {
			if !colors[n] {
				continue
			}
			if !first {
				out.WriteByte('$')
			}
			first = false
			fmt.Fprintf(&out, "#%d", n)
			var prev byte
			run := 0
			flush := func() {
				if run == 0 {
					return
				}
				if run > 3 {
					fmt.Fprintf(&out, "!%d%c", run, prev)
				} else {
					for i := 0; i < run; i++ {
						out.WriteByte(prev)
					}
				}
				run = 0
			}
			for x := 0; x < w; x++ {
				var bits byte
				for k := 0; k < 6; k++ {
					y := band*6 + k
					if y < h && idx[y*w+x] == n {
						bits |= 1 << uint(k)
					}
				}
				ch := bits + '?'
				if run > 0 && ch == prev {
					run++
				} else {
					flush()
					prev, run = ch, 1
				}
			}
			flush()
		}
		out.WriteByte('-')
	}
	out.WriteString("\x1b\\")
	return out.Bytes()
}

func testSixelImage(w, h int) []uint32 {
	pix := make([]uint32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// rainbow gradient with a vertical fade
			hue := float64(x) / float64(w) * 6
			f := hue - math.Floor(hue)
			var r, g, b float64
			switch int(hue) % 6 {
			case 0:
				r, g, b = 1, f, 0
			case 1:
				r, g, b = 1-f, 1, 0
			case 2:
				r, g, b = 0, 1, f
			case 3:
				r, g, b = 0, 1-f, 1
			case 4:
				r, g, b = f, 0, 1
			default:
				r, g, b = 1, 0, 1-f
			}
			fade := 1 - float64(y)/float64(h)*0.85
			pix[y*w+x] = uint32(r*fade*255)<<16 | uint32(g*fade*255)<<8 | uint32(b*fade*255)
			// border + diagonal so geometry errors are obvious
			if x == 0 || y == 0 || x == w-1 || y == h-1 || x == y*w/h {
				pix[y*w+x] = 0xFFFFFF
			}
		}
	}
	return pix
}

// TestPatternStream returns an ANSI byte stream exercising the 16-color,
// 256-color and truecolor modes, blink/bold, line drawing, sixel graphics
// and ANSI music.
func TestPatternStream() []byte {
	var b bytes.Buffer
	w := func(s string, a ...interface{}) { fmt.Fprintf(&b, s, a...) }
	w("\x1b[0m\x1b[2J\x1b[H")
	w("\x1b[1;37mGoTerm test pattern\x1b[0m  16 / 256 / truecolor, sixel, ANSI music\r\n")
	// 16 colors
	for i := 0; i < 16; i++ {
		w("\x1b[48;5;%dm  ", i)
	}
	w("\x1b[0m  ")
	for i := 0; i < 8; i++ {
		w("\x1b[%dm\x1b[1m%d\x1b[0m ", 30+i, i)
	}
	w("\r\n")
	for i := 0; i < 8; i++ {
		w("\x1b[%dm  ", 40+i)
	}
	for i := 0; i < 8; i++ {
		w("\x1b[%dm  ", 100+i)
	}
	w("\x1b[0m  SGR 40-47 / 100-107\r\n")
	// 256-color cube
	for r := 0; r < 6; r++ {
		for g := 0; g < 6; g++ {
			for bl := 0; bl < 6; bl++ {
				w("\x1b[48;5;%dm \x1b[0m", 16+r*36+g*6+bl)
			}
		}
		w(" ")
		if r == 0 {
			for i := 232; i < 256; i++ {
				w("\x1b[48;5;%dm \x1b[0m", i)
			}
			w(" 256-color")
		}
		w("\r\n")
	}
	// truecolor ramps
	for x := 0; x < 64; x++ {
		v := x * 255 / 63
		w("\x1b[48;2;%d;%d;%dm \x1b[0m", v, 0, 255-v)
	}
	w("\r\n")
	for x := 0; x < 64; x++ {
		v := x * 255 / 63
		w("\x1b[48;2;%d;%d;%dm \x1b[0m", 255-v, v, 128)
	}
	w(" truecolor\r\n")
	// attributes
	w("\x1b[1mBold\x1b[0m \x1b[4mUnderline\x1b[0m \x1b[7mReverse\x1b[0m \x1b[5mBlink\x1b[0m \x1b[1;5;33;44mBold+Blink\x1b[0m \x1b[9mStrike\x1b[0m\r\n")
	// line drawing
	w("\x1b[1;36m\xc9\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xbb \xb0\xb1\xb2\xdb\xdb\xb2\xb1\xb0 \x01\x02\x03\x04\x05 \xdf\xdc\xdd\xde\x1b[0m\r\n")
	w("\x1b[1;36m\xc8\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xcd\xbc\x1b[0m  Sixel image below; ANSI music plays now\r\n")
	// sixel
	b.Write(encodeSixel(160, 60, testSixelImage(160, 60)))
	w("\x1b[0mdone.\r\n")
	w("\x1b[|MFT160O4L8CDEFGAB>C\x0e")
	return b.Bytes()
}
