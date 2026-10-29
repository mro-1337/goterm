package main

import (
	"encoding/binary"
	"math"
)

// Audio file decoding for the SyncTERM audio APC (Load / LoadBlob).
// SyncTERM uses libsndfile; GoTerm is dependency-free so it decodes the
// common uncompressed containers itself: WAV (PCM, float, A-law, µ-law,
// WAVE_FORMAT_EXTENSIBLE), AIFF/AIFF-C (PCM, float, A-law, µ-law) and
// Sun/NeXT AU. The result is always 44100 Hz interleaved stereo S16, the
// mixer's native format, converted exactly as SyncTERM's own resampler
// does (linear interpolation, mono duplicated, extra channels dropped).

const apRate = 44100

// libsndfile major-format and subtype numbers (see cterm.adoc).
const (
	sfWAV   = 1
	sfAIFF  = 2
	sfAU    = 3
	sfWAVEX = 19
	sfFLAC  = 23
	sfOGG   = 32
	sfMPEG  = 35

	sfPCMS8   = 1
	sfPCM16   = 2
	sfPCM24   = 3
	sfPCM32   = 4
	sfPCMU8   = 5
	sfFloat   = 6
	sfDouble  = 7
	sfULaw    = 16
	sfALaw    = 17
	maxAudioB = 64 << 20
)

// audioFormatSupported answers SyncTERM:Q;libsndfileFormat for the
// container/subtype pairs this decoder really handles.
func audioFormatSupported(major, sub uint32) bool {
	switch major {
	case sfWAV, sfWAVEX:
		switch sub {
		case sfPCMS8, sfPCM16, sfPCM24, sfPCM32, sfPCMU8, sfFloat, sfDouble, sfULaw, sfALaw:
			return true
		}
	case sfAIFF:
		switch sub {
		case sfPCMS8, sfPCM16, sfPCM24, sfPCM32, sfFloat, sfDouble, sfULaw, sfALaw:
			return true
		}
	case sfFLAC:
		return sub >= sfPCMS8 && sub <= sfPCM24
	case sfOGG:
		return sub == 96 // Vorbis (Opus is not supported)
	case sfMPEG:
		return sub == 130 // Layer III
	case sfAU:
		switch sub {
		case sfPCMS8, sfPCM16, sfPCM24, sfPCM32, sfFloat, sfDouble, sfULaw, sfALaw:
			return true
		}
	}
	return false
}

// decodeAudio sniffs the container and returns stereo S16 44.1 kHz frames
// (interleaved L,R).
func decodeAudio(b []byte) ([]int16, bool) {
	if len(b) < 12 || len(b) > maxAudioB {
		return nil, false
	}
	var pcm []int16
	var ch, rate int
	var ok bool
	switch {
	case string(b[:4]) == "RIFF" && string(b[8:12]) == "WAVE":
		pcm, ch, rate, ok = decodeWAV(b)
	case string(b[:4]) == "FORM" && (string(b[8:12]) == "AIFF" || string(b[8:12]) == "AIFC"):
		pcm, ch, rate, ok = decodeAIFF(b)
	case string(b[:4]) == ".snd":
		pcm, ch, rate, ok = decodeAU(b)
	case string(b[:4]) == "fLaC":
		pcm, ch, rate, ok = decodeFLAC(b)
	case string(b[:4]) == "OggS":
		pcm, ch, rate, ok = decodeOggVorbis(b)
	case string(b[:3]) == "ID3" || b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		pcm, ch, rate, ok = decodeMP3(b)
	}
	if !ok || ch < 1 || rate < 1 || len(pcm) < ch {
		return nil, false
	}
	out := resampleStereo44k(pcm, len(pcm)/ch, ch, rate)
	return out, len(out) > 0
}

func decodeWAV(b []byte) (pcm []int16, ch, rate int, ok bool) {
	var tag, bits int
	var data []byte
	haveFmt := false
	for p := 12; p+8 <= len(b); {
		id := string(b[p : p+4])
		sz := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		body := p + 8
		end := body + sz
		if end > len(b) || end < body {
			end = len(b)
		}
		switch id {
		case "fmt ":
			if end-body >= 16 {
				f := b[body:end]
				tag = int(binary.LittleEndian.Uint16(f[0:]))
				ch = int(binary.LittleEndian.Uint16(f[2:]))
				rate = int(binary.LittleEndian.Uint32(f[4:]))
				bits = int(binary.LittleEndian.Uint16(f[14:]))
				if tag == 0xFFFE && len(f) >= 26 {
					tag = int(binary.LittleEndian.Uint16(f[24:]))
				}
				haveFmt = true
			}
		case "data":
			data = b[body:end]
		}
		if haveFmt && data != nil {
			break
		}
		p = end + (sz & 1)
	}
	if !haveFmt || data == nil || ch < 1 {
		return nil, 0, 0, false
	}
	var kind string
	switch tag {
	case 1:
		kind = "pcm"
		if bits == 8 {
			kind = "pcmu8"
		}
	case 3:
		kind = "float"
	case 6:
		kind, bits = "alaw", 8
	case 7:
		kind, bits = "ulaw", 8
	default:
		return nil, 0, 0, false
	}
	pcm, ok = convertSamples(data, kind, bits, false)
	return pcm, ch, rate, ok
}

func decodeAIFF(b []byte) (pcm []int16, ch, rate int, ok bool) {
	var bits int
	kind, big := "pcm", true
	var data []byte
	haveComm := false
	aifc := string(b[8:12]) == "AIFC"
	for p := 12; p+8 <= len(b); {
		id := string(b[p : p+4])
		sz := int(binary.BigEndian.Uint32(b[p+4 : p+8]))
		body := p + 8
		end := body + sz
		if end > len(b) || end < body {
			end = len(b)
		}
		switch id {
		case "COMM":
			if end-body >= 18 {
				c := b[body:end]
				ch = int(binary.BigEndian.Uint16(c[0:]))
				bits = int(binary.BigEndian.Uint16(c[6:]))
				rate = int(extended80(c[8:18]))
				haveComm = true
				if aifc && len(c) >= 22 {
					switch string(c[18:22]) {
					case "NONE", "twos":
					case "sowt":
						big = false
					case "fl32", "FL32":
						kind, bits = "float", 32
					case "fl64", "FL64":
						kind, bits = "float", 64
					case "ulaw", "ULAW":
						kind, bits = "ulaw", 8
					case "alaw", "ALAW":
						kind, bits = "alaw", 8
					default:
						return nil, 0, 0, false
					}
				}
			}
		case "SSND":
			if end-body >= 8 {
				off := int(binary.BigEndian.Uint32(b[body:]))
				s := body + 8 + off
				if s <= end {
					data = b[s:end]
				}
			}
		}
		if haveComm && data != nil {
			break
		}
		p = end + (sz & 1)
	}
	if !haveComm || data == nil || ch < 1 {
		return nil, 0, 0, false
	}
	pcm, ok = convertSamples(data, kind, bits, big)
	return pcm, ch, rate, ok
}

func decodeAU(b []byte) (pcm []int16, ch, rate int, ok bool) {
	if len(b) < 24 {
		return nil, 0, 0, false
	}
	off := int(binary.BigEndian.Uint32(b[4:]))
	size := binary.BigEndian.Uint32(b[8:])
	enc := int(binary.BigEndian.Uint32(b[12:]))
	rate = int(binary.BigEndian.Uint32(b[16:]))
	ch = int(binary.BigEndian.Uint32(b[20:]))
	if off < 24 || off > len(b) || ch < 1 || ch > 64 {
		return nil, 0, 0, false
	}
	end := len(b)
	if size != 0xFFFFFFFF && off+int(size) <= len(b) && int(size) >= 0 {
		end = off + int(size)
	}
	data := b[off:end]
	var kind string
	var bits int
	switch enc {
	case 1:
		kind, bits = "ulaw", 8
	case 2:
		kind, bits = "pcm", 8
	case 3:
		kind, bits = "pcm", 16
	case 4:
		kind, bits = "pcm", 24
	case 5:
		kind, bits = "pcm", 32
	case 6:
		kind, bits = "float", 32
	case 7:
		kind, bits = "float", 64
	case 27:
		kind, bits = "alaw", 8
	default:
		return nil, 0, 0, false
	}
	pcm, ok = convertSamples(data, kind, bits, true)
	return pcm, ch, rate, ok
}

// extended80 decodes an IEEE 754 80-bit extended float (AIFF sample rate).
func extended80(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:])&0x7FFF) - 16383
	mant := binary.BigEndian.Uint64(b[2:])
	if mant == 0 {
		return 0
	}
	return math.Ldexp(float64(mant), exp-63)
}

func clip16(f float64) int16 {
	f *= 32767
	if f > 32767 {
		return 32767
	}
	if f < -32768 {
		return -32768
	}
	return int16(f)
}

// convertSamples turns raw sample bytes into interleaved S16.
func convertSamples(d []byte, kind string, bits int, big bool) ([]int16, bool) {
	var bo binary.ByteOrder = binary.LittleEndian
	if big {
		bo = binary.BigEndian
	}
	switch kind {
	case "pcmu8":
		out := make([]int16, len(d))
		for i, v := range d {
			out[i] = int16(int(v)-128) << 8
		}
		return out, true
	case "ulaw":
		out := make([]int16, len(d))
		for i, v := range d {
			out[i] = ulawTab[v]
		}
		return out, true
	case "alaw":
		out := make([]int16, len(d))
		for i, v := range d {
			out[i] = alawDecode(v)
		}
		return out, true
	case "pcm":
		switch bits {
		case 8:
			out := make([]int16, len(d))
			for i, v := range d {
				out[i] = int16(int8(v)) << 8
			}
			return out, true
		case 16:
			out := make([]int16, len(d)/2)
			for i := range out {
				out[i] = int16(bo.Uint16(d[i*2:]))
			}
			return out, true
		case 24:
			out := make([]int16, len(d)/3)
			for i := range out {
				var v int32
				if big {
					v = int32(d[i*3])<<24 | int32(d[i*3+1])<<16 | int32(d[i*3+2])<<8
				} else {
					v = int32(d[i*3])<<8 | int32(d[i*3+1])<<16 | int32(d[i*3+2])<<24
				}
				out[i] = int16(v >> 16)
			}
			return out, true
		case 32:
			out := make([]int16, len(d)/4)
			for i := range out {
				out[i] = int16(int32(bo.Uint32(d[i*4:])) >> 16)
			}
			return out, true
		}
	case "float":
		switch bits {
		case 32:
			out := make([]int16, len(d)/4)
			for i := range out {
				out[i] = clip16(float64(math.Float32frombits(bo.Uint32(d[i*4:]))))
			}
			return out, true
		case 64:
			out := make([]int16, len(d)/8)
			for i := range out {
				out[i] = clip16(math.Float64frombits(bo.Uint64(d[i*8:])))
			}
			return out, true
		}
	}
	return nil, false
}

var ulawTab = func() (t [256]int16) {
	for i := 0; i < 256; i++ {
		u := ^i
		sign := u & 0x80
		exp := (u >> 4) & 7
		man := u & 15
		v := ((man << 3) + 0x84) << uint(exp)
		v -= 0x84
		if sign != 0 {
			v = -v
		}
		t[i] = int16(v)
	}
	return
}()

func alawDecode(a byte) int16 {
	a ^= 0x55
	t := int(a&15) << 4
	seg := int(a&0x70) >> 4
	switch seg {
	case 0:
		t += 8
	case 1:
		t += 0x108
	default:
		t += 0x108
		t <<= uint(seg - 1)
	}
	if a&0x80 != 0 {
		return int16(t)
	}
	return int16(-t)
}

// resampleStereo44k mirrors SyncTERM's resample_to_s16_stereo_44100().
func resampleStereo44k(src []int16, n, chans, rate int) []int16 {
	if n == 0 || rate <= 0 || chans <= 0 {
		return nil
	}
	dn := int((uint64(n)*apRate + uint64(rate/2)) / uint64(rate))
	if dn == 0 {
		dn = 1
	}
	dst := make([]int16, dn*2)
	for j := 0; j < dn; j++ {
		whole := uint64(j) * uint64(rate)
		idx := whole / apRate
		rem := whole - idx*apRate
		frac := uint32((rem << 32) / apRate)
		if idx >= uint64(n) {
			idx, frac = uint64(n-1), 0
		}
		la := int(idx) * chans
		lb := la
		if int(idx)+1 < n {
			lb = la + chans
		}
		ra, rb := la, lb
		if chans >= 2 {
			ra, rb = la+1, lb+1
		}
		lerp := func(a, b int32) int16 {
			v := a + int32((int64(b-a)*int64(frac))>>32)
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			return int16(v)
		}
		dst[j*2] = lerp(int32(src[la]), int32(src[lb]))
		dst[j*2+1] = lerp(int32(src[ra]), int32(src[rb]))
	}
	return dst
}
