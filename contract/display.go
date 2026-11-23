package contract

import (
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// The display module: windows, screens, input. What the other modules rely on.
const (
	DisplayModule = "display"

	ActionDisplayPower = "display.power" // args: {"on": bool}

	// The window actions, which the apps module sends so a started app
	// comes to the front.
	ActionWindowFocus = "window.focus" // args: {"instance": id, "front": bool}
	ActionWindowClose = "window.close" // args: {"instance": id}
	// ActionMenu opens hostd's on-screen menu.
	ActionMenu = "display.menu"

	// ScopeFront lets an action take the screen even while someone is using
	// it (app.open, instance.switch, window.focus: their front argument).
	ScopeFront = "display.front"
)

// Display is the display module's state, as others read it.
var Display = sdk.ReadRef[DisplayState]{Module: DisplayModule, Name: "display"}

// DisplayState is the part of the display module's state others read.
type DisplayState struct {
	// Attached: the compositor is there.
	Attached bool `json:"attached"`
	// Focused is the instance in front ("" for a window hostd did not start).
	Focused string          `json:"focused_instance"`
	Outputs []DisplayOutput `json:"outputs"`
	// Present: someone used the screen lately. LastInput is when (zero: not
	// since hostd started).
	Present   bool      `json:"present"`
	LastInput time.Time `json:"last_input"`
}

// DisplayOutput is a screen.
type DisplayOutput struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Power  bool   `json:"power"` // false: off (power saving)
}
