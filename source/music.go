package main

import (
	"encoding/binary"
	"math"
	"strings"
)

const audioRate = 44100

// Characters allowed in a music string (SyncTERM cterm "musicchars").
const musicChars = "aAbBcCdDeEfFgGlLmMnNoOpPsStT0123456789.-+#<> "

const (
	shapeNormal = iota
	shapeLegato
	shapeStaccato
)

// MusicState is the persistent PLAY-style state (tempo, octave, ...),
// mirroring SyncTERM's per-terminal music variables.
type MusicState struct {
	Tempo   int
	Octave  int
	NoteLen int
	Shape   int
	Fore    bool
}

func newMusicState() MusicState {
	return MusicState{Tempo: 120, Octave: 4, NoteLen: 4, Shape: shapeNormal, Fore: true}
}

func validMusicString(s []byte) bool {
	for _, b := range s {
		if !strings.ContainsRune(musicChars, rune(b)) {
			return false
		}
	}
	return true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func upper(b byte) byte {
	if b >= 'a' && b <= 'z' {
		return b - 32
	}
	return b
}

// Render interprets a music string and returns mono 16-bit PCM at
// audioRate plus whether any foreground notes were generated.
// kind==1 is the "CSI | / CSI M" form where a leading F/B/N/L/S is an
// implied M-prefixed setting; kind==2 is BananaCom (CSI N).
func (m *MusicState) Render(str []byte, kind int) (pcm []int16, fore bool) {
	p := 0
	at := func(i int) byte {
		if i < len(str) {
			return str[i]
		}
		return 0
	}
	if kind == 1 && len(str) > 0 {
		switch upper(str[0]) {
		case 'F':
			m.Fore = true
			p++
		case 'B':
			m.Fore = false
			p++
		case 'N':
			if !isDigit(at(1)) {
				m.Shape = shapeNormal
				p++
			}
		case 'L':
			m.Shape = shapeLegato
			p++
		case 'S':
			m.Shape = shapeStaccato
			p++
		}
	}
	num := func() (int, bool) {
		v, got := 0, false
		for isDigit(at(p + 1)) {
			p++
			v = v*10 + int(str[p]-'0')
			if v > 100000 {
				v = 100000
			}
			got = true
		}
		return v, got
	}
	const octave = "C#D#EF#G#A#B"
	for ; p < len(str); p++ {
		c := upper(str[p])
		notenum := 0
		offset := 0
		switch c {
		case 'M':
			p++
			switch upper(at(p)) {
			case 'F':
				m.Fore = true
			case 'B':
				m.Fore = false
			case 'N':
				m.Shape = shapeNormal
			case 'L':
				m.Shape = shapeLegato
			case 'S':
				m.Shape = shapeStaccato
			default:
				p--
			}
			continue
		case 'T':
			v, _ := num()
			if v > 255 {
				v = 255
			}
			if v < 32 {
				v = 32
			}
			m.Tempo = v
			continue
		case 'O':
			v, _ := num()
			if v > 6 {
				v = 6
			}
			m.Octave = v
			continue
		case 'L':
			v, _ := num()
			if v < 1 {
				v = 1
			}
			if v > 64 {
				v = 64
			}
			m.NoteLen = v
			continue
		case '<':
			m.Octave--
			if m.Octave < 0 {
				m.Octave = 0
			}
			continue
		case '>':
			m.Octave++
			if m.Octave > 6 {
				m.Octave = 6
			}
			continue
		case 'N', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'P':
		default:
			continue
		}

		// Note / pause
		note := c
		if c == 'N' {
			if isDigit(at(p + 1)) {
				notenum, _ = num()
			} else {
				notenum = -1 // "N" with no number: silence
			}
			notenum = notenum + 1 // shift so that 0 below means "by letter"
			// N0 -> table[0]; internal notenum is table index + 1
		}
		notelen := m.NoteLen
		dotted := false
		for more := true; more; {
			more = false
			if n := at(p + 1); n == '+' || n == '#' {
				offset++
				p++
				more = true
			}
			if at(p+1) == '-' {
				offset--
				p++
				more = true
			}
			if at(p+1) == '.' {
				dotted = true
				p++
				more = true
			}
			if isDigit(at(p + 1)) {
				v, _ := num()
				notelen = v
				more = true
			}
		}
		idx := -1 // index into noteFreqMilliHz; -1 = rest
		switch {
		case note == 'P':
			idx = -1
		case note == 'N':
			if notenum > 0 {
				idx = notenum - 1 + offset
			}
		default:
			k := strings.IndexByte(octave, note)
			if k >= 0 {
				idx = m.Octave*12 + k + offset
			}
		}
		if notelen < 1 {
			notelen = 1
		}
		if notelen > 64 {
			notelen = 64
		}
		dur := 240000 / m.Tempo
		if dotted {
			dur = 360000 / m.Tempo
		}
		dur /= notelen
		pause := dur / 8
		switch m.Shape {
		case shapeStaccato:
			pause = dur / 4
		case shapeLegato:
			pause = 0
		}
		dur -= pause
		freq := 0.0
		if idx >= 0 && idx < 72+12 && idx < len(noteFreqMilliHz) {
			freq = float64(noteFreqMilliHz[idx]) / 1000.0
		}
		noteFrames := audioRate * dur / 1000
		pauseFrames := audioRate * pause / 1000
		start := len(pcm)
		pcm = append(pcm, make([]int16, noteFrames+pauseFrames)...)
		if freq > 0 && noteFrames > 0 {
			synthTone(pcm[start:start+noteFrames], freq)
		}
		if m.Fore {
			fore = true
		}
	}
	return pcm, fore
}

// synthTone fills dst with a sine+saw blend (SyncTERM uses a
// "sine saw harmonic" wave) with a tiny attack/release to avoid clicks.
func synthTone(dst []int16, freq float64) {
	n := len(dst)
	ramp := audioRate / 400 // 2.5ms
	if ramp*2 > n {
		ramp = n / 2
	}
	phase := 0.0
	step := freq / audioRate
	const amp = 0.25 // about -12 dB
	for i := 0; i < n; i++ {
		s := 0.7*math.Sin(2*math.Pi*phase) + 0.3*(2*phase-1)
		env := 1.0
		if i < ramp {
			env = float64(i) / float64(ramp)
		} else if i >= n-ramp {
			env = float64(n-1-i) / float64(ramp)
		}
		dst[i] = int16(s * env * amp * 32767)
		phase += step
		if phase >= 1 {
			phase -= 1
		}
	}
}

// beepPCM returns a short bell tone.
func beepPCM() []int16 {
	n := audioRate * 110 / 1000
	b := make([]int16, n)
	synthTone(b, 880)
	return b
}

// pcmToWAV wraps mono 16-bit PCM in a RIFF/WAVE container.
func pcmToWAV(pcm []int16) []byte {
	dataLen := len(pcm) * 2
	out := make([]byte, 44+dataLen)
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+dataLen))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], audioRate)
	binary.LittleEndian.PutUint32(out[28:], audioRate*2)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(dataLen))
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(out[44+i*2:], uint16(v))
	}
	return out
}

func pcmDurationMs(pcm []int16) int { return len(pcm) * 1000 / audioRate }
