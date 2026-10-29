package main

import (
	"math"
	"os"
	"testing"
)

// Files made with ffmpeg from a 1 s 440 Hz stereo sine (right at half level).
func TestCompressedFormats(t *testing.T) {
	dir := "testdata/audio"
	for _, name := range []string{"t.flac", "t24.flac", "t.ogg", "t.mp3"} {
		b, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := decodeAudio(b)
		if !ok {
			t.Fatalf("%s: decode failed", name)
		}
		n := len(f) / 2
		if n < 40000 || n > 48000 {
			t.Errorf("%s: %d frames", name, n)
		}
		var pl, pr float64
		for i := 20000; i < 22000; i++ {
			pl = math.Max(pl, math.Abs(float64(f[i*2])))
			pr = math.Max(pr, math.Abs(float64(f[i*2+1])))
		}
		t.Logf("%s: frames=%d peakL=%.0f peakR=%.0f", name, n, pl, pr)
		if pl < 3000 || pr < 1000 || math.Abs(pr/pl-0.5) > 0.1 {
			t.Errorf("%s: wrong levels L=%.0f R=%.0f", name, pl, pr)
		}
	}
	// Exactness for lossless: compare against the WAV reference.
	wb, _ := os.ReadFile(dir + "/ref.wav")
	ref, _ := decodeAudio(wb)
	fb, _ := os.ReadFile(dir + "/t.flac")
	fl, _ := decodeAudio(fb)
	if len(ref) != len(fl) {
		t.Fatalf("flac length %d vs %d", len(fl), len(ref))
	}
	for i := range ref {
		if ref[i] != fl[i] {
			t.Fatalf("flac sample %d differs: %d vs %d", i, fl[i], ref[i])
		}
	}
	if _, ok := decodeAudio(mustRead(t, dir+"/t.opus")); ok {
		t.Error("opus should be unsupported")
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
