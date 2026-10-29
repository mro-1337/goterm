//go:build engine

package main

import (
	"os"
	_ "time/tzdata"
)

// Android: no window of our own; the Kotlin shell drives us over stdio.
func runGUI(a *App) {
	engineEnvSetup()
	if engineDownDir != "" && a.cfg.DownDir == "" {
		a.cfg.DownDir = engineDownDir
	}
	if d := os.Getenv("GOTERM_UPLOADDIR"); d != "" && a.cfg.LastDir == "" {
		a.cfg.LastDir = d
	}
	runEngine(a, os.Stdin, os.Stdout)
}
