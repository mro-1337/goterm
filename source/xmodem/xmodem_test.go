package xmodem

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type pipeLink struct{ r, w *os.File }

func (p *pipeLink) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeLink) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeLink) SetReadDeadline(t time.Time) error {
	return p.r.SetReadDeadline(t)
}

func runTool(t *testing.T, dir, name string, args ...string) (*pipeLink, *exec.Cmd) {
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	outR, outW, _ := os.Pipe()
	inR, inW, _ := os.Pipe()
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Stdin, cmd.Stdout = dir, inR, outW
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	outW.Close()
	inR.Close()
	return &pipeLink{outR, inW}, cmd
}

func mk(t *testing.T, dir, name string, n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	os.WriteFile(filepath.Join(dir, name), b, 0o644)
	return b
}

func TestYmodemReceiveFromSB(t *testing.T) {
	for _, tool := range [][]string{{"sb"}, {"sb", "--ymodem"}} {
		src, dst := t.TempDir(), t.TempDir()
		a := mk(t, src, "a.bin", 5000)
		b := mk(t, src, "b.bin", 129)
		link, cmd := runTool(t, src, tool[0], "a.bin", "b.bin")
		res, err := Receive(link, Ymodem, dst, "", &Hooks{})
		link.w.Close()
		cmd.Wait()
		if err != nil || len(res.Files) != 2 {
			t.Fatalf("%v: %v %v", tool, res.Files, err)
		}
		ga, _ := os.ReadFile(filepath.Join(dst, "a.bin"))
		gb, _ := os.ReadFile(filepath.Join(dst, "b.bin"))
		if !bytes.Equal(ga, a) || !bytes.Equal(gb, b) {
			t.Fatal("content mismatch")
		}
	}
}

func TestYmodemSendToRB(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	a := mk(t, src, "a.bin", 5000)
	b := mk(t, src, "b.bin", 1)
	link, cmd := runTool(t, dst, "rb")
	_, err := Send(link, Ymodem, []string{filepath.Join(src, "a.bin"), filepath.Join(src, "b.bin")}, &Hooks{})
	link.w.Close()
	cmd.Wait()
	if err != nil {
		t.Fatal(err)
	}
	ga, _ := os.ReadFile(filepath.Join(dst, "a.bin"))
	gb, _ := os.ReadFile(filepath.Join(dst, "b.bin"))
	if !bytes.Equal(ga, a) || !bytes.Equal(gb, b) {
		t.Fatalf("mismatch %d %d", len(ga), len(gb))
	}
}

func TestXmodemReceiveFromSX(t *testing.T) {
	for _, flags := range [][]string{{}, {"-k"}} {
		for _, mode := range []Mode{XmodemCRC, XmodemChecksum} {
			src, dst := t.TempDir(), t.TempDir()
			a := mk(t, src, "a.bin", 3000)
			link, cmd := runTool(t, src, "sx", append(flags, "a.bin")...)
			_, err := Receive(link, mode, dst, "a.bin", &Hooks{})
			link.w.Close()
			cmd.Wait()
			if err != nil {
				t.Fatalf("%v %v: %v", flags, mode, err)
			}
			got, _ := os.ReadFile(filepath.Join(dst, "a.bin"))
			// plain XMODEM pads to a block boundary with ^Z
			if !bytes.HasPrefix(got, a) || len(got) > len(a)+1024 {
				t.Fatalf("%v %v mismatch %d vs %d", flags, mode, len(got), len(a))
			}
		}
	}
}

func TestXmodemSendToRX(t *testing.T) {
	for _, mode := range []Mode{XmodemCRC, Xmodem1K} {
		src, dst := t.TempDir(), t.TempDir()
		a := mk(t, src, "a.bin", 3000)
		link, cmd := runTool(t, dst, "rx", "out.bin")
		_, err := Send(link, mode, []string{filepath.Join(src, "a.bin")}, &Hooks{})
		link.w.Close()
		cmd.Wait()
		if err != nil {
			t.Fatalf("%v: %v", mode, err)
		}
		got, _ := os.ReadFile(filepath.Join(dst, "out.bin"))
		if !bytes.HasPrefix(got, a) || len(got) > len(a)+1024 {
			t.Fatalf("%v mismatch %d vs %d", mode, len(got), len(a))
		}
	}
}
