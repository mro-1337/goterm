// mkicon draws the GoTerm terminal icon and writes goterm.ico plus a
// Windows resource object (rsrc_windows_amd64.syso) that the Go linker
// embeds in the executable.
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"sort"
)

type fcol struct{ r, g, b, a float64 }

func hex(v uint32, a float64) fcol {
	return fcol{float64(v >> 16 & 255), float64(v >> 8 & 255), float64(v & 255), a}
}

// sdf of a rounded rectangle
func rr(x, y, x0, y0, x1, y1, r float64) float64 {
	cx, cy := (x0+x1)/2, (y0+y1)/2
	hx, hy := (x1-x0)/2-r, (y1-y0)/2-r
	dx := math.Abs(x-cx) - hx
	dy := math.Abs(y-cy) - hy
	ox, oy := math.Max(dx, 0), math.Max(dy, 0)
	return math.Hypot(ox, oy) + math.Min(math.Max(dx, dy), 0) - r
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	vx, vy := bx-ax, by-ay
	t := ((px-ax)*vx + (py-ay)*vy) / (vx*vx + vy*vy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(ax+t*vx), py-(ay+t*vy))
}

func over(dst, src fcol) fcol {
	a := src.a + dst.a*(1-src.a)
	if a == 0 {
		return fcol{}
	}
	f := func(s, d float64) float64 { return (s*src.a + d*dst.a*(1-src.a)) / a }
	return fcol{f(src.r, dst.r), f(src.g, dst.g), f(src.b, dst.b), a}
}

func cov(d float64) float64 { // 1px-wide antialiased edge at 256 scale handled by supersampling
	if d <= 0 {
		return 1
	}
	return 0
}

// render draws the icon at 256x256 with 3x3 supersampling per pixel.
func render256() *image.NRGBA {
	const N = 256
	img := image.NewNRGBA(image.Rect(0, 0, N, N))
	for py := 0; py < N; py++ {
		for px := 0; px < N; px++ {
			var sr, sg, sb, sa float64
			const S = 4
			for sy := 0; sy < S; sy++ {
				for sx := 0; sx < S; sx++ {
					x := float64(px) + (float64(sx)+0.5)/S
					y := float64(py) + (float64(sy)+0.5)/S
					c := fcol{}
					// outer window: light bezel
					if cov(rr(x, y, 10, 14, 246, 242, 30)) > 0 {
						c = over(c, hex(0xC9CED8, 1))
						// inner screen
						if cov(rr(x, y, 16, 20, 240, 236, 25)) > 0 {
							c = over(c, hex(0x14171F, 1))
							// title bar
							if y < 62 {
								c = over(c, hex(0x2B3140, 1))
							}
							// traffic lights
							for i, col := range []uint32{0xFF5F57, 0xFEBC2E, 0x28C840} {
								if math.Hypot(x-(42+float64(i)*28), y-41) < 9 {
									c = over(c, hex(col, 1))
								}
							}
							// scanline glow (subtle)
							if y > 62 && int(y)%8 < 2 {
								c = over(c, hex(0x1D2230, 1))
							}
							// prompt chevron ">"
							d := math.Min(segDist(x, y, 62, 100, 112, 142), segDist(x, y, 112, 142, 62, 184))
							if d < 11 {
								c = over(c, hex(0x39FF7A, 1))
							}
							// cursor block "_"
							if x > 128 && x < 190 && y > 166 && y < 188 {
								c = over(c, hex(0x39FF7A, 1))
							}
						}
					}
					sr += c.r * c.a
					sg += c.g * c.a
					sb += c.b * c.a
					sa += c.a
				}
			}
			n := float64(S * S)
			if sa > 0 {
				img.SetNRGBA(px, py, color.NRGBA{uint8(sr / sa), uint8(sg / sa), uint8(sb / sa), uint8(sa / n * 255)})
			}
		}
	}
	return img
}

func downscale(src *image.NRGBA, n int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	f := src.Bounds().Dx() / n
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var r, g, b, a float64
			for j := 0; j < f; j++ {
				for i := 0; i < f; i++ {
					c := src.NRGBAAt(x*f+i, y*f+j)
					al := float64(c.A)
					r += float64(c.R) * al
					g += float64(c.G) * al
					b += float64(c.B) * al
					a += al
				}
			}
			if a > 0 {
				dst.SetNRGBA(x, y, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(a / float64(f*f))})
			}
		}
	}
	return dst
}

type entry struct {
	size int
	data []byte
}

func dib(img *image.NRGBA) []byte {
	n := img.Bounds().Dx()
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [10]uint32{40, uint32(n), uint32(n * 2), 1 | 32<<16, 0, uint32(n * n * 4), 0, 0, 0, 0})
	for y := n - 1; y >= 0; y-- {
		for x := 0; x < n; x++ {
			c := img.NRGBAAt(x, y)
			b.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	rowBytes := ((n + 31) / 32) * 4
	b.Write(make([]byte, rowBytes*n)) // AND mask (alpha channel is used)
	return b.Bytes()
}

func pngBytes(img *image.NRGBA) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func main() {
	big := render256()
	var ents []entry
	for _, n := range []int{16, 24, 32, 48, 64, 128, 256} {
		img := big
		if n != 256 {
			img = downscale(big, n)
		}
		if n >= 128 {
			ents = append(ents, entry{n, pngBytes(img)})
		} else {
			ents = append(ents, entry{n, dib(img)})
		}
	}
	os.WriteFile("goterm.png", pngBytes(big), 0o644)

	// .ico
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, uint16(len(ents))})
	off := 6 + 16*len(ents)
	for _, e := range ents {
		w := byte(e.size)
		if e.size == 256 {
			w = 0
		}
		ico.Write([]byte{w, w, 0, 0})
		binary.Write(&ico, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&ico, binary.LittleEndian, [2]uint32{uint32(len(e.data)), uint32(off)})
		off += len(e.data)
	}
	for _, e := range ents {
		ico.Write(e.data)
	}
	os.WriteFile("goterm.ico", ico.Bytes(), 0o644)
	os.WriteFile("rsrc_windows_amd64.syso", buildSyso(0x8664, 3, ents), 0o644)
	os.WriteFile("rsrc_windows_386.syso", buildSyso(0x14c, 7, ents), 0o644)
}

// buildSyso creates a COFF object with a .rsrc section holding RT_ICON
// resources (ids 1..n) and an RT_GROUP_ICON resource (id 1).
func buildSyso(machine uint16, relType uint16, ents []entry) []byte {
	n := len(ents)
	// group icon directory
	var grp bytes.Buffer
	binary.Write(&grp, binary.LittleEndian, [3]uint16{0, 1, uint16(n)})
	for i, e := range ents {
		w := byte(e.size)
		if e.size == 256 {
			w = 0
		}
		grp.Write([]byte{w, w, 0, 0})
		binary.Write(&grp, binary.LittleEndian, [2]uint16{1, 32})
		binary.Write(&grp, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&grp, binary.LittleEndian, uint16(i+1))
	}

	// Resource tree layout (all offsets relative to section start):
	//  root dir (2 type entries: RT_ICON=3, RT_GROUP_ICON=14)
	//  icon type dir (n name entries) -> n language dirs (1 entry each)
	//  group type dir (1 name entry) -> 1 language dir
	//  data entries (n+1), then raw data
	dirSize := func(entries int) int { return 16 + 8*entries }
	rootOff := 0
	iconDirOff := rootOff + dirSize(2)
	iconLangOff := iconDirOff + dirSize(n)
	grpDirOff := iconLangOff + n*dirSize(1)
	grpLangOff := grpDirOff + dirSize(1)
	dataEntOff := grpLangOff + dirSize(1)
	rawOff := dataEntOff + (n+1)*16
	for rawOff%4 != 0 {
		rawOff++
	}
	// raw data offsets
	rawOffs := make([]int, n+1)
	cur := rawOff
	for i := 0; i < n; i++ {
		rawOffs[i] = cur
		cur += len(ents[i].data)
		for cur%4 != 0 {
			cur++
		}
	}
	rawOffs[n] = cur
	cur += grp.Len()
	for cur%4 != 0 {
		cur++
	}
	sec := make([]byte, cur)
	le := binary.LittleEndian
	putDir := func(off int, ids []uint32, targets []uint32) {
		// characteristics, timestamp, version, named, id count
		le.PutUint16(sec[off+12:], 0)
		le.PutUint16(sec[off+14:], uint16(len(ids)))
		for i := range ids {
			le.PutUint32(sec[off+16+8*i:], ids[i])
			le.PutUint32(sec[off+20+8*i:], targets[i])
		}
	}
	const sub = 0x80000000
	putDir(rootOff, []uint32{3, 14}, []uint32{uint32(iconDirOff) | sub, uint32(grpDirOff) | sub})
	var ids, tg []uint32
	for i := 0; i < n; i++ {
		ids = append(ids, uint32(i+1))
		tg = append(tg, uint32(iconLangOff+i*dirSize(1))|sub)
	}
	putDir(iconDirOff, ids, tg)
	var relocs []int
	for i := 0; i < n; i++ {
		putDir(iconLangOff+i*dirSize(1), []uint32{0x409}, []uint32{uint32(dataEntOff + i*16)})
	}
	putDir(grpDirOff, []uint32{1}, []uint32{uint32(grpLangOff) | sub})
	putDir(grpLangOff, []uint32{0x409}, []uint32{uint32(dataEntOff + n*16)})
	for i := 0; i <= n; i++ {
		o := dataEntOff + i*16
		le.PutUint32(sec[o:], uint32(rawOffs[i])) // RVA (relocated)
		size := grp.Len()
		if i < n {
			size = len(ents[i].data)
			copy(sec[rawOffs[i]:], ents[i].data)
		} else {
			copy(sec[rawOffs[i]:], grp.Bytes())
		}
		le.PutUint32(sec[o+4:], uint32(size))
		relocs = append(relocs, o)
	}
	sort.Ints(relocs)

	var out bytes.Buffer
	secPtr := 20 + 40
	relPtr := secPtr + len(sec)
	symPtr := relPtr + 10*len(relocs)
	binary.Write(&out, le, struct {
		Machine, NSec            uint16
		Time, SymPtr, NSym       uint32
		OptSize, Characteristics uint16
	}{machine, 1, 0, uint32(symPtr), 1, 0, 0})
	name := [8]byte{'.', 'r', 's', 'r', 'c'}
	out.Write(name[:])
	binary.Write(&out, le, struct {
		VSize, VAddr, RawSize, RawPtr, RelPtr, LinePtr uint32
		NReloc, NLine                                  uint16
		Flags                                          uint32
	}{0, 0, uint32(len(sec)), uint32(secPtr), uint32(relPtr), 0, uint16(len(relocs)), 0, 0x40000040})
	out.Write(sec)
	for _, r := range relocs {
		binary.Write(&out, le, struct {
			Addr, Sym uint32
			Type      uint16
		}{uint32(r), 0, relType}) // ADDR32NB / DIR32NB
	}
	// symbol table: one section symbol
	out.Write(name[:])
	binary.Write(&out, le, struct {
		Value   uint32
		Section int16
		Type    uint16
		Class   uint8
		Aux     uint8
	}{0, 1, 0, 3, 0})
	binary.Write(&out, le, uint32(4)) // empty string table
	return out.Bytes()
}
