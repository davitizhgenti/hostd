package daemon

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/modules/audio"
	"github.com/davitizhgenti/hostd/modules/automation"
	"github.com/davitizhgenti/hostd/modules/demo"
	"github.com/davitizhgenti/hostd/modules/deploy"
	"github.com/davitizhgenti/hostd/modules/display"
)

// builtins are the modules compiled into hostd. Adding one is adding its
// package's Builtin here.
var builtins = []host.Builtin{
	apps.Builtin,
	display.Builtin,
	audio.Builtin,
	deploy.Builtin,
	automation.Builtin,
	demo.Builtin,
}

func builtinNamed(name string) *host.Builtin {
	for i := range builtins {
		if builtins[i].Name == name {
			return &builtins[i]
		}
	}
	return nil
}

func builtinNames() []string {
	out := make([]string, len(builtins))
	for i, b := range builtins {
		out[i] = b.Name
	}
	return out
}

func defaultModules() []string {
	var out []string
	for _, b := range builtins {
		if b.Default {
			out = append(out, b.Name)
		}
	}
	return out
}

// moduleConfigs decodes and checks the section of every built-in module, so
// a mistake in any of them stops hostd before anything starts. It returns
// the settings by module.
func moduleConfigs(c Config) (map[string]any, error) {
	confs := map[string]any{}
	for _, b := range builtins {
		_, has := c.sections[b.Name]
		if b.Settings == nil {
			if has {
				return nil, fmt.Errorf("%s: [%s]: the %s module has no settings", c.path, b.Name, b.Name)
			}
			continue
		}
		conf := b.Settings()
		if has {
			if err := c.sectionsMD.PrimitiveDecode(c.sections[b.Name], conf); err != nil {
				return nil, fmt.Errorf("%s: [%s]: %w", c.path, b.Name, err)
			}
			var unknown []string
			for _, k := range c.sectionsMD.Undecoded() {
				if len(k) > 1 && k[0] == b.Name {
					unknown = append(unknown, k.String())
				}
			}
			if len(unknown) > 0 {
				sort.Strings(unknown)
				return nil, fmt.Errorf("%s: unknown key(s): %s", c.path, strings.Join(unknown, ", "))
			}
		}
		if ch, ok := conf.(interface{ Check() error }); ok {
			if err := ch.Check(); err != nil {
				return nil, fmt.Errorf("%s: [%s] %w", c.path, b.Name, err)
			}
		}
		confs[b.Name] = conf
	}
	return confs, nil
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
