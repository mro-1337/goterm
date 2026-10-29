package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type shell struct {
	in  io.Writer
	out *bufio.Reader
}

func (s *shell) send(typ byte, p []byte) {
	h := make([]byte, 5)
	h[0] = typ
	binary.BigEndian.PutUint32(h[1:], uint32(len(p)))
	s.in.Write(append(h, p...))
}

func (s *shell) recv(t *testing.T) (byte, []byte) {
	h := make([]byte, 5)
	if _, err := io.ReadFull(s.out, h); err != nil {
		t.Fatal(err)
	}
	p := make([]byte, binary.BigEndian.Uint32(h[1:]))
	io.ReadFull(s.out, p)
	return h[0], p
}

// next reads messages until one of type want arrives.
func (s *shell) next(t *testing.T, want byte, d time.Duration) []byte {
	type m struct {
		t byte
		p []byte
	}
	ch := make(chan m, 1)
	go func() {
		for {
			ty, p := s.recv(t)
			if ty == want {
				ch <- m{ty, p}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		return r.p
	case <-time.After(d):
		t.Fatalf("no message type %d", want)
		return nil
	}
}

func TestEngineProtocol(t *testing.T) {
	defer func() { sinkOverride, engineOut = nil, nil }()
	cfg := newDefaultConfig()
	a := NewApp(cfg)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go runEngine(a, inR, outW)
	sh := &shell{in: inW, out: bufio.NewReader(outR)}

	f := sh.next(t, eFrame, 3*time.Second)
	w, h := int(binary.BigEndian.Uint32(f)), int(binary.BigEndian.Uint32(f[4:]))
	if w != 640 || h != 400 || len(f) != 8+w*h*4 {
		t.Fatalf("frame %dx%d len %d", w, h, len(f))
	}
	first := append([]byte(nil), f[8:]...)
	// F1 opens the help modal -> a different frame arrives.
	sh.send(iKey, []byte{15, 0, 0, 0, 0, 0})
	f2 := sh.next(t, eFrame, 3*time.Second)
	same := true
	for i := range first {
		if first[i] != f2[8+i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("frame did not change after F1")
	}
	// A tap on the Cancel path: Esc closes help.
	sh.send(iKey, []byte{12, 0, 0, 0, 0, 0})
	sh.next(t, eFrame, 3*time.Second)
	// Quick connect via soft-keyboard text path: Alt+Q then Esc leaves the app alive.
	sh.send(iKey, []byte{0, 0, 0, 0, 'q', 2})
	sh.next(t, eFrame, 3*time.Second)
	sh.send(iText, []byte("x"))
	sh.send(iKey, []byte{12, 0, 0, 0, 0, 0})
	// Clipboard round trip: Alt+V while no session does nothing, so test the platform hook directly.
	got := make(chan string, 1)
	go func() { got <- plat.Clipboard() }()
	sh.next(t, eClipReq, 2*time.Second)
	sh.send(iClipReply, []byte("hello"))
	if g := <-got; g != "hello" {
		t.Fatalf("clipboard %q", g)
	}
	// Audio sink writes PCM messages.
	sink := sinkOverride()
	go sink.Write(make([]byte, 4096))
	if p := sh.next(t, ePCM, 2*time.Second); len(p) != 4096 {
		t.Fatalf("pcm len %d", len(p))
	}
	// Quit message.
	go plat.Quit()
	sh.next(t, eQuit, 2*time.Second)
	inW.Close()
}

func TestDNSSetup(t *testing.T) {
	old := net.DefaultResolver
	defer func() { net.DefaultResolver = old }()
	setupDNS("1.2.3.4, [2001:db8::1] ,8.8.8.8:5353")
	if r := defaultResolverPreferGo(); !r {
		t.Fatal("resolver not replaced")
	}
}

func TestEngineConnect(t *testing.T) {
	defer func() { sinkOverride, engineOut = nil, nil }()
	a := NewApp(newDefaultConfig())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go runEngine(a, inR, outW)
	sh := &shell{in: inW, out: bufio.NewReader(outR)}
	sh.next(t, eFrame, 3*time.Second)
	got := make(chan struct{})
	h, p := fakeBBS(t, func(c net.Conn) { c.Write([]byte("hi from bbs")); close(got); time.Sleep(300 * time.Millisecond) })
	sh.send(iConnect, []byte(fmt.Sprintf("%s:%d", h, p)))
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("not connected")
	}
	inW.Close()
}
