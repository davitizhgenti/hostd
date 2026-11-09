package contract

import (
	"context"
	"encoding/json"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// The display module: windows, screens, input. What the other modules rely on.
const (
	DisplayModule = "display"

	ReadDisplay = "display" // the display's state: DisplayState

	ActionDisplayPower = "display.power" // args: {"on": bool}
)

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

// ReadDisplayState asks the display module for its state.
func ReadDisplayState(ctx context.Context, core sdk.Core) (DisplayState, error) {
	var st DisplayState
	raw, err := core.Read(ctx, DisplayModule, ReadDisplay, nil)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(raw, &st)
}
