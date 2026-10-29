package main

import (
	"fmt"
)

// Tabs: several BBS sessions can stay connected at once. The App's
// sess/term/parser/audio/modals fields always describe the *active* tab;
// switching saves them into the old Tab and loads the new one. A tab
// without a session shows the dialing directory.

type Tab struct {
	sess   *Session
	term   *Screen
	parser *Parser
	audio  *AudioAPC
	modals []Modal
}

func (a *App) initTabs() {
	a.tabs = []*Tab{{}}
	a.cur = 0
}

func (a *App) saveTab() {
	t := a.tabs[a.cur]
	t.sess, t.term, t.parser, t.audio, t.modals = a.sess, a.term, a.parser, a.audio, a.modals
}

func (a *App) loadTab(i int) {
	if a.cur >= 0 && a.cur < len(a.tabs) && a.tabs[a.cur].audio != nil {
		a.tabs[a.cur].audio.SetHidden(true)
	}
	a.cur = i
	t := a.tabs[i]
	a.sess, a.term, a.parser, a.audio, a.modals = t.sess, t.term, t.parser, t.audio, t.modals
	a.selecting = false
	if t.audio != nil {
		t.audio.SetHidden(false)
	}
	if t.sess != nil {
		t.sess.active = false
	}
	if a.term == nil {
		a.term = a.newTerm(screenModeByName(a.cfg.Mode))
	}
	if a.sess == nil && len(a.modals) == 0 {
		a.modals = []Modal{a.dir}
	}
	a.dirty = true
}

func (a *App) switchTab(i int) {
	if i < 0 || i >= len(a.tabs) || i == a.cur {
		return
	}
	a.saveTab()
	a.loadTab(i)
}

// newTab opens an empty tab showing the dialing directory.
func (a *App) newTab() {
	a.saveTab()
	a.tabs = append(a.tabs, &Tab{})
	a.cur = len(a.tabs) - 1
	a.sess, a.parser, a.audio = nil, nil, nil
	a.term = a.newTerm(screenModeByName(a.cfg.Mode))
	a.modals = []Modal{a.dir}
	a.selecting = false
	a.dirty = true
	// loadTab semantics for the old tab's audio
	for i, t := range a.tabs {
		if i != a.cur && t.audio != nil {
			t.audio.SetHidden(true)
		}
	}
}

func (a *App) removeTab() {
	if len(a.tabs) < 2 {
		return
	}
	i := a.cur
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if i >= len(a.tabs) {
		i = len(a.tabs) - 1
	}
	a.cur = -1
	a.loadTab(i)
}

// closeTab hangs up the current tab and removes it (the last tab stays,
// showing the directory).
func (a *App) closeTab() {
	if a.sess != nil {
		a.confirm("Close tab", "Disconnect from "+a.sess.entry.Name+" and close this tab?", func(a *App) {
			a.hangupNow()
			// A pending "save to directory?" prompt keeps the tab open.
			if len(a.tabs) > 1 && len(a.modals) == 1 {
				a.removeTab()
			}
		})
		return
	}
	a.removeTab()
}

// onTab runs fn (which pushes/pops modals) against the modal stack of the
// tab that owns s, whether or not it is the active one. a.mu must be held.
func (s *Session) onTab(fn func()) {
	a := s.a
	if a.sess == s {
		fn()
		return
	}
	for _, t := range a.tabs {
		if t.sess == s {
			saved := a.modals
			a.modals = t.modals
			fn()
			t.modals = a.modals
			a.modals = saved
			a.dirty = true
			return
		}
	}
}

func (a *App) barOn() bool { return len(a.tabs) > 1 }

func (a *App) tabLabel(i int) string {
	t := a.tabs[i]
	sess := t.sess
	if i == a.cur {
		sess = a.sess
	}
	name := "(directory)"
	mark := ""
	if sess != nil {
		name = orStr(sess.entry.Name, sess.entry.Host)
		if sess.stopped || sess.conn == nil && !sess.dialing && !sess.local {
			mark = "!"
		} else if sess.active {
			mark = "*"
		}
	}
	return fmt.Sprintf("%d:%s%s", i+1, mark, name)
}

// tabSpans returns the [x0,x1) cell range of each tab label in a bar of
// the given width.
func (a *App) tabSpans(cols int) [][2]int {
	n := len(a.tabs)
	each := cols / n
	if each > 24 {
		each = 24
	}
	if each < 4 {
		each = 4
	}
	sp := make([][2]int, n)
	x := 0
	for i := range sp {
		sp[i] = [2]int{x, x + each}
		x += each
	}
	return sp
}

func (a *App) drawTabBar(c *Canvas) {
	c.fill(0, 0, c.W(), 1, ' ', rBackdrop)
	for i, sp := range a.tabSpans(c.W()) {
		role := rFrame
		if i == a.cur {
			role = rSelect
		}
		w := sp[1] - sp[0] - 1
		c.fill(sp[0], 0, w, 1, ' ', role)
		c.strClip(sp[0]+1, 0, w-3, a.tabLabel(i), role)
		c.put(sp[1]-3, 0, 'x', role) // close button
	}
}

// tabAt maps a bar cell column to a tab index (-1 if none).
func (a *App) tabAt(x, cols int) int {
	for i, sp := range a.tabSpans(cols) {
		if x >= sp[0] && x < sp[1] {
			return i
		}
	}
	return -1
}

// tabKey handles tab shortcuts; reports whether the key was used.
func (a *App) tabKey(ev KeyEvent) bool {
	if !ev.Alt || ev.Ctrl {
		return false
	}
	switch {
	case ev.Ch == 't' || ev.Ch == 'T':
		a.newTab()
	case ev.Ch == 'w' || ev.Ch == 'W':
		a.closeTab()
	case ev.Ch >= '1' && ev.Ch <= '9':
		a.switchTab(int(ev.Ch - '1'))
	case ev.Key == KeyRight:
		a.switchTab((a.cur + 1) % len(a.tabs))
	case ev.Key == KeyLeft:
		a.switchTab((a.cur + len(a.tabs) - 1) % len(a.tabs))
	default:
		return false
	}
	return true
}
