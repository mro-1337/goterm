# GoTerm

Telnet/SSH/RLogin BBS terminal for Windows (32/64-bit) and Linux (32/64-bit, window and terminal versions) in Go (no third-party modules), SyncTERM-style interface.

Features: 16 / 256 / truecolor ANSI, sixel graphics, ANSI music + bell, CP437 fonts (8x16/8x14/8x8)
with optional 9-dot cells, ICE colors, blink, scrollback, multiple connections in tabs, ZMODEM (auto-detected) and XMODEM/YMODEM upload/download,
dialing directory, SyncTERM audio APC (BBS sound; MP3/FLAC/Ogg/WAV), mouse copy / paste, BBS mouse support, fullscreen, editable palettes and interface themes.

Directory: Enter dial, Ins/A add, E edit, Del remove (no prompt), C copy, F1 help, F2 settings,

  F3 terminal ANSI palette (I = import colors from a console .reg file such as FixANSIColors.reg),
  
  F4 interface themes (17 themes incl. Black & White; B = border characters, E = element colors,
  
  P = interface palette, S = save your own). Themes affect only the dialing screen/menus.
  
  F5 or Alt+Q quick connect, F6 test pattern, F7 import syncterm.lst, Q/F10 quit
  
Online: Alt+Q quick connect, Alt+Z menu, Alt+H hang up, Alt+U upload, Alt+D download, Alt+V / Shift+Ins / right-click paste,

  drag to select (copies on release), Shift+PgUp/PgDn or wheel scrollback, Alt+Enter fullscreen
  


Config: %APPDATA%\GoTerm\config.json
Icon:   go run ./tools/mkicon   (regenerates goterm.ico and rsrc_windows_amd64.syso)

Build:  GOOS=windows GOARCH=amd64 go build -ldflags "-s -w -H windowsgui" -o goterm.exe .


## Android
An Android Studio project is in `android/` (see android/README.md). It is untested Kotlin around the tested Go engine.
