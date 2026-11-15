# Changelog

## 1.15.0 (2026-11-15)
- **Built-in FTP, FTPS and SFTP client with a two-panel file manager** in the style of Midnight Commander: local
  files on the left, the server on the right. Tag files, copy folders recursively in either direction (F5), rename
  or move (F6), make folders (F7), delete (F8), sort (F2), go to a path (F4), progress with rate and ETA, Esc to
  cancel. Mouse supported. Connection types **ftp**, **ftpes** (explicit TLS), **ftps** (implicit TLS) and **sftp**;
  quick connect and the command line accept `ftp://`, `ftpes://`, `ftps://` and `sftp://` URLs with user, password,
  port and start folder. Directory entries have a new "Remote folder" field (saved as `RemoteDir` in goterm.lst).
- FTPS certificates are verified normally; self-signed ones are remembered on first use (`known_certs`) and refused
  if they change. SFTP uses the same SSH login and `known_hosts` as the SSH terminal, with pipelined transfers.
- Tests run the client against real pyftpdlib and paramiko servers (skipped if those are not installed).

## 1.14.1 (2026-11-15)
- **Fix: ANSI screens drawn one row too low.** `ESC[2J` (clear screen) now also moves the cursor to the top-left,
  as in SyncTERM and ANSI.SYS. Before, a BBS header sent after a clear started on the row where the cursor
  happened to be, which pushed it down a row so that later cursor positioning overwrote part of it (the missing
  "bulletin" text under the logo).
- **PgUp/PgDn scrollback option:** Settings > "PgUp/PgDn scroll the scrollback". When on, plain PgUp and PgDn scroll the
  scrollback instead of going to the BBS; Alt+PgUp / Alt+PgDn still send the key. Shift+PgUp and Ctrl+PgUp always scroll.

## 1.14.0 (2026-10-31)
- **ASCII and Raw uploads** (as in SyncTERM): Alt+U now offers "ASCII" and "Raw" besides ZMODEM / XMODEM / YMODEM.
  ASCII sends a text file line by line with every line ending in CR LF (UTF-8 text is converted to CP437 unless the
  entry uses UTF-8). Raw sends the bytes exactly as they are, for ANSI screens. A SAUCE record at the end of an ANSI
  file is not sent. Anything the host sends back is shown while uploading, but no terminal replies are sent.
  Esc cancels. Several files can be chosen at once and are sent in order.
- **Start-up logo:** a new logo is shown when GoTerm opens and fades out to the dialing directory (any key or click
  skips it). Settings > Display options > "Show logo at start-up" turns it off.

## 1.13.2 (2026-10-29)
- **Selecting text when a BBS uses the mouse:** many BBSes turn on mouse reporting, and then a plain drag goes to
  the BBS (as in SyncTERM). Hold **Shift** and drag to select, or press **Alt+M** (also in the Online menu) to switch
  BBS mouse off for the current call so a plain drag selects and copies again. Settings has a new permanent option,
  "Ignore BBS mouse requests".
- Help lists Alt+M.

## 1.13.1 (2026-10-29)
- **Input boxes** (web import, quick connect, ...) now put the label above a full-width field that scrolls with long
  text, so long URLs can be typed and pasted. Paste works with Ctrl+V, Alt+V, Shift+Ins and right/middle click in
  every input field; clicking selects fields, presses buttons and flips options.
- **Settings reorganised:** a short main page plus "Display options", "Scrollback & audio cache" and
  "Colors, themes & borders" pages; labels no longer run into their values and nothing overflows the screen.
- **Help** rewritten, aligned and complete (all directory, online, tab, paste and scrollback commands).
- Directory footer lists the new S / D / U keys.

## 1.13.0 (2026-10-29)
- The dialing directory is now saved in **goterm.lst** (SyncTERM-style INI, next to config.json). An existing
  directory in config.json is moved there automatically on first start.
- **Remove duplicate entries** (D in the directory): same address, port and connection type; call counts and the
  latest call time are merged. Imports also skip entries you already have.
- **Import from the web** (U or F9): type the address of a syncterm.lst / goterm.lst list and it is downloaded and added.
- **Sorting:** S toggles sort by name / last call (most recent first); Settings has "Sort directory by"
  (name, last call, none).
- Android: HTTPS uses the system certificate store.

## 1.12.0 (2026-10-29)
- **Audio cache limits:** the cache (sounds stored by BBSes) is capped at **500 MB** by default (oldest files are
  deleted first) and is **cleared every time GoTerm starts**. Settings has new options: "Audio cache limit in MB"
  (0 = no limit), "Clear audio cache at start-up", and a "Clear audio cache now" button.
- **Host-requested resizing:** a BBS can change the terminal size with `CSI 8 ; rows ; cols t` (80-255 columns,
  25-100 rows); the text is kept and the window content rescales. SSH hosts are told the new size.
  New option "Ignore host resize requests" in Settings (all calls) and in each directory entry.

## 1.11.0 (2026-10-28)
- **Android app (source project)** in `android/`: a thin Kotlin shell around the same Go engine (prebuilt for
  arm64, arm, x86_64, x86). Touch = mouse, soft keyboard + key bar, menu for tabs/quick connect/transfers,
  audio, clipboard, file import/export, keep-alive service. The APK itself must be built with Android Studio or
  the supplied GitHub Actions workflow; see `android/README.md`. The Kotlin code has not been compiled or run yet.
- Engine mode (`-tags engine`) and custom DNS resolver for Android.

## 1.10.2 (2026-10-28)
- Terminal type is now a pick list (Left/Right) in the directory entry editor and in Settings: ansi-bbs, ansi,
  syncterm, xterm-256color, xterm, xterm-color, linux, vt100, vt102, vt220, rxvt, pcansi, cygwin, dumb. Entries
  can also use "(default)" to follow the Settings value. A custom type already saved is kept in the list.

## 1.10.1 (2026-10-28)
- Each tab now has a clickable **x** close button on the tab bar (asks before hanging up a live connection).

## 1.10.0 (2026-10-28)
- **BBS mouse support** (as in SyncTERM): when a BBS turns on mouse reporting, clicks, releases, wheel and drags are
  sent to it so you can click its menus and buttons. Supported: X10 (`?9`), normal (`?1000`), button-event
  (`?1002`) and any-event (`?1003`) tracking, with the classic encoding, SGR (`?1006`) and SGR pixel (`?1016`)
  coordinates, plus Ctrl/Alt modifiers. Hold **Shift** to select/copy text locally while a BBS has the mouse.
- Answers the queries BBS software uses to detect mouse support: `CSI < c` (CTerm device attributes, mouse = 7) and
  `CSI ? Ps $ p` (DEC mode query, including the mouse modes).
- Bracketed paste (`?2004`) is honoured when a BBS asks for it.
- Right/middle button releases and Ctrl/Alt are now delivered by the Windows and X11 windows; goterm-cli now requests
  full motion reporting from the terminal.

## 1.9.0 (2026-10-28)
- **Tabs**: stay logged on to several BBSes and switch between them. Alt+T new tab, Alt+W close tab,
  Alt+1..9 / Alt+Left / Alt+Right or a mouse click to switch. The tab bar appears when there are two or more tabs;
  `*` marks new activity and `!` a closed connection. Each tab keeps its own screen, scrollback and BBS audio
  (muted while in the background); dialogs and file transfers belong to their tab.
- Alt+Q while connected now opens the new address in a new tab instead of dropping the current call.

## 1.8.0 (2026-10-24)
- BBS audio now decodes **MP3**, **FLAC** and **Ogg Vorbis** (in addition to WAV, AIFF and AU) for `Load` and
  `LoadBlob`, and reports them as supported in the `libsndfileFormat` query. Ogg Opus is not supported and is
  reported as unsupported.
- Bundled pure-Go decoders (no DLLs or system libraries needed): go-mp3 (Apache-2.0) and jfreymuth/oggvorbis +
  vorbis (MIT) under `third_party/`, and a built-in FLAC decoder.

## 1.7.0 (2026-10-24)
- **Alt+Q** (directory or online): a small prompt where you type an address and port - `host:port`, `host port`,
  `ssh://user@host`, `rlogin://host`, or a directory entry name. If you are connected, the current call is dropped
  once you press Enter (Esc leaves everything as it was). F5 still works in the directory.
- **SyncTERM audio APC support** (ported from the SyncTERM source), so BBS software that plays sound through
  `ESC _ SyncTERM:A;... ESC \` now works:
  - Verbs: `Load`, `LoadBlob`, `Synth` (sine, saw, square and the SyncTERM "noisy" shapes), `Copy`, `Queue`
    (fade in/out, crossfade, loop, volume/pan), `Flush` (optional fade-out), `Volume` (optional ramp), `Wait`
    (pauses the terminal until the channel is idle) and `Update` (idle notification `CSI = 7 ; ch ; 0 n`).
  - 256 patch slots, 16 channels (2-15 for the BBS), mixed at 44.1 kHz stereo with SyncTERM's equal-power fades and
    soft clipping.
  - Feature queries now answer: `SyncTERM:Q;libsndfile` and `SyncTERM:Q;libsndfileFormat;major;subtype`,
    `SyncTERM:VER`, `SyncTERM:Q;JXL`; and the audio state report `CSI = 7 n` / `CSI = 7 ; ch n`.
  - `SyncTERM:C;S;name;base64` stores files in a per-host cache folder (`SyncTERM:C;L` lists them) and `Load` reads
    from it.
  - Audio file formats: WAV (8/16/24/32-bit, float, A-law, mu-law), AIFF/AIFF-C and AU. Ogg Vorbis/Opus, FLAC and MP3
    cannot be decoded without libsndfile, so GoTerm truthfully reports them as unsupported.
  - Output: Windows uses waveOut; Linux uses paplay, pw-cat, aplay or sox `play` (whichever is installed).
  - Respects the Sound setting (silent, but timing and replies still work).

## 1.6.0 (2026-10-22)
- Command line: `goterm.exe host:port` (also `host port`, `ssh://user@host`, `rlogin://host`, or a directory entry
  name) starts and connects immediately. Options `-mode`, `-term`, `-user`.
- When you disconnect from an address that is not in the dialing directory (command line or quick connect), GoTerm
  asks for a name and offers to save it. Esc declines. Not asked again once the address is in the list.
- Copy / paste: Ctrl+V pastes (in addition to Alt+V, Shift+Ins and right/middle click); Ctrl+C copies when text is
  selected (otherwise it is sent to the host); Ctrl+Ins copies. Paste also works in every text field (Quick connect,
  entry editor, settings, search) with Ctrl+V / Shift+Ins; Ctrl+C / Ctrl+X copy / cut the field.

## 1.5.1 (2026-10-21)
- Fixed: text from the previous connection showed in the window shadows (right edge and bottom) of the dialing
  directory and other menus when not connected. Shadows now fall on the desktop backdrop there.

## 1.5.0 (2026-10-21)
- Dialing directory search: press `/`, Ctrl+F or F8 and type; the list filters as you type across name, address, notes,
  connection type, user and port (all words must match, case-insensitive). Up/Down move within the matches, Enter dials
  the selected one, Esc clears the search. Also works in goterm-cli.

## 1.4.0 (2026-10-21)
- Builds for four platforms: Windows 64-bit and 32-bit, Linux 64-bit and 32-bit. The 32-bit Windows build has the
  icon too. (The 32-bit Windows build could not be run here; it needs Windows 10 or newer.)
- New **goterm-cli** for Linux (64/32-bit): the full application - dialing directory, menus, themes, scrollback,
  ZMODEM/XMODEM/YMODEM, telnet/SSH/RLogin - drawn in a terminal with ANSI escape codes (24-bit or 256 colours),
  mouse and bracketed paste. `goterm-cli [-mode 80x25] [-256] [target]`; Alt+Z or Ctrl+] opens the online menu.
- build.sh builds all targets into dist/.

## 1.3.0 (2026-10-21)
- Scrollback viewer: Ctrl+PgUp / Shift+PgUp / mouse wheel enters it; a position bar and scrollbar show where you are.
  Keys while scrolled back: PgUp/PgDn, Up/Down, Home (oldest), End or Esc (back to live), `/` or Ctrl+F search,
  F3 older match, Shift+F3 newer match, F2 or Ctrl+S save to a text file (in the download folder).
- The screen is now pushed into scrollback before a full clear (ESC[2J), so menus and screens that the BBS
  clears are not lost. Identical repeated redraws are not duplicated. Can be turned off in Settings.
- Online menu gains Scrollback: view / search / save / clear.

## 1.2.0 (2026-10-21)
- New connection types: **SSH** and **RLogin** (in addition to Telnet and Raw). Directory entries gain
  User name and Password fields; the port switches to 22 / 513 when you change the type.
- SSH: password, keyboard-interactive and unencrypted `~/.ssh/id_ed25519|id_ecdsa|id_rsa` keys; if no password is
  stored you are prompted. Host keys are trusted on first use and saved to `%APPDATA%\GoTerm\known_hosts`; a changed
  key is refused. Terminal resizes are sent to the server.
- Quick connect accepts `ssh://user@host:port`, `rlogin://...` and `telnet://...`; syncterm.lst import maps SSH/RLogin.
- SSH support uses golang.org/x/crypto (BSD licence), vendored under `third_party/`.

## 1.1.0 (2026-10-21)
- File transfer aligned with SyncTERM's own source (sbbs3/zmodem.c, xmodem.c): ZRINIT no longer requests
  control-character escaping, hex headers end in CR LF like SyncTERM's, matching what Synchronet-based BBSes expect.
- New XMODEM, XMODEM-CRC, XMODEM-1K, YMODEM and YMODEM-G upload/download (Go port of xmodem.c). Alt+U / Alt+D
  now ask which protocol to use (ZMODEM is first in the list).
- Tests against real lrzsz (sb/sx/rb/rx) added for the new protocols.

## 1.0.1 (2026-10-21)
- ZMODEM: every transfer now writes a trace to `%APPDATA%\GoTerm\zmodem.log` (headers seen/sent, first bytes
  in each direction, result). A failed transfer's error box names the log file.
- ZMODEM: downloads also auto-start when the host sends a binary ZFILE header without a ZRQINIT first.
- Added stress tests (fragmented TCP delivery, several files, 2 MB file) against real `sz`.

## 1.0.0 (2026-10-21)
First numbered release.

- Native Windows window (Win32, no third-party modules); SyncTERM-style dialing directory.
- ANSI/ANSImation fidelity: CP437 fonts from SyncTERM (8x16, 8x14, 8x8), 9-dot cells, blink, ICE colors,
  scroll regions, scrollback, 16 / 256 / truecolor SGR.
- Sixel graphics, ANSI music (CSI | / BananaCom / CSI M) and bell.
- ZMODEM upload and download (auto-detected, tested against lrzsz sz/rz over telnet).
- Telnet (IAC negotiation, NAWS, terminal type) and raw TCP connections.
- Copy/paste: mouse selection copies on release; Alt+V / Shift+Ins / right-click paste.
- Terminal palette defaults to SyncTERM's VGA colors (A8/54/FF); import from a console .reg file.
- Interface themes (17, including Black & White), separate from terminal colors; editable border
  characters; per-element colors with a 256-color picker; save your own themes.
- Delete in the dialing directory is immediate (no prompt).
- Program icon (terminal window).
- Saved settings from earlier builds that held a leaked theme palette are reset to the default.
