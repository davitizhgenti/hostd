// Package paths is where hostd keeps things for the user it runs as: the
// one place that knows the XDG rules, so no module looks at the environment
// itself.
package paths

import (
	"os"
	"path/filepath"
)

// ConfigHome is $XDG_CONFIG_HOME, or ~/.config.
func ConfigHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// Config is hostd's config folder, ~/.config/hostd, with elements joined
// onto it: Config("apps") is where app files live.
func Config(elem ...string) string {
	return filepath.Join(append([]string{ConfigHome(), "hostd"}, elem...)...)
}

// DataHome is $XDG_DATA_HOME, or ~/.local/share.
func DataHome() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// State is hostd's state folder: $XDG_STATE_HOME/hostd, or
// ~/.local/state/hostd ("hostd-state" where there is no home).
func State() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "hostd")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hostd-state"
	}
	return filepath.Join(home, ".local", "state", "hostd")
}
