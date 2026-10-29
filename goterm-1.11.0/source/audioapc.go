package main

import (
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// SyncTERM audio APC engine ("ACP audio"): a port of SyncTERM's
// audio_apc.c and the xpbeep mixer. A BBS drives it with
//
//	ESC _ SyncTERM:A;<verb>;... ESC \
//
// Load/LoadBlob/Synth/Copy fill 256 patch slots with 44.1 kHz stereo S16;
// Queue hands a slot to one of channels 2..15; Flush/Volume/Wait/Update
// control the channels. SyncTERM:Q;libsndfile* feature queries and the
// CSI = 7 n state report let the BBS discover what works.

const (
	apSlots   = 256
	apChans   = 16
	apFirstCh = 2
	apBaseDB  = -12.0
	apPull    = 1024 // frames per mixer pull (~23 ms)
	apMaxSyn  = 5 * 60 * apRate
)

type apBuf struct {
	frames          []int16 // interleaved stereo
	n               int
	consumed        int
	fadeIn, fadeOut int
	loop            bool
	loopIter        int
	volL, volR      float64 // dB, summed with the stream base
	overlayTotal    int
	overlayElapsed  int
}

type apStream struct {
	bufs                     []*apBuf
	volL, volR               float64
	rampTotal, rampElapsed   int
	rampStartL, rampStartR   float64 // linear amplitude
	rampTargetL, rampTargetR float64
	rampTgtDBL, rampTgtDBR   float64
}

// Sink receives mixed PCM (S16LE stereo 44100). Write blocks at the
// device rate; Close releases the device.
type audioSink interface {
	Write(p []byte) error
	Close()
}

// AudioAPC is the shared audio engine (one per App).
type AudioAPC struct {
	mu       sync.Mutex
	patches  [apSlots][]int16
	streams  [apChans]*apStream
	notify   [apChans]bool
	lastRun  [apChans]bool
	running  bool
	sinkOpen func() audioSink // nil → silent but time-paced

	// Send transmits bytes to the host (async notifications).
	Send func([]byte)
	// CacheDir is where SyncTERM:C;S stores files and Load reads them.
	CacheDir string
	// Enabled mirrors the sound setting; when false nothing is played
	// but all protocol replies and timing still work.
	Muted bool
	// Hidden silences output while the session's tab is in the background.
	Hidden bool
}

func NewAudioAPC() *AudioAPC { return &AudioAPC{} }

// Reset drops all patches and stops every channel (new connection / hangup).
func (a *AudioAPC) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.patches {
		a.patches[i] = nil
	}
	for i := range a.streams {
		a.streams[i] = nil
		a.notify[i] = false
		a.lastRun[i] = false
	}
}

// ----- parsing helpers ------------------------------------------------

func ulongPrefix(s string) uint64 {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	v, err := strconv.ParseUint(s[:i], 10, 64)
	if err != nil {
		return math.MaxUint32
	}
	return v
}

func floatPrefix(s string) (float64, string) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && s[j] >= '0' && s[j] <= '9' {
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			i = j
		}
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, s
	}
	return v, s[i:]
}

// parseDuration: "<n>" / "<n>ms" = ms, "<n>f" = frames, "<n>p" = periods
// of freq. Returns 0 for anything invalid.
func parseDuration(s string, freq float64, allowP bool) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n := ulongPrefix(s[:i])
	rest := s[i:]
	switch {
	case rest == "" || rest[0] == ';' || rest == "ms":
		return apClamp(n * apRate / 1000)
	case rest == "f":
		return apClamp(n)
	case rest == "p":
		if !allowP || freq <= 0 {
			return 0
		}
		return apClamp(uint64(float64(n)*apRate/freq + 0.5))
	}
	return 0
}

func apClamp(v uint64) int {
	if v > apMaxSyn*4 {
		return apMaxSyn * 4
	}
	return int(v)
}

// parseVolume: "<pct>" (0..100) or "<x>dB".
func parseVolume(s string) float64 {
	v, rest := floatPrefix(s)
	if rest == s {
		return 0
	}
	if len(rest) >= 2 && (rest[0] == 'd' || rest[0] == 'D') && (rest[1] == 'b' || rest[1] == 'B') {
		return v
	}
	if v <= 0 {
		return -60
	}
	if v > 100 {
		v = 100
	}
	return 20 * math.Log10(v/100)
}

func parseShape(s string) int {
	names := []struct {
		n string
		v int
	}{
		{"SINE_SAW_HARM", shSineSawHarm}, {"SINE_SAW_CHORD", shSineSawChord},
		{"SINE_HARM", shSineHarm}, {"SINE_SAW", shSineSaw}, {"SILENCE", shSilence},
		{"SAW", shSaw}, {"SIN", shSine}, {"SQ", shSquare},
	}
	for _, e := range names {
		if strings.HasPrefix(s, e.n) && (len(s) == len(e.n) || s[len(e.n)] == ';') {
			return e.v
		}
	}
	return -1
}

// args splits ";A=1;B;C=x" into its fields (first empty one dropped).
func apcArgs(s string) []string {
	if !strings.HasPrefix(s, ";") {
		return nil
	}
	return strings.Split(s[1:], ";")
}

// ----- synth ------------------------------------------------------------

const (
	shSine = iota
	shSaw
	shSquare
	shSineSaw
	shSineHarm
	shSineSawChord
	shSineSawHarm
	shSilence = -2
)

const (
	waveTPI = 6.28318530717958647692
	wavePI  = 3.14159265358979323846
)

// makeWave is a port of xptone_makewave(): stereo S16 (L==R). The
// "noisy" shapes reproduce the original U8 arithmetic quirks on purpose.
func makeWave(freq float64, samples, shape int) []int16 {
	out := make([]int16, samples*2)
	if freq == 0 || samples <= 0 {
		return out
	}
	mono := make([]int16, samples)
	inc := 8.0 * math.Atan(1.0)
	inc *= freq / apRate
	noisy := shape == shSineHarm || shape == shSineSaw || shape == shSineSawChord || shape == shSineSawHarm
	u8 := func(f float64) uint8 { return uint8(int(f)) }
	for i := 0; i < samples; i++ {
		fi := float64(i)
		pos := inc * fi
		pos -= float64(int(pos/waveTPI)) * waveTPI
		if noisy {
			var u uint8
			switch shape {
			case shSineHarm:
				u = u8(math.Sin(pos)*64 + 128)
				u = u8(math.Sin((inc*2)*fi) * 24)
				u = u8(math.Sin((inc*3)*fi) * 16)
			case shSineSaw:
				u = u8(((math.Sin(pos)*127 + 128) + ((waveTPI - pos) * 40.5)) / 2)
			case shSineSawChord:
				u = u8(((math.Sin(pos)*64 + 128) + ((waveTPI - pos) * 6.2)) / 2)
				u = u8(float64(u) + math.Sin((inc/2)*fi)*24)
				u = u8(float64(u) + math.Sin((inc/3)*fi)*16)
			case shSineSawHarm:
				u = u8(((math.Sin(pos)*64 + 128) + ((waveTPI - pos) * 6.2)) / 2)
				u = u8(float64(u) + math.Sin((inc*2)*fi)*24)
				u = u8(float64(u) + math.Sin((inc*3)*fi)*16)
			}
			mono[i] = int16((int(u) - 128) << 8)
		} else {
			var v float64
			switch shape {
			case shSine:
				v = math.Sin(pos) * 32767
			case shSaw:
				v = 32767 - (pos/waveTPI)*65535
			case shSquare:
				if pos < wavePI {
					v = 32767
				} else {
					v = -32768
				}
			}
			mono[i] = int16(v)
		}
	}
	// Click prevention: trim trailing partial wave, ramp the outer crossings.
	mid := samples / 2
	endHigh := mono[samples-1] > 0
	i := samples - 1
	if mono[i] != 0 {
		for ; i > mid; i-- {
			if endHigh && mono[i] < 0 {
				break
			}
			if !endHigh && mono[i] > 0 {
				break
			}
			mono[i] = 0
		}
	}
	numcross := int(freq / 100)
	if numcross != 0 {
		crossings := 0
		for i = 0; i < samples-1; i++ {
			if ((mono[i] < 0 && mono[i+1] >= 0) || (mono[i] > 0 && mono[i+1] <= 0)) && i > 2 {
				crossings++
				if crossings >= numcross {
					break
				}
			}
			d := numcross - crossings
			mono[i] = int16(int(mono[i]) / (d * d))
		}
		crossings = 0
		for i = samples - 1; i > 0; i-- {
			if ((mono[i] < 0 && mono[i-1] >= 0) || (mono[i] > 0 && mono[i-1] <= 0)) && i > 2 {
				crossings++
				if crossings >= numcross {
					break
				}
			}
			d := numcross - crossings
			mono[i] = int16(int(mono[i]) / (d * d))
		}
	}
	for i := 0; i < samples; i++ {
		out[i*2], out[i*2+1] = mono[i], mono[i]
	}
	return out
}

// ----- mixer --------------------------------------------------------------

func fadeGain(pos, total int) int32 {
	if total <= 0 || pos >= total || pos < 0 {
		return 32767
	}
	g := math.Sin(float64(pos) / float64(total) * math.Pi / 2)
	return int32(g*32767 + 0.5)
}

func dbGain(db float64) float64 { return math.Pow(10, db/20) }

// mixOne adds up to `frames` frames of b into acc; returns frames written
// and whether the buffer is finished.
func mixOne(b *apBuf, acc []int32, frames int, vl, vr float64) (int, bool) {
	gl := dbGain(vl + b.volL)
	gr := dbGain(vr + b.volR)
	drop := false
	j := 0
	for ; j < frames; j++ {
		if b.overlayTotal > 0 && b.overlayElapsed >= b.overlayTotal {
			drop = true
			break
		}
		if b.consumed >= b.n {
			if b.loop {
				b.consumed = 0
				b.loopIter++
			} else {
				drop = true
				break
			}
		}
		sl := int32(b.frames[b.consumed*2])
		sr := int32(b.frames[b.consumed*2+1])
		g := int32(32767)
		if b.loopIter == 0 && b.fadeIn > 0 && b.consumed < b.fadeIn {
			g = (g * fadeGain(b.consumed, b.fadeIn)) >> 15
		}
		if !b.loop && b.fadeOut > 0 && b.consumed+b.fadeOut >= b.n {
			g = (g * fadeGain(b.n-1-b.consumed, b.fadeOut)) >> 15
		}
		if b.overlayTotal > 0 {
			g = (g * fadeGain(b.overlayTotal-1-b.overlayElapsed, b.overlayTotal)) >> 15
		}
		lv := (sl * g) >> 15
		rv := (sr * g) >> 15
		acc[j*2] += int32(float64(lv) * gl)
		acc[j*2+1] += int32(float64(rv) * gr)
		b.consumed++
		if b.overlayTotal > 0 {
			b.overlayElapsed++
		}
	}
	if !drop && b.overlayTotal > 0 && b.overlayElapsed >= b.overlayTotal {
		drop = true
	}
	if !drop && b.consumed >= b.n && !b.loop {
		drop = true
	}
	return j, drop
}

func (s *apStream) advanceRamp(frames int) {
	if s.rampTotal == 0 {
		return
	}
	rem := s.rampTotal - s.rampElapsed
	step := frames
	if step > rem {
		step = rem
	}
	s.rampElapsed += step
	if s.rampElapsed >= s.rampTotal {
		s.volL, s.volR = s.rampTgtDBL, s.rampTgtDBR
		s.rampTotal = 0
		return
	}
	pos := s.rampElapsed
	rising := float64(fadeGain(pos, s.rampTotal)) / 32767
	falling := float64(fadeGain(s.rampTotal-1-pos, s.rampTotal)) / 32767
	mix := func(start, target float64) float64 {
		var amp float64
		if target >= start {
			amp = start + (target-start)*rising
		} else {
			amp = target + (start-target)*falling
		}
		if amp > 1e-6 {
			return 20 * math.Log10(amp)
		}
		return -60
	}
	s.volL = mix(s.rampStartL, s.rampTargetL)
	s.volR = mix(s.rampStartR, s.rampTargetR)
}

func softClip(sum int32) int16 {
	y := math.Tanh(float64(sum)/32767) * 32767
	v := int32(y)
	if v > 32767 {
		v = 32767
	} else if v < -32768 {
		v = -32768
	}
	return int16(v)
}

// Pull mixes `frames` frames into out (len >= frames*2) and returns the
// number of leading frames that had audio. Also returns any Update
// notifications that fired.
func (a *AudioAPC) Pull(out []int16, frames int) (int, []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	acc := make([]int32, frames*2)
	maxN := 0
	for ci, s := range a.streams {
		if s == nil {
			continue
		}
		s.advanceRamp(frames)
		if len(s.bufs) == 0 {
			continue
		}
		vl, vr := s.volL, s.volR
		off := 0
		for len(s.bufs) > 0 && off < frames {
			head := s.bufs[0]
			if head.overlayTotal > 0 && len(s.bufs) > 1 {
				next := s.bufs[1]
				w, drop := mixOne(head, acc[off*2:], frames-off, vl, vr)
				if off+w > maxN {
					maxN = off + w
				}
				w2, dropNext := mixOne(next, acc[off*2:], frames-off, vl, vr)
				if off+w2 > maxN {
					maxN = off + w2
				}
				if dropNext {
					s.bufs = append(s.bufs[:1], s.bufs[2:]...)
				}
				if drop {
					s.bufs = s.bufs[1:]
				}
				break
			}
			w, drop := mixOne(head, acc[off*2:], frames-off, vl, vr)
			off += w
			if off > maxN {
				maxN = off
			}
			if !drop {
				break
			}
			s.bufs = s.bufs[1:]
		}
		_ = ci
	}
	for i := range acc {
		out[i] = softClip(acc[i])
		if a.Hidden {
			out[i] = 0
		}
	}
	// Update notifications: running -> stopped.
	var msg []byte
	for ch := 0; ch < apChans; ch++ {
		run := a.runningLocked(ch)
		if a.notify[ch] && a.lastRun[ch] && !run {
			msg = append(msg, fmt.Sprintf("\x1b[=7;%d;0n", ch)...)
			a.notify[ch] = false
		}
		a.lastRun[ch] = run
	}
	return maxN, msg
}

func (a *AudioAPC) runningLocked(ch int) bool {
	s := a.streams[ch]
	return s != nil && len(s.bufs) > 0
}

// ----- driver -------------------------------------------------------------

func (a *AudioAPC) kickLocked() {
	if a.running {
		return
	}
	a.running = true
	go a.drive()
}

func (a *AudioAPC) drive() {
	var sink audioSink
	if a.sinkOpen != nil && !a.Muted {
		sink = a.sinkOpen()
	}
	buf := make([]int16, apPull*2)
	bytes := make([]byte, apPull*4)
	var idleSince time.Time
	next := time.Now()
	for {
		n, msg := a.Pull(buf, apPull)
		if len(msg) > 0 && a.Send != nil {
			a.Send(msg)
		}
		if n == 0 {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if time.Since(idleSince) > 1500*time.Millisecond {
				a.mu.Lock()
				busy := false
				for ch := 0; ch < apChans; ch++ {
					if a.runningLocked(ch) {
						busy = true
					}
				}
				if !busy {
					a.running = false
					a.mu.Unlock()
					if sink != nil {
						sink.Close()
					}
					return
				}
				a.mu.Unlock()
			}
		} else {
			idleSince = time.Time{}
		}
		if sink != nil {
			for i, v := range buf {
				bytes[i*2] = byte(uint16(v))
				bytes[i*2+1] = byte(uint16(v) >> 8)
			}
			if err := sink.Write(bytes); err != nil {
				sink.Close()
				sink = nil
				next = time.Now()
			}
			continue
		}
		next = next.Add(time.Duration(apPull) * time.Second / apRate)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		} else if d < -time.Second {
			next = time.Now()
		}
	}
}

// ----- stream/channel plumbing ----------------------------------------------

func (a *AudioAPC) stream(ch int) *apStream {
	if ch < apFirstCh || ch >= apChans {
		return nil
	}
	if a.streams[ch] == nil {
		a.streams[ch] = &apStream{volL: apBaseDB, volR: apBaseDB}
	}
	return a.streams[ch]
}

// Idle reports whether a channel has nothing queued.
func (a *AudioAPC) Idle(ch int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ch < 0 || ch >= apChans {
		return true
	}
	return !a.runningLocked(ch)
}

// WaitIdle blocks until channel ch drains (not for a looping head) or stop
// is closed. Used to implement the Wait verb.
func (a *AudioAPC) WaitIdle(ch int, stop <-chan struct{}) {
	for {
		a.mu.Lock()
		s := a.streams[ch]
		wait := ch >= apFirstCh && ch < apChans && s != nil && len(s.bufs) > 0
		if wait && s.bufs[0].loop && s.bufs[0].overlayTotal == 0 {
			wait = false
		}
		a.mu.Unlock()
		if !wait {
			return
		}
		select {
		case <-stop:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// ----- verbs ------------------------------------------------------------------

// Handle processes the text after "SyncTERM:A;". It returns a channel
// number (>=0) when the parser should pause for a Wait, else -1.
func (a *AudioAPC) Handle(str string) (waitCh int) {
	waitCh = -1
	verb, rest := str, ""
	if i := strings.IndexByte(str, ';'); i >= 0 {
		verb, rest = str[:i], str[i:]
	}
	switch verb {
	case "LoadBlob":
		a.doLoadBlob(rest)
	case "Load":
		a.doLoad(rest)
	case "Synth":
		a.doSynth(rest)
	case "Copy":
		a.doCopy(rest)
	case "Queue":
		a.doQueue(rest)
	case "Flush":
		a.doFlush(rest)
	case "Volume":
		a.doVolume(rest)
	case "Wait":
		for _, f := range apcArgs(rest) {
			if strings.HasPrefix(f, "C=") {
				if c := ulongPrefix(f[2:]); c < apChans && c >= apFirstCh {
					return int(c)
				}
			}
		}
	case "Update":
		for _, f := range apcArgs(rest) {
			if strings.HasPrefix(f, "C=") {
				if c := ulongPrefix(f[2:]); c < apChans {
					a.mu.Lock()
					a.notify[c] = true
					a.lastRun[c] = a.runningLocked(int(c))
					a.kickLocked()
					a.mu.Unlock()
				}
			}
		}
	}
	return
}

// slotAndRest consumes leading "S=n" fields as SyncTERM does: the first
// field that isn't S= is the payload (filename / base64).
func slotAndRest(rest string) (slot uint64, payload string, ok bool) {
	slot = apSlots
	p := rest
	for strings.HasPrefix(p, ";") {
		if strings.HasPrefix(p[1:], "S=") {
			slot = ulongPrefix(p[3:])
			if i := strings.IndexByte(p[1:], ';'); i >= 0 {
				p = p[1+i:]
				continue
			}
			return slot, "", false
		}
		break
	}
	if slot >= apSlots || !strings.HasPrefix(p, ";") {
		return slot, "", false
	}
	return slot, p[1:], true
}

func (a *AudioAPC) setPatch(slot uint64, f []int16) {
	a.mu.Lock()
	a.patches[slot] = f
	a.mu.Unlock()
}

func (a *AudioAPC) doLoad(rest string) {
	slot, name, ok := slotAndRest(rest)
	if !ok || a.CacheDir == "" {
		return
	}
	clean := cleanCachePath(name)
	if clean == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(a.CacheDir, clean))
	if err != nil {
		return
	}
	if f, ok := decodeAudio(data); ok {
		a.setPatch(slot, f)
	}
}

func (a *AudioAPC) doLoadBlob(rest string) {
	slot, b64, ok := slotAndRest(rest)
	if !ok {
		return
	}
	blob, ok := decodeB64(b64)
	if !ok {
		return
	}
	if f, ok := decodeAudio(blob); ok {
		a.setPatch(slot, f)
	}
}

func (a *AudioAPC) doSynth(rest string) {
	slot := uint64(apSlots)
	shape := -1
	freq := 0.0
	n := 0
	for _, f := range apcArgs(rest) {
		switch {
		case strings.HasPrefix(f, "S="):
			slot = ulongPrefix(f[2:])
		case strings.HasPrefix(f, "W="):
			shape = parseShape(f[2:])
		case strings.HasPrefix(f, "F="):
			freq, _ = floatPrefix(f[2:])
		case strings.HasPrefix(f, "T="):
			n = parseDuration(f[2:], freq, true)
		}
	}
	if slot >= apSlots || shape < 0 && shape != shSilence || n == 0 {
		return
	}
	if n > apMaxSyn {
		n = apMaxSyn
	}
	if shape == shSilence {
		freq, shape = 0, shSine
	}
	a.setPatch(slot, makeWave(freq, n, shape))
}

func (a *AudioAPC) doCopy(rest string) {
	src, dst := uint64(apSlots), uint64(apSlots)
	for _, f := range apcArgs(rest) {
		if strings.HasPrefix(f, "S=") {
			src = ulongPrefix(f[2:])
		} else if strings.HasPrefix(f, "D=") {
			dst = ulongPrefix(f[2:])
		}
	}
	if src >= apSlots || dst >= apSlots {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.patches[src]) == 0 {
		return
	}
	a.patches[dst] = append([]int16(nil), a.patches[src]...)
}

func (a *AudioAPC) doQueue(rest string) {
	ch, slot := uint64(apChans), uint64(apSlots)
	b := &apBuf{}
	for _, f := range apcArgs(rest) {
		switch {
		case strings.HasPrefix(f, "C="):
			ch = ulongPrefix(f[2:])
		case strings.HasPrefix(f, "S="):
			slot = ulongPrefix(f[2:])
		case strings.HasPrefix(f, "I="):
			b.fadeIn = parseDuration(f[2:], 0, false)
		case strings.HasPrefix(f, "O="):
			b.fadeOut = parseDuration(f[2:], 0, false)
		case f == "X":
			b.overlayTotal = -1 // crossfade requested (resolved below)
		case f == "L":
			b.loop = true
		case strings.HasPrefix(f, "VL="):
			b.volL = parseVolume(f[3:])
		case strings.HasPrefix(f, "VR="):
			b.volR = parseVolume(f[3:])
		case strings.HasPrefix(f, "V="):
			b.volL = parseVolume(f[2:])
			b.volR = b.volL
		}
	}
	crossfade := b.overlayTotal == -1
	b.overlayTotal = 0
	if ch >= apChans || ch < apFirstCh || slot >= apSlots {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f := a.patches[slot]
	if len(f) == 0 {
		return
	}
	a.patches[slot] = nil // ownership moves to the mixer
	b.frames, b.n = f, len(f)/2
	if b.fadeIn > b.n {
		b.fadeIn = b.n
	}
	if b.fadeOut > b.n {
		b.fadeOut = b.n
	}
	if b.fadeIn+b.fadeOut > b.n {
		sum := b.fadeIn + b.fadeOut
		nf := int(uint64(b.n) * uint64(b.fadeIn) / uint64(sum))
		b.fadeOut = b.n - nf
		b.fadeIn = nf
	}
	s := a.stream(int(ch))
	if crossfade && b.fadeIn > 0 && len(s.bufs) > 0 {
		t := s.bufs[len(s.bufs)-1]
		t.overlayTotal, t.overlayElapsed = b.fadeIn, 0
	}
	s.bufs = append(s.bufs, b)
	a.kickLocked()
}

func (a *AudioAPC) doFlush(rest string) {
	ch := uint64(apChans)
	fade := 0
	have := false
	for _, f := range apcArgs(rest) {
		if strings.HasPrefix(f, "C=") {
			ch = ulongPrefix(f[2:])
		} else if strings.HasPrefix(f, "O=") {
			fade = parseDuration(f[2:], 0, false)
			have = true
		}
	}
	if ch >= apChans || ch < apFirstCh {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stream(int(ch))
	if have && fade > 0 && len(s.bufs) > 0 {
		sil := &apBuf{frames: make([]int16, fade*2), n: fade, fadeIn: fade}
		s.bufs[len(s.bufs)-1].overlayTotal = fade
		s.bufs[len(s.bufs)-1].overlayElapsed = 0
		s.bufs = append(s.bufs, sil)
		a.kickLocked()
		return
	}
	s.bufs = nil
}

func (a *AudioAPC) doVolume(rest string) {
	ch := uint64(apChans)
	var vl, vr float64
	var haveL, haveR bool
	n := 0
	for _, f := range apcArgs(rest) {
		switch {
		case strings.HasPrefix(f, "C="):
			ch = ulongPrefix(f[2:])
		case strings.HasPrefix(f, "VL="):
			vl, haveL = parseVolume(f[3:]), true
		case strings.HasPrefix(f, "VR="):
			vr, haveR = parseVolume(f[3:]), true
		case strings.HasPrefix(f, "V="):
			vl = parseVolume(f[2:])
			vr, haveL, haveR = vl, true, true
		case strings.HasPrefix(f, "T="):
			n = parseDuration(f[2:], 0, false)
		}
	}
	if ch >= apChans || ch < apFirstCh || !haveL && !haveR {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.stream(int(ch))
	if !haveL {
		vl = s.volL
	}
	if !haveR {
		vr = s.volR
	}
	if n > 0 {
		s.rampStartL, s.rampStartR = dbGain(s.volL), dbGain(s.volR)
		s.rampTargetL, s.rampTargetR = dbGain(vl), dbGain(vr)
		s.rampTgtDBL, s.rampTgtDBR = vl, vr
		s.rampElapsed, s.rampTotal = 0, n
	} else {
		s.volL, s.volR = vl, vr
		s.rampTotal = 0
	}
}

// ----- state reports ---------------------------------------------------------------

// StateReport builds the reply to CSI = 7 [; ch] n.
func (a *AudioAPC) StateReport(target int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("\x1b[=7")
	if target < 0 {
		for i := 0; i < apChans; i++ {
			if a.runningLocked(i) {
				fmt.Fprintf(&sb, ";%d;1", i)
			}
		}
	} else if target < apChans {
		st := 0
		if a.runningLocked(target) {
			st = 1
		}
		fmt.Fprintf(&sb, ";%d;%d", target, st)
	}
	sb.WriteString("n")
	return sb.String()
}

// ----- misc helpers -----------------------------------------------------------------

func decodeB64(s string) ([]byte, bool) {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\r' || c == '\n' || c == ' ' || c == '\t' {
			continue
		}
		sb.WriteByte(c)
	}
	t := strings.TrimRight(sb.String(), "=")
	b, err := base64.RawStdEncoding.DecodeString(t)
	if err != nil {
		return nil, false
	}
	return b, true
}

// cleanCachePath makes a BBS-supplied name safe to join under the cache
// directory: relative, forward-slash, no "." / ".." elements.
func cleanCachePath(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	var parts []string
	for _, e := range strings.Split(name, "/") {
		if e == "" || e == "." {
			continue
		}
		if e == ".." || strings.ContainsAny(e, ":\x00") {
			return ""
		}
		parts = append(parts, e)
	}
	if len(parts) == 0 {
		return ""
	}
	return filepath.Join(parts...)
}

// cacheStore implements SyncTERM:C;S;<name>;<base64>.
func (a *AudioAPC) cacheStore(rest string) {
	if a.CacheDir == "" {
		return
	}
	i := strings.IndexByte(rest, ';')
	if i < 0 {
		return
	}
	clean := cleanCachePath(rest[:i])
	if clean == "" {
		return
	}
	data, ok := decodeB64(rest[i+1:])
	if !ok {
		return
	}
	fn := filepath.Join(a.CacheDir, clean)
	if os.MkdirAll(filepath.Dir(fn), 0o755) != nil {
		return
	}
	os.WriteFile(fn, data, 0o644)
}

// cacheDirFor returns the per-host cache directory.
func cacheDirFor(host string, port int) string {
	d, err := os.UserCacheDir()
	if err != nil || d == "" {
		d = os.TempDir()
	}
	h := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, host)
	return filepath.Join(d, "GoTerm", "cache", fmt.Sprintf("%s_%d", h, port))
}

// SetHidden mutes or unmutes output (background tab).
func (a *AudioAPC) SetHidden(h bool) {
	a.mu.Lock()
	a.Hidden = h
	a.mu.Unlock()
}
