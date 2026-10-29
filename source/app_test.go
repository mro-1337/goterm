package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	clip   string
	wavs   int
	wavMu  sync.Mutex
	pngDir = os.Getenv("GT_PNGDIR")
)

func setupPlat() {
	plat = Platform{
		Clipboard:    func() string { return clip },
		SetClipboard: func(s string) { clip = s },
		PlayWAV:      func(b []byte) { wavMu.Lock(); wavs++; wavMu.Unlock() },
	}
}

func shot(t *testing.T, a *App, name string) []uint32 {
	fb, w, h := a.Frame()
	if pngDir != "" {
		if err := savePNG(filepath.Join(pngDir, name+".png"), fb, w, h); err != nil {
			t.Fatal(err)
		}
	}
	return append([]uint32(nil), fb...)
}

func key(a *App, k Key) { a.OnKey(KeyEvent{Key: k}) }
func chr(a *App, s string) {
	for _, r := range s {
		a.OnKey(KeyEvent{Ch: r})
	}
}

func fakeBBS(t *testing.T, script func(c net.Conn)) (string, int) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		script(c)
		ln.Close()
	}()
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestUIAndSession(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	cfg.DownDir = t.TempDir()
	var got bytes.Buffer
	var gmu sync.Mutex
	host, port := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte{255, 253, 24, 255, 251, 1, 255, 251, 3, 255, 250, 24, 1, 255, 240}) // DO TERMTYPE, WILL ECHO, WILL SGA, SB SEND
		c.Write(TestPatternStream())
		buf := make([]byte, 256)
		for {
			n, err := c.Read(buf)
			gmu.Lock()
			got.Write(buf[:n])
			gmu.Unlock()
			if err != nil {
				return
			}
		}
	})
	cfg.Entries = []DialEntry{{Name: "Local test BBS", Host: host, Port: port, Conn: "telnet", Mode: "80x25", Notes: "fake"}}
	a := NewApp(cfg)
	shot(t, a, "01_directory")

	// forms
	key(a, KeyF2)
	shot(t, a, "02_settings")
	key(a, KeyEsc)
	key(a, KeyF3)
	shot(t, a, "03_palette")
	a.OnKey(KeyEvent{Ch: 'p'})
	shot(t, a, "04_palette_preset")
	key(a, KeyEsc)
	key(a, KeyF4)
	shot(t, a, "05_themes")
	key(a, KeyEsc)
	a.OnKey(KeyEvent{Ch: 'e'})
	shot(t, a, "07_edit_entry")
	key(a, KeyEsc)
	key(a, KeyF1)
	shot(t, a, "08_help")
	key(a, KeyEsc)
	a.mu.Lock()
	a.modals = []Modal{a.dir}
	a.applyTheme(builtinThemes[0])
	a.mu.Unlock()
	// connect
	key(a, KeyEnter)
	waitFor(t, "pattern", 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.term.gfx != nil && a.sess != nil && !a.sess.dialing
	})
	time.Sleep(300 * time.Millisecond)
	wavMu.Lock()
	if wavs == 0 {
		t.Errorf("no music")
	}
	wavMu.Unlock()
	before := shot(t, a, "09_online")
	a.OnKey(KeyEvent{Ch: 'z', Alt: true})
	shot(t, a, "10_online_menu")
	key(a, KeyEsc)

	// typing reaches host
	chr(a, "hi")
	key(a, KeyEnter)
	key(a, KeyUp)
	waitFor(t, "host input", 2*time.Second, func() bool {
		gmu.Lock()
		defer gmu.Unlock()
		return strings.Contains(got.String(), "hi\r\x1b[A")
	})
	gmu.Lock()
	if !strings.Contains(got.String(), "ansi-bbs") {
		t.Errorf("termtype not sent: %q", got.String())
	}
	gmu.Unlock()

	// themes must not touch terminal colors
	a.mu.Lock()
	a.applyTheme(builtinThemes[2])
	a.cfg.Border = borderSingle
	a.paletteChanged()
	a.mu.Unlock()
	themed := shot(t, a, "10b_theme_online")
	for i := range before {
		if before[i] != themed[i] {
			t.Fatalf("theme changed terminal pixel %d", i)
		}
	}
	a.mu.Lock()
	a.applyTheme(builtinThemes[0])
	a.mu.Unlock()
	// palette edit recolors existing screen + UI
	a.mu.Lock()
	a.cfg.Palette[4] = RGB{0xFF, 0x80, 0x00}
	a.paletteChanged()
	a.mu.Unlock()
	after := shot(t, a, "11_online_recolored")
	diff := 0
	for i := range before {
		if before[i] != after[i] {
			diff++
		}
	}
	if diff == 0 {
		t.Errorf("palette change did not recolor the screen")
	}
	// selection copy
	a.OnMouse(MouseEvent{X: 8, Y: 16, Down: true})
	a.OnMouse(MouseEvent{X: 8 * 20, Y: 16, Move: true})
	a.OnMouse(MouseEvent{X: 8 * 20, Y: 16, Up: true})
	if !strings.Contains(clip, "GoTerm") {
		t.Errorf("copy failed: %q", clip)
	}
	shot(t, a, "12_selection")
	// paste
	clip = "pasted\r\ntext"
	a.OnKey(KeyEvent{Ch: 'v', Alt: true})
	waitFor(t, "paste", 2*time.Second, func() bool {
		gmu.Lock()
		defer gmu.Unlock()
		return strings.Contains(got.String(), "pasted\rtext")
	})
	a.OnKey(KeyEvent{Ch: 'h', Alt: true})
	key(a, KeyRight)
	key(a, KeyEnter) // "No" is default? select Yes
	shot(t, a, "13_hangup_confirm")
}

// runTelnetCmd runs cmd with its stdio attached to a telnet-style
// connection: 0xFF is doubled on output and un-doubled on input.
var fragment bool

func runTelnetCmd(c net.Conn, cmd *exec.Cmd) {
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Start()
	go func() {
		buf := make([]byte, 4096)
		iac := false
		for {
			n, err := c.Read(buf)
			var o []byte
			for _, b := range buf[:n] {
				if iac {
					iac = false
					if b == 255 {
						o = append(o, 255)
					}
					continue
				}
				if b == 255 {
					iac = true
					continue
				}
				o = append(o, b)
			}
			in.Write(o)
			if err != nil {
				in.Close()
				return
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			o := bytes.ReplaceAll(buf[:n], []byte{255}, []byte{255, 255})
			if fragment {
				for len(o) > 0 {
					k := 1 + int(o[0])%37
					if k > len(o) {
						k = len(o)
					}
					c.Write(o[:k])
					o = o[k:]
					if k%5 == 0 {
						time.Sleep(time.Millisecond)
					}
				}
			} else {
				c.Write(o)
			}
		}
		if err != nil {
			break
		}
	}
	cmd.Wait()
}

func hasTool(n string) bool { _, err := exec.LookPath(n); return err == nil }

// ZMODEM download from a host running real sz through the app.
func TestAppZmodemDownload(t *testing.T) {
	if !hasTool("sz") {
		t.Skip("no sz")
	}
	setupPlat()
	src := filepath.Join(t.TempDir(), "hello.bin")
	data := make([]byte, 200000)
	for i := range data {
		data[i] = byte(i*7 + i>>8)
	}
	os.WriteFile(src, data, 0o644)
	cfg := newDefaultConfig()
	cfg.DownDir = t.TempDir()
	cfg.path = filepath.Join(t.TempDir(), "config.json")
	host, port := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("Welcome. Sending file...\r\n"))
		cmd := exec.Command("sz", "-e", "-b", src)
		runTelnetCmd(c, cmd)
		c.Write([]byte("\r\nDone sending.\r\n"))
		time.Sleep(500 * time.Millisecond)
	})
	cfg.Entries = []DialEntry{{Name: "z", Host: host, Port: port, Conn: "telnet", Mode: "80x25"}}
	a := NewApp(cfg)
	key(a, KeyEnter)
	waitFor(t, "download", 20*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(cfg.DownDir, "hello.bin"))
		return err == nil && bytes.Equal(b, data)
	})
	waitFor(t, "done msg", 5*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return strings.Contains(a.term.AllText(), "Done sending")
	})
	shot(t, a, "14_after_download")
	if lb, err := os.ReadFile(filepath.Join(filepath.Dir(cfg.path), "zmodem.log")); err != nil || !strings.Contains(string(lb), "rx ZFILE") {
		t.Errorf("trace log missing/incomplete: %v %q", err, lb)
	}
}

// ZMODEM upload to a host running rz; started by the host's ZRINIT.
func TestAppZmodemUpload(t *testing.T) {
	if !hasTool("rz") {
		t.Skip("no rz")
	}
	setupPlat()
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "up.dat")
	data := make([]byte, 150000)
	for i := range data {
		data[i] = byte(i * 31)
	}
	os.WriteFile(src, data, 0o644)
	recv := t.TempDir()
	cfg := newDefaultConfig()
	host, port := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("Ready to receive.\r\n"))
		cmd := exec.Command("rz", "-b", "-e", "-y")
		cmd.Dir = recv
		runTelnetCmd(c, cmd)
		time.Sleep(300 * time.Millisecond)
	})
	cfg.Entries = []DialEntry{{Name: "z", Host: host, Port: port, Conn: "telnet", Mode: "80x25"}}
	a := NewApp(cfg)
	key(a, KeyEnter)
	// the picker appears when rz announces itself
	waitFor(t, "picker", 10*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		_, ok := a.top().(*PickerModal)
		return ok
	})
	shot(t, a, "15_upload_picker")
	a.mu.Lock()
	p := a.top().(*PickerModal)
	a.pop()
	p.OnDone(a, []string{src})
	a.mu.Unlock()
	waitFor(t, "upload", 20*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(recv, "up.dat"))
		return err == nil && bytes.Equal(b, data)
	})
}

func TestThemesAndBorders(t *testing.T) {
	setupPlat()
	cfg := newDefaultConfig()
	a := NewApp(cfg)
	pal0 := cfg.Palette
	key(a, KeyF4)
	key(a, KeyDown) // Black & White
	shot(t, a, "20_theme_bw")
	if cfg.Theme != "Black & White" {
		t.Fatalf("theme = %q", cfg.Theme)
	}
	a.OnKey(KeyEvent{Ch: 'b'})
	key(a, KeyLeft)
	shot(t, a, "21_border_preset")
	key(a, KeyDown)
	key(a, KeyDown)
	key(a, KeyEnter) // char picker for top-right
	key(a, KeyRight)
	key(a, KeyRight)
	shot(t, a, "22_char_picker")
	key(a, KeyEnter)
	a.OnKey(KeyEvent{Ch: '*'}) // type a char directly for next position
	shot(t, a, "23_border_edited")
	key(a, KeyF10)
	key(a, KeyEnter) // accept theme
	shot(t, a, "24_directory_bw_custom_border")
	if cfg.Palette != pal0 {
		t.Errorf("theme changed terminal palette")
	}
	if cfg.Border[3] == borderSingle[3] && cfg.Border[2] == borderSingle[2] {
		t.Errorf("border not edited: %v", cfg.Border)
	}
}

func TestAppZmodemStress(t *testing.T) {
	if !hasTool("sz") {
		t.Skip("no sz")
	}
	fragment = os.Getenv("GT_FRAG") != ""
	defer func() { fragment = false }()
	setupPlat()
	dir := t.TempDir()
	var srcs []string
	want := map[string][]byte{}
	for i, n := range []int{1, 1023, 70000, 2_000_000} {
		d := make([]byte, n)
		for j := range d {
			d[j] = byte(j*(i+3) + j>>9)
		}
		p := filepath.Join(dir, "f"+string(rune('a'+i))+".dat")
		os.WriteFile(p, d, 0o644)
		srcs = append(srcs, p)
		want[filepath.Base(p)] = d
	}
	cfg := newDefaultConfig()
	cfg.DownDir = t.TempDir()
	host, port := fakeBBS(t, func(c net.Conn) {
		c.Write([]byte("Starting transfer\r\n"))
		runTelnetCmd(c, exec.Command("sz", append([]string{"-b"}, srcs...)...))
		c.Write([]byte("\r\nAll done.\r\n"))
		time.Sleep(500 * time.Millisecond)
	})
	cfg.Entries = []DialEntry{{Name: "z", Host: host, Port: port, Conn: "telnet", Mode: "80x25"}}
	a := NewApp(cfg)
	key(a, KeyEnter)
	waitFor(t, "done text", 60*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return strings.Contains(a.term.AllText(), "All done")
	})
	for name, d := range want {
		b, err := os.ReadFile(filepath.Join(cfg.DownDir, name))
		if err != nil || !bytes.Equal(b, d) {
			t.Errorf("%s mismatch (err=%v len=%d want %d)", name, err, len(b), len(d))
		}
	}
}
