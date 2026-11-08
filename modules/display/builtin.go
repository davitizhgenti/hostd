package display

import (
	"path/filepath"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
	"github.com/davitizhgenti/hostd/internal/gpu"
	"github.com/davitizhgenti/hostd/internal/host"
	"github.com/davitizhgenti/hostd/internal/notify"
	"github.com/davitizhgenti/hostd/sdk"
)

// Builtin is how hostd builds the display module: Sway, evdev controllers,
// desktop notices and the GPU's sysfs.
var Builtin = host.Builtin{
	Name:     "display",
	Default:  true,
	Settings: func() any { return &Config{} },
	New: func(h *host.Host, conf any) (sdk.Module, error) {
		c := conf.(*Config)
		profiles, err := evdev.LoadProfiles(filepath.Join(h.ConfigDir, "controllers"))
		if err != nil {
			return nil, err
		}
		keys, buttons, err := Bindings(c.Keys, c.Buttons)
		if err != nil {
			return nil, err
		}
		return New(Options{Connect: SwayConnector(h.RuntimeDir), Logger: h.Log,
			Input: &evdev.Evdev{Profiles: profiles}, Profiles: profiles, IdleAfter: time.Duration(c.IdleAfter),
			Notifier: &notify.Desktop{RuntimeDir: h.RuntimeDir}, Keys: keys, Buttons: buttons,
			HoldFor: time.Duration(c.Hold), GPU: gpu.System{}}), nil
	},
}
