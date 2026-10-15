package daemon

import (
	"bytes"
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

// Config is ~/.config/hostd/hostd.toml, with the files in hostd.d/ beside
// it merged in (see readConfig). Every field is optional.
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
//	# display.Config.
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
	External    map[string]External `toml:"external"`

	path       string
	sections   map[string]toml.Primitive // [apps], [display]...: each module's own settings
	sectionsMD toml.MetaData
}

// movedKeys are top-level keys of 0.1 that are now in a module's section.
var movedKeys = map[string]string{
	"idle_after": "[display] idle_after",
	"input":      "[display]: hold, [display.keys] and [display.buttons]",
	"browser":    "[apps] browser",
	"gamescope":  "[apps] gamescope",
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
	data, err := readConfig(path)
	var md toml.MetaData
	if err == nil {
		md, err = toml.Decode(string(data), &c)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c = Config{}
	case err != nil:
		return Config{}, fmt.Errorf("%s: %w", path, err)
	default:
		if c.sections, c.sectionsMD, err = decodeSections(data); err != nil {
			return Config{}, fmt.Errorf("%s: %w", path, err)
		}
		// A module's section is the module's to check (moduleConfigs).
		var keys []string
		for _, k := range md.Undecoded() {
			if to := movedKeys[k[0]]; to != "" {
				return Config{}, fmt.Errorf("%s: %s is now %s (moved in 0.2)", path, k[0], to)
			}
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
	if _, err := moduleConfigs(c); err != nil {
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

// DropInDir is where files that add to hostd.toml go: hostd.d/ beside it.
// An add-on, or a person, puts an external module or a module's settings
// there in a file of its own, without editing hostd.toml.
func DropInDir(configPath string) string { return filepath.Join(filepath.Dir(configPath), "hostd.d") }

// readConfig returns hostd.toml with the files in hostd.d/ (*.toml, in name
// order) merged into it. A drop-in may hold module sections ([apps],
// [display]...) and [external.NAME]; anything set in two files is an error
// naming both. Without drop-ins it is hostd.toml as it is. Neither file
// existing is fs.ErrNotExist.
func readConfig(path string) ([]byte, error) {
	main, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	drops, _ := filepath.Glob(filepath.Join(DropInDir(path), "*.toml"))
	sort.Strings(drops)
	if len(drops) == 0 {
		return main, err
	}
	merged := map[string]any{}
	owner := map[string]string{} // setting -> the file that set it
	if main != nil {
		if _, err := toml.Decode(string(main), &merged); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		mark(merged, "", path, owner)
	}
	for _, f := range drops {
		var m map[string]any
		b, err := os.ReadFile(f)
		if err == nil {
			_, err = toml.Decode(string(b), &m)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for k := range m {
			if k != "external" && builtinNamed(k) == nil {
				return nil, fmt.Errorf("%s: %s belongs in %s; a drop-in holds module sections ([apps], [display]...) and [external.NAME]", f, k, path)
			}
		}
		if err := mergeInto(merged, m, "", f, owner); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(merged); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mark records file as the owner of every setting in m. An external module
// counts as one setting: its command and env come from one file.
func mark(m map[string]any, prefix, file string, owner map[string]string) {
	for k, v := range m {
		key := prefix + k
		if sub, ok := v.(map[string]any); ok && prefix != "external." {
			mark(sub, key+".", file, owner)
			continue
		}
		owner[key] = file
	}
}

func mergeInto(dst, src map[string]any, prefix, file string, owner map[string]string) error {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		key, v := prefix+k, src[k]
		sub, isTable := v.(map[string]any)
		if isTable && prefix != "external." {
			d, ok := dst[k].(map[string]any)
			if !ok {
				if _, taken := dst[k]; taken {
					return fmt.Errorf("%s is set in both %s and %s", key, owner[key], file)
				}
				d = map[string]any{}
				dst[k] = d
			}
			if err := mergeInto(d, sub, key+".", file, owner); err != nil {
				return err
			}
			continue
		}
		if _, taken := dst[k]; taken {
			return fmt.Errorf("%s is set in both %s and %s", key, owner[key], file)
		}
		dst[k] = v
		owner[key] = file
	}
	return nil
}
