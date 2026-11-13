package apps

import (
	"path/filepath"

	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/modules/apps/catalog"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units"
	"github.com/davitizhgenti/hostd/sdk"
)

// Builtin is how hostd builds the apps module: with the exec runner (systemd
// units) and the docker and compose runners (the Podman socket).
var Builtin = host.Builtin{
	Name:     "apps",
	Default:  true,
	Settings: func() any { return &Config{} },
	New: func(h *host.Host, conf any) (sdk.Module, error) {
		c := conf.(*Config)
		engine := &podman.EngineAPI{Socket: podman.DefaultEngineSocket(h.RuntimeDir)}
		return New(Options{
			DesktopDirs: catalog.DefaultDesktopDirs(), Sandboxes: catalog.DefaultSandboxes(), AppsDir: filepath.Join(h.ConfigDir, "apps"), SecretsDir: h.Secrets.Dir, Logger: h.Log,
			Backends: map[string]Backend{
				RunnerExec: &units.ExecRunner{Systemd: h.Systemd, RuntimeDir: h.RuntimeDir, HomeDir: h.Home,
					StateDir: h.StateDir, Browser: c.Browser, Gamescope: c.Gamescope},
				RunnerDocker:  &podman.DockerRunner{Docker: engine},
				RunnerCompose: &podman.ComposeRunner{Docker: engine, Socket: engine.Socket},
			},
		}), nil
	},
}
