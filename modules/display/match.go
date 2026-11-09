package display

import (
	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/proc"
)

// subject is what the match rules look at for a window.
func subject(w Window) proc.Subject {
	return proc.Subject{PID: w.PID, Class: w.Class, AppID: w.AppID, Title: w.Title}
}

// resolveWindow finds a window's instance (see proc.Resolve).
func resolveWindow(procRoot string, w Window, live []contract.Instance) string {
	return proc.Resolve(procRoot, subject(w), live)
}
