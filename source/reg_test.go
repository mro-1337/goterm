package main

import (
	"os"
	"testing"
)

func TestParseConsoleReg(t *testing.T) {
	const p = "/root/.claude/uploads/1f9018eb-fa62-5a0a-be35-01f04f76edb2/ddc00fbe-FixANSIColors.reg"
	if _, err := os.Stat(p); err != nil {
		t.Skip("no reg file")
	}
	pal, err := ParseConsoleReg(p)
	if err != nil {
		t.Fatal(err)
	}
	want := defaultPalette()
	for i := 8; i < 16; i++ { // the .reg uses FC for full intensity
		for _, p := range []*uint8{&want[i].R, &want[i].G, &want[i].B} {
			if *p == 0xFF {
				*p = 0xFC
			}
		}
	}
	if pal != want {
		t.Fatalf("parsed palette differs: %v", pal)
	}
}

func TestPaletteMigration(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	c := newDefaultConfig()
	c.path = configPath()
	c.Palette = builtinThemes[len(builtinThemes)-1].Palette // xterm, as leaked by the old theme browser
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	got := LoadConfig()
	if got.Palette != defaultPalette() {
		t.Fatalf("palette not migrated: %v", got.Palette)
	}
}
