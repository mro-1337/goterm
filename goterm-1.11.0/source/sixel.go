package main

import "math"

// SixelImage is a decoded sixel graphic at device-pixel resolution.
// Pix holds 0xFFRRGGBB for painted pixels and 0 for transparent ones.
type SixelImage struct {
	W, H int
	Pix  []uint32
}

const (
	sixelMaxW      = 4096
	sixelMaxBands  = 1024
	sixelMaxPixels = 16 << 20
)

// VT340 default color registers (percent RGB).
var sixelDefault16 = [16][3]int{
	{0, 0, 0}, {20, 20, 80}, {80, 13, 13}, {20, 80, 20},
	{80, 20, 80}, {20, 80, 80}, {80, 80, 20}, {53, 53, 53},
	{26, 26, 26}, {33, 33, 60}, {60, 26, 26}, {33, 60, 33},
	{60, 33, 60}, {33, 60, 60}, {60, 60, 33}, {80, 80, 80},
}

func pct(v int) uint32 {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	return uint32((v*255 + 50) / 100)
}

func hlsToRGB(h, l, s int) uint32 {
	// DEC HLS: hue 0 = blue, 120 = red, 240 = green.
	hf := float64(((h+240)%360+360)%360) / 360.0
	lf := float64(l) / 100.0
	sf := float64(s) / 100.0
	if sf == 0 {
		v := uint32(lf*255 + 0.5)
		return v<<16 | v<<8 | v
	}
	var q float64
	if lf < 0.5 {
		q = lf * (1 + sf)
	} else {
		q = lf + sf - lf*sf
	}
	p := 2*lf - q
	f := func(t float64) float64 {
		t = t - math.Floor(t)
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 0.5:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	r := uint32(f(hf+1.0/3)*255 + 0.5)
	g := uint32(f(hf)*255 + 0.5)
	b := uint32(f(hf-1.0/3)*255 + 0.5)
	return r<<16 | g<<8 | b
}

// sixelTok walks the sixel data stream and calls back for each element.
type sixelCB struct {
	color  func(n int)
	define func(n, space, a, b, c int)
	data   func(bits byte, repeat int)
	cr     func()
	nl     func()
	raster func(pan, pad, ph, pv int)
}

func parseSixel(data []byte, cb *sixelCB) {
	i := 0
	n := len(data)
	readNum := func() (int, bool) {
		v, got := 0, false
		for i < n && data[i] >= '0' && data[i] <= '9' {
			v = v*10 + int(data[i]-'0')
			if v > 1<<20 {
				v = 1 << 20
			}
			got = true
			i++
		}
		return v, got
	}
	for i < n {
		c := data[i]
		switch {
		case c >= '?' && c <= '~':
			cb.data(c-'?', 1)
			i++
		case c == '!':
			i++
			rep, _ := readNum()
			if i < n && data[i] >= '?' && data[i] <= '~' {
				if rep < 1 {
					rep = 1
				}
				cb.data(data[i]-'?', rep)
				i++
			}
		case c == '#':
			i++
			var p [5]int
			cnt := 0
			for cnt < 5 {
				v, got := readNum()
				if !got && !(i < n && data[i] == ';') {
					break
				}
				p[cnt] = v
				cnt++
				if i < n && data[i] == ';' {
					i++
					continue
				}
				break
			}
			if cnt == 1 {
				cb.color(p[0])
			} else if cnt >= 5 {
				cb.define(p[0], p[1], p[2], p[3], p[4])
			}
		case c == '"':
			i++
			var p [4]int
			cnt := 0
			for cnt < 4 {
				v, got := readNum()
				if !got && !(i < n && data[i] == ';') {
					break
				}
				p[cnt] = v
				cnt++
				if i < n && data[i] == ';' {
					i++
					continue
				}
				break
			}
			if cnt >= 2 {
				cb.raster(p[0], p[1], p[2], p[3])
			}
		case c == '$':
			cb.cr()
			i++
		case c == '-':
			cb.nl()
			i++
		default:
			i++ // ignore whitespace / unknown
		}
	}
}

// DecodeSixel decodes the data portion of a DCS sixel string (everything
// after the final 'q'). p1/p2 are the DCS parameters; bg is the color
// used for unset pixels when p2 == 0.
func DecodeSixel(data []byte, p1, p2 int, bg uint32) *SixelImage {
	// Pixel aspect from p1 (see SyncTERM cterm.adoc): vertical size of a
	// sixel pixel in device pixels. Raster attributes may override.
	vs, hs := 2, 1
	switch p1 {
	case 0, 1, 5, 6:
		vs = 2
	case 2:
		vs = 5
	case 3, 4:
		vs = 3
	case 7, 8, 9:
		vs = 1
	}

	// Pass 1: measure.
	maxX, x, curBand, maxBand := 0, 0, 0, 0
	declW, declH := 0, 0
	parseSixel(data, &sixelCB{
		color:  func(int) {},
		define: func(int, int, int, int, int) {},
		data: func(_ byte, rep int) {
			x += rep
			if x > maxX {
				maxX = x
			}
			if curBand > maxBand {
				maxBand = curBand
			}
		},
		cr: func() { x = 0 },
		nl: func() { x = 0; curBand++ },
		raster: func(pan, pad, ph, pv int) {
			if pan > 0 {
				vs = pan
			}
			if pad > 0 {
				hs = pad
			} else if pan > 0 {
				hs = 1
			}
			declW, declH = ph, pv
		},
	})
	// (aspect from raster attributes is final after pass 1; re-run is
	// unnecessary since it only affects scaling below)
	bands := maxBand + 1
	w := maxX
	if declW > w {
		w = declW
	}
	hpx := bands * 6
	if declH > hpx {
		hpx = declH
	}
	if w <= 0 || hpx <= 0 {
		return nil
	}
	if w > sixelMaxW {
		w = sixelMaxW
	}
	if hpx > sixelMaxBands*6 {
		hpx = sixelMaxBands * 6
	}
	if vs < 1 {
		vs = 1
	}
	if hs < 1 {
		hs = 1
	}
	for w*hs*hpx*vs > sixelMaxPixels && (hs > 1 || vs > 1) {
		if vs > 1 {
			vs--
		}
		if hs > 1 {
			hs--
		}
	}
	if w*hs*hpx*vs > sixelMaxPixels {
		return nil
	}

	// Pass 2: paint into a 1:1 sixel-pixel buffer.
	buf := make([]uint32, w*hpx)
	if p2 != 1 {
		fill := 0xFF000000 | bg
		for i := range buf {
			buf[i] = fill
		}
	}
	var regs [256]uint32
	for i := 0; i < 16; i++ {
		c := sixelDefault16[i]
		regs[i] = pct(c[0])<<16 | pct(c[1])<<8 | pct(c[2])
	}
	cur := uint32(0)
	x, band := 0, 0
	parseSixel(data, &sixelCB{
		color: func(n int) {
			if n >= 0 && n < 256 {
				cur = regs[n]
			}
		},
		define: func(n, space, a, b, c int) {
			if n < 0 || n > 255 {
				return
			}
			var v uint32
			if space == 1 {
				v = hlsToRGB(a, b, c)
			} else {
				v = pct(a)<<16 | pct(b)<<8 | pct(c)
			}
			regs[n] = v
			cur = v
		},
		data: func(bits byte, rep int) {
			if band >= hpx/6+1 {
				x += rep
				return
			}
			col := 0xFF000000 | cur
			for r := 0; r < rep; r++ {
				xx := x + r
				if xx >= w {
					break
				}
				for bit := 0; bit < 6; bit++ {
					if bits&(1<<uint(bit)) != 0 {
						yy := band*6 + bit
						if yy < hpx {
							buf[yy*w+xx] = col
						}
					}
				}
			}
			x += rep
		},
		cr:     func() { x = 0 },
		nl:     func() { x = 0; band++ },
		raster: func(int, int, int, int) {},
	})

	img := &SixelImage{W: w * hs, H: hpx * vs}
	if hs == 1 && vs == 1 {
		img.Pix = buf
		return img
	}
	img.Pix = make([]uint32, img.W*img.H)
	for y := 0; y < hpx; y++ {
		for yy := 0; yy < vs; yy++ {
			dst := img.Pix[(y*vs+yy)*img.W : (y*vs+yy+1)*img.W]
			src := buf[y*w : (y+1)*w]
			for x := 0; x < w; x++ {
				v := src[x]
				for xx := 0; xx < hs; xx++ {
					dst[x*hs+xx] = v
				}
			}
		}
	}
	return img
}

// PlaceSixel draws a decoded image into the screen's pixel layer at the
// cursor (or the top-left corner when sixel scrolling is disabled) and
// moves the cursor the way SyncTERM's cterm documents.
func (s *Screen) PlaceSixel(img *SixelImage) {
	if img == nil || img.W == 0 || img.H == 0 {
		return
	}
	s.ensureGfx()
	startX, startY := s.CX*s.CW, s.CY*s.CH
	hCells := (img.H + s.CH - 1) / s.CH
	scroll := !s.SixelNoScroll
	if scroll {
		need := s.CY + hCells - (s.Rows - 1)
		if need > 0 {
			s.top, s.bot = 0, s.Rows-1 // sixels scroll the whole page
			s.scrollUp(need)
			s.CY -= need
		}
		startY = s.CY * s.CH
	} else {
		startX, startY = 0, 0
	}
	stride := s.gfxStride()
	maxW := s.Cols * s.CW
	maxH := s.Rows * s.CH
	for y := 0; y < img.H; y++ {
		dy := startY + y
		if dy < 0 {
			continue
		}
		if dy >= maxH {
			break
		}
		src := img.Pix[y*img.W : (y+1)*img.W]
		for x := 0; x < img.W; x++ {
			dx := startX + x
			if dx >= maxW {
				break
			}
			if src[x] != 0 {
				s.gfx[dy*stride+dx] = src[x]
			}
		}
	}
	if scroll {
		s.CY += hCells
		if s.CY > s.Rows-1 {
			s.CY = s.Rows - 1
		}
		s.CX = 0
		s.wrapPending = false
	}
	s.Dirty = true
}
