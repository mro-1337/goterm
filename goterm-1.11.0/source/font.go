package main

import _ "embed"

// The three CP437 bitmap fonts below were extracted from SyncTERM's
// conio/allfonts.c ("Codepage 437 English"): one byte per scanline,
// 8 pixels wide, MSB = leftmost pixel, 256 glyphs.

//go:embed font8x16.bin
var font8x16 []byte

//go:embed font8x14.bin
var font8x14 []byte

//go:embed font8x8.bin
var font8x8 []byte

// Font is a fixed-cell bitmap font.
type Font struct {
	W, H int
	Data []byte // 256 * H bytes
}

func fontForHeight(h int) *Font {
	switch h {
	case 8:
		return &Font{8, 8, font8x8}
	case 14:
		return &Font{8, 14, font8x14}
	default:
		return &Font{8, 16, font8x16}
	}
}

// ScreenMode describes a text mode: grid size + which font height it uses.
type ScreenMode struct {
	Name       string
	Cols, Rows int
	FontH      int
}

var screenModes = []ScreenMode{
	{"80x25", 80, 25, 16},
	{"80x28", 80, 28, 14},
	{"80x30", 80, 30, 16},
	{"80x43", 80, 43, 8},
	{"80x50", 80, 50, 8},
	{"80x60", 80, 60, 8},
	{"132x25", 132, 25, 16},
	{"132x30", 132, 30, 16},
	{"132x43", 132, 43, 8},
	{"132x50", 132, 50, 8},
	{"132x60", 132, 60, 8},
}

func screenModeByName(name string) ScreenMode {
	for _, m := range screenModes {
		if m.Name == name {
			return m
		}
	}
	return screenModes[0]
}
