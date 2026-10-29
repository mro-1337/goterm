//go:build !windows && !linux

package main

import (
	"fmt"
	"os"
)

// GoTerm's window is implemented with the Win32 API; on other systems only
// the headless test harness runs.
func runGUI(a *App) {
	fmt.Fprintln(os.Stderr, "GoTerm is a Windows program (build with GOOS=windows).")
	os.Exit(2)
}
