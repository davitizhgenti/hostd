package apps

import (
	"context"
	"encoding/json"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

// The verbs of an instance, the same for every instance whatever runs it:
//
//	app.open         start an app, or bring it forward if it runs (per its settings)
//	instance.switch  bring an instance's window to the front
//	instance.close   ask it to close, as its window's close button would;
//	                 stop it if it does not
//	instance.stop    end it now
//
// switch and close need a window, which the display module knows; for an
// instance with none (a background service) close is a polite stop.

const closeTimeout = 90 * time.Second // the display module waits up to a minute for the windows

var switchSchema = json.RawMessage(`{"type":"object","properties":{
	"id":{"type":"string","description":"instance ID, e.g. firefox or firefox#2"},
	"front":{"type":"boolean","description":"switch even while someone is using the screen (needs scope display.front)"}},
	"required":["id"],"additionalProperties":false}`)

func verbActions() []sdk.ActionSpec {
	return []sdk.ActionSpec{
		{Type: contract.ActionInstanceSwitch, Description: "Bring an instance's window to the front",
			Schema: switchSchema, Keys: []sdk.KeyTemplate{"instance:{id}"}, Scope: contract.ScopeApps,
			ArgScopes: map[string]string{"front": contract.ScopeFront},
			Timeout:   sdk.Duration(20 * time.Second),
			Route:     &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/switch"},
			CLI: &sdk.CLI{Alias: []string{"switch", "focus"}, Done: []string{
				"not switched: someone is using the screen (they got a notice; --front switches anyway) if reason=in_use",
				"switched to {instance}"}}},
		{Type: contract.ActionInstanceClose, Description: "Close an instance as its window's close button would; stop it if it stays open",
			Schema: instanceSchema, Keys: []sdk.KeyTemplate{"instance:{id}"}, Scope: contract.ScopeApps,
			Timeout: sdk.Duration(closeTimeout),
			Route:   &sdk.Route{Method: "POST", Path: "/v1/instances/{id}/close"},
			CLI:     &sdk.CLI{Alias: []string{"close"}, Done: []string{"stopped {instance} (it did not close) if stopped", "closed {instance}"}}},
	}
}

// handleSwitch brings an instance's window to the front. The windows are the
// display module's: without it, or for an instance with no window, there is
// nothing to switch to.
func (m *Module) handleSwitch(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		ID    string
		Front bool
	}
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	in, err := m.liveInstance(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	if in.Surface != SurfaceWindow {
		return sdk.Result{}, sdk.Errorf(sdk.CodeInvalidArgs, "instance %q runs in the background: it has no window to switch to", in.ID)
	}
	if !m.core.Handles(contract.ActionWindowFocus) {
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "there is no display to switch on (the display module is not running)")
	}
	res, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionWindowFocus,
		Args: sdk.MustJSON(map[string]any{"instance": in.ID, "front": args.Front})})
	if err != nil {
		return sdk.Result{}, err
	}
	data := map[string]any{}
	_ = json.Unmarshal(res.Data, &data)
	data["instance"] = in.ID
	return sdk.Result{Data: sdk.MustJSON(data)}, nil
}

// handleClose closes an instance politely and stops it if it stays.
func (m *Module) handleClose(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct{ ID string }
	if err := a.DecodeArgs(&args); err != nil {
		return sdk.Result{}, err
	}
	in, err := m.liveInstance(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	if in.Surface == SurfaceWindow && m.core.Handles(contract.ActionWindowClose) {
		// The display module marks it as closing, closes the windows, waits,
		// and stops the app if one stays open.
		res, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionWindowClose, Args: sdk.MustJSON(map[string]string{"instance": in.ID})})
		if err != nil {
			return sdk.Result{}, err
		}
		data := map[string]any{}
		_ = json.Unmarshal(res.Data, &data)
		data["instance"] = in.ID
		return sdk.Result{Data: sdk.MustJSON(data)}, nil
	}
	// No window to close: tell the instance's end to count as asked-for, and stop it.
	if _, err := m.handleClosing(sdk.Action{Args: sdk.MustJSON(map[string]string{"id": in.ID})}); err != nil {
		return sdk.Result{}, err
	}
	if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionInstanceStop, Args: sdk.MustJSON(map[string]string{"id": in.ID})}); err != nil &&
		sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]any{"instance": in.ID, "stopped": true})}, nil
}

// liveInstance returns a copy of an instance that is running.
func (m *Module) liveInstance(id string) (Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.instances[id]
	if !ok || in.State.Ended() {
		return Instance{}, sdk.Errorf(sdk.CodeInstanceNotRunning, "instance %q is not running", id)
	}
	return *in, nil
}
