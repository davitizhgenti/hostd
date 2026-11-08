package paths

import (
	"path/filepath"
	"testing"
)

func TestPaths(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	for got, want := range map[string]string{
		Config():       "/home/u/.config/hostd",
		Config("apps"): "/home/u/.config/hostd/apps",
		DataHome():     "/home/u/.local/share",
		State():        "/home/u/.local/state/hostd",
		ConfigHome():   "/home/u/.config",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "/x/c")
	t.Setenv("XDG_DATA_HOME", "/x/d")
	t.Setenv("XDG_STATE_HOME", "/x/s")
	for got, want := range map[string]string{
		Config("rules.toml"): filepath.Join("/x/c", "hostd", "rules.toml"),
		DataHome():           "/x/d",
		State():              "/x/s/hostd",
	} {
		if got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	}
}
