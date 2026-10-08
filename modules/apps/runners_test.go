package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

// --- Flatpak discovery ---------------------------------------------------------

func TestFlatpakEntries(t *testing.T) {
	dir := t.TempDir()
	apps := filepath.Join(dir, "applications")
	_ = os.MkdirAll(apps, 0o755)
	_ = os.WriteFile(filepath.Join(apps, "tv.kodi.Kodi.desktop"), []byte(`[Desktop Entry]
Type=Application
Name=Kodi
Exec=/usr/bin/flatpak run --branch=stable --arch=x86_64 --command=kodi --file-forwarding tv.kodi.Kodi @@ %F @@
X-Flatpak=tv.kodi.Kodi
`), 0o644)
	found, problems := (&DesktopSource{Dirs: []string{apps}}).Scan()
	if len(problems) != 0 || len(found) != 1 {
		t.Fatalf("found %+v, problems %v", found, problems)
	}
	cat := Build(found, nil, nil)
	kodi, ok := cat.Get("kodi")
	if !ok {
		t.Fatalf("no kodi: %+v", cat.List(true))
	}
	if kodi.Runner.Type != RunnerFlatpak || kodi.Runner.AppID != "tv.kodi.Kodi" {
		t.Fatalf("runner %+v", kodi.Runner)
	}
	// Flatpak apps run in a scope of their own: windows match by app ID
	// or by the sandbox's FLATPAK_ID.
	if kodi.Match != (Match{AppID: "tv.kodi.Kodi", Env: "FLATPAK_ID=tv.kodi.Kodi"}) {
		t.Fatalf("match %+v", kodi.Match)
	}
}

func TestDefaultDesktopDirsIncludeFlatpak(t *testing.T) {
	// A service has no XDG_DATA_DIRS: the default plus Flatpak's exports.
	t.Setenv("XDG_DATA_DIRS", "")
	t.Setenv("XDG_DATA_HOME", "/home/u/.local/share")
	want := []string{
		"/usr/share/applications", "/usr/local/share/applications",
		"/var/lib/flatpak/exports/share/applications",
		"/home/u/.local/share/flatpak/exports/share/applications",
		"/home/u/.local/share/applications",
	}
	if got := DefaultDesktopDirs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("dirs\n%q\nwant\n%q", got, want)
	}
	// A session's own list is used as it is.
	t.Setenv("XDG_DATA_DIRS", "/opt/share:/usr/share")
	if got := DefaultDesktopDirs(); !reflect.DeepEqual(got, []string{"/usr/share/applications", "/opt/share/applications", "/home/u/.local/share/applications"}) {
		t.Fatalf("dirs %q", got)
	}
}

// --- commands: flatpak, url, gamescope ---------------------------------------------

func TestRunnerCommands(t *testing.T) {
	have := map[string]bool{"firefox-esr": true, "gamescope": true}
	look := func(name string) (string, error) {
		if have[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	state := t.TempDir()
	r := &ExecRunner{StateDir: state, LookPath: look}
	for _, c := range []struct {
		app  App
		want []string
	}{
		{App{ID: "kodi", Runner: Runner{Type: RunnerFlatpak, AppID: "tv.kodi.Kodi"}},
			[]string{"flatpak", "run", "tv.kodi.Kodi"}},
		{App{ID: "jelly", Runner: Runner{Type: RunnerURL, URL: "http://tv.lan:8096"}},
			[]string{"firefox-esr", "--kiosk", "--new-instance", "--profile", filepath.Join(state, "browser", "jelly"), "http://tv.lan:8096"}},
		{App{ID: "game", Runner: Runner{Type: RunnerExec, Command: []string{"supertux2"}}, Window: Window{Wrap: "gamescope"}},
			[]string{"gamescope", "-f", "--", "supertux2"}},
	} {
		got, err := r.command(&c.app)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q %v, want %q", c.app.ID, got, err, c.want)
		}
	}
	if fi, err := os.Stat(filepath.Join(state, "browser", "jelly")); err != nil || !fi.IsDir() {
		t.Fatal("no browser profile directory")
	}

	// Configured browser and gamescope arguments win.
	r.Browser = []string{"chromium", "--app={url}", "--user-data-dir={profile}"}
	r.Gamescope = []string{"gamescope", "-W", "1280", "-H", "800", "-f"}
	got, _ := r.command(&App{ID: "jelly", Runner: Runner{Type: RunnerURL, URL: "https://x"}, Window: Window{Wrap: "gamescope"}})
	want := []string{"gamescope", "-W", "1280", "-H", "800", "-f", "--", "chromium", "--app=https://x", "--user-data-dir=" + filepath.Join(state, "browser", "jelly")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configured: %q", got)
	}

	// Missing programs are clear errors.
	r = &ExecRunner{StateDir: state, LookPath: func(string) (string, error) { return "", errors.New("no") }}
	if _, err := r.command(&App{ID: "w", Runner: Runner{Type: RunnerURL, URL: "http://x"}}); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("no browser: %v", err)
	}
	if _, err := r.command(&App{ID: "g", Runner: Runner{Type: RunnerExec, Command: []string{"x"}}, Window: Window{Wrap: "gamescope"}}); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("no gamescope: %v", err)
	}
}

// --- handoff -------------------------------------------------------------------------

// fakeProcs is a /proc with processes that have an environment.
type fakeProcs struct {
	root   string
	mu     sync.Mutex
	killed []string
}

func newFakeProcs(t *testing.T) *fakeProcs { return &fakeProcs{root: t.TempDir()} }

func (p *fakeProcs) spawn(pid int, env ...string) {
	dir := filepath.Join(p.root, fmt.Sprint(pid))
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644)
}

func (p *fakeProcs) exit(pid int) { _ = os.RemoveAll(filepath.Join(p.root, fmt.Sprint(pid))) }

// kill records the signal; SIGTERM ends a process unless it is stubborn.
func (p *fakeProcs) kill(stubborn map[int]bool) func(int, syscall.Signal) error {
	return func(pid int, sig syscall.Signal) error {
		p.mu.Lock()
		p.killed = append(p.killed, fmt.Sprintf("%d %v", pid, sig))
		p.mu.Unlock()
		if sig == syscall.SIGKILL || !stubborn[pid] {
			p.exit(pid)
		}
		return nil
	}
}

func (p *fakeProcs) signals() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.killed...)
}

type handoffRig struct {
	r     *ExecRunner
	sd    *fakeSystemd
	procs *fakeProcs
	clock *clock.Fake
	ended chan Ended
	ran   chan []string
}

func newHandoffRig(t *testing.T) *handoffRig {
	t.Helper()
	h := &handoffRig{sd: newFakeSystemd(), procs: newFakeProcs(t),
		clock: clock.NewFake(time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)), ended: make(chan Ended, 4), ran: make(chan []string, 4)}
	h.r = &ExecRunner{Systemd: h.sd, RuntimeDir: fakeSession(t), ProcRoot: h.procs.root, Clock: h.clock,
		Kill: h.procs.kill(map[int]bool{502: true}),
		RunCommand: func(_ context.Context, argv, _ []string, _ string) error {
			h.ran <- argv
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.r.Watch(ctx, func(e Ended) { h.ended <- e }) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, func() bool { return h.sd.watching() == 1 })
	return h
}

// poll lets the poller run once.
func (h *handoffRig) poll(t *testing.T) {
	t.Helper()
	h.clock.BlockUntil(1)
	h.clock.Advance(2 * time.Second)
}

// seen waits until the poller has seen an instance's processes.
func (h *handoffRig) seen(t *testing.T, id string) {
	t.Helper()
	waitUntil(t, func() bool {
		h.clock.Advance(2 * time.Second)
		h.r.mu.Lock()
		defer h.r.mu.Unlock()
		hh, ok := h.r.handoffs[id]
		return ok && hh.seen
	})
}

// exitedWhileAway marks a unit's command as exited without telling any
// watcher, as if hostd was not running.
func (h *handoffRig) exitedWhileAway(name string) {
	h.sd.mu.Lock()
	defer h.sd.mu.Unlock()
	u := h.sd.units[name]
	u.info.SubState, u.info.ExitCode, u.info.MainPID = "exited", 1, 0
}

func (h *handoffRig) noEnd(t *testing.T) {
	t.Helper()
	select {
	case e := <-h.ended:
		t.Fatalf("ended early: %+v", e)
	case <-time.After(30 * time.Millisecond):
	}
}

func (h *handoffRig) end(t *testing.T) Ended {
	t.Helper()
	for range 50 {
		select {
		case e := <-h.ended:
			return e
		case <-time.After(10 * time.Millisecond):
			h.clock.Advance(2 * time.Second)
		}
	}
	t.Fatal("instance did not end")
	return Ended{}
}

var portal = &App{ID: "portal2", Surface: SurfaceWindow,
	Runner: Runner{Type: RunnerExec, Command: []string{"steam", "steam://rungameid/620"}, Handoff: "SteamAppId=620"}}

func TestHandoffLifecycle(t *testing.T) {
	h := newHandoffRig(t)
	ctx := context.Background()
	inst, err := h.r.Start(ctx, Instance{ID: "portal2", App: "portal2", Surface: SurfaceWindow}, portal)
	if err != nil {
		t.Fatal(err)
	}
	unit := unitName("portal2")
	if d := h.sd.units[unit].info.Description; d != "hostd instance portal2 of app portal2 handoff SteamAppId=620" {
		t.Fatalf("description %q", d)
	}
	// The command hands off and exits: the instance goes on.
	h.sd.exit(unit, 0, 1)
	h.poll(t)
	h.noEnd(t)
	// The game runs, then ends.
	h.procs.spawn(501, "SteamAppId=620", "HOME=/home/screen")
	h.procs.spawn(777, "HOME=/home/screen") // someone else
	h.seen(t, "portal2")
	h.noEnd(t)
	h.procs.exit(501)
	if e := h.end(t); e.Instance != "portal2" || e.ExitCode != 0 {
		t.Fatalf("ended %+v", e)
	}
	if names := h.sd.unitNames(); len(names) != 0 {
		t.Fatalf("units left: %v (inst %+v)", names, inst)
	}
}

func TestHandoffNeverAppears(t *testing.T) {
	h := newHandoffRig(t)
	if _, err := h.r.Start(context.Background(), Instance{ID: "portal2", App: "portal2", Surface: SurfaceWindow}, portal); err != nil {
		t.Fatal(err)
	}
	h.sd.exit(unitName("portal2"), 0, 1)
	h.clock.BlockUntil(1)
	h.clock.Advance(3*time.Minute + 2*time.Second)
	if e := h.end(t); e.ExitCode != 1 || !strings.Contains(e.Reason, "SteamAppId=620") {
		t.Fatalf("ended %+v", e)
	}
}

func TestHandoffCommandFails(t *testing.T) {
	// The command fails instead of handing off: reported as usual.
	h := newHandoffRig(t)
	if _, err := h.r.Start(context.Background(), Instance{ID: "portal2", App: "portal2", Surface: SurfaceWindow}, portal); err != nil {
		t.Fatal(err)
	}
	h.sd.exit(unitName("portal2"), 2, 1)
	select {
	case e := <-h.ended:
		if e.ExitCode != 2 {
			t.Fatalf("ended %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failure not reported")
	}
}

func TestHandoffHostStaysAndIsReused(t *testing.T) {
	// The first launch started Steam itself, which stays in the unit:
	// the game's end does not stop it, and the next launch hands off to
	// it without a new unit.
	h := newHandoffRig(t)
	ctx := context.Background()
	in := Instance{ID: "portal2", App: "portal2", Surface: SurfaceWindow}
	if _, err := h.r.Start(ctx, in, portal); err != nil {
		t.Fatal(err)
	}
	h.procs.spawn(501, "SteamAppId=620")
	h.seen(t, "portal2")
	h.procs.exit(501)
	h.end(t)
	if names := h.sd.unitNames(); len(names) != 1 {
		t.Fatalf("the host unit was stopped: %v", names)
	}
	if _, err := h.r.Start(ctx, in, portal); err != nil {
		t.Fatal(err)
	}
	select {
	case argv := <-h.ran:
		if !reflect.DeepEqual(argv, portal.Runner.Command) {
			t.Fatalf("ran %q", argv)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not hand off to the running host")
	}
	// Stopping the game ends its processes, politely, then firmly for
	// the stubborn one; the host stays.
	h.procs.spawn(501, "SteamAppId=620")
	h.procs.spawn(502, "SteamAppId=620")
	stopped := make(chan error, 1)
	go func() { stopped <- h.r.Stop(ctx, in) }()
	waitUntil(t, func() bool { return len(h.procs.signals()) == 2 })
	h.clock.BlockUntil(2)
	h.clock.Advance(10 * time.Second)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	want := []string{"501 terminated", "502 terminated", "502 killed"}
	if got := h.procs.signals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("signals %q", got)
	}
	if names := h.sd.unitNames(); len(names) != 1 {
		t.Fatalf("units %v", names)
	}
}

func TestHandoffAdopt(t *testing.T) {
	h := newHandoffRig(t)
	ctx := context.Background()
	desc := description(Instance{ID: "portal2", App: "portal2"}, "SteamAppId=620")
	// Before hostd restarted: the command exited, the game still runs.
	_ = h.sd.StartTransient(ctx, unitName("portal2"), UnitSpec{Description: desc, Argv: []string{"steam"}})
	h.exitedWhileAway(unitName("portal2"))
	h.procs.spawn(501, "SteamAppId=620")
	// Another one whose game ended while hostd was away.
	_ = h.sd.StartTransient(ctx, unitName("hl"), UnitSpec{Description: description(Instance{ID: "hl", App: "hl"}, "SteamAppId=70"), Argv: []string{"steam"}})
	h.exitedWhileAway(unitName("hl"))
	// And a unit that hosts Steam, with no game running.
	_ = h.sd.StartTransient(ctx, unitName("tf2"), UnitSpec{Description: description(Instance{ID: "tf2", App: "tf2"}, "SteamAppId=440"), Argv: []string{"steam"}})

	found, err := h.r.Adopt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]State{}
	for _, in := range found {
		got[in.ID] = in.State
	}
	if !reflect.DeepEqual(got, map[string]State{"portal2": StateRunning, "hl": StateExited}) {
		t.Fatalf("adopted %v", got)
	}
	h.procs.exit(501)
	if e := h.end(t); e.Instance != "portal2" {
		t.Fatalf("ended %+v", e)
	}
}

// --- app actions ---------------------------------------------------------------------

func TestDesktopActions(t *testing.T) {
	e, err := ParseDesktopEntry([]byte(`[Desktop Entry]
Type=Application
Name=Chromium
Exec=chromium %U
Actions=new-window;new-private-window;missing;

[Desktop Action new-window]
Name=New Window
Name[de]=Neues Fenster
Exec=chromium

[Desktop Action new-private-window]
Name=New Incognito Window
Exec=chromium --incognito

[Desktop Action unlisted]
Name=Not listed, so not offered
Exec=chromium --secret
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []DesktopAction{{"new-window", "New Window", "chromium"}, {"new-private-window", "New Incognito Window", "chromium --incognito"}}
	if !reflect.DeepEqual(e.Actions, want) || e.Exec != "chromium %U" {
		t.Fatalf("entry %+v", e)
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "chromium.desktop"), []byte(`[Desktop Entry]
Type=Application
Name=Chromium
Exec=chromium %U
Actions=new-private-window;
[Desktop Action new-private-window]
Name=New Incognito Window
Exec=chromium --incognito %U
`), 0o644)
	found, _ := (&DesktopSource{Dirs: []string{dir}}).Scan()
	// An app file adds one and renames another.
	files := []AppFile{{Path: "/x/chromium.toml", Extends: "chromium", Actions: []AppAction{
		{ID: "kiosk", Name: "Kiosk", Command: []string{"chromium", "--kiosk"}},
		{ID: "new-private-window", Name: "Private", Command: []string{"chromium", "--incognito"}},
	}}}
	cat := Build(found, files, nil)
	app, _ := cat.Get("chromium")
	wantActions := []AppAction{
		{ID: "new-private-window", Name: "Private", Command: []string{"chromium", "--incognito"}},
		{ID: "kiosk", Name: "Kiosk", Command: []string{"chromium", "--kiosk"}},
	}
	if len(cat.Problems) != 0 || !reflect.DeepEqual(app.Actions, wantActions) {
		t.Fatalf("actions %+v, problems %v", app.Actions, cat.Problems)
	}

	for body, want := range map[string]string{
		`[[actions]]` + "\nid = \"x\"\nname = \"X\"":                                                                         "needs a name and a command",
		`[[actions]]` + "\nid = \"Bad ID\"\nname = \"X\"\ncommand = [\"x\"]":                                                 "action id",
		"[[actions]]\nid = \"a\"\nname = \"A\"\ncommand = [\"x\"]\n[[actions]]\nid = \"a\"\nname = \"B\"\ncommand = [\"y\"]": "defined twice",
	} {
		f, err := ParseAppFile("/x/a.toml", []byte("runner = { type = \"exec\", command = [\"a\"] }\n"+body))
		if err != nil {
			t.Fatal(err)
		}
		c := Build(nil, []AppFile{f}, nil)
		if len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Error, want) {
			t.Errorf("%q: problems %v, want %q", body, c.Problems, want)
		}
	}
}

func TestStartAction(t *testing.T) {
	sd := newFakeSystemd()
	r := newRig(t, sd, newFakeDocker(), fakeSession(t))
	writeApps(t, r.m.opts.AppsDir, `
-- chrome.toml --
runner = { type = "exec", command = ["chromium"] }
[[actions]]
id = "incognito"
name = "New incognito window"
command = ["chromium", "--incognito"]
`)
	r.m.rescan()
	r.start(t, "chrome")
	r.next(t)
	r.next(t)
	// The app runs (single, if_running focus): its action is still a new
	// instance, with the action's command.
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: "app.start", Args: json.RawMessage(`{"id":"chrome","action":"incognito"}`),
		Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Data), `"instance":"chrome#2"`) {
		t.Fatalf("result %s", res.Data)
	}
	sd.mu.Lock()
	last := sd.started[len(sd.started)-1]
	sd.mu.Unlock()
	if !reflect.DeepEqual(last.Argv, []string{"chromium", "--incognito"}) {
		t.Fatalf("argv %q", last.Argv)
	}
	if ev := r.next(t); !strings.Contains(string(ev.Data), `"action":"incognito"`) {
		t.Fatalf("event %s", ev.Data)
	}
	_, err = r.e.Submit(context.Background(), sdk.Action{Type: "app.start", Args: json.RawMessage(`{"id":"chrome","action":"nope"}`),
		Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
	if sdk.CodeOf(err) != sdk.CodeNotFound || !strings.Contains(err.Error(), "it has: incognito") {
		t.Fatalf("unknown action: %v", err)
	}
}
