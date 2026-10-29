package main

import (
	"fmt"
	"strconv"
)

// Host mouse reporting (xterm modes as implemented by SyncTERM): a BBS
// that turns on DECSET 9/1000/1002/1003 receives press/release/motion
// reports instead of GoTerm doing local selection. Hold Shift to select
// text locally anyway.

// decrqm answers CSI ? Ps $ p.
func (p *Parser) decrqm(mode int) {
	s := p.S
	b2 := func(on bool) int {
		if on {
			return 1
		}
		return 2
	}
	pm := 0
	switch mode {
	case 6:
		pm = b2(s.Origin)
	case 7:
		pm = b2(s.AutoWrap)
	case 25:
		pm = b2(s.CursorVisible)
	case 32:
		pm = b2(s.NoBright)
	case 33:
		pm = b2(s.ICE)
	case 35:
		pm = b2(s.NoBlink)
	case 9, 1000, 1002, 1003:
		pm = b2(p.MouseMode == mode)
	case 1001, 1004, 1005, 1007, 1015:
		pm = 2
	case 1006:
		pm = b2(p.MouseSGR && !p.MousePix)
	case 1016:
		pm = b2(p.MousePix)
	case 2004:
		pm = b2(p.BracketPaste)
	}
	p.reply(fmt.Sprintf("\x1b[?%d;%d$y", mode, pm))
}

// mouseReport builds the escape sequence for one mouse event, or nil.
// col,row are 0-based cells; px,py 0-based pixels inside the terminal.
// held is the bitmask of buttons currently down (before this event).
func (p *Parser) mouseReport(ev MouseEvent, col, row, px, py int, held int) []byte {
	if p.MouseMode == 0 {
		return nil
	}
	var button int
	release := false
	switch {
	case ev.Wheel != 0:
		if p.MouseMode == 9 {
			return nil
		}
		button = 64
		if ev.Wheel < 0 {
			button = 65
		}
	case ev.Down:
		button = ev.Button
	case ev.Up:
		if p.MouseMode == 9 {
			return nil
		}
		button, release = ev.Button, true
	case ev.Move:
		if p.MouseMode != 1002 && p.MouseMode != 1003 {
			return nil
		}
		b := -1
		for i := 0; i < 3; i++ {
			if held&(1<<uint(i)) != 0 {
				b = i
				break
			}
		}
		if b < 0 {
			if p.MouseMode == 1002 {
				return nil
			}
			b = 3 // motion with no button
		}
		button = b + 32
	default:
		return nil
	}
	if p.MouseMode != 9 {
		if ev.Alt {
			button |= 8
		}
		if ev.Ctrl {
			button |= 16
		}
	}
	x, y := col+1, row+1
	if !p.MouseSGR {
		if release {
			button = button&^3 | 3
		}
		cx, cy := col, row
		if cx > 222 {
			cx = 222
		}
		if cy > 222 {
			cy = 222
		}
		return []byte{0x1b, '[', 'M', byte(' ' + button), byte('!' + cx), byte('!' + cy)}
	}
	if p.MousePix {
		x, y = px+1, py+1
	}
	fin := "M"
	if release {
		fin = "m"
	}
	return []byte("\x1b[<" + strconv.Itoa(button) + ";" + strconv.Itoa(x) + ";" + strconv.Itoa(y) + fin)
}

// hostMouse forwards an event to the host if the BBS has asked for mouse
// input. Returns true when the event was consumed. a.mu held.
func (a *App) hostMouse(ev MouseEvent, px, py, cw, ch int) bool {
	s, p := a.sess, a.parser
	if s == nil || p == nil || s.local || p.MouseMode == 0 || ev.Shift || a.term.ViewOffset > 0 {
		return false
	}
	if len(a.modals) > 0 {
		return false
	}
	t := a.term
	col, row := clampInt(ev.X, 0, t.Cols-1), clampInt(ev.Y, 0, t.Rows-1)
	if ev.Wheel != 0 && p.MouseMode == 9 {
		return false // X10 has no wheel: scroll back locally
	}
	if ev.Move {
		key := [2]int{col, row}
		if p.MousePix {
			key = [2]int{px, py}
		}
		if key == a.mouseLast {
			return true
		}
		a.mouseLast = key
	} else {
		a.mouseLast = [2]int{-1, -1}
	}
	rep := p.mouseReport(ev, col, row, px, py, a.mouseBtns)
	switch {
	case ev.Down && ev.Button >= 0 && ev.Button < 3:
		a.mouseBtns |= 1 << uint(ev.Button)
	case ev.Up && ev.Button >= 0 && ev.Button < 3:
		a.mouseBtns &^= 1 << uint(ev.Button)
	}
	if rep != nil {
		s.send(rep)
	}
	return true
}
