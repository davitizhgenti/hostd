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
//	instance.restart stop it and start its app again, as it was started
//
// switch and close need a window, which the display module knows; for an
// instance with none (a background service) close is a polite stop.

const closeTimeout = 90 * time.Second // the display module waits up to a minute for the windows

// quitWait is how long an app's quit command has to end it (Steam takes
// several seconds to shut down) before it is closed the usual way.
const quitWait = 20 * time.Second

// handleSwitch brings an instance's window to the front. The windows are the
// display module's: without it, or for an instance with no window, there is
// nothing to switch to.
func (m *Module) handleSwitch(ctx context.Context, a sdk.Action, args switchArgs) (sdk.Result, error) {
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
func (m *Module) handleClose(ctx context.Context, a sdk.Action, args instanceArgs) (sdk.Result, error) {
	in, err := m.liveInstance(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	if quit, ok := m.quitApp(ctx, in); ok {
		return quit, nil
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
	if _, err := m.handleClosing(ctx, sdk.Action{}, instanceArgs{ID: in.ID}); err != nil {
		return sdk.Result{}, err
	}
	if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionInstanceStop, Args: sdk.MustJSON(map[string]string{"id": in.ID})}); err != nil &&
		sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: sdk.MustJSON(map[string]any{"instance": in.ID, "stopped": true})}, nil
}

// handleRestart stops an instance, then opens its app again with what it
// was started with: the same action, and a release's directory,
// environment, image and ports.
func (m *Module) handleRestart(ctx context.Context, a sdk.Action, args instanceArgs) (sdk.Result, error) {
	in, err := m.liveInstance(args.ID)
	if err != nil {
		return sdk.Result{}, err
	}
	if _, err := m.core.Do(ctx, sdk.Action{Type: contract.ActionInstanceStop, Args: sdk.MustJSON(map[string]string{"id": in.ID})}); err != nil &&
		sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
		return sdk.Result{}, err
	}
	start := contract.AppStart{ID: in.App, Action: in.Action, Dir: in.Dir, Env: in.Env, Image: in.Image, Ports: in.Ports}
	return m.core.Do(ctx, sdk.Action{Type: contract.ActionAppOpen, Args: sdk.MustJSON(start)})
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

// quitApp asks an app with a quit command (runner.quit) to quit, and waits
// for it to end: ok is false when the app has none, cannot be asked, or
// did not end in quitWait (then it is closed the usual way).
func (m *Module) quitApp(ctx context.Context, in Instance) (sdk.Result, bool) {
	ptr, known := m.catalog().Get(in.App)
	if !known || len(ptr.Runner.Quit) == 0 {
		return sdk.Result{}, false
	}
	app := *ptr
	p, ok := m.backend(app.Runner.Type).(Passer)
	if !ok {
		return sdk.Result{}, false
	}
	// Its end counts as asked-for.
	if _, err := m.handleClosing(ctx, sdk.Action{}, instanceArgs{ID: in.ID}); err != nil {
		return sdk.Result{}, false
	}
	quitter := app
	quitter.Runner.Command = app.Runner.Quit
	qctx, cancel := context.WithTimeout(ctx, quitWait)
	defer cancel()
	if err := p.Pass(qctx, in, &quitter); err != nil {
		m.log.Warn("asking an app to quit", "instance", in.ID, "err", err)
		return sdk.Result{}, false
	}
	tick := m.opts.Clock.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	deadline := m.opts.Clock.Now().Add(quitWait)
	for {
		if _, err := m.liveInstance(in.ID); err != nil {
			return sdk.Result{Data: sdk.MustJSON(map[string]any{"instance": in.ID, "quit": true})}, true
		}
		if !m.opts.Clock.Now().Before(deadline) {
			m.log.Info("the app did not quit in time; closing it", "instance", in.ID)
			return sdk.Result{}, false
		}
		select {
		case <-tick.C():
		case <-ctx.Done():
			return sdk.Result{}, false
		}
	}
}
