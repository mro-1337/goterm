# GoTerm 1.16.2 - User Guide

GoTerm is a Windows telnet/BBS terminal modelled on SyncTERM. Run `goterm.exe`; no installation is needed.
Settings are stored in `%APPDATA%\GoTerm\config.json`.

## Dialing directory
| Key | Action |
|---|---|
| Enter / double-click | Connect |
| Ins or A | Add entry |
| E | Edit entry |
| C | Copy entry |
| Del | Remove entry (immediately, no prompt) |
| F1 | Help |
| F2 | Settings |
| F3 | Terminal ANSI palette |
| F4 | Interface themes |
| F5 or Alt+Q | Quick connect (host:port, host port, ssh://user@host) |
| F6 | Local test pattern (colors, sixel, music) |
| F7 | Import a SyncTERM `syncterm.lst` |
| Q / F10 / Esc | Quit |

Each entry has: name, address, port, connection type (telnet or raw), terminal type, screen mode
(80x25 up to 132x60), music mode, UTF-8 vs CP437, backspace sends DEL, mute sound, notes.

## While connected
| Key | Action |
|---|---|
| Alt+T | New tab (shows the dialing directory) |
| Alt+W | Close the tab (hangs up after asking) |
| Alt+1 .. Alt+9, Alt+Left / Alt+Right | Switch tabs (or click a tab) |
| Alt+Q | Quick connect: type an address and port; if you are connected it opens in a new tab |
| Alt+Z | Online menu |
| Alt+H | Hang up |
| Alt+U / Alt+D | Upload / download (choose ZMODEM, YMODEM, YMODEM-G, XMODEM-CRC, XMODEM-1K or XMODEM) |
| Alt+V, Shift+Insert, right-click | Paste |
| Mouse drag | Select text (copied on release) |
| Alt+C | Copy selection (or whole screen) |
| Ctrl+PgUp / Shift+PgUp, mouse wheel | Enter scrollback (see below) |
| Alt+Enter | Fullscreen |
| Esc (while dialing) | Cancel |

## File transfers
ZMODEM downloads start automatically (for XMODEM/YMODEM pick the protocol from the Alt+D menu first; XMODEM asks for a file name) when the host begins sending; files go to the download
directory (Settings). When the host asks to receive, a file picker opens; Space marks several files,
F10 sends them. Esc or Ctrl+X cancels a transfer.

Every transfer is traced in `%APPDATA%\GoTerm\zmodem.log` (the previous log is kept as `zmodem.log.old`).
If a transfer fails, that file shows what the host and GoTerm exchanged.

## Colors
There are two separate color systems:

* **Terminal palette (F3)** - the 16 ANSI colors used by BBS screens. The default matches SyncTERM
  (VGA levels A8/54/FF). Keys: arrows to choose color/channel, +/- or PgUp/PgDn to adjust, H hex entry,
  P presets, I import ANSI colors from a Windows console `.reg` file (e.g. FixANSIColors.reg),
  D reset one color, Z reset all.
* **Interface themes (F4)** - colors of the dialing directory, menus and dialogs only. Arrow keys preview
  a theme live; Enter keeps it, Esc undoes. B = border characters, P = interface palette,
  E = per-element colors (256-color picker), S = save as your own theme, D = delete your theme.
  Themes never change terminal colors.

Border characters: choose a preset (double, single, block, shade, ASCII...) or set each of the eight
border pieces and the desktop backdrop from the CP437 table (Enter) or by typing a character.

## Settings (F2)
ICE colors, 9-dot cells, window scaling (fit / integer / stretch), smooth scaling, block cursor,
scrollback lines, download directory, sound, bell, copy-on-select, default terminal type and
screen mode, start fullscreen.

## Building from source
Requires Go 1.22+. Run `./build.sh` (tests, then a 64-bit Windows exe). `go run ./tools/mkicon`
regenerates the icon resources.

## Layout of the source
* `screen.go`, `parser.go`, `render.go` - terminal emulation and drawing
* `sixel.go`, `music.go` - sixel decoder and ANSI music synthesizer
* `telnet.go`, `session.go`, `zmodem/`, `xmodem/` - networking and file transfer
* `ui*.go`, `themes.go`, `app.go` - interface
* `gui_windows.go` - Win32 window, clipboard, audio

## Command line

    goterm.exe host[:port]            goterm.exe host port
    goterm.exe ssh://user@host        goterm.exe "Entry name"
    options: -mode 80x25  -term TYPE  -user NAME

Connects straight away. On disconnect you are asked whether to save the address to the dialing directory.

## Mouse in BBSes

Many BBSes can use the mouse (clickable menus, buttons, scrolling lists). When a BBS asks for it, GoTerm sends it
your clicks, drags, scroll wheel and - if it wants them - plain mouse movement. While a BBS has the mouse, local
text selection and middle/right-click paste are off: **hold Shift** to select text (or paste) as usual. In
goterm-cli your terminal's own selection is Shift+drag as well. The tab bar and menus always use the mouse locally.

## Tabs

You can be logged on to several BBSes at once. Alt+T opens a new tab (it shows the dialing directory; pick a BBS
as usual), and a tab bar appears across the top as soon as there are two or more tabs. Click a tab (or its **x** to close it) or use
Alt+1..9 / Alt+Left / Alt+Right to switch. Connections in background tabs keep running and keep receiving; a `*`
before a tab name means new data arrived, `!` means that connection closed. Each tab has its own screen,
scrollback and sound (only the visible tab is audible). Alt+W closes a tab. File transfers run per tab, but the
transfer progress numbers are shared, so start one transfer at a time. Works in goterm-cli too.

## BBS audio (SyncTERM audio APC)

GoTerm implements the same audio protocol as SyncTERM, so BBS programs that stream or trigger sounds work: sounds
are loaded (from the BBS as base64, or from files the BBS first stored in the cache), synthesised, queued on
channels 2-15, faded, looped and waited on. The BBS can ask what is supported: GoTerm answers that these can be decoded: MP3 (Layer III), FLAC, Ogg Vorbis, and WAV, AIFF and AU
(8/16/24/32-bit PCM, float, A-law, mu-law). Ogg Opus, and MP1/MP2 files, are not supported. Sound goes through the
normal Sound setting (F2) and the per-entry "no sound" flag. On Linux the sound is sent to `paplay`, `pw-cat`,
`aplay` or `play` - one of them has to be installed. Cached files live under your user cache folder in
`GoTerm/cache/<host>_<port>/`.

## Copy and paste

Drag with the mouse to select (copied automatically; Settings can turn that off), or Ctrl+C / Ctrl+Ins. Paste with
Ctrl+V, Shift+Ins, Alt+V or right/middle click. Ctrl+C with nothing selected is sent to the host. In text fields
Ctrl+V / Shift+Ins paste and Ctrl+C / Ctrl+X copy / cut.

## Searching the directory

Press `/` (or Ctrl+F, F8) in the dialing directory and start typing. The list narrows to entries whose name, address,
notes, connection type, user or port contain every word you typed. Up/Down/PgUp/PgDn move through the matches, Enter
dials the highlighted entry, Backspace edits the search, Ctrl+U clears it and Esc leaves search mode.

## Connection types

Each directory entry has a Connection type: telnet (default, port 23), ssh (22), rlogin (513) or raw TCP, plus the file-transfer types ftp (21), ftpes (explicit FTPS, 21), ftps (implicit FTPS, 990) and sftp (22),
which open the two-panel file manager (see "File manager" below).

* **SSH** - set the user name; leave the password blank to be asked on connect (a stored password is kept in plain
  text in the config file). Unencrypted keys in `~/.ssh` are tried first. The first connection to a host records its
  key in `%APPDATA%\GoTerm\known_hosts`; if the key later changes the connection is refused.
* **RLogin** - sends the user name and terminal type; no password is used by the protocol.
* Quick connect accepts `ssh://user@host:port`, `rlogin://host`, `telnet://host`, and for files `ftp://`, `ftpes://`,
  `ftps://` and `sftp://` as `scheme://user:password@host:port/folder`.

## Scrollback

Enter with Ctrl+PgUp, Shift+PgUp, the mouse wheel or Online menu > Scrollback: view. A bar at the top shows the
position; a scrollbar runs down the right edge. While scrolled back:

| Key | Action |
|---|---|
| PgUp / PgDn, Up / Down | scroll |
| Home | oldest line |
| End or Esc | back to the live screen |
| `/` or Ctrl+F | search (case-insensitive) |
| F3 / Shift+F3 | next older / newer match |
| F2 or Ctrl+S | save scrollback and screen to a .txt file in the download folder |

Any other key returns to the live screen and is sent to the host. The screen is copied into scrollback before
the BBS clears it (Settings > "Keep screen in scrollback on clear"). Size: Settings > Scrollback lines.

## Platforms and builds

| Folder | What |
|---|---|
| windows-x64 / windows-x86 | `goterm.exe`, native window (Windows 10 or newer) |
| linux-x64 / linux-x86 | `goterm` (native X11 window, no libraries needed) and `goterm-cli` (runs in a terminal) |

### goterm-cli

    goterm-cli [options] [host[:port] | ssh://user@host | rlogin://host | directory-entry-name]

Without a target the dialing directory opens. Options: `-mode 80x25|80x28|80x30|80x43|80x50|80x60`, `-term TYPE`,
`-user NAME`, `-256` (use the 256-colour palette instead of 24-bit colour; chosen automatically unless `COLORTERM` is
`truecolor`). The terminal window must be at least as big as the BBS screen. Alt+Z or Ctrl+] opens the online menu;
the mouse selects and copies (via OSC 52) and the wheel scrolls back; paste with your terminal's paste. Sixel graphics
and sound-through-the-terminal are not available in the CLI (sound uses paplay/aplay if installed).


## Android

The `android/` folder in the source contains an Android Studio project. See `android/README.md` for how to build
the APK. On the phone: tap = click, drag = scroll, long press + drag = select text, Back = Esc, and the bar under
the screen provides Esc/Tab/Ctrl/Alt/arrows/F-keys and a Menu (tabs, quick connect, upload/download, hang up).
Downloads are saved in the app's Download folder; use Menu > "Save a downloaded file..." to export. To upload,
use Menu > "Add a file to upload..." first.


## Audio cache and host resizing

Sounds a BBS stores are kept in the audio cache. Settings > "Audio cache limit in MB" (default 500, 0 = unlimited)
deletes the oldest files when the limit is exceeded; "Clear audio cache at start-up" (default on) empties it each
time GoTerm starts; "Clear audio cache now" empties it immediately.

A BBS may ask for a different screen size (`CSI 8;rows;cols t`). GoTerm follows it (80-255 x 25-100). To refuse,
turn on "Ignore host resize requests" in Settings (everywhere) or in a directory entry (that BBS only).


## Directory file, duplicates, web import, sorting

The dialing directory lives in `goterm.lst` in the GoTerm settings folder (Windows `%AppData%\GoTerm`, Linux
`~/.config/GoTerm`). It is a SyncTERM-style list, so it can also be imported by other programs.

In the directory: **D** removes duplicate entries, **U** or **F9** imports a list from a web address, **F7** imports a
file, and **S** switches the sort between name and last call. The permanent choice is Settings > "Sort directory by".


## Selecting and copying text

Drag with the mouse to select; the text is copied when you release (Settings > "Copy selection on release").
If a BBS has switched on its own mouse support, a plain drag is sent to the BBS instead: hold **Shift** while
dragging, or press **Alt+M** to turn BBS mouse off for that call. Settings > "Ignore BBS mouse requests" makes
this permanent.


## Uploading text and ANSI screens (ASCII and Raw)

Press Alt+U while connected and choose **ASCII** or **Raw**, then pick the file(s).

- **ASCII** sends a text file line by line; every line ends in CR LF. UTF-8 text is converted to the CP437 character
  set unless the directory entry has "UTF-8 text" on. Use it for replies, messages and text files.
- **Raw** sends the bytes exactly as they are. Use it for ANSI screens and anything that must not be changed.

In both modes the transfer has no error checking or flow control, anything the host sends back is displayed while
uploading, and GoTerm does not answer terminal queries during the upload. A SAUCE record at the end of an ANSI file
is not sent. Press Esc to cancel.

## Start-up logo

GoTerm shows its logo for about three seconds when it opens, then fades to the dialing directory. Press any key or
click to skip. Turn it off in Settings > Display options > "Show logo at start-up".


## Scrolling back with Page Up / Page Down

Shift+PgUp, Ctrl+PgUp, the mouse wheel and the scrollbar always scroll the scrollback. If you want plain Page Up and
Page Down to do it, turn on Settings > "PgUp/PgDn scroll the scrollback". With that on, Alt+PgUp and Alt+PgDn send
the keys to the BBS.

## Finding files in the upload picker

In the file picker (Alt+U, imports and so on) **just type the first letters of a file or folder name** to jump to
it. Typing the same letter again moves to the next name starting with it, and more letters narrow the jump (the
prefix resets after a second). Press **/** or **Ctrl+F** to **search**: only names containing what you type are
shown (Backspace edits, Esc clears the search). Space marks a file as before; Ins also marks. The file manager
panels jump by typing too, and `/` asks for a name to find (Enter on the same text again finds the next match).

## File manager (FTP, FTPS, SFTP)

Choose the connection type **ftp**, **ftpes** (explicit TLS: starts plain, then AUTH TLS), **ftps** (implicit TLS,
port 990) or **sftp** (over SSH) in a directory entry, or type a URL such as `sftp://me@host/home/me` into quick
connect. Leave the user name empty for anonymous FTP. A blank password is asked for when connecting. The optional
"Remote folder" field is where the right panel starts.

The screen is modelled on Midnight Commander: **your computer on the left, the server on the right** (the left panel
starts in the download directory). A progress box with rate and time left is shown while copying; Esc cancels and
removes the half-written file.

| Key | Action |
| --- | --- |
| Tab, Left, Right | switch panel |
| Up, Down, PgUp, PgDn, Home, End | move |
| Enter | open the folder (the `..` row goes up); Backspace also goes up |
| Ins or Space | tag / untag a file and move down; `+` tag all, `-` untag all, `*` invert |
| F2 | sort by name, size or time |
| F4 | go to a path |
| F5 | copy the tagged files (or the file under the cursor) to the other panel; folders are copied with everything in them |
| F6 | rename the file under the cursor; with tagged files, move them to the other panel |
| F7 / F8 (Del) | new folder / delete (with confirmation, folders recursively) |
| F9, Ctrl+R | reload |
| F10, Esc | close the connection and leave |

The mouse works too: click selects, double-click opens, the wheel scrolls, right-click tags and the bottom bar
buttons act as the F keys. The panels also work in both directions (server to your computer, or your computer to the
server); copy always goes to the panel that is not active. Copying also works between the two panels in the opposite
direction by pressing Tab first.

**Security.** FTPS certificates are checked against the system roots. A server with a self-signed certificate is
accepted on first use and its fingerprint is saved in `known_certs` next to the config file; if it ever changes the
connection is refused (the same idea as `known_hosts` for SSH and SFTP). Plain `ftp` sends the password unencrypted;
prefer ftpes, ftps or sftp.

## Graphics doors (sixel and JPEG XL)

Synchronet's SyncDOOM, SyncDuke and similar doors draw the game as an image. GoTerm supports both ways they do it:
JPEG XL (preferred, smaller) and sixel. Sixel colors are kept between images, as the doors expect. GoTerm reports
itself like SyncTERM (CTerm revision 1.332, JPEG XL supported), which makes the host choose the JPEG XL tier.
JPEG XL decoding is done in software; on a slow computer a lower-quality setting on the host (higher
`jxl_distance`) or the sixel tier (host setting `tier=sixel`) is lighter.

The JPEG XL decoder is third-party code under the MIT license (`source/third_party/jxl/LICENSE`).
