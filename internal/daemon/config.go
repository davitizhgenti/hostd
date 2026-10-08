package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is ~/.config/hostd/hostd.toml. Every field is optional.
//
//	# Which built-in modules to load. A headless machine leaves out
//	# display and audio.
//	modules = ["apps", "display", "audio"]
//
//	# TCP address of the API for the home network; "" turns it off.
//	listen = ":7300"
//
//	# How long a person's change holds a resource against automation,
//	# and per resource key prefix.
//	hold_window = "3m"
//	[hold_windows]
//	"audio." = "1m"
//
//	# After this long without keyboard, mouse or controller input nobody
//	# is at the screen, and remote launches come to the front again.
//	idle_after = "5m"
type Config struct {
	Modules     []string            `toml:"modules"`
	Listen      *string             `toml:"listen"`
	HoldWindow  duration            `toml:"hold_window"`
	HoldWindows map[string]duration `toml:"hold_windows"`
	IdleAfter   duration            `toml:"idle_after"`
}

// builtinModules are the modules this hostd can load, in start order
// preference; DefaultModules are loaded when the config names none.
var (
	builtinModules = []string{"apps", "display", "audio", "demo"}
	DefaultModules = []string{"apps", "display", "audio"}
)

// laterModules are in the design but not in this version yet; naming one
// gets a clear message rather than "unknown".
var laterModules = map[string]string{"deploy": "M4", "automation": "M5"}

type duration time.Duration

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("%q is not a duration such as \"3m\" or \"90s\"", b)
	}
	if v < 0 {
		return fmt.Errorf("%q is negative", b)
	}
	*d = duration(v)
	return nil
}

// DefaultConfigPath is $XDG_CONFIG_HOME/hostd/hostd.toml.
func DefaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hostd", "hostd.toml")
}

// LoadConfig reads the config file; a missing file means all defaults.
// Unknown keys and modules are errors, so a typo never goes unnoticed.
func LoadConfig(path string) (Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c = Config{}
	case err != nil:
		return Config{}, fmt.Errorf("%s: %w", path, err)
	default:
		if und := md.Undecoded(); len(und) > 0 {
			keys := make([]string, len(und))
			for i, k := range und {
				keys[i] = k.String()
			}
			sort.Strings(keys)
			return Config{}, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(keys, ", "))
		}
	}
	if c.Modules == nil {
		c.Modules = DefaultModules
	}
	seen := map[string]bool{}
	for _, m := range c.Modules {
		switch {
		case seen[m]:
			return Config{}, fmt.Errorf("%s: module %q is listed twice", path, m)
		case laterModules[m] != "":
			return Config{}, fmt.Errorf("%s: module %q comes in a later version (%s); remove it for now", path, m, laterModules[m])
		case !contains(builtinModules, m):
			return Config{}, fmt.Errorf("%s: no module %q; available: %s", path, m, strings.Join(builtinModules, ", "))
		}
		seen[m] = true
	}
	return c, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// holdWindows converts the config's per-prefix windows for the core.
func (c Config) holdWindows() map[string]time.Duration {
	if len(c.HoldWindows) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(c.HoldWindows))
	for k, v := range c.HoldWindows {
		out[k] = time.Duration(v)
	}
	return out
}
