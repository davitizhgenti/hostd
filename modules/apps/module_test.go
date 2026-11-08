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

	"github.com/davitizhgenti/hostd/modules/apps/catalog"

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

func (f *fakeCore) Handles(string) bool          { return false }
func (f *fakeCore) Tokens() sdk.TokenMinter      { return nil }
func (f *fakeCore) Limiter() sdk.Limiter         { return nil }
func (f *fakeCore) Introspect() sdk.Introspector { return nil }

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

	// A file is applied: one change event. A bad one is refused, so the
	// core can say so on the resource, and changes nothing.
	if _, err := m.Apply(ctx, sdk.Resource{Kind: "app", Name: "kodi", Raw: []byte(`runner = { type = "flatpak", app_id = "tv.kodi.Kodi" }`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Apply(ctx, sdk.Resource{Kind: "app", Name: "oops", Raw: []byte(`runner = { type = "nope" }`)}); err == nil {
		t.Fatal("a bad app file was accepted")
	}
	ev := core.take()
	if len(ev) != 1 || ev[0].Type != EventCatalogChanged || !strings.Contains(string(ev[0].Data), `"added":["kodi"]`) {
		t.Fatalf("events = %+v", ev)
	}
	_, _ = m.Handle(ctx, sdk.Action{Type: "app.rescan"})
	if ev := core.take(); len(ev) != 0 {
		t.Fatalf("a rescan with nothing new emitted %+v", ev)
	}
	if err := m.Delete(ctx, "app", "kodi"); err != nil {
		t.Fatal(err)
	}
	if ev := core.take(); len(ev) != 1 || !strings.Contains(string(ev[0].Data), `"removed":["kodi"]`) {
		t.Fatalf("delete events = %+v", ev)
	}
	// A problem the catalog finds in what the machine has is announced once.
	_ = os.WriteFile(filepath.Join(appsDir, "ignored.toml"), []byte(`runner = { type = "nope" }`), 0o644) // not read: the core reads app files
	if st, _ := m.State(ctx); len(st.(map[string]any)["catalog"].(AppList).Problems) == 0 {
		t.Fatal("State has no problems (the desktop fixtures have some)")
	}
}

func TestWatchRescansAfterChangesSettle(t *testing.T) {
	desktopDir := t.TempDir()
	fc := clock.NewFake(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	m := New(Options{DesktopDirs: []string{desktopDir}, Clock: fc, LookPath: fakeLookPath})
	core := &fakeCore{}
	ctx := context.Background()
	if err := m.Start(ctx, core); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	core.take()

	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(desktopDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	entry := func(name string) string {
		return "[Desktop Entry]\nType=Application\nName=" + name + "\nExec=" + name + "\n"
	}
	write("a.desktop", entry("a"))
	fc.BlockUntil(1) // the watcher saw the change and armed the timer
	fc.Advance(400 * time.Millisecond)
	if m.Catalog().Len() != 0 {
		t.Fatal("rescanned before the changes settled")
	}
	seen := m.seenChanges()
	write("b.desktop", entry("b")) // re-arms the timer
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

// loadFiles reads the app files in the folder into use, as the core's
// Apply calls would for each of them, and rebuilds the catalog.
func (m *Module) loadFiles() {
	files, _ := catalog.LoadAppFiles(m.opts.AppsDir)
	m.mu.Lock()
	m.files = map[string]catalog.AppFile{}
	for _, f := range files {
		m.files[strings.TrimSuffix(filepath.Base(f.Path), ".toml")] = f
	}
	m.mu.Unlock()
	m.rescan()
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
