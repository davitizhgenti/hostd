package apps

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/sdk"
)

// windows stands in for the display module: it takes window.focus and
// window.close, and records what it was asked.
type windows struct {
	mu   sync.Mutex
	seen []string
	// focused is the instance the display says is in front.
	focused string
	// closeEnds: closing the windows ends the app, as a polite close does.
	closeEnds func(instance string)
}

func (w *windows) module() *testutil.Module {
	m := testutil.NewModule("display", nil, []string{"window.focus", "window.close"}, nil)
	m.M.Owns = []string{"display.*", "window.*"}
	m.M.Reads = []sdk.ReadSpec{{Name: "display", Path: "/v1/display"}}
	m.ReadFunc = func(context.Context, string, map[string]string) (any, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		return map[string]any{"focused_instance": w.focused}, nil
	}
	for i := range m.M.Actions {
		m.M.Actions[i].Schema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	}
	m.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args struct {
			Instance string
			Front    bool
		}
		_ = a.DecodeArgs(&args)
		w.mu.Lock()
		w.seen = append(w.seen, a.Type+" "+args.Instance)
		w.mu.Unlock()
		if a.Type == "window.close" && w.closeEnds != nil {
			w.closeEnds(args.Instance)
		}
		return sdk.Result{Data: sdk.MustJSON(map[string]any{"instance": args.Instance, "front": args.Front})}, nil
	}
	return m
}

func (w *windows) calls() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.seen...)
}

func (r *rig) verb(t *testing.T, typ string, args map[string]any) (map[string]any, error) {
	t.Helper()
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: sdk.MustJSON(args),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, admin)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	_ = json.Unmarshal(res.Data, &data)
	return data, nil
}

func TestSwitchGoesToTheWindow(t *testing.T) {
	w := &windows{}
	r := newRigWith(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t), nil, rigApps, w.module())
	r.start(t, "term")
	r.start(t, "jelly") // a service: no window

	data, err := r.verb(t, contract.ActionInstanceSwitch, map[string]any{"id": "term", "front": true})
	if err != nil || data["instance"] != "term" || data["front"] != true {
		t.Fatalf("switch: %v %v", data, err)
	}
	if got := w.calls(); len(got) != 1 || got[0] != "window.focus term" {
		t.Fatalf("display was asked %v", got)
	}
	if _, err := r.verb(t, contract.ActionInstanceSwitch, map[string]any{"id": "jelly"}); sdk.CodeOf(err) != sdk.CodeInvalidArgs ||
		!strings.Contains(err.Error(), "no window") {
		t.Fatalf("a service has no window: %v", err)
	}
	if _, err := r.verb(t, contract.ActionInstanceSwitch, map[string]any{"id": "ghost"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("an unknown instance: %v", err)
	}
	// Needs the scope the instance verbs need; front needs its own.
	phone := sdk.Action{Type: contract.ActionInstanceSwitch, Args: sdk.MustJSON(map[string]any{"id": "term", "front": true}),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "phone"}}
	if _, err := r.e.Submit(context.Background(), phone, coreAuth(contract.ScopeApps)); sdk.CodeOf(err) != sdk.CodeForbidden {
		t.Fatalf("front without display.front: %v", err)
	}
}

func TestSwitchWithoutADisplay(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	r.start(t, "term")
	if _, err := r.verb(t, contract.ActionInstanceSwitch, map[string]any{"id": "term"}); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("no display: %v", err)
	}
}

func TestCloseAsksTheWindowsFirst(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	w := &windows{}
	r := newRigWith(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t), nil, rigApps, w.module())
	// A polite close ends the app by itself.
	w.closeEnds = func(instance string) { sd.Exit(contract.UnitName(instance), 1, 1) }
	waitUntil(t, func() bool { return sd.Watching() == 1 })
	r.start(t, "term")

	data, err := r.verb(t, contract.ActionInstanceClose, map[string]any{"id": "term"})
	if err != nil || data["instance"] != "term" {
		t.Fatalf("close: %v %v", data, err)
	}
	if got := w.calls(); len(got) != 1 || got[0] != "window.close term" {
		t.Fatalf("display was asked %v", got)
	}
}

func TestCloseStopsWhatHasNoWindow(t *testing.T) {
	d := podmantest.NewFakeEngine()
	sd := unitstest.NewFakeSystemd()
	w := &windows{}
	r := newRigWith(t, sd, d, unitstest.FakeSession(t), nil, rigApps, w.module())
	r.start(t, "jelly")
	data, err := r.verb(t, contract.ActionInstanceClose, map[string]any{"id": "jelly"})
	if err != nil || data["stopped"] != true {
		t.Fatalf("close of a service: %v %v", data, err)
	}
	if got := r.live(); len(got) != 0 {
		t.Fatalf("still running: %v", got)
	}
	if got := w.calls(); len(got) != 0 {
		t.Fatalf("a service has no windows to ask: %v", got)
	}
	// Closing what is not running is said so.
	if _, err := r.verb(t, contract.ActionInstanceClose, map[string]any{"id": "jelly"}); sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
		t.Fatalf("closing twice: %v", err)
	}
}

func TestCloseWithoutADisplayStops(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	r.start(t, "term")
	data, err := r.verb(t, contract.ActionInstanceClose, map[string]any{"id": "term"})
	if err != nil || data["stopped"] != true {
		t.Fatalf("close: %v %v", data, err)
	}
	if got := r.live(); len(got) != 0 {
		t.Fatalf("still running: %v", got)
	}
	if u, ok := sd.Units[contract.UnitName("term")]; ok && u.Info.SubState != "dead" {
		t.Fatalf("unit left: %+v", u.Info)
	}
}

func coreAuth(scopes ...string) core.Auth { return core.Auth{Name: "phone", Scopes: scopes} }

// A command started with app.run is an instance like any other: switch,
// close and stop work on it, window or background.
func TestRunInstancesTakeTheSameVerbs(t *testing.T) {
	w := &windows{}
	r := newRigWith(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t), nil, rigApps, w.module())
	win, err := r.verb(t, ActionRun, map[string]any{"command": []string{"imv", "plot.png"}})
	if err != nil {
		t.Fatal(err)
	}
	bg, err := r.verb(t, ActionRun, map[string]any{"command": []string{"sleep", "100"}, "name": "nap", "background": true})
	if err != nil {
		t.Fatal(err)
	}
	winID, bgID := win["instance"].(string), bg["instance"].(string)
	if winID != "run" || bgID != "run-nap" {
		t.Fatalf("instances %q %q", winID, bgID)
	}
	if _, err := r.verb(t, contract.ActionInstanceSwitch, map[string]any{"id": winID}); err != nil {
		t.Fatalf("switch to a run window: %v", err)
	}
	if _, err := r.verb(t, contract.ActionInstanceSwitch, map[string]any{"id": bgID}); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("switch to a background run: %v", err)
	}
	// Close: the window one is asked to close (the stand-in leaves it open,
	// so it is not ended by it), the background one is stopped.
	if data, err := r.verb(t, contract.ActionInstanceClose, map[string]any{"id": bgID}); err != nil || data["stopped"] != true {
		t.Fatalf("close a background run: %v %v", data, err)
	}
	if _, err := r.verb(t, contract.ActionInstanceStop, map[string]any{"id": winID}); err != nil {
		t.Fatalf("stop a run window: %v", err)
	}
	if got := r.live(); len(got) != 0 {
		t.Fatalf("still running: %v", got)
	}
}
