package main

import "errors"

// Minimal FLAC decoder (native FLAC streams, all subframe types, 4-32
// bits per sample). Returns interleaved S16.

type flacBits struct {
	b   []byte
	pos int // bit position
}

var errFlac = errors.New("flac: bad stream")

func (r *flacBits) u(n int) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	if r.pos+n > len(r.b)*8 {
		return 0, errFlac
	}
	var v uint64
	for n > 0 {
		byt := r.b[r.pos>>3]
		avail := 8 - r.pos&7
		take := avail
		if take > n {
			take = n
		}
		v = v<<uint(take) | uint64(byt>>uint(avail-take))&(1<<uint(take)-1)
		r.pos += take
		n -= take
	}
	return v, nil
}

func (r *flacBits) s(n int) (int64, error) {
	v, err := r.u(n)
	if err != nil || n == 0 {
		return 0, err
	}
	return int64(v<<uint(64-n)) >> uint(64-n), nil
}

func (r *flacBits) align() { r.pos = (r.pos + 7) &^ 7 }

func (r *flacBits) unary() (int, error) {
	n := 0
	for {
		bit, err := r.u(1)
		if err != nil {
			return 0, err
		}
		if bit == 1 {
			return n, nil
		}
		n++
	}
}

func decodeFLAC(b []byte) (pcm []int16, ch, rate int, ok bool) {
	if len(b) < 42 || string(b[:4]) != "fLaC" {
		return nil, 0, 0, false
	}
	p := 4
	var bps int
	have := false
	for p+4 <= len(b) {
		last := b[p]&0x80 != 0
		typ := b[p] & 0x7F
		sz := int(b[p+1])<<16 | int(b[p+2])<<8 | int(b[p+3])
		p += 4
		if p+sz > len(b) {
			return nil, 0, 0, false
		}
		if typ == 0 && sz >= 34 {
			si := b[p : p+34]
			rate = int(si[10])<<12 | int(si[11])<<4 | int(si[12])>>4
			ch = int(si[12]>>1&7) + 1
			bps = (int(si[12]&1)<<4 | int(si[13]>>4)) + 1
			have = true
		}
		p += sz
		if last {
			break
		}
	}
	if !have || rate == 0 {
		return nil, 0, 0, false
	}
	r := &flacBits{b: b, pos: p * 8}
	var out []int16
	for r.pos+16 <= len(b)*8 {
		start := r.pos
		samples, fch, fbps, frate, err := flacFrame(r, ch, bps, rate)
		if err != nil {
			if len(out) > 0 {
				break // keep what decoded before a damaged/truncated frame
			}
			return nil, 0, 0, false
		}
		_ = start
		if fch != ch {
			ch = fch
		}
		rate = frate
		for _, v := range samples {
			switch {
			case fbps > 16:
				out = append(out, int16(v>>uint(fbps-16)))
			case fbps < 16:
				out = append(out, int16(v<<uint(16-fbps)))
			default:
				out = append(out, int16(v))
			}
		}
		if len(out) > maxAudioB/2 {
			break
		}
	}
	return out, ch, rate, len(out) > 0
}

// flacFrame decodes one frame into interleaved samples.
func flacFrame(r *flacBits, siCh, siBps, siRate int) (out []int32, ch, bps, rate int, err error) {
	r.align()
	sync, err := r.u(14)
	if err != nil || sync != 0x3FFE {
		return nil, 0, 0, 0, errFlac
	}
	r.u(2) // reserved, blocking strategy
	bsCode, _ := r.u(4)
	srCode, _ := r.u(4)
	chCode, _ := r.u(4)
	bpsCode, _ := r.u(3)
	r.u(1)
	// UTF-8 style frame/sample number
	first, err := r.u(8)
	if err != nil {
		return nil, 0, 0, 0, err
	}
	for m := byte(0x80); first&uint64(m) != 0 && m > 1; m >>= 1 {
		if _, err = r.u(8); err != nil {
			return nil, 0, 0, 0, err
		}
	}
	var bs int
	switch bsCode {
	case 0:
		return nil, 0, 0, 0, errFlac
	case 1:
		bs = 192
	case 2, 3, 4, 5:
		bs = 576 << uint(bsCode-2)
	case 6:
		v, _ := r.u(8)
		bs = int(v) + 1
	case 7:
		v, _ := r.u(16)
		bs = int(v) + 1
	default:
		bs = 256 << uint(bsCode-8)
	}
	rate = siRate
	switch srCode {
	case 0:
	case 1:
		rate = 88200
	case 2:
		rate = 176400
	case 3:
		rate = 192000
	case 4:
		rate = 8000
	case 5:
		rate = 16000
	case 6:
		rate = 22050
	case 7:
		rate = 24000
	case 8:
		rate = 32000
	case 9:
		rate = 44100
	case 10:
		rate = 48000
	case 11:
		rate = 96000
	case 12:
		v, _ := r.u(8)
		rate = int(v) * 1000
	case 13:
		v, _ := r.u(16)
		rate = int(v)
	case 14:
		v, _ := r.u(16)
		rate = int(v) * 10
	default:
		return nil, 0, 0, 0, errFlac
	}
	bps = siBps
	switch bpsCode {
	case 1:
		bps = 8
	case 2:
		bps = 12
	case 4:
		bps = 16
	case 5:
		bps = 20
	case 6:
		bps = 24
	case 7:
		bps = 32
	}
	r.u(8) // header CRC
	ch = siCh
	mode := 0
	switch {
	case chCode < 8:
		ch = int(chCode) + 1
	case chCode <= 10:
		ch, mode = 2, int(chCode)
	default:
		return nil, 0, 0, 0, errFlac
	}
	chans := make([][]int32, ch)
	for c := 0; c < ch; c++ {
		sb := bps
		if (mode == 8 && c == 1) || (mode == 9 && c == 0) || (mode == 10 && c == 1) {
			sb++
		}
		chans[c], err = flacSubframe(r, bs, sb)
		if err != nil {
			return nil, 0, 0, 0, err
		}
	}
	r.align()
	r.u(16) // frame CRC
	switch mode {
	case 8: // left/side
		for i := 0; i < bs; i++ {
			chans[1][i] = chans[0][i] - chans[1][i]
		}
	case 9: // side/right
		for i := 0; i < bs; i++ {
			chans[0][i] += chans[1][i]
		}
	case 10: // mid/side
		for i := 0; i < bs; i++ {
			m, s := chans[0][i], chans[1][i]
			m = m<<1 | s&1
			chans[0][i], chans[1][i] = (m+s)>>1, (m-s)>>1
		}
	}
	out = make([]int32, 0, bs*ch)
	for i := 0; i < bs; i++ {
		for c := 0; c < ch; c++ {
			out = append(out, chans[c][i])
		}
	}
	return out, ch, bps, rate, nil
}

func flacSubframe(r *flacBits, n, bps int) ([]int32, error) {
	r.u(1)
	typ, err := r.u(6)
	if err != nil {
		return nil, err
	}
	wasted := 0
	if f, _ := r.u(1); f == 1 {
		k, err := r.unary()
		if err != nil {
			return nil, err
		}
		wasted = k + 1
	}
	bps -= wasted
	if bps < 1 || bps > 33 {
		return nil, errFlac
	}
	x := make([]int32, n)
	switch {
	case typ == 0:
		v, err := r.s(bps)
		if err != nil {
			return nil, err
		}
		for i := range x {
			x[i] = int32(v)
		}
	case typ == 1:
		for i := range x {
			v, err := r.s(bps)
			if err != nil {
				return nil, err
			}
			x[i] = int32(v)
		}
	case typ >= 8 && typ <= 12:
		order := int(typ - 8)
		if order > n {
			return nil, errFlac
		}
		for i := 0; i < order; i++ {
			v, err := r.s(bps)
			if err != nil {
				return nil, err
			}
			x[i] = int32(v)
		}
		if err := flacResidual(r, x, order, n); err != nil {
			return nil, err
		}
		for i := order; i < n; i++ {
			switch order {
			case 1:
				x[i] += x[i-1]
			case 2:
				x[i] += 2*x[i-1] - x[i-2]
			case 3:
				x[i] += 3*x[i-1] - 3*x[i-2] + x[i-3]
			case 4:
				x[i] += 4*x[i-1] - 6*x[i-2] + 4*x[i-3] - x[i-4]
			}
		}
	case typ >= 32:
		order := int(typ-32) + 1
		if order > n {
			return nil, errFlac
		}
		for i := 0; i < order; i++ {
			v, err := r.s(bps)
			if err != nil {
				return nil, err
			}
			x[i] = int32(v)
		}
		pv, _ := r.u(4)
		if pv == 15 {
			return nil, errFlac
		}
		prec := int(pv) + 1
		shift, _ := r.s(5)
		if shift < 0 {
			return nil, errFlac
		}
		coef := make([]int64, order)
		for i := range coef {
			c, err := r.s(prec)
			if err != nil {
				return nil, err
			}
			coef[i] = c
		}
		if err := flacResidual(r, x, order, n); err != nil {
			return nil, err
		}
		for i := order; i < n; i++ {
			var sum int64
			for j := 0; j < order; j++ {
				sum += coef[j] * int64(x[i-1-j])
			}
			x[i] += int32(sum >> uint(shift))
		}
	default:
		return nil, errFlac
	}
	if wasted > 0 {
		for i := range x {
			x[i] <<= uint(wasted)
		}
	}
	return x, nil
}

func flacResidual(r *flacBits, x []int32, order, n int) error {
	method, err := r.u(2)
	if err != nil || method > 1 {
		return errFlac
	}
	po, _ := r.u(4)
	parts := 1 << po
	pbits, esc := 4, uint64(15)
	if method == 1 {
		pbits, esc = 5, 31
	}
	i := order
	for p := 0; p < parts; p++ {
		cnt := n >> po
		if p == 0 {
			cnt -= order
		}
		if cnt < 0 {
			return errFlac
		}
		k, err := r.u(pbits)
		if err != nil {
			return err
		}
		if k == esc {
			nb, _ := r.u(5)
			for j := 0; j < cnt; j++ {
				v, err := r.s(int(nb))
				if err != nil {
					return err
				}
				x[i] = int32(v)
				i++
			}
			continue
		}
		for j := 0; j < cnt; j++ {
			q, err := r.unary()
			if err != nil {
				return err
			}
			lo, err := r.u(int(k))
			if err != nil {
				return err
			}
			u := uint64(q)<<k | lo
			x[i] = int32(u>>1) ^ -int32(u&1)
			i++
		}
	}
	return nil
}
