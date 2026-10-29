package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// oldVGA is the palette earlier versions used (0xAA/0x55/0xFF levels).
var oldVGA = [16]RGB{
	{0x00, 0x00, 0x00}, {0xAA, 0x00, 0x00}, {0x00, 0xAA, 0x00}, {0xAA, 0x55, 0x00},
	{0x00, 0x00, 0xAA}, {0xAA, 0x00, 0xAA}, {0x00, 0xAA, 0xAA}, {0xAA, 0xAA, 0xAA},
	{0x55, 0x55, 0x55}, {0xFF, 0x55, 0x55}, {0x55, 0xFF, 0x55}, {0xFF, 0xFF, 0x55},
	{0x55, 0x55, 0xFF}, {0xFF, 0x55, 0xFF}, {0x55, 0xFF, 0xFF}, {0xFF, 0xFF, 0xFF},
}

// defaultPalette is the VGA palette SyncTERM displays (6-bit DAC values
// 0x2A/0x15/0x3F scaled to A8/54/FF), in ANSI order (1 = red, 4 = blue).
func defaultPalette() [16]RGB {
	return [16]RGB{
		{0x00, 0x00, 0x00}, {0xA8, 0x00, 0x00}, {0x00, 0xA8, 0x00}, {0xA8, 0x54, 0x00},
		{0x00, 0x00, 0xA8}, {0xA8, 0x00, 0xA8}, {0x00, 0xA8, 0xA8}, {0xA8, 0xA8, 0xA8},
		{0x54, 0x54, 0x54}, {0xFF, 0x54, 0x54}, {0x54, 0xFF, 0x54}, {0xFF, 0xFF, 0x54},
		{0x54, 0x54, 0xFF}, {0xFF, 0x54, 0xFF}, {0x54, 0xFF, 0xFF}, {0xFF, 0xFF, 0xFF},
	}
}

// ParseConsoleReg reads the ColorTable00..15 values of a Windows console
// .reg file (as in FixANSIColors.reg) and returns an ANSI-ordered palette.
// The registry stores COLORREF (0x00BBGGRR) in console order (bit0 = blue,
// bit2 = red), so both the byte order and the index order are converted.
func ParseConsoleReg(path string) ([16]RGB, error) {
	var pal [16]RGB
	b, err := os.ReadFile(path)
	if err != nil {
		return pal, err
	}
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE { // UTF-16LE
		var r []rune
		for i := 2; i+1 < len(b); i += 2 {
			r = append(r, rune(uint16(b[i])|uint16(b[i+1])<<8))
		}
		b = []byte(string(r))
	}
	found := 0
	seen := [16]bool{}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "\"ColorTable") {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(k, "\"ColorTable"), "\" "))
		if err != nil || n < 0 || n > 15 || seen[n] {
			continue
		}
		v = strings.TrimPrefix(strings.TrimSpace(v), "dword:")
		x, err := strconv.ParseUint(v, 16, 32)
		if err != nil {
			continue
		}
		ansi := (n&1)<<2 | n&2 | (n&4)>>2 | n&8
		pal[ansi] = RGB{uint8(x), uint8(x >> 8), uint8(x >> 16)}
		seen[n] = true
		found++
	}
	if found < 16 {
		return pal, fmt.Errorf("found %d of 16 ColorTable entries", found)
	}
	return pal, nil
}

// UI roles. Each role is a (foreground, background) pair of palette
// indexes, so editing the palette recolors the interface and the roles can
// also be reassigned individually.
const (
	rBackdrop = iota
	rFrame
	rTitle
	rText
	rHilite
	rSelect
	rInput
	rStatus
	rShadow
	rError
	numRoles
)

var roleNames = [numRoles]string{
	"Desktop backdrop", "Window frame/body", "Window title", "Normal text",
	"Highlighted text", "Selection bar", "Input field", "Status/help line",
	"Window shadow", "Error window",
}

type RoleColor struct{ Fg, Bg uint8 }

func defaultRoles() [numRoles]RoleColor {
	return [numRoles]RoleColor{
		rBackdrop: {4, 0},  // blue on black shade
		rFrame:    {15, 4}, // white on blue
		rTitle:    {0, 7},  // black on grey
		rText:     {7, 4},  // grey on blue
		rHilite:   {14, 4}, // yellow on blue
		rSelect:   {0, 3},  // black on brown (SyncTERM style bar)
		rInput:    {15, 0}, // white on black
		rStatus:   {0, 7},  // black on grey
		rShadow:   {8, 0},  // dark grey on black
		rError:    {15, 1}, // white on red
	}
}

type DialEntry struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Conn     string `json:"conn"` // "telnet", "ssh", "rlogin" or "raw"
	Term     string `json:"term"`
	Mode     string `json:"mode"`  // screen mode name
	Music    int    `json:"music"` // 0 CSI|, 1 +BANSI, 2 +CSI M
	UTF8     bool   `json:"utf8"`
	BSDel    bool   `json:"bsdel"` // backspace sends DEL
	NoSound  bool   `json:"nosound"`
	Notes    string `json:"notes"`
	Last     string `json:"last"`
	Uploads  string `json:"uploads"`
	Password string `json:"password"`
	User     string `json:"user"`
	Count    int    `json:"count"`
}

type Config struct {
	Palette     [16]RGB             `json:"palette"`
	Roles       [numRoles]RoleColor `json:"roles"`
	Entries     []DialEntry         `json:"entries"`
	ICE         bool                `json:"ice"`
	NineDot     bool                `json:"ninedot"`
	Scale       string              `json:"scale"` // fit, integer, stretch
	Smooth      bool                `json:"smooth"`
	BlockCur    bool                `json:"blockcursor"`
	Scrollback  int                 `json:"scrollback"`
	KeepOnClear bool                `json:"sb_on_clear"`
	DownDir     string              `json:"downdir"`
	Sound       bool                `json:"sound"`
	Bell        bool                `json:"bell"`
	AutoCopy    bool                `json:"autocopy"`
	Term        string              `json:"term"`
	Mode        string              `json:"mode"`
	LastDir     string              `json:"lastdir"`
	Fullscreen  bool                `json:"fullscreen"`
	Themes      []Theme             `json:"themes"`
	Theme       string              `json:"theme"`
	UIPalette   [16]RGB             `json:"uipalette"`
	Border      [9]uint8            `json:"border"`
	Ver         int                 `json:"ver"`
	path        string
}

func newDefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Palette: defaultPalette(), Roles: defaultRoles(), Theme: "SyncTERM Blue", UIPalette: defaultPalette(), Border: borderDouble,
		Scale: "fit", Scrollback: 2000, KeepOnClear: true, Sound: true, Bell: true, AutoCopy: true,
		Term: "ansi-bbs", Mode: "80x25", NineDot: false, BlockCur: false,
		DownDir: filepath.Join(home, "Downloads"),
		Entries: []DialEntry{
			{Name: "Synchronet BBS List", Host: "telnet.synchro.net", Port: 23, Conn: "telnet", Mode: "80x25", Notes: "Public directory of BBSes"},
			{Name: "Level 29 BBS", Host: "bbs.fozztexx.com", Port: 23, Conn: "telnet", Mode: "80x25"},
			{Name: "Particles BBS", Host: "particlesbbs.dyndns.org", Port: 6400, Conn: "telnet", Mode: "80x25"},
		},
	}
}

func configPath() string {
	d, err := os.UserConfigDir()
	if err != nil || d == "" {
		d, _ = os.UserHomeDir()
	}
	return filepath.Join(d, "GoTerm", "config.json")
}

func LoadConfig() *Config {
	c := newDefaultConfig()
	c.path = configPath()
	if b, err := os.ReadFile(c.path); err == nil {
		json.Unmarshal(b, c)
	}
	if c.Theme == "SyncTERM Blue" && c.Roles[rBackdrop] == (RoleColor{1, 0}) {
		c.Roles[rBackdrop] = RoleColor{4, 0} // was mistakenly red
	}
	if c.Ver < 2 {
		// Earlier builds let the theme browser overwrite the terminal
		// palette. Put the default back if it holds a built-in palette.
		for _, t := range builtinThemes {
			if c.Palette == t.Palette && t.Palette != defaultPalette() {
				c.Palette = defaultPalette()
			}
		}
		for _, p := range palPresets {
			if c.Palette == p.Pal && p.Pal != defaultPalette() {
				c.Palette = defaultPalette()
			}
		}
		c.Ver = 2
	}
	if c.Palette == oldVGA {
		c.Palette = defaultPalette()
	}
	if c.Border[0] == 0 && c.Border[1] == 0 {
		c.Border = borderDouble
	}
	if c.Scrollback < 100 {
		c.Scrollback = 100
	}
	return c
}

func (c *Config) Save() error {
	if c.path == "" {
		return nil
	}
	os.MkdirAll(filepath.Dir(c.path), 0o755)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(c.path, b, 0o644)
}

func (c *Config) SortEntries() {
	sort.SliceStable(c.Entries, func(i, j int) bool {
		return strings.ToLower(c.Entries[i].Name) < strings.ToLower(c.Entries[j].Name)
	})
}

// ImportSyncTermLst reads a SyncTERM "syncterm.lst" (INI style) file.
func ImportSyncTermLst(path string) ([]DialEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []DialEntry
	var cur *DialEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(ln, "[") && strings.HasSuffix(ln, "]") {
			out = append(out, DialEntry{Name: ln[1 : len(ln)-1], Port: 23, Conn: "telnet", Mode: "80x25"})
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "address":
			cur.Host = v
		case "port":
			cur.Port, _ = strconv.Atoi(v)
		case "connectiontype":
			switch strings.ToLower(v) {
			case "raw":
				cur.Conn = "raw"
			case "ssh":
				cur.Conn = "ssh"
			case "rlogin":
				cur.Conn = "rlogin"
			case "telnet":
				cur.Conn = "telnet"
			default:
				cur.Conn = "telnet"
			}
		case "username":
			cur.User = v
		case "password":
			cur.Password = v
		case "comment":
			cur.Notes = v
		case "music":
			cur.Music, _ = strconv.Atoi(v)
		}
	}
	return out, sc.Err()
}
