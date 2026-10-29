package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testWAV(rate, ch int, samples []int16) []byte {
	var b bytes.Buffer
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(s))
	}
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(ch))
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*ch*2))
	binary.Write(&b, binary.LittleEndian, uint16(ch*2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

type apTest struct {
	p   *Parser
	a   *AudioAPC
	out bytes.Buffer
}

func newAPTest(t *testing.T) *apTest {
	t.Helper()
	x := &apTest{a: NewAudioAPC()}
	x.a.CacheDir = t.TempDir()
	x.p = NewParser(NewScreen(80, 25, 8, 16))
	x.p.Audio = x.a
	x.p.Reply = func(b []byte) { x.out.Write(b) }
	return x
}

func (x *apTest) apc(s string) { x.p.Feed([]byte("\x1b_" + s + "\x1b\\")) }

func (x *apTest) take() string { s := x.out.String(); x.out.Reset(); return s }

func TestAPCFeatureQueries(t *testing.T) {
	x := newAPTest(t)
	x.apc("SyncTERM:Q;libsndfile")
	if g := x.take(); g != "\x1b[=7;100;1n" {
		t.Fatalf("libsndfile reply %q", g)
	}
	x.apc("SyncTERM:Q;libsndfileFormat;1;2")
	if g := x.take(); g != "\x1b[=7;101;1;2;1n" {
		t.Fatalf("wav pcm16 reply %q", g)
	}
	x.apc("SyncTERM:Q;libsndfileFormat;32;96")
	if g := x.take(); g != "\x1b[=7;101;32;96;1n" {
		t.Fatalf("vorbis reply %q", g)
	}
	x.apc("SyncTERM:Q;libsndfileFormat;35;130")
	if g := x.take(); g != "\x1b[=7;101;35;130;1n" {
		t.Fatalf("mp3 reply %q", g)
	}
	x.apc("SyncTERM:Q;libsndfileFormat;0x20;0x64")
	if g := x.take(); g != "\x1b[=7;101;32;100;0n" {
		t.Fatalf("opus reply %q", g)
	}
	x.apc("SyncTERM:VER")
	if g := x.take(); !strings.HasPrefix(g, "\x1b_SyncTERM:VER;SyncTERM ") || !strings.HasSuffix(g, "\x1b\\") {
		t.Fatalf("ver reply %q", g)
	}
	x.apc("SyncTERM:Q;JXL")
	if g := x.take(); g != "\x1b[=1;0-n" {
		t.Fatalf("jxl reply %q", g)
	}
}

func TestSynthShapes(t *testing.T) {
	for _, sh := range []int{shSine, shSaw, shSquare, shSineSaw, shSineHarm, shSineSawChord, shSineSawHarm} {
		w := makeWave(440, 4410, sh)
		if len(w) != 8820 {
			t.Fatalf("shape %d len %d", sh, len(w))
		}
		nz := 0
		for i := 0; i < 4410; i++ {
			if w[i*2] != w[i*2+1] {
				t.Fatalf("shape %d L!=R", sh)
			}
			if w[i*2] != 0 {
				nz++
			}
		}
		if nz < 1000 {
			t.Errorf("shape %d mostly silent (%d)", sh, nz)
		}
	}
	// A clean sine: peak near full scale, edges ramped towards zero.
	w := makeWave(440, 44100, shSine)
	var peak int16
	for i := 0; i < len(w); i += 2 {
		if w[i] > peak {
			peak = w[i]
		}
	}
	if peak < 32000 {
		t.Errorf("sine peak %d", peak)
	}
	if abs16(w[0]) > 100 || abs16(w[2]) > 2000 {
		t.Errorf("sine start not ramped: %d %d", w[0], w[2])
	}
	if s := makeWave(0, 100, shSine); len(s) != 200 || s[10] != 0 {
		t.Error("silence")
	}
}

func abs16(v int16) int {
	if v < 0 {
		return -int(v)
	}
	return int(v)
}

func TestSynthAndQueueState(t *testing.T) {
	x := newAPTest(t)
	x.apc("SyncTERM:A;Synth;S=3;W=SIN;F=440;T=200")
	if len(x.a.patches[3]) != 200*apRate/1000*2 {
		t.Fatalf("patch frames %d", len(x.a.patches[3])/2)
	}
	x.apc("SyncTERM:A;Copy;S=3;D=4")
	if len(x.a.patches[4]) != len(x.a.patches[3]) {
		t.Fatal("copy")
	}
	x.p.Feed([]byte("\x1b[=7;2n"))
	if g := x.take(); g != "\x1b[=7;2;0n" {
		t.Fatalf("idle state %q", g)
	}
	x.apc("SyncTERM:A;Queue;C=2;S=3")
	if len(x.a.patches[3]) != 0 {
		t.Fatal("slot should be consumed by Queue")
	}
	x.p.Feed([]byte("\x1b[=7n"))
	if g := x.take(); g != "\x1b[=7;2;1n" {
		t.Fatalf("running state %q", g)
	}
	x.apc("SyncTERM:A;Update;C=2")
	out := make([]int16, apPull*2)
	var msg []byte
	total := 0
	for i := 0; i < 20 && len(msg) == 0; i++ {
		n, m := x.a.Pull(out, apPull)
		total += n
		msg = append(msg, m...)
	}
	if string(msg) != "\x1b[=7;2;0n" {
		t.Fatalf("update notification %q", msg)
	}
	if total != 200*apRate/1000 {
		t.Fatalf("played %d frames", total)
	}
	if !x.a.Idle(2) {
		t.Fatal("not idle")
	}
	// Channels 0/1 belong to the terminal: Queue is rejected.
	x.apc("SyncTERM:A;Queue;C=1;S=4")
	if !x.a.Idle(1) || len(x.a.patches[4]) == 0 {
		t.Fatal("queue on ch1 must be ignored")
	}
	x.a.Reset()
}

func TestLoadBlobAndResample(t *testing.T) {
	x := newAPTest(t)
	src := make([]int16, 22050) // 1 s mono @ 22.05 kHz
	for i := range src {
		src[i] = int16(10000 * math.Sin(float64(i)/10))
	}
	blob := base64.StdEncoding.EncodeToString(testWAV(22050, 1, src))
	x.apc("SyncTERM:A;LoadBlob;S=7;" + blob)
	p := x.a.patches[7]
	if len(p) != 2*44100 {
		t.Fatalf("resampled to %d frames", len(p)/2)
	}
	if p[0] != p[1] || p[2000] != p[2001] {
		t.Fatal("mono not duplicated")
	}
	// bad slot / garbage ignored
	x.apc("SyncTERM:A;LoadBlob;S=999;" + blob)
	x.apc("SyncTERM:A;LoadBlob;S=8;!!notbase64!!")
	if len(x.a.patches[8]) != 0 {
		t.Fatal("garbage loaded")
	}
}

func TestCacheStoreAndLoad(t *testing.T) {
	x := newAPTest(t)
	w := testWAV(44100, 2, []int16{1, -1, 2, -2, 3, -3, 4, -4})
	x.apc("SyncTERM:C;S;snd/beep.wav;" + base64.StdEncoding.EncodeToString(w))
	if _, err := os.Stat(filepath.Join(x.a.CacheDir, "snd", "beep.wav")); err != nil {
		t.Fatal("not stored:", err)
	}
	x.apc("SyncTERM:C;S;../evil.wav;" + base64.StdEncoding.EncodeToString(w))
	if _, err := os.Stat(filepath.Join(filepath.Dir(x.a.CacheDir), "evil.wav")); err == nil {
		t.Fatal("path traversal")
	}
	x.apc("SyncTERM:A;Load;S=1;snd/beep.wav")
	if len(x.a.patches[1]) != 8 || x.a.patches[1][6] != 4 {
		t.Fatalf("load: %v", x.a.patches[1])
	}
	x.apc("SyncTERM:C;L;snd/*.wav")
	if g := x.take(); !strings.HasPrefix(g, "\x1b_SyncTERM:C;L\nbeep.wav\t") || !strings.HasSuffix(g, "\n\x1b\\") {
		t.Fatalf("cache list %q", g)
	}
}

func TestFadeAndCrossfade(t *testing.T) {
	a := NewAudioAPC()
	loud := make([]int16, 2*4410)
	for i := range loud {
		loud[i] = 20000
	}
	a.patches[1] = append([]int16(nil), loud...)
	a.Handle("Queue;C=2;S=1;I=50;V=100")
	out := make([]int16, 4410*2)
	a.Pull(out, 4410)
	if abs16(out[0]) > 5 {
		t.Errorf("fade-in start %d", out[0])
	}
	if out[2*2200] <= out[2*100] {
		t.Errorf("fade-in not rising")
	}
	// -12 dB base: steady-state level ~ 20000 * 0.251 = 5019
	if v := int(out[2*4000]); v < 4500 || v > 5500 {
		t.Errorf("steady level %d", v)
	}
	// Crossfade: queue another with X;I=: tail starts decaying.
	a.patches[1] = append([]int16(nil), loud...)
	a.Handle("Queue;C=2;S=1")
	a.patches[2] = append([]int16(nil), loud...)
	a.Handle("Queue;C=2;S=2;I=100;X")
	if a.streams[2].bufs[0].overlayTotal == 0 {
		t.Error("crossfade overlay not set")
	}
	// Flush clears.
	a.Handle("Flush;C=2")
	if !a.Idle(2) {
		t.Error("flush")
	}
}

func TestVolumeRampAndLoopWait(t *testing.T) {
	a := NewAudioAPC()
	a.patches[1] = makeWave(220, 4410, shSquare)
	a.Handle("Queue;C=3;S=1;L")
	if ch := a.Handle("Wait;C=3"); ch != 3 {
		t.Fatalf("wait ch %d", ch)
	}
	done := make(chan struct{})
	go func() { a.WaitIdle(3, nil); close(done) }()
	select {
	case <-done: // looping head: Wait is a no-op
	case <-time.After(time.Second):
		t.Fatal("wait on loop blocked")
	}
	a.Handle("Volume;C=3;V=50;T=100")
	out := make([]int16, 4410*2)
	for i := 0; i < 6; i++ {
		a.Pull(out, 4410)
	}
	if v := a.streams[3].volL; math.Abs(v-20*math.Log10(0.5)) > 0.01 {
		t.Errorf("ramp end volume %.2f dB", v)
	}
	a.Handle("Flush;C=3")
}

func TestParserWaitAndFeed(t *testing.T) {
	x := newAPTest(t)
	x.apc("SyncTERM:A;Synth;S=1;W=SQ;F=300;T=40")
	x.apc("SyncTERM:A;Queue;C=4;S=1")
	n := x.p.Feed([]byte("\x1b_SyncTERM:A;Wait;C=4\x1b\\after"))
	if x.p.WaitCh != 4 || n != len("\x1b_SyncTERM:A;Wait;C=4\x1b\\") {
		t.Fatalf("wait not requested: ch=%d n=%d", x.p.WaitCh, n)
	}
	x.p.WaitCh = 0
	x.a.Reset()
}

type fakeSink struct {
	mu     sync.Mutex
	bytes  int
	closed bool
}

func (f *fakeSink) Write(p []byte) error {
	f.mu.Lock()
	f.bytes += len(p)
	f.mu.Unlock()
	time.Sleep(time.Millisecond)
	return nil
}
func (f *fakeSink) Close() { f.mu.Lock(); f.closed = true; f.mu.Unlock() }

func TestDriverPlaysAndStops(t *testing.T) {
	a := NewAudioAPC()
	fs := &fakeSink{}
	a.sinkOpen = func() audioSink { return fs }
	a.Handle("Synth;S=0;W=SIN;F=500;T=100")
	a.Handle("Queue;C=2;S=0")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fs.mu.Lock()
		c := fs.closed
		fs.mu.Unlock()
		if c {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if !fs.closed || fs.bytes < 100*apRate/1000*4 {
		t.Fatalf("driver: closed=%v bytes=%d", fs.closed, fs.bytes)
	}
}

func TestOtherFormats(t *testing.T) {
	// 8-bit unsigned WAV
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+4))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, []uint32{16})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&b, binary.LittleEndian, []uint32{44100, 44100})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 8})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(4))
	b.Write([]byte{128, 255, 0, 128})
	f, ok := decodeAudio(b.Bytes())
	if !ok || len(f) != 8 || f[2] != 127<<8 || f[4] != -128<<8 {
		t.Fatalf("u8 wav: %v %v", ok, f)
	}
	// AU 16-bit BE
	var au bytes.Buffer
	au.WriteString(".snd")
	binary.Write(&au, binary.BigEndian, []uint32{24, 4, 3, 44100, 1})
	binary.Write(&au, binary.BigEndian, []int16{1000, -1000})
	f, ok = decodeAudio(au.Bytes())
	if !ok || len(f) != 4 || f[0] != 1000 || f[2] != -1000 {
		t.Fatalf("au: %v %v", ok, f)
	}
	if _, ok := decodeAudio([]byte("OggS\x00\x02garbagegarbage")); ok {
		t.Fatal("ogg must be rejected")
	}
	if cleanCachePath("a/../b") != "" || cleanCachePath("/x/y.wav") != filepath.Join("x", "y.wav") {
		t.Fatal("cleanCachePath")
	}
}
