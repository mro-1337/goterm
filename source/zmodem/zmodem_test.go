package zmodem

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ch := make(chan net.Conn, 1)
	go func() { c, _ := l.Accept(); ch <- c }()
	a, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return a, <-ch
}

func makeFiles(t *testing.T, dir string) map[string][]byte {
	files := map[string][]byte{}
	sizes := []int{0, 1, 1023, 1024, 1025, 70000, 1 << 20}
	for _, n := range sizes {
		b := make([]byte, n)
		rand.Read(b)
		files[fmt.Sprintf("rand%d.bin", n)] = b
	}
	// every byte value, repeatedly, plus CAN runs and "@\r" patterns
	var all []byte
	for i := 0; i < 40; i++ {
		for v := 0; v < 256; v++ {
			all = append(all, byte(v))
		}
	}
	all = append(all, []byte("@\r@\r\x18\x18\x18\x18\x18\x18\x18**\x18B00")...)
	files["allbytes.bin"] = all
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestLoopback(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	files := makeFiles(t, src)
	var paths []string
	for name := range files {
		paths = append(paths, filepath.Join(src, name))
	}
	a, b := tcpPair(t)
	defer a.Close()
	defer b.Close()

	recvDone := make(chan error, 1)
	var rres Result
	go func() {
		var err error
		rres, err = Receive(b, nil, dst, &Hooks{})
		recvDone <- err
	}()
	// Receiver speaks first (ZRINIT); sender detects it like the app does.
	sres, err := Send(a, nil, paths, &Hooks{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := <-recvDone; err != nil {
		t.Fatalf("recv: %v", err)
	}
	if len(sres.Files) != len(paths) || len(rres.Files) != len(paths) {
		t.Fatalf("file counts: sent %d recv %d want %d", len(sres.Files), len(rres.Files), len(paths))
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: content mismatch (%d vs %d bytes)", name, len(got), len(want))
		}
	}
}

type pipeLink struct {
	r *os.File
	w *os.File
}

func (p *pipeLink) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if os.Getenv("ZDEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[rx %q err=%v]\n", b[:n], err)
	}
	return n, err
}
func (p *pipeLink) Write(b []byte) (int, error) {
	if os.Getenv("ZDEBUG") != "" {
		n := len(b)
		if n > 80 {
			n = 80
		}
		fmt.Fprintf(os.Stderr, "[tx %d bytes %q]\n", len(b), b[:n])
	}
	return p.w.Write(b)
}
func (p *pipeLink) SetReadDeadline(t time.Time) error { return p.r.SetReadDeadline(t) }

func runTool(t *testing.T, dir string, name string, args ...string) (*pipeLink, *exec.Cmd) {
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	outR, outW, _ := os.Pipe() // tool stdout -> us
	inR, inW, _ := os.Pipe()   // us -> tool stdin
	cmd := exec.Command(path, args...)
	cmd.Dir = dir
	cmd.Stdin = inR
	cmd.Stdout = outW
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	outW.Close()
	inR.Close()
	return &pipeLink{r: outR, w: inW}, cmd
}

func TestReceiveFromSZ(t *testing.T) {
	for _, flags := range [][]string{{}, {"-e"}, {"-b"}, {"-w", "4096"}, {"-8"}} {
		src, dst := t.TempDir(), t.TempDir()
		files := makeFiles(t, src)
		args := append([]string{}, flags...)
		var names []string
		for n := range files {
			names = append(names, n)
		}
		args = append(args, names...)
		link, cmd := runTool(t, src, "sz", args...)
		res, err := Receive(link, nil, dst, &Hooks{})
		link.w.Close()
		cmd.Wait()
		if err != nil {
			t.Fatalf("flags %v: receive: %v", flags, err)
		}
		if len(res.Files) != len(files) {
			t.Fatalf("flags %v: got %d files want %d", flags, len(res.Files), len(files))
		}
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(dst, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("flags %v: %s mismatch (err=%v, %d vs %d)", flags, name, err, len(got), len(want))
			}
		}
	}
}

func TestSendToRZ(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	files := makeFiles(t, src)
	var paths []string
	for n := range files {
		paths = append(paths, filepath.Join(src, n))
	}
	link, cmd := runTool(t, dst, "rz", "-b", "-e", "-y")
	res, err := Send(link, nil, paths, &Hooks{})
	link.w.Close()
	cmd.Wait()
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(res.Files) != len(paths) {
		t.Fatalf("sent %d want %d", len(res.Files), len(paths))
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s mismatch (err=%v, %d vs %d)", name, err, len(got), len(want))
		}
	}
}

func TestDetect(t *testing.T) {
	hdr := encodeHex(ZRQINIT, [4]byte{})
	buf := append([]byte("rz\r"), hdr...)
	i, ft, ok := Detect(buf)
	if i != 3 || ft != ZRQINIT || !ok {
		t.Errorf("Detect = %d %d %v", i, ft, ok)
	}
	if i, _, ok := Detect([]byte("hello ***")); i != -1 && ok {
		t.Errorf("false positive")
	}
	if _, _, ok := Detect(hdr[:3]); ok {
		t.Errorf("partial header reported complete")
	}
}
