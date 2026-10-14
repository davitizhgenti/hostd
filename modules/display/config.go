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
type Config struct {
	IdleAfter sdk.Duration      `toml:"idle_after"`
	Hold      sdk.Duration      `toml:"hold"`
	Keys      map[string]string `toml:"keys"`
	Buttons   map[string]string `toml:"buttons"`
}

// Check reports what is wrong with c, as hostd.toml should say it.
func (c *Config) Check() error {
	switch {
	case c.IdleAfter < 0:
		return fmt.Errorf("idle_after is negative")
	case c.Hold < 0:
		return fmt.Errorf("hold is negative")
	}
	_, _, err := Bindings(c.Keys, c.Buttons)
	return err
}
