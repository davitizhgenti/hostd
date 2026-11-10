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
		// The controllers' player slots, kept across restarts of hostd.
		players := &evdev.Players{File: filepath.Join(h.StateDir, "players.json")}
		if err := players.Load(); err != nil {
			h.Log.Warn("the saved player slots cannot be read; starting without them", "err", err)
		}
		keys, buttons, err := Bindings(c.Keys, c.Buttons)
		if err != nil {
			return nil, err
		}
		playerButtons, err := PlayerBindings(buttons, c.Players)
		if err != nil {
			return nil, err
		}
		return New(Options{Connect: SwayConnector(h.RuntimeDir), Logger: h.Log,
			Input: &evdev.Evdev{Profiles: profiles, Players: players}, Players: players, Profiles: profiles, IdleAfter: time.Duration(c.IdleAfter),
			Notifier: &notify.Desktop{RuntimeDir: h.RuntimeDir}, Keys: keys, Buttons: buttons, PlayerButtons: playerButtons,
			HoldFor: time.Duration(c.Hold), GPU: gpu.System{}}), nil
	},
}
