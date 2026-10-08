package display

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"

	"github.com/davitizhgenti/hostd/sdk"
)

// resolver returns a function that finds windows' instances. It reads the
// running instances (for their match rules) at most once, and only when
// some window needs them.
func (m *Module) resolver(ctx context.Context) func(Window) string {
	var live []liveInstance
	loaded := false
	return func(w Window) string {
		if !loaded {
			loaded = true
			live = m.liveInstances(ctx)
		}
		return resolveWindow(m.opts.ProcRoot, w, live)
	}
}

// liveInstances asks the apps module for the running instances.
func (m *Module) liveInstances(ctx context.Context) []liveInstance {
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core == nil {
		return nil
	}
	raw, err := core.Read(ctx, "apps", "instances", nil)
	if err != nil {
		return nil
	}
	var all []liveInstance
	if json.Unmarshal(raw, &all) != nil {
		return nil
	}
	out := all[:0]
	for _, in := range all {
		switch in.State {
		case "exited", "failed":
		default:
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// --- window.place ----------------------------------------------------------------

type placeArgs struct {
	Instance string `json:"instance"`
	Beside   string `json:"beside"`
}

func (m *Module) placeArgs(ctx context.Context, a sdk.Action) (Backend, Window, Window, error) {
	var args placeArgs
	if err := a.DecodeArgs(&args); err != nil {
		return nil, Window{}, Window{}, err
	}
	if args.Instance == args.Beside {
		return nil, Window{}, Window{}, sdk.Errorf(sdk.CodeInvalidArgs, "cannot place %s beside itself", args.Instance)
	}
	b, wins, err := m.windowsOf(ctx, args.Instance)
	if err != nil {
		return nil, Window{}, Window{}, err
	}
	_, others, err := m.windowsOf(ctx, args.Beside)
	if err != nil {
		return nil, Window{}, Window{}, err
	}
	return b, wins[0], others[0], nil
}

func (m *Module) handlePlace(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	b, w, beside, err := m.placeArgs(ctx, a)
	if err != nil {
		return sdk.Result{}, err
	}
	var args placeArgs
	_ = a.DecodeArgs(&args)
	if m.inUse(a) {
		return m.skippedInUse(args.Instance, m.nameOf(args.Instance)+" wants the screen",
			map[string]any{"instance": args.Instance, "placed": false})
	}
	if err := b.Show(ctx, beside.Workspace); err != nil {
		return sdk.Result{}, err
	}
	if err := b.Place(ctx, w.ID, beside.ID); err != nil {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: mustJSON(map[string]any{"instance": args.Instance, "beside": args.Beside,
		"workspace": beside.Workspace})}, nil
}

// --- screens ---------------------------------------------------------------------------

type outputArgs struct {
	Output  string `json:"output"`
	On      *bool  `json:"on"`
	Mode    string `json:"mode"`
	Enabled *bool  `json:"enabled"`
}

var reMode = regexp.MustCompile(`^(\d{2,5})x(\d{2,5})(?:@(\d+(?:\.\d+)?)(?:Hz)?)?$`)

// outputArgs checks the arguments against the screens there are, and
// returns the Sway output setting to apply.
func (m *Module) outputArgs(ctx context.Context, a sdk.Action) (Backend, string, error) {
	var args outputArgs
	if err := a.DecodeArgs(&args); err != nil {
		return nil, "", err
	}
	m.mu.Lock()
	b := m.backend
	m.mu.Unlock()
	if b == nil {
		return nil, "", sdk.Errorf(sdk.CodeModuleUnavailable, "no display session: Sway is not running")
	}
	outs, err := b.Outputs(ctx)
	if err != nil {
		return nil, "", err
	}
	var target *Output
	if args.Output != "" && args.Output != "*" {
		names := make([]string, 0, len(outs))
		for i := range outs {
			names = append(names, outs[i].Name)
			if outs[i].Name == args.Output {
				target = &outs[i]
			}
		}
		if target == nil {
			return nil, "", sdk.Errorf(sdk.CodeNotFound, "no screen %q; screens: %v", args.Output, names)
		}
	}
	if target == nil && a.Type != "display.power" {
		return nil, "", sdk.Errorf(sdk.CodeInvalidArgs, "%s needs one screen's name (see GET /v1/display)", a.Type)
	}
	switch a.Type {
	case "display.power":
		if *args.On {
			return b, "power on", nil
		}
		return b, "power off", nil
	case "display.output.enable":
		if args.Enabled == nil || *args.Enabled {
			return b, "enable", nil
		}
		active := 0
		for _, o := range outs {
			if o.Active && o.Name != args.Output {
				active++
			}
		}
		if active == 0 {
			return nil, "", sdk.Errorf(sdk.CodeInvalidArgs, "%s is the only screen in use; turn it off with display.power instead", args.Output)
		}
		return b, "disable", nil
	case "display.mode":
		mode, err := pickMode(args.Mode, target.Modes)
		if err != nil {
			return nil, "", err
		}
		return b, "mode " + mode, nil
	}
	return nil, "", sdk.Errorf(sdk.CodeNotFound, "display module has no action %q", a.Type)
}

// pickMode turns "1920x1080" or "1920x1080@60" into a mode the screen
// has, taking the closest (or, without a rate, the highest) refresh rate.
// A screen that lists no modes takes the request as it is.
func pickMode(want string, modes []Mode) (string, error) {
	mt := reMode.FindStringSubmatch(want)
	if mt == nil {
		return "", sdk.Errorf(sdk.CodeInvalidArgs, "mode %q: use WIDTHxHEIGHT or WIDTHxHEIGHT@HZ, e.g. 1920x1080@60", want)
	}
	w, _ := strconv.Atoi(mt[1])
	h, _ := strconv.Atoi(mt[2])
	hz := -1.0
	if mt[3] != "" {
		hz, _ = strconv.ParseFloat(mt[3], 64)
	}
	if len(modes) == 0 {
		if hz < 0 {
			return fmt.Sprintf("%dx%d", w, h), nil
		}
		return fmt.Sprintf("%dx%d@%gHz", w, h, hz), nil
	}
	var best *Mode
	for i := range modes {
		md := &modes[i]
		if md.Width != w || md.Height != h {
			continue
		}
		switch {
		case best == nil:
			best = md
		case hz < 0 && md.Refresh > best.Refresh:
			best = md
		case hz >= 0 && math.Abs(md.Refresh-hz) < math.Abs(best.Refresh-hz):
			best = md
		}
	}
	if best == nil {
		var have []string
		for _, md := range modes {
			have = append(have, md.String())
		}
		return "", sdk.Errorf(sdk.CodeInvalidArgs, "the screen has no %dx%d mode; it has %v", w, h, have)
	}
	return best.String(), nil
}

func (m *Module) handleOutput(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	b, setting, err := m.outputArgs(ctx, a)
	if err != nil {
		return sdk.Result{}, err
	}
	var args outputArgs
	_ = a.DecodeArgs(&args)
	output := args.Output
	if output == "" {
		output = "*"
	}
	// Turning a screen on never takes anything from anyone.
	if setting != "power on" && setting != "enable" && m.inUse(a) {
		return m.skippedInUse("", "Someone wants to change the screen", map[string]any{"output": output, "changed": false})
	}
	if err := b.SetOutput(ctx, output, setting); err != nil {
		return sdk.Result{}, err
	}
	outs, _ := b.Outputs(ctx)
	if outs == nil {
		outs = []Output{}
	}
	m.mu.Lock()
	core := m.core
	m.mu.Unlock()
	if core != nil {
		core.Emit(sdk.Event{Type: EventOutputs, Action: a.ID, Data: mustJSON(map[string]any{"outputs": outs})})
	}
	return sdk.Result{Data: mustJSON(map[string]any{"output": output, "applied": setting, "outputs": outs})}, nil
}

// --- moving between apps -----------------------------------------------------------

func (m *Module) attached() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backend == nil {
		return sdk.Errorf(sdk.CodeModuleUnavailable, "no display session: Sway is not running")
	}
	return nil
}

// show brings a window and its workspace to the front.
func (m *Module) show(ctx context.Context, w *trackedWindow) (sdk.Result, error) {
	m.mu.Lock()
	b := m.backend
	m.mu.Unlock()
	if b == nil {
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "no display session: Sway is not running")
	}
	if err := b.Show(ctx, w.Workspace); err != nil {
		return sdk.Result{}, err
	}
	if err := b.Focus(ctx, w.ID); err != nil {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: mustJSON(map[string]any{"instance": w.Instance, "window": w.ID})}, nil
}

// nothing answers an action that had nowhere to go: not an error, the
// screen just stays as it is.
func nothing(reason string) (sdk.Result, error) {
	return sdk.Result{Status: sdk.StatusSkipped, Reason: reason}, nil
}

func (m *Module) handleBack(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	m.mu.Lock()
	back := m.backTarget()
	m.mu.Unlock()
	if back == nil {
		// No history (hostd just started, or that app closed): any other
		// app beats staying where nothing can be done.
		back = m.cycleTarget(1)
	}
	if back == nil {
		return nothing("no_other_app")
	}
	if m.inUse(a) {
		return m.skippedInUse(back.Instance, m.nameOf(back.Instance)+" wants the screen", map[string]any{"instance": back.Instance})
	}
	return m.show(ctx, back)
}

// handleCycle steps through the running apps with windows.
func (m *Module) handleCycle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	step := 1
	if a.Type == "window.prev" {
		step = -1
	}
	target := m.cycleTarget(step)
	if target == nil {
		return nothing("no_other_app")
	}
	if m.inUse(a) {
		return m.skippedInUse(target.Instance, m.nameOf(target.Instance)+" wants the screen", map[string]any{"instance": target.Instance})
	}
	return m.show(ctx, target)
}

// cycleTarget returns a window of the app step places from the one in
// front, among the running apps with windows in instance ID order (the
// menu is not one of them); nil if there is no other.
func (m *Module) cycleTarget(step int) *trackedWindow {
	m.mu.Lock()
	first := map[string]*trackedWindow{}
	for _, t := range m.windows {
		if t.Instance == "" || appOfInstance(t.Instance) == m.opts.Menu {
			continue
		}
		if cur, ok := first[t.Instance]; !ok || t.ID < cur.ID {
			c := *t
			first[t.Instance] = &c
		}
	}
	current := ""
	if n := len(m.stack); n > 0 {
		current = m.stack[n-1]
	}
	m.mu.Unlock()
	ids := slices.Sorted(maps.Keys(first))
	if len(ids) == 0 || (len(ids) == 1 && ids[0] == current) {
		return nil
	}
	i := slices.Index(ids, current)
	switch {
	case i < 0 && step > 0:
		i = 0
	case i < 0:
		i = len(ids) - 1
	default:
		i = (i + step + len(ids)) % len(ids)
	}
	return first[ids[i]]
}

// handleMenu opens the menu, or goes back if it is in front.
func (m *Module) handleMenu(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	m.mu.Lock()
	inFront := len(m.stack) > 0 && appOfInstance(m.stack[len(m.stack)-1]) == m.opts.Menu
	core := m.core
	m.mu.Unlock()
	if inFront {
		return m.handleBack(ctx, a)
	}
	if core == nil || !core.Handles("app.start") {
		return sdk.Result{}, sdk.Errorf(sdk.CodeModuleUnavailable, "the apps module is not running")
	}
	return core.Do(ctx, sdk.Action{Type: "app.start", Args: mustJSON(map[string]string{"id": m.opts.Menu})})
}

// frontWindow returns the focused window and its instance ("" for a window
// hostd did not start).
func (m *Module) frontWindow(ctx context.Context) (Backend, Window, error) {
	m.mu.Lock()
	b := m.backend
	m.mu.Unlock()
	if b == nil {
		return nil, Window{}, sdk.Errorf(sdk.CodeModuleUnavailable, "no display session: Sway is not running")
	}
	all, err := b.Windows(ctx)
	if err != nil {
		return nil, Window{}, err
	}
	for _, w := range all {
		if w.Focused {
			return b, w, nil
		}
	}
	return nil, Window{}, sdk.Errorf(sdk.CodeNotFound, "no window is in front")
}

// closeFront closes the window in front: its whole instance, or just the
// window if hostd did not start it.
func (m *Module) closeFront(ctx context.Context) (sdk.Result, error) {
	b, w, err := m.frontWindow(ctx)
	if err != nil {
		return sdk.Result{}, err
	}
	if inst := m.resolver(ctx)(w); inst != "" {
		_, wins, err := m.windowsOf(ctx, inst)
		if err != nil {
			return sdk.Result{}, err
		}
		return m.closeInstance(ctx, b, inst, wins)
	}
	if err := b.CloseWindow(ctx, w.ID); err != nil {
		return sdk.Result{}, err
	}
	return sdk.Result{Data: mustJSON(map[string]any{"window": w.ID, "closed": 1})}, nil
}
