package daemon

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/internal/gpu"
	"github.com/davitizhgenti/hostd/internal/notify"
	"github.com/davitizhgenti/hostd/internal/secrets"
	"github.com/davitizhgenti/hostd/internal/systemd"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units"
	"github.com/davitizhgenti/hostd/modules/audio"
	"github.com/davitizhgenti/hostd/modules/automation"
	"github.com/davitizhgenti/hostd/modules/demo"
	"github.com/davitizhgenti/hostd/modules/deploy"
	"github.com/davitizhgenti/hostd/modules/display"
	"github.com/davitizhgenti/hostd/sdk"
)

// A builtin is a module compiled into hostd. Its settings are its own
// section of hostd.toml ([name]), decoded into what config returns and
// checked by the module (a Check method); build makes the module from them
// and what hostd provides (env). Adding a module is adding one here.
type builtin struct {
	name      string
	byDefault bool       // loaded when hostd.toml names no modules
	config    func() any // a pointer to the module's settings, with defaults; nil: it has none
	build     func(e *env, conf any) (sdk.Module, error)
	// legacy maps hostd.toml keys of earlier versions to this module's
	// settings ("idle_after" became [display] idle_after), for one release.
	legacy []legacyKey
}

type legacyKey struct {
	old  []string // e.g. {"input", "keys"}
	new  string   // the key in the module's section
	copy func(c *Config, conf any)
}

// env is what hostd gives the modules it builds.
type env struct {
	runtimeDir, stateDir, configDir, socket, home string
	log                                           *slog.Logger
	systemd                                       *systemd.UserSystemd // shared; Run closes it
}

var builtins = []builtin{
	{name: "apps", byDefault: true,
		config: func() any { return &apps.Config{} },
		legacy: []legacyKey{
			{[]string{"browser"}, "browser", func(c *Config, conf any) { conf.(*apps.Config).Browser = c.Browser }},
			{[]string{"gamescope"}, "gamescope", func(c *Config, conf any) { conf.(*apps.Config).Gamescope = c.Gamescope }},
		},
		build: func(e *env, conf any) (sdk.Module, error) {
			c := conf.(*apps.Config)
			engine := &podman.EngineAPI{Socket: podman.DefaultEngineSocket(e.runtimeDir)}
			return apps.New(apps.Options{
				DesktopDirs: apps.DefaultDesktopDirs(), AppsDir: apps.DefaultAppsDir(), SecretsDir: secrets.DefaultDir(), Logger: e.log,
				Backends: map[string]apps.Backend{
					apps.RunnerExec: &units.ExecRunner{Systemd: e.systemd, RuntimeDir: e.runtimeDir, HomeDir: e.home,
						StateDir: e.stateDir, Browser: c.Browser, Gamescope: c.Gamescope},
					apps.RunnerDocker:  &podman.DockerRunner{Docker: engine},
					apps.RunnerCompose: &podman.ComposeRunner{Docker: engine, Socket: engine.Socket},
				},
			}), nil
		}},
	{name: "display", byDefault: true,
		config: func() any { return &display.Config{} },
		legacy: []legacyKey{
			{[]string{"idle_after"}, "idle_after", func(c *Config, conf any) { conf.(*display.Config).IdleAfter = sdk.Duration(c.IdleAfter) }},
			{[]string{"input", "hold"}, "hold", func(c *Config, conf any) { conf.(*display.Config).Hold = sdk.Duration(c.Input.Hold) }},
			{[]string{"input", "keys"}, "keys", func(c *Config, conf any) { conf.(*display.Config).Keys = c.Input.Keys }},
			{[]string{"input", "buttons"}, "buttons", func(c *Config, conf any) { conf.(*display.Config).Buttons = c.Input.Buttons }},
		},
		build: func(e *env, conf any) (sdk.Module, error) {
			c := conf.(*display.Config)
			profiles, err := evdev.LoadProfiles(filepath.Join(e.configDir, "controllers"))
			if err != nil {
				return nil, err
			}
			keys, buttons, err := display.Bindings(c.Keys, c.Buttons)
			if err != nil {
				return nil, err
			}
			return display.New(display.Options{Connect: display.SwayConnector(e.runtimeDir), Logger: e.log,
				Input: &evdev.Evdev{Profiles: profiles}, Profiles: profiles, IdleAfter: time.Duration(c.IdleAfter),
				Notifier: &notify.Desktop{RuntimeDir: e.runtimeDir}, Keys: keys, Buttons: buttons,
				HoldFor: time.Duration(c.Hold), GPU: gpu.System{}}), nil
		}},
	{name: "audio", byDefault: true, build: func(e *env, _ any) (sdk.Module, error) {
		return audio.New(audio.Options{Backend: &audio.WirePlumber{}, Logger: e.log,
			Media: &audio.DBusMedia{RuntimeDir: e.runtimeDir}}), nil
	}},
	{name: "deploy", byDefault: true, build: func(e *env, _ any) (sdk.Module, error) {
		return deploy.New(deploy.Options{Logger: e.log, SecretsDir: secrets.DefaultDir(), AppsDir: apps.DefaultAppsDir()}), nil
	}},
	{name: "automation", byDefault: true, build: func(e *env, _ any) (sdk.Module, error) {
		return automation.New(automation.Options{Logger: e.log, URL: "unix://" + e.socket}), nil
	}},
	{name: "demo", build: func(*env, any) (sdk.Module, error) { return demo.New(), nil }},
}

func builtinNamed(name string) *builtin {
	for i := range builtins {
		if builtins[i].name == name {
			return &builtins[i]
		}
	}
	return nil
}

func builtinNames() []string {
	out := make([]string, len(builtins))
	for i, b := range builtins {
		out[i] = b.name
	}
	return out
}

func defaultModules() []string {
	var out []string
	for _, b := range builtins {
		if b.byDefault {
			out = append(out, b.name)
		}
	}
	return out
}

// moduleConfigs decodes and checks the section of every built-in module, so
// a mistake in any of them stops hostd before anything starts. It returns
// the settings by module, and a note for each key of an earlier version
// that was used.
func moduleConfigs(c Config) (map[string]any, []string, error) {
	confs := map[string]any{}
	var notes []string
	for _, b := range builtins {
		_, has := c.sections[b.name]
		if b.config == nil {
			if has {
				return nil, nil, fmt.Errorf("%s: [%s]: the %s module has no settings", c.path, b.name, b.name)
			}
			continue
		}
		conf := b.config()
		for _, l := range b.legacy {
			if !c.md.IsDefined(l.old...) {
				continue
			}
			oldName := strings.Join(l.old, ".")
			if has && c.sectionsMD.IsDefined(b.name, l.new) {
				return nil, nil, fmt.Errorf("%s: %s and [%s] %s are the same setting; keep [%s] %s", c.path, oldName, b.name, l.new, b.name, l.new)
			}
			l.copy(&c, conf)
			notes = append(notes, fmt.Sprintf("%s: %s is now [%s] %s; the old name works until the next release", c.path, oldName, b.name, l.new))
		}
		if has {
			if err := c.sectionsMD.PrimitiveDecode(c.sections[b.name], conf); err != nil {
				return nil, nil, fmt.Errorf("%s: [%s]: %w", c.path, b.name, err)
			}
			var unknown []string
			for _, k := range c.sectionsMD.Undecoded() {
				if len(k) > 1 && k[0] == b.name {
					unknown = append(unknown, k.String())
				}
			}
			if len(unknown) > 0 {
				sort.Strings(unknown)
				return nil, nil, fmt.Errorf("%s: unknown key(s): %s", c.path, strings.Join(unknown, ", "))
			}
		}
		if ch, ok := conf.(interface{ Check() error }); ok {
			if err := ch.Check(); err != nil {
				return nil, nil, fmt.Errorf("%s: [%s] %w", c.path, b.name, err)
			}
		}
		confs[b.name] = conf
	}
	return confs, notes, nil
}

// decodeSections reads the [module] sections of hostd.toml, for
// moduleConfigs.
func decodeSections(data []byte) (map[string]toml.Primitive, toml.MetaData, error) {
	var raw map[string]toml.Primitive
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, md, err
	}
	out := map[string]toml.Primitive{}
	for name, p := range raw {
		if builtinNamed(name) != nil {
			out[name] = p
		}
	}
	return out, md, nil
}
