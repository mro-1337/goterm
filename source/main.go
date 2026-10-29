//go:build !cli

package main

import (
	"fmt"
	"os"
	"runtime"
)

func init() { runtime.LockOSThread() }

func main() {
	sa := parseStartArgs(os.Args[1:])
	if sa.Bad {
		fmt.Fprintln(os.Stderr, "usage: goterm [-mode 80x25] [-term TYPE] [-user NAME] [host[:port] | host port | ssh://user@host | rlogin://host | entry-name]")
		os.Exit(2)
	}
	cfg := LoadConfig()
	if sa.Mode != "" {
		cfg.Mode = sa.Mode
	}
	if sa.Term != "" {
		cfg.Term = sa.Term
	}
	app := NewApp(cfg)
	if sa.Target != "" {
		e := entryFromTarget(cfg, sa.Target)
		if sa.User != "" {
			e.User = sa.User
		}
		app.mu.Lock()
		app.modals = nil
		app.connect(e, -1)
		app.mu.Unlock()
	}
	runGUI(app)
}
