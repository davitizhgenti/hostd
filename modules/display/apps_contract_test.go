package display

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/evdev/evdevtest"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/sdk"
)

// The rest of the display tests stand in for the apps module with a fake.
// This one runs the real apps module beside the real display module, with
// only the compositor and the process runner faked, so a change on either
// side that the contract package does not catch (a scope, an event's data,
// the unit name windows are matched by, an action the other side expects)
// fails here.

// runner stands in for the exec runner: an instance is "running" as soon
// as it starts, as a process in the unit contract.UnitName names.
type runner struct {
	mu      sync.Mutex
	pid     int
	stopped []string
}

func (r *runner) Start(_ context.Context, inst apps.Instance, _ *apps.App) (apps.Instance, error) {
	inst.Unit, inst.PID = contract.UnitName(inst.ID), r.pid
	return inst, nil
}

func (r *runner) Stop(_ context.Context, inst apps.Instance) error {
	r.mu.Lock()
	r.stopped = append(r.stopped, inst.ID)
	r.mu.Unlock()
	return nil
}

func (r *runner) Adopt(context.Context) ([]apps.Instance, error) { return nil, nil }

func (r *runner) Watch(ctx context.Context, _ func(apps.Ended)) error {
	<-ctx.Done()
	return nil
}

func (r *runner) stops() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.stopped)
}

func TestDisplayWithTheRealAppsModule(t *testing.T) {
	const pid = 4100
	// The window's process is in the unit the apps module names.
	proc := fakeProc(t, map[int]string{
		pid: "0::/user.slice/user-1000.slice/user@1000.service/app.slice/" + contract.UnitName("term"),
	})
	appsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appsDir, "term.toml"), []byte("runner = { type = \"exec\", command = [\"foot\"] }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := &runner{pid: pid}
	am := apps.New(apps.Options{AppsDir: appsDir, NoWatch: true, Backends: map[string]apps.Backend{apps.RunnerExec: run}})
	b := newFakeBackend()
	dm := New(Options{ProcRoot: proc, SysRoot: evdevtest.FakeSys(t), CloseTimeout: 100 * time.Millisecond, EndWait: 100 * time.Millisecond,
		Connect: func(context.Context) (Backend, error) { return b, nil }})

	reg := core.NewRegistry()
	for _, m := range []sdk.Module{am, dm} { // adding them checks every scope and dependency
		if err := reg.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	e := core.New(reg, core.Options{})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	admin := core.Auth{Scopes: []string{sdk.ScopeAdmin}}
	submit := func(typ string, args any) {
		t.Helper()
		if _, err := e.Submit(context.Background(), sdk.Action{Type: typ, Args: sdk.MustJSON(args),
			Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, admin); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	waitFor(t, "the compositor", func() bool { return b.bindCalls() > 0 })

	// An app starts, and its window turns up: it goes to the instance's
	// own workspace and is shown, found through the unit name.
	submit(contract.ActionAppStart, contract.AppStart{ID: "term"})
	b.open(1, pid)
	waitFor(t, "the window placed", func() bool {
		ws, _ := b.Windows(context.Background())
		return len(ws) == 1 && ws[0].Workspace == WorkspacePrefix+"term"
	})

	// Display actions need the apps scope, which only the apps module
	// declares; focusing reaches the window of that instance.
	b.commands()
	submit("window.focus", map[string]string{"instance": "term"})
	waitFor(t, "the focus", func() bool { return slices.Contains(b.commands(), "focus 1") })

	// Closing: the apps module is told the end is a normal exit, the window
	// is asked to close, and an app whose window stays open is stopped.
	b.mu.Lock()
	b.ignore[1] = true
	b.mu.Unlock()
	submit("window.close", map[string]string{"instance": "term"})
	waitFor(t, "the app stopped", func() bool { return reflect.DeepEqual(run.stops(), []string{"term"}) })
}
