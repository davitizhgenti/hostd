package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is ~/.config/hostd/hostd.toml. Every field is optional.
//
//	# Which built-in modules to load. A headless machine leaves out
//	# display and audio.
//	modules = ["apps", "display", "audio", "deploy", "automation"]
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
//	# Each built-in module's settings are its own section, decoded and
//	# checked by the module (internal/daemon/modules.go): apps.Config,
//	# display.Config. The top-level idle_after, browser, gamescope and
//	# [input] of earlier versions still work for one release.
//	[apps]
//	browser = ["chromium", "--kiosk", "--user-data-dir={profile}", "{url}"]
//	[display]
//	idle_after = "5m"
//	[display.keys]
//	"F1" = "display.menu"
//
//	# External modules: programs that speak the module protocol
//	# (sdk/PROTOCOL.md), in any language.
//	[external.lights]
//	command = ["python3", "/home/screen/hostd/modules/lights.py"]
//	env = { LIGHTS_BRIDGE = "192.168.1.20" }
//	transport = "stdio"   # optional: talk over the program's stdin/stdout, not a socket
type Config struct {
	Modules     []string            `toml:"modules"`
	Listen      *string             `toml:"listen"`
	HoldWindow  duration            `toml:"hold_window"`
	HoldWindows map[string]duration `toml:"hold_windows"`
	// Earlier versions' places for module settings (see builtin.legacy).
	IdleAfter duration `toml:"idle_after"`
	Browser   []string `toml:"browser"`
	Gamescope []string `toml:"gamescope"`
	Input     struct {
		Keys    map[string]string `toml:"keys"`
		Buttons map[string]string `toml:"buttons"`
		Hold    duration          `toml:"hold"`
	} `toml:"input"`
	External map[string]External `toml:"external"`

	path       string
	md         toml.MetaData             // of the whole file, to find keys of earlier versions
	sections   map[string]toml.Primitive // [apps], [display]...: each module's own settings
	sectionsMD toml.MetaData
}

// laterModules are in the design but not in this version yet; naming one
// gets a clear message rather than "unknown".
var laterModules = map[string]string{}

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
		c.md = md
		if c.sections, c.sectionsMD, err = decodeSections(path); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		// A module's section is the module's to check (moduleConfigs).
		var keys []string
		for _, k := range md.Undecoded() {
			if builtinNamed(k[0]) == nil {
				keys = append(keys, k.String())
			}
		}
		if len(keys) > 0 {
			sort.Strings(keys)
			return Config{}, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(keys, ", "))
		}
	}
	c.path = path
	if c.Modules == nil {
		c.Modules = defaultModules()
	}
	seen := map[string]bool{}
	for _, m := range c.Modules {
		switch {
		case seen[m]:
			return Config{}, fmt.Errorf("%s: module %q is listed twice", path, m)
		case laterModules[m] != "":
			return Config{}, fmt.Errorf("%s: module %q comes in a later version (%s); remove it for now", path, m, laterModules[m])
		case builtinNamed(m) == nil:
			return Config{}, fmt.Errorf("%s: no module %q; available: %s", path, m, strings.Join(builtinNames(), ", "))
		}
		seen[m] = true
	}
	for name, x := range c.External {
		switch {
		case builtinNamed(name) != nil:
			return Config{}, fmt.Errorf("%s: external module %q has a built-in module's name", path, name)
		case !externalName.MatchString(name):
			return Config{}, fmt.Errorf("%s: external module %q: use lowercase letters, digits and _", path, name)
		case len(x.Command) == 0:
			return Config{}, fmt.Errorf("%s: external module %q needs a command", path, name)
		case x.Transport != "" && x.Transport != "socket" && x.Transport != "stdio":
			return Config{}, fmt.Errorf("%s: external module %q: transport %q: use \"socket\" or \"stdio\"", path, name, x.Transport)
		}
	}
	if _, _, err := moduleConfigs(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// External is an external module's settings.
type External struct {
	Command   []string          `toml:"command"`
	Env       map[string]string `toml:"env"`
	Transport string            `toml:"transport"` // "socket" (default) or "stdio"
}

var externalName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

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
