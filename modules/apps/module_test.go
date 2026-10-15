package apps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// fakeLookPath finds every binary except ones named "missing...".
func fakeLookPath(name string) (string, error) {
	if strings.HasPrefix(filepath.Base(name), "missing") {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + filepath.Base(name), nil
}

var fixtureDirs = []string{"catalog/testdata/system/applications", "catalog/testdata/user/applications"}

type fakeCore struct {
	mu     sync.Mutex
	events []sdk.Event
}

func (f *fakeCore) Emit(e sdk.Event) { f.mu.Lock(); f.events = append(f.events, e); f.mu.Unlock() }

func (f *fakeCore) Do(context.Context, sdk.Action) (sdk.Result, error) {
	return sdk.Result{}, nil
}

func (f *fakeCore) Subscribe(context.Context, string) <-chan sdk.Event { return nil }

func (f *fakeCore) Handles(string) bool { return false }

func (f *fakeCore) Read(context.Context, string, string, map[string]string) (json.RawMessage, error) {
	return nil, sdk.Errorf(sdk.CodeNotFound, "no reads in this fake")
}

func (f *fakeCore) take() []sdk.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev := f.events
	f.events = nil
	return ev
}

func TestModuleManifestIsValid(t *testing.T) {
	m := New(Options{}).Manifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestModuleReadsAndRescan(t *testing.T) {
	appsDir := t.TempDir()
	m := New(Options{DesktopDirs: fixtureDirs, AppsDir: appsDir, LookPath: fakeLookPath, NoWatch: true})
	core := &fakeCore{}
	ctx := context.Background()
	if err := m.Start(ctx, core); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)

	first := core.take()
	if len(first) == 0 || first[len(first)-1].Type != EventCatalogChanged {
		t.Fatalf("start events = %+v", first)
	}

	v, err := m.Read(ctx, "apps", nil)
	if err != nil {
		t.Fatal(err)
	}
	visible := v.(AppList).Apps
	v, _ = m.Read(ctx, "apps", map[string]string{"all": "true"})
	if len(v.(AppList).Apps) <= len(visible) {
		t.Fatal("all=true does not add hidden apps")
	}
	if _, err := m.Read(ctx, "app", map[string]string{"id": "steam"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Read(ctx, "app", map[string]string{"id": "nope"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("missing app: %v", err)
	}

	// Rescan with nothing changed: no events.
	res, err := m.Handle(ctx, sdk.Action{Type: "app.rescan"})
	if err != nil || !strings.Contains(string(res.Data), `"apps":`) {
		t.Fatalf("%+v %v", res, err)
	}
	if ev := core.take(); len(ev) != 0 {
		t.Fatalf("unchanged rescan emitted %+v", ev)
	}

	// Add a file and a bad file: one change event, one rejection, once.
	_ = os.WriteFile(filepath.Join(appsDir, "kodi.toml"), []byte(`runner = { type = "flatpak", app_id = "tv.kodi.Kodi" }`), 0o644)
	_ = os.WriteFile(filepath.Join(appsDir, "oops.toml"), []byte(`runner = { type = "nope" }`), 0o644)
	_, _ = m.Handle(ctx, sdk.Action{Type: "app.rescan"})
	ev := core.take()
	types := map[string]int{}
	for _, e := range ev {
		types[e.Type]++
	}
	if types[EventCatalogChanged] != 1 || types[EventFileRejected] != 1 {
		t.Fatalf("events = %v", types)
	}
	for _, e := range ev {
		if e.Type == EventCatalogChanged && !strings.Contains(string(e.Data), `"added":["kodi"]`) {
			t.Fatalf("change event = %s", e.Data)
		}
	}
	_, _ = m.Handle(ctx, sdk.Action{Type: "app.rescan"})
	if ev := core.take(); len(ev) != 0 {
		t.Fatalf("problem reported twice: %+v", ev)
	}
	if st, _ := m.State(ctx); len(st.(map[string]any)["catalog"].(AppList).Problems) == 0 {
		t.Fatal("State has no problems")
	}
}

func TestWatchRescansAfterChangesSettle(t *testing.T) {
	appsDir := t.TempDir()
	fc := clock.NewFake(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	m := New(Options{AppsDir: appsDir, Clock: fc, LookPath: fakeLookPath})
	core := &fakeCore{}
	ctx := context.Background()
	if err := m.Start(ctx, core); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	core.take()

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(appsDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.toml", `runner = { type = "exec", command = ["a"] }`)
	fc.BlockUntil(1) // the watcher saw the change and armed the timer
	fc.Advance(400 * time.Millisecond)
	if m.Catalog().Len() != 0 {
		t.Fatal("rescanned before the changes settled")
	}
	seen := m.seenChanges()
	write("b.toml", `runner = { type = "exec", command = ["b"] }`) // re-arms the timer
	waitUntil(t, func() bool { return m.seenChanges() > seen })
	fc.Advance(499 * time.Millisecond)
	if m.Catalog().Len() != 0 {
		t.Fatal("timer was not reset by the second change")
	}
	waitUntil(t, func() bool {
		fc.Advance(10 * time.Millisecond)
		return m.Catalog().Len() == 2
	})
	changes := 0
	for _, e := range core.take() {
		if e.Type == EventCatalogChanged {
			changes++
		}
	}
	if changes != 1 {
		t.Fatalf("%d change events for two quick edits, want 1", changes)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}

// --- fuzzing ----------------------------------------------------------------

func (m *Module) seenChanges() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changes
}
