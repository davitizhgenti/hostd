package display

import (
	"context"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// The arguments of the module's actions. The handlers read what they need
// from the action; these types are what callers may send.
type (
	focusArgs struct {
		Instance string `json:"instance" desc:"instance ID, e.g. firefox or firefox#2"`
		Front    bool   `json:"front,omitempty" desc:"even while someone is using the screen (needs scope display.front)"`
	}
	closeArgs struct {
		Instance string `json:"instance,omitempty" desc:"instance ID; default: the window in front"`
	}
	fullscreenArgs struct {
		Instance string `json:"instance" desc:"instance ID"`
		Enabled  *bool  `json:"enabled,omitempty" desc:"true (default) or false"`
	}
	assignArgs struct {
		ID     string `json:"id" desc:"the controller's ID (hostctl devices)"`
		Player int    `json:"player" min:"1" max:"16" desc:"the slot"`
	}
	renameArgs struct {
		ID   string `json:"id" desc:"the device's ID (hostctl devices)"`
		Name string `json:"name" max:"24" desc:"the name the menu shows; empty: its own name again"`
	}
	colourArgs struct {
		ID     string `json:"id" desc:"the device's ID (hostctl devices)"`
		Colour string `json:"colour" enum:"blue,orange,green,pink,yellow,teal,red,violet" desc:"the colour of its pointer and mark"`
	}
	calibrateArgs struct {
		ID    string `json:"id" desc:"the Wii Remote's ID (hostctl devices)"`
		Reset bool   `json:"reset,omitempty" desc:"clear what was measured instead of measuring"`
	}
	noticeArgs struct {
		Text string `json:"text" min:"1" max:"300"`
	}
	powerArgs struct {
		On     bool   `json:"on" desc:"true: screen on; false: off (power saving)"`
		Output string `json:"output,omitempty" desc:"a screen name (see GET /v1/display); default all"`
		Front  bool   `json:"front,omitempty" desc:"even while someone is using the screen (needs scope display.front)"`
	}
	modeArgs struct {
		Output string `json:"output" desc:"a screen name (see GET /v1/display)"`
		Mode   string `json:"mode" desc:"WIDTHxHEIGHT or WIDTHxHEIGHT@HZ, e.g. 1920x1080@60"`
		Front  bool   `json:"front,omitempty" desc:"even while someone is using the screen (needs scope display.front)"`
	}
	enableArgs struct {
		Output  string `json:"output" desc:"a screen name (see GET /v1/display)"`
		Enabled *bool  `json:"enabled,omitempty" desc:"true (default) or false"`
		Front   bool   `json:"front,omitempty" desc:"even while someone is using the screen (needs scope display.front)"`
	}
)

// handler adapts one of the module's handlers, which read the action
// itself, to a typed table entry.
func handler[A any](h func(ctx context.Context, a sdk.Action) (sdk.Result, error)) func(context.Context, sdk.Action, A) (sdk.Result, error) {
	return func(ctx context.Context, a sdk.Action, _ A) (sdk.Result, error) { return h(ctx, a) }
}

// check adapts a check that reads the action itself.
func check[A any](c func(ctx context.Context, a sdk.Action) error) func(context.Context, sdk.Action, A) error {
	return func(ctx context.Context, a sdk.Action, _ A) error { return c(ctx, a) }
}

// declare is the module's actions.
func (m *Module) declare() *kit.Actions {
	t := kit.NewActions("display")
	focus := []sdk.KeyTemplate{"display.focus"}
	front := map[string]string{"front": contract.ScopeFront}

	kit.OnResult(t, sdk.ActionSpec{Type: "window.focus", Description: "Bring an instance's window to the front",
		Keys: focus, Scope: contract.ScopeApps, ArgScopes: front, Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/focus"},
		CLI: &sdk.CLI{Done: []string{
			"not switched: someone is using the screen (they got a notice; --front switches anyway) if reason=in_use",
			"focused {instance}"}}}, handler[focusArgs](m.handleWindow))
	kit.Check(t, "window.focus", check[focusArgs](m.validateWindow))
	kit.OnResult(t, sdk.ActionSpec{Type: "window.close", Description: "Close an instance's windows politely (default: the app in front); stop the app if they stay open",
		Keys: []sdk.KeyTemplate{"display.close"}, Scope: contract.ScopeApps, Timeout: sdk.Duration(time.Minute),
		Route: &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/close"}}, handler[closeArgs](m.handleWindow))
	kit.Check(t, "window.close", check[closeArgs](m.validateWindow))
	kit.OnResult(t, sdk.ActionSpec{Type: "window.fullscreen", Description: "Turn fullscreen on or off for an instance's windows",
		Keys: []sdk.KeyTemplate{"instance:{instance}"}, Scope: "display", Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/fullscreen"}}, handler[fullscreenArgs](m.handleWindow))
	kit.Check(t, "window.fullscreen", check[fullscreenArgs](m.validateWindow))

	attached := func(context.Context, sdk.Action, struct{}) error { return m.attached() }
	for _, a := range []struct {
		typ, desc, path string
		timeout         time.Duration
		h               func(context.Context, sdk.Action) (sdk.Result, error)
	}{
		{"window.back", "Go back to the app that was in front before", "/v1/windows/back", 10 * time.Second, m.handleBack},
		{"window.next", "Bring the next running app to the front", "/v1/windows/next", 10 * time.Second, m.handleCycle},
		{"window.prev", "Bring the previous running app to the front", "/v1/windows/prev", 10 * time.Second, m.handleCycle},
		{"display.menu", "Open hostd's on-screen menu, or go back if it is in front", "/v1/display/menu", time.Minute, m.handleMenu},
	} {
		kit.OnResult(t, sdk.ActionSpec{Type: a.typ, Description: a.desc, Keys: focus, Scope: contract.ScopeApps,
			Timeout: sdk.Duration(a.timeout), Route: &sdk.Route{Method: "POST", Path: a.path}}, handler[struct{}](a.h))
		kit.Check(t, a.typ, attached)
	}

	kit.OnResult(t, sdk.ActionSpec{Type: "window.place", Description: "Put an instance's window beside another's, side by side",
		Keys: focus, Scope: "display", ArgScopes: front, Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/windows/{instance}/place"}}, handler[placeArgs](m.handlePlace))
	kit.Check(t, "window.place", func(ctx context.Context, a sdk.Action, _ placeArgs) error {
		_, _, _, err := m.placeArgs(ctx, a)
		return err
	})

	devices := []sdk.KeyTemplate{"display.devices"}
	kit.OnResult(t, sdk.ActionSpec{Type: "device.assign", Description: "Give a controller a player slot (the controller that has it gets this one's old slot)",
		Keys: devices, Scope: "display", Timeout: sdk.Duration(5 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/devices/{id}/assign"}}, handler[assignArgs](m.handleAssign))
	kit.OnResult(t, sdk.ActionSpec{Type: "device.rename", Description: "Name an input device for the menu (a Wii Remote, a mouse, a controller)",
		Keys: devices, Scope: "display", Timeout: sdk.Duration(5 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/devices/{id}/rename"}}, handler[renameArgs](m.handleIdentity))
	kit.OnResult(t, sdk.ActionSpec{Type: "device.colour", Description: "Give an input device a colour: its pointer on the menu and its mark are drawn in it",
		Keys: devices, Scope: "display", Timeout: sdk.Duration(5 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/devices/{id}/colour"}}, handler[colourArgs](m.handleIdentity))
	kit.OnResult(t, sdk.ActionSpec{Type: "device.calibrate", Description: "Measure a Wii Remote's pointer: point it at the screen's four corners, pressing A at each (the menu shows where); it is kept for that remote",
		Keys: devices, Scope: "display", Timeout: sdk.Duration(5 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/devices/{id}/calibrate"}}, handler[calibrateArgs](m.handleCalibrate))

	kit.On(t, sdk.ActionSpec{Type: "display.notice", Description: "Show a short notice on the screen, above any app",
		Scope: "display", Timeout: sdk.Duration(5 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/display/notice"}},
		func(_ context.Context, _ sdk.Action, args noticeArgs) (struct{}, error) {
			m.notice("", args.Text)
			return struct{}{}, nil
		})

	outputs := []sdk.KeyTemplate{"display.outputs"}
	checkOutput := func(ctx context.Context, a sdk.Action) error { _, _, err := m.outputArgs(ctx, a); return err }
	kit.OnResult(t, sdk.ActionSpec{Type: "display.power", Description: "Turn screens on or off (power saving)",
		Keys: outputs, Scope: "display", ArgScopes: front, Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/display/power"}}, handler[powerArgs](m.handleOutput))
	kit.Check(t, "display.power", check[powerArgs](checkOutput))
	kit.OnResult(t, sdk.ActionSpec{Type: "display.mode", Description: "Set a screen's resolution and refresh rate",
		Keys: outputs, Scope: "display", ArgScopes: front, Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/display/outputs/{output}/mode"}}, handler[modeArgs](m.handleOutput))
	kit.Check(t, "display.mode", check[modeArgs](checkOutput))
	kit.OnResult(t, sdk.ActionSpec{Type: "display.output.enable", Description: "Use or stop using a screen",
		Keys: outputs, Scope: "display", ArgScopes: front, Timeout: sdk.Duration(10 * time.Second),
		Route: &sdk.Route{Method: "POST", Path: "/v1/display/outputs/{output}/enable"}}, handler[enableArgs](m.handleOutput))
	kit.Check(t, "display.output.enable", check[enableArgs](checkOutput))
	return t
}
