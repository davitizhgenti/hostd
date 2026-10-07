package apps

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeLookPath finds every binary except ones named "missing...".
func fakeLookPath(name string) (string, error) {
	if strings.HasPrefix(filepath.Base(name), "missing") {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + filepath.Base(name), nil
}

var fixtureDirs = []string{"testdata/system/applications", "testdata/user/applications"}

func TestParseDesktopEntry(t *testing.T) {
	e, err := ParseDesktopEntry([]byte(`# comment
[Desktop Entry]
Name=Firefox
Name[de]=Feuerfuchs
Exec=firefox %u
Categories=Network;WebBrowser;
NoDisplay=true
OnlyShowIn=GNOME;sway;

[Desktop Action private]
Name=Private window
Exec=firefox --private
`))
	if err != nil {
		t.Fatal(err)
	}
	want := DesktopEntry{Name: "Firefox", Exec: "firefox %u", NoDisplay: true,
		Categories: []string{"Network", "WebBrowser"}, OnlyShowIn: []string{"GNOME", "sway"}}
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("got  %+v\nwant %+v", e, want)
	}
	if _, err := ParseDesktopEntry([]byte("Name=x\n")); err == nil {
		t.Fatal("file without [Desktop Entry] accepted")
	}
}

func TestExecArgs(t *testing.T) {
	for _, tc := range []struct {
		exec string
		want []string
		err  bool
	}{
		{"firefox %u", []string{"firefox"}, false},
		{"/usr/bin/vlc --started-from-file %U", []string{"/usr/bin/vlc", "--started-from-file"}, false},
		{`"/opt/My App/run" --flag`, []string{"/opt/My App/run", "--flag"}, false},
		{`run "a \"b\" c" \$x`, []string{"run", `a "b" c`, `\$x`}, false},
		{`run --rate=100%% --file=%f %i %c %k`, []string{"run", "--rate=100%"}, false},
		{"  spaced   out  ", []string{"spaced", "out"}, false},
		{`run ""`, []string{"run"}, false},
		{`run "unterminated`, nil, true},
		{"%U", nil, true},
		{"", nil, true},
	} {
		got, err := ExecArgs(tc.exec)
		if tc.err {
			if err == nil {
				t.Errorf("ExecArgs(%q) = %q, want error", tc.exec, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExecArgs(%q) = %q, %v; want %q", tc.exec, got, err, tc.want)
		}
	}
}

func buildFixture(t *testing.T) *Catalog {
	t.Helper()
	src := DesktopSource{Dirs: fixtureDirs, LookPath: fakeLookPath}
	discovered, problems := src.Scan()
	files, fileProblems := LoadAppFiles("testdata/apps")
	return Build(discovered, files, append(problems, fileProblems...))
}

// The whole merged catalog from the fixtures, pinned in a golden file.
// Regenerate after an intended change: go test ./modules/apps -update
func TestCatalogGolden(t *testing.T) {
	cat := buildFixture(t)
	got, _ := json.MarshalIndent(map[string]any{"apps": cat.List(true), "problems": cat.Problems}, "", "  ")
	got = append(got, '\n')
	path := "testdata/catalog.golden.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("catalog changed; diff against %s:\n%s", path, got)
	}
}

func TestCatalogDetails(t *testing.T) {
	cat := buildFixture(t)
	get := func(id string) *App {
		t.Helper()
		a, ok := cat.Get(id)
		if !ok {
			t.Fatalf("no app %q", id)
		}
		return a
	}

	// The user directory overrides the system one, and a file renames it;
	// the old ID still finds it.
	b := get("browser")
	if b.Name != "Browser" || b.Instance.Policy != "multiple" ||
		!reflect.DeepEqual(b.Runner.Command, []string{"/usr/lib/firefox-esr/firefox-esr", "--kiosk"}) {
		t.Fatalf("browser = %+v", b)
	}
	if get("firefox-esr") != b {
		t.Fatal("old ID does not find the renamed app")
	}
	if _, ok := cat.apps["firefox-esr"]; ok {
		t.Fatal("renamed app still listed under its old ID")
	}

	// Defaults by surface.
	if b.Surface != SurfaceWindow || b.Restart != "never" || !b.Window.Fullscreen || b.Instance.IfRunning != "focus" {
		t.Fatalf("window defaults: %+v", b)
	}
	j := get("jellyfin")
	if j.Surface != SurfaceBackground || j.Restart != "always" || j.Instance.Policy != "single" || j.Source != "file" {
		t.Fatalf("background app: %+v", j)
	}
	if tv := get("jellyfin-tv"); tv.Audio.Volume == nil || *tv.Audio.Volume != 70 || tv.Runner.URL == "" {
		t.Fatalf("jellyfin-tv: %+v", tv)
	}

	// Hidden by default, and shown again by a file.
	for _, id := range []string{"settings", "htop", "dolphin"} {
		if a, ok := cat.Get(id); ok && !a.Hidden {
			t.Errorf("%s should be hidden", id)
		}
	}
	if get("foot").Hidden || get("foot").Window.Fullscreen {
		t.Fatalf("foot.toml should show foot, windowed: %+v", get("foot"))
	}
	if get("kde-dolphin") == nil || get("kcalc").Aliases[0] != "org.kde.kcalc" || get("org.kde.kcalc").ID != "kcalc" {
		t.Fatal("IDs from subdirectories or reverse-DNS names wrong")
	}
	if got := get("quoted"); got.Name != "My Tool" ||
		!reflect.DeepEqual(got.Runner.Command, []string{"/opt/My App/run", "--title", `Big "Screen"`, "--rate=100%"}) {
		t.Fatalf("quoted = %+v", got)
	}

	// Left out quietly: links, deleted by the user, TryExec missing.
	for _, id := range []string{"website", "vlc", "missing"} {
		if _, ok := cat.Get(id); ok {
			t.Errorf("%s should not be in the catalog", id)
		}
	}
	// The hidden list excludes hidden apps; the full one has them.
	if len(cat.List(false)) >= len(cat.List(true)) {
		t.Fatal("List(false) includes hidden apps")
	}
}

func TestCatalogProblems(t *testing.T) {
	cat := buildFixture(t)
	want := map[string]string{
		"broken.desktop":     "no [Desktop Entry]",
		"noexec.desktop":     "has no command",
		"Bad Name.toml":      `id "Bad Name"`,
		"bad-runner.toml":    `runner type "teleport"`,
		"broken.toml":        "",
		"later-clash.toml":   "already defined by testdata/apps/jellyfin.toml",
		"discovered-id.toml": `use extends = "steam"`,
		"needs-ghost.toml":   `requires "ghost"`,
		"orphan.toml":        `extends "dota3"`,
		"typo.toml":          "unknown key(s): window.fulscreen",
	}
	got := map[string]string{}
	for _, p := range cat.Problems {
		got[filepath.Base(p.File)] = p.Error
	}
	for file, sub := range want {
		msg, ok := got[file]
		if !ok {
			t.Errorf("%s: not reported", file)
			continue
		}
		if !strings.Contains(msg, sub) {
			t.Errorf("%s: %q does not mention %q", file, msg, sub)
		}
	}
	if len(got) != len(want) {
		t.Errorf("problems = %v", got)
	}
	// A rejected clash leaves the first definition intact.
	if j, _ := cat.Get("jellyfin"); j.Runner.Type != RunnerDocker {
		t.Fatal("clashing file replaced the first one")
	}
}

func TestAppFileDefaultsIDFromName(t *testing.T) {
	f, err := ParseAppFile("/x/apps/blog.toml", []byte(`runner = { type = "docker", build = "." }`))
	if err != nil || f.ID != "blog" {
		t.Fatalf("%+v %v", f, err)
	}
	cat := Build(nil, []AppFile{f}, nil)
	if a, ok := cat.Get("blog"); !ok || a.Surface != SurfaceBackground {
		t.Fatalf("blog = %+v", a)
	}
}

func TestValidation(t *testing.T) {
	for body, want := range map[string]string{
		`runner = { type = "exec" }`:                                                         "needs a command",
		`runner = { type = "url", url = "localhost:8096" }`:                                  "http://",
		`runner = { type = "docker" }`:                                                       "image or a build",
		`runner = { type = "compose" }`:                                                      "needs a file",
		`runner = { type = "steam" }`:                                                        "needs an app_id",
		"runner = { type = \"exec\", command = [\"x\"] }\nsurface = \"tv\"":                  `surface "tv"`,
		"runner = { type = \"exec\", command = [\"x\"] }\nrestart = \"maybe\"":               `restart "maybe"`,
		"runner = { type = \"exec\", command = [\"x\"] }\n[instance]\npolicy = \"many\"":     "instance.policy",
		"runner = { type = \"exec\", command = [\"x\"] }\n[instance]\nif_running = \"kill\"": "instance.if_running",
		"runner = { type = \"exec\", command = [\"x\"] }\n[window]\nwrap = \"xvfb\"":         "window.wrap",
		"runner = { type = \"exec\", command = [\"x\"] }\n[audio]\nvolume = 200":             "audio.volume",
	} {
		f, err := ParseAppFile("/x/a.toml", []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		cat := Build(nil, []AppFile{f}, nil)
		if len(cat.Problems) != 1 || !strings.Contains(cat.Problems[0].Error, want) {
			t.Errorf("%q: problems %v, want %q", body, cat.Problems, want)
		}
	}
}

// --- the module -----------------------------------------------------------

type fakeCore struct {
	mu     sync.Mutex
	events []sdk.Event
}

func (f *fakeCore) Emit(e sdk.Event) { f.mu.Lock(); f.events = append(f.events, e); f.mu.Unlock() }
func (f *fakeCore) Do(context.Context, sdk.Action) (sdk.Result, error) {
	return sdk.Result{}, nil
}
func (f *fakeCore) Subscribe(context.Context, string) <-chan sdk.Event { return nil }
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
	if st, _ := m.State(ctx); len(st.(AppList).Problems) == 0 {
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

func FuzzParseDesktopEntry(f *testing.F) {
	for _, file := range []string{"firefox-esr.desktop", "quoted.desktop", "broken.desktop"} {
		if b, err := os.ReadFile(filepath.Join("testdata/system/applications", file)); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := ParseDesktopEntry(data)
		if err == nil {
			_, _ = ExecArgs(e.Exec)
		}
	})
}

func FuzzExecArgs(f *testing.F) {
	for _, s := range []string{`firefox %u`, `"a b" c\ d`, `run "x \"y\""`, `%%%`, `"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		args, err := ExecArgs(s)
		if err == nil && len(args) == 0 {
			t.Fatal("no error but no args")
		}
	})
}

func FuzzParseAppFile(f *testing.F) {
	entries, _ := os.ReadDir("testdata/apps")
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join("testdata/apps", e.Name())); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if file, err := ParseAppFile("/x/fuzz.toml", data); err == nil {
			Build(nil, []AppFile{file}, nil) // must not panic
		}
	})
}

func (m *Module) seenChanges() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changes
}
