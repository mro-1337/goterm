//go:build windows

package main

import (
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	winmm    = syscall.NewLazyDLL("winmm.dll")

	pRegisterClassExW   = user32.NewProc("RegisterClassExW")
	pCreateWindowExW    = user32.NewProc("CreateWindowExW")
	pShowWindow         = user32.NewProc("ShowWindow")
	pUpdateWindow       = user32.NewProc("UpdateWindow")
	pGetMessageW        = user32.NewProc("GetMessageW")
	pTranslateMessage   = user32.NewProc("TranslateMessage")
	pDispatchMessageW   = user32.NewProc("DispatchMessageW")
	pDefWindowProcW     = user32.NewProc("DefWindowProcW")
	pPostQuitMessage    = user32.NewProc("PostQuitMessage")
	pPostMessageW       = user32.NewProc("PostMessageW")
	pDestroyWindow      = user32.NewProc("DestroyWindow")
	pLoadCursorW        = user32.NewProc("LoadCursorW")
	pSetTimer           = user32.NewProc("SetTimer")
	pInvalidateRect     = user32.NewProc("InvalidateRect")
	pBeginPaint         = user32.NewProc("BeginPaint")
	pEndPaint           = user32.NewProc("EndPaint")
	pGetClientRect      = user32.NewProc("GetClientRect")
	pGetWindowRect      = user32.NewProc("GetWindowRect")
	pSetWindowTextW     = user32.NewProc("SetWindowTextW")
	pSetWindowLongPtrW  = user32.NewProc("SetWindowLongPtrW")
	pGetWindowLongPtrW  = user32.NewProc("GetWindowLongPtrW")
	pSetWindowPos       = user32.NewProc("SetWindowPos")
	pMonitorFromWindow  = user32.NewProc("MonitorFromWindow")
	pGetMonitorInfoW    = user32.NewProc("GetMonitorInfoW")
	pGetKeyState        = user32.NewProc("GetKeyState")
	pOpenClipboard      = user32.NewProc("OpenClipboard")
	pCloseClipboard     = user32.NewProc("CloseClipboard")
	pGetClipboardData   = user32.NewProc("GetClipboardData")
	pSetClipboardData   = user32.NewProc("SetClipboardData")
	pEmptyClipboard     = user32.NewProc("EmptyClipboard")
	pSetCapture         = user32.NewProc("SetCapture")
	pReleaseCapture     = user32.NewProc("ReleaseCapture")
	pAdjustWindowRectEx = user32.NewProc("AdjustWindowRectEx")
	pGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	pScreenToClient     = user32.NewProc("ScreenToClient")
	pSetProcessDPIAware = user32.NewProc("SetProcessDPIAware")
	pStretchDIBits      = gdi32.NewProc("StretchDIBits")
	pSetStretchBltMode  = gdi32.NewProc("SetStretchBltMode")
	pPatBlt             = gdi32.NewProc("PatBlt")
	pGlobalAlloc        = kernel32.NewProc("GlobalAlloc")
	pGlobalLock         = kernel32.NewProc("GlobalLock")
	pGlobalUnlock       = kernel32.NewProc("GlobalUnlock")
	pGetLogicalDrives   = kernel32.NewProc("GetLogicalDrives")
	pGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	pPlaySoundW         = winmm.NewProc("PlaySoundW")
	pLoadImageW         = user32.NewProc("LoadImageW")
)

const (
	wmSize        = 0x0005
	wmClose       = 0x0010
	wmDestroy     = 0x0002
	wmPaint       = 0x000F
	wmEraseBkgnd  = 0x0014
	wmKeyDown     = 0x0100
	wmChar        = 0x0102
	wmSysKeyDown  = 0x0104
	wmSysChar     = 0x0106
	wmTimer       = 0x0113
	wmMouseMove   = 0x0200
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmLButtonDbl  = 0x0203
	wmRButtonDown = 0x0204
	wmMButtonDown = 0x0207
	wmRButtonUp   = 0x0205
	wmMButtonUp   = 0x0208
	wmMouseWheel  = 0x020A

	wsOverlapped = 0x00CF0000
	wsPopup      = 0x80000000
	wsVisible    = 0x10000000
	gwlStyle     = ^uintptr(15) // -16
)

type rect struct{ left, top, right, bottom int32 }
type point struct{ x, y int32 }
type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}
type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}
type paintStruct struct {
	hdc     uintptr
	erase   int32
	rc      rect
	restore int32
	incUpd  int32
	rsvd    [32]byte
}
type bmiHeader struct {
	size          uint32
	width, height int32
	planes, bits  uint16
	compression   uint32
	sizeImage     uint32
	xppm, yppm    int32
	clrUsed       uint32
	clrImportant  uint32
}
type monitorInfo struct {
	size    uint32
	monitor rect
	work    rect
	flags   uint32
}

type winState struct {
	app        *App
	hwnd       uintptr
	dx, dy     int
	dw, dh     int
	fbW, fbH   int
	fullscreen bool
	saveStyle  uintptr
	saveRect   rect
	title      string
	highSur    rune
	sound      chan []byte
}

var W winState

func utf16Ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func keyDown(vk uintptr) bool {
	r, _, _ := pGetKeyState.Call(vk)
	return int16(r) < 0
}

func mods() (ctrl, alt, shift bool) {
	return keyDown(0x11), keyDown(0x12), keyDown(0x10)
}

var vkMap = map[uintptr]Key{
	0x08: KeyBS, 0x09: KeyTab, 0x0D: KeyEnter, 0x1B: KeyEsc,
	0x21: KeyPgUp, 0x22: KeyPgDn, 0x23: KeyEnd, 0x24: KeyHome,
	0x25: KeyLeft, 0x26: KeyUp, 0x27: KeyRight, 0x28: KeyDown,
	0x2D: KeyIns, 0x2E: KeyDel,
}

func vkToKey(vk uintptr) Key {
	if k, ok := vkMap[vk]; ok {
		return k
	}
	if vk >= 0x70 && vk <= 0x7B {
		return KeyF1 + Key(vk-0x70)
	}
	return KeyNone
}

func lo16(l uintptr) int { return int(int16(l & 0xFFFF)) }
func hi16(l uintptr) int { return int(int16((l >> 16) & 0xFFFF)) }

func (w *winState) mouse(ev MouseEvent, x, y int) {
	if w.dw > 0 && w.dh > 0 {
		ev.X = (x - w.dx) * w.fbW / w.dw
		ev.Y = (y - w.dy) * w.fbH / w.dh
	}
	c, al, sh := mods()
	ev.Shift, ev.Ctrl, ev.Alt = sh, c, al
	w.app.OnMouse(ev)
}

func wndProc(hwnd, m, wp, lp uintptr) uintptr {
	a := W.app
	switch m {
	case wmEraseBkgnd:
		return 1
	case wmSize:
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmPaint:
		W.paint(hwnd)
		return 0
	case wmTimer:
		if a.Tick() {
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		if t := a.Title(); t != W.title {
			W.title = t
			pSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(t))))
		}
		return 0
	case wmKeyDown:
		k := vkToKey(wp)
		if k == KeyNone {
			return 0
		}
		c, al, sh := mods()
		a.OnKey(KeyEvent{Key: k, Ctrl: c, Alt: al, Shift: sh})
		return 0
	case wmSysKeyDown:
		c, al, sh := mods()
		if wp == 0x0D && !c { // Alt+Enter
			a.ToggleFullscreen()
			return 0
		}
		if wp == 0x73 || wp == 0x20 || wp == 0x12 || c { // Alt+F4, Alt+Space, bare Alt, AltGr
			r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
			return r
		}
		if k := vkToKey(wp); k != KeyNone {
			a.OnKey(KeyEvent{Key: k, Alt: al, Shift: sh})
			return 0
		}
		if (wp >= 0x41 && wp <= 0x5A) || (wp >= 0x30 && wp <= 0x39) {
			ch := rune(wp)
			if wp >= 0x41 && wp <= 0x5A {
				ch += 32
			}
			a.OnKey(KeyEvent{Ch: ch, Alt: true, Shift: sh})
			return 0
		}
		r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
		return r
	case wmSysChar:
		c, _, _ := mods()
		if c { // AltGr composed character
			a.OnKey(KeyEvent{Ch: rune(wp)})
		}
		return 0
	case wmChar:
		ch := rune(wp)
		switch ch {
		case 8, 9, 13, 27:
			return 0
		}
		if ch >= 0xD800 && ch < 0xDC00 {
			W.highSur = ch
			return 0
		}
		if ch >= 0xDC00 && ch < 0xE000 {
			ch = utf16.DecodeRune(W.highSur, ch)
			W.highSur = 0
		}
		c, al, _ := mods()
		if ch < 32 {
			a.OnKey(KeyEvent{Ch: ch, Ctrl: true})
		} else {
			a.OnKey(KeyEvent{Ch: ch, Ctrl: c && !al})
		}
		return 0
	case wmLButtonDown, wmLButtonDbl:
		pSetCapture.Call(hwnd)
		W.mouse(MouseEvent{Button: 0, Down: true, Double: m == wmLButtonDbl}, lo16(lp), hi16(lp))
		return 0
	case wmLButtonUp:
		pReleaseCapture.Call()
		W.mouse(MouseEvent{Button: 0, Up: true}, lo16(lp), hi16(lp))
		return 0
	case wmRButtonDown:
		pSetCapture.Call(hwnd)
		W.mouse(MouseEvent{Button: 2, Down: true}, lo16(lp), hi16(lp))
		return 0
	case wmRButtonUp:
		pReleaseCapture.Call()
		W.mouse(MouseEvent{Button: 2, Up: true}, lo16(lp), hi16(lp))
		return 0
	case wmMButtonDown:
		pSetCapture.Call(hwnd)
		W.mouse(MouseEvent{Button: 1, Down: true}, lo16(lp), hi16(lp))
		return 0
	case wmMButtonUp:
		pReleaseCapture.Call()
		W.mouse(MouseEvent{Button: 1, Up: true}, lo16(lp), hi16(lp))
		return 0
	case wmMouseMove:
		W.mouse(MouseEvent{Move: true}, lo16(lp), hi16(lp))
		return 0
	case wmMouseWheel:
		pt := point{int32(lo16(lp)), int32(hi16(lp))}
		pScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&pt)))
		d := hi16(wp)
		w := 1
		if d < 0 {
			w = -1
		}
		W.mouse(MouseEvent{Wheel: w}, int(pt.x), int(pt.y))
		return 0
	case wmClose:
		a.cfg.Save()
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func (w *winState) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	defer pEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	fb, fw, fh := w.app.Frame()
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	cw, ch := int(rc.right), int(rc.bottom)
	if cw <= 0 || ch <= 0 || fw == 0 {
		return
	}
	mode := w.app.scaleMode()
	var dw, dh int
	switch mode {
	case "stretch":
		dw, dh = cw, ch
	case "integer":
		n := cw / fw
		if m := ch / fh; m < n {
			n = m
		}
		if n < 1 {
			dw, dh = fw*ch/fh, ch
			if dw > cw {
				dw, dh = cw, fh*cw/fw
			}
		} else {
			dw, dh = fw*n, fh*n
		}
	default:
		dw, dh = cw, fh*cw/fw
		if dh > ch {
			dw, dh = fw*ch/fh, ch
		}
	}
	dx, dy := (cw-dw)/2, (ch-dh)/2
	w.dx, w.dy, w.dw, w.dh, w.fbW, w.fbH = dx, dy, dw, dh, fw, fh
	const blackness = 0x00000042
	if dx > 0 {
		pPatBlt.Call(hdc, 0, 0, uintptr(dx), uintptr(ch), blackness)
		pPatBlt.Call(hdc, uintptr(dx+dw), 0, uintptr(cw-dx-dw), uintptr(ch), blackness)
	}
	if dy > 0 {
		pPatBlt.Call(hdc, 0, 0, uintptr(cw), uintptr(dy), blackness)
		pPatBlt.Call(hdc, 0, uintptr(dy+dh), uintptr(cw), uintptr(ch-dy-dh), blackness)
	}
	smooth := uintptr(3) // COLORONCOLOR
	if w.app.smooth() {
		smooth = 4 // HALFTONE
	}
	pSetStretchBltMode.Call(hdc, smooth)
	bi := bmiHeader{size: 40, width: int32(fw), height: -int32(fh), planes: 1, bits: 32}
	pStretchDIBits.Call(hdc, uintptr(dx), uintptr(dy), uintptr(dw), uintptr(dh), 0, 0, uintptr(fw), uintptr(fh),
		uintptr(unsafe.Pointer(&fb[0])), uintptr(unsafe.Pointer(&bi)), 0, 0x00CC0020)
}

func (a *App) scaleMode() string { a.mu.Lock(); defer a.mu.Unlock(); return a.cfg.Scale }
func (a *App) smooth() bool      { a.mu.Lock(); defer a.mu.Unlock(); return a.cfg.Smooth }

func (w *winState) setFullscreen(on bool) {
	if on == w.fullscreen || w.hwnd == 0 {
		return
	}
	if on {
		st, _, _ := pGetWindowLongPtrW.Call(w.hwnd, gwlStyle)
		w.saveStyle = st
		pGetWindowRect.Call(w.hwnd, uintptr(unsafe.Pointer(&w.saveRect)))
		hm, _, _ := pMonitorFromWindow.Call(w.hwnd, 2)
		mi := monitorInfo{size: 40}
		pGetMonitorInfoW.Call(hm, uintptr(unsafe.Pointer(&mi)))
		pSetWindowLongPtrW.Call(w.hwnd, gwlStyle, wsPopup|wsVisible)
		pSetWindowPos.Call(w.hwnd, 0, uintptr(mi.monitor.left), uintptr(mi.monitor.top),
			uintptr(mi.monitor.right-mi.monitor.left), uintptr(mi.monitor.bottom-mi.monitor.top), 0x20|0x40)
	} else {
		pSetWindowLongPtrW.Call(w.hwnd, gwlStyle, w.saveStyle)
		r := w.saveRect
		pSetWindowPos.Call(w.hwnd, 0, uintptr(r.left), uintptr(r.top), uintptr(r.right-r.left), uintptr(r.bottom-r.top), 0x20|0x40)
	}
	w.fullscreen = on
}

func clipboardGet() string {
	if r, _, _ := pOpenClipboard.Call(W.hwnd); r == 0 {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(13)
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	var u []uint16
	for i := 0; i < 1<<24; i++ {
		c := *(*uint16)(unsafe.Pointer(p + uintptr(i)*2))
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

func clipboardSet(s string) {
	u := utf16.Encode([]rune(s))
	u = append(u, 0)
	if r, _, _ := pOpenClipboard.Call(W.hwnd); r == 0 {
		return
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGlobalAlloc.Call(2, uintptr(len(u)*2))
	if h == 0 {
		return
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return
	}
	for i, c := range u {
		*(*uint16)(unsafe.Pointer(p + uintptr(i)*2)) = c
	}
	pGlobalUnlock.Call(h)
	pEmptyClipboard.Call()
	pSetClipboardData.Call(13, h)
}

func soundLoop() {
	for wav := range W.sound {
		if len(wav) == 0 {
			continue
		}
		// SND_MEMORY | SND_NODEFAULT, synchronous (on this goroutine only)
		pPlaySoundW.Call(uintptr(unsafe.Pointer(&wav[0])), 0, 0x4|0x2)
		runtime.KeepAlive(wav)
	}
}

func playWAV(wav []byte) {
	select {
	case W.sound <- wav:
	default: // queue full: drop
	}
}

func drives() []string {
	m, _, _ := pGetLogicalDrives.Call()
	var out []string
	for i := 0; i < 26; i++ {
		if m&(1<<uint(i)) != 0 {
			out = append(out, string(rune('A'+i))+`:\`)
		}
	}
	return out
}

func runGUI(a *App) {
	W.app = a
	W.sound = make(chan []byte, 32)
	go soundLoop()
	pSetProcessDPIAware.Call()
	plat = Platform{
		Clipboard: clipboardGet, SetClipboard: clipboardSet, PlayWAV: playWAV, Drives: drives,
		SetFullscreen: func(on bool) { W.setFullscreen(on) },
		Quit:          func() { pPostMessageW.Call(W.hwnd, wmClose, 0, 0) },
	}
	hinst, _, _ := pGetModuleHandleW.Call(0)
	cur, _, _ := pLoadCursorW.Call(0, 32512)
	icoBig := loadIcon(hinst, 11)
	icoSmall := loadIcon(hinst, 49)
	cls := utf16Ptr("GoTermWindow")
	wc := wndClassEx{size: uint32(unsafe.Sizeof(wndClassEx{})), style: 0x8 | 0x2 | 0x1, // DBLCLKS|HREDRAW|VREDRAW
		wndProc: syscall.NewCallback(wndProc), instance: hinst, icon: icoBig, iconSm: icoSmall, cursor: cur, className: cls}
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	_, fw, fh := a.Frame()
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	scale := 1
	if fw*2+40 < int(sw) && fh*2+80 < int(sh) {
		scale = 2
	}
	rc := rect{0, 0, int32(fw * scale), int32(fh * scale)}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rc)), wsOverlapped, 0, 0)
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(utf16Ptr("GoTerm"))),
		wsOverlapped, 0x80000000, 0x80000000, uintptr(rc.right-rc.left), uintptr(rc.bottom-rc.top), 0, 0, hinst, 0)
	if hwnd == 0 {
		return
	}
	W.hwnd = hwnd
	pShowWindow.Call(hwnd, 1)
	pUpdateWindow.Call(hwnd)
	if a.WantFullscreen() {
		W.setFullscreen(true)
	}
	pSetTimer.Call(hwnd, 1, 40, 0)
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// loadIcon loads the embedded icon (resource id 1) at the system's icon
// size given by the metric (SM_CXICON = 11, SM_CXSMICON = 49).
func loadIcon(hinst uintptr, metric uintptr) uintptr {
	sz, _, _ := pGetSystemMetrics.Call(metric)
	h, _, _ := pLoadImageW.Call(hinst, 1, 1, sz, sz, 0)
	return h
}
