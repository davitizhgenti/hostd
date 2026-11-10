package display

import (
	"fmt"

	"github.com/davitizhgenti/hostd/sdk"
)

// Config is the display module's section of hostd.toml:
//
//	[display]
//	idle_after = "5m"   # no keyboard, mouse or controller input this long: nobody is at the screen
//	hold = "600ms"      # how long a controller button is held before hostd acts on it
//	[display.keys]      # keys and buttons, mapped to display actions (see Bindings)
//	"F1" = "display.menu"
//	[display.buttons]
//	guide = "display.menu"
//	[display.players.2.buttons]   # what player 2's controller does differently
//	guide = ""                     # "" turns a binding off for that player
//	[display.outputs.HDMI-A-1]    # a screen's resolution, set whenever hostd meets the compositor
//	mode = "1920x1080@60"          # ("*" names every screen); every app then draws at this size
type Config struct {
	IdleAfter sdk.Duration      `toml:"idle_after"`
	Hold      sdk.Duration      `toml:"hold"`
	Keys      map[string]string `toml:"keys"`
	Buttons   map[string]string `toml:"buttons"`
	// Players change the button bindings for one controller, by its player
	// slot (see PlayerBindings).
	Players map[string]PlayerConfig `toml:"players"`
	// Outputs set screens' modes when hostd attaches to the compositor
	// (at start, and again when Sway restarts), so every app sees one size.
	Outputs map[string]OutputConfig `toml:"outputs"`
}

// OutputConfig is what hostd sets on one screen.
type OutputConfig struct {
	Mode string `toml:"mode"` // WIDTHxHEIGHT or WIDTHxHEIGHT@HZ
}

// PlayerConfig is what one player's controller binds differently.
type PlayerConfig struct {
	Buttons map[string]string `toml:"buttons"`
}

// Check reports what is wrong with c, as hostd.toml should say it.
func (c *Config) Check() error {
	switch {
	case c.IdleAfter < 0:
		return fmt.Errorf("idle_after is negative")
	case c.Hold < 0:
		return fmt.Errorf("hold is negative")
	}
	for name, o := range c.Outputs {
		if !reMode.MatchString(o.Mode) {
			return fmt.Errorf("outputs.%s: mode %q: use WIDTHxHEIGHT or WIDTHxHEIGHT@HZ, e.g. 1920x1080@60", name, o.Mode)
		}
	}
	_, buttons, err := Bindings(c.Keys, c.Buttons)
	if err != nil {
		return err
	}
	_, err = PlayerBindings(buttons, c.Players)
	return err
}
