//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A fake "paplay" on PATH proves the pipe sink streams raw PCM to a player.
func TestPipeSinkFakePlayer(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "pcm.raw")
	script := "#!/bin/sh\nexec cat > " + capture + "\n"
	if err := os.WriteFile(filepath.Join(dir, "paplay"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	a := NewAudioAPC()
	a.sinkOpen = platformAudioSink()
	a.Handle("Synth;S=0;W=SQ;F=300;T=100")
	a.Handle("Queue;C=2;S=0")
	time.Sleep(400 * time.Millisecond)
	a.Reset()
	st, err := os.Stat(capture)
	if err != nil || st.Size() < 100*apRate/1000*4 {
		t.Fatalf("player received too little: %v %v", st, err)
	}
}
