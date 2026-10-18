package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/sdk"

	"github.com/davitizhgenti/hostd/modules/apps/catalog"
)

func TestStartAction(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
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
	sd.Mu.Lock()
	last := sd.Started[len(sd.Started)-1]
	sd.Mu.Unlock()
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

func TestReconcileAfterLostStream(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	waitUntil(t, func() bool { return sd.Watching() == 1 })
	r.start(t, "term")
	r.next(t) // starting
	r.next(t) // started
	r.start(t, "tabs")
	r.next(t)
	r.next(t)

	// term ends while the bus connection is down: its event is lost.
	sd.ExitQuietly(contract.UnitName("term"), 0)
	sd.BreakWatch()

	ev := r.next(t)
	var in Instance
	_ = json.Unmarshal(ev.Data, &in)
	if ev.Type != EventExited || in.ID != "term" || in.State != StateExited {
		t.Fatalf("event %s %s", ev.Type, ev.Data)
	}
	if live := r.live(); len(live) != 1 || live[0] != "tabs" {
		t.Fatalf("live after reconcile: %v", live)
	}
	// Watching again: a later end arrives as an event, as before.
	waitUntil(t, func() bool { return sd.Watching() == 1 })
	sd.Exit(contract.UnitName("tabs"), 3, 1)
	if ev := r.next(t); ev.Type != EventFailed {
		t.Fatalf("after reconnect: %s %s", ev.Type, ev.Data)
	}
}

// A command that ends while systemd announces its unit, without any
// signal about it: the check shortly after the start finds it.
func TestEarlyEndFoundAfterTheStart(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	fc := clock.NewFake(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	r := newRigClock(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t), fc)
	waitUntil(t, func() bool { return sd.Watching() == 1 })
	r.start(t, "term")
	r.next(t) // starting
	r.next(t) // started
	sd.ExitQuietly(contract.UnitName("term"), 0)
	// Time passes until the look again after the start (at 250ms) finds it.
	var ev sdk.Event
	waitUntil(t, func() bool {
		select {
		case ev = <-r.events:
			return true
		default:
			fc.Advance(50 * time.Millisecond)
			return false
		}
	})
	var in Instance
	_ = json.Unmarshal(ev.Data, &in)
	if ev.Type != EventExited || in.ID != "term" || in.ExitCode == nil || *in.ExitCode != 0 {
		t.Fatalf("event %s %s", ev.Type, ev.Data)
	}
	if live := r.live(); len(live) != 0 {
		t.Fatalf("still live: %v", live)
	}
}

// An autostart app is started when hostd starts, unless it already runs
// (hostd restarted, the app did not); others are not.
func TestAutostart(t *testing.T) {
	apps := rigApps + `-- notebook.toml --
runner = { type = "process", command = ["jupyter", "lab"] }
autostart = true
`
	sd := unitstest.NewFakeSystemd()
	r := newRigApps(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t), nil, apps)
	ev := r.next(t) // starting
	if ev.Type != EventStarting || !strings.Contains(string(ev.Data), `"app":"notebook"`) {
		t.Fatalf("event %s %s", ev.Type, ev.Data)
	}
	r.next(t) // started
	if live := r.live(); len(live) != 1 || live[0] != "notebook" {
		t.Fatalf("live: %v", live)
	}

	// hostd starts again while it runs: it is found, not started twice.
	again := newRigApps(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t), nil, apps)
	waitUntil(t, func() bool { return sd.Watching() == 2 })
	if live := again.live(); len(live) != 1 || live[0] != "notebook" {
		t.Fatalf("live after a restart: %v", live)
	}
	if n := len(sd.Started); n != 1 {
		t.Fatalf("started %d times", n)
	}
}

// spawn makes a process the exec runner can see, with this environment.
func (r *rig) spawn(t *testing.T, pid int, env ...string) {
	t.Helper()
	dir := filepath.Join(r.proc, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), []byte(strings.Join(env, "\x00")+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A handoff app hostd did not start (a game started from Steam's own
// menu) becomes an instance while its processes run: in front, since
// someone here started it. It ends with them, and is not found again
// while they linger after an end.
func TestAppsStartedElsewhereBecomeInstances(t *testing.T) {
	apps := rigApps + `-- game.toml --
name = "Game"
runner = { type = "exec", command = ["steam", "steam://rungameid/550"], handoff = "SteamAppId=550" }
[match]
env = "SteamAppId=550"
`
	sd := unitstest.NewFakeSystemd()
	fc := clock.NewFake(time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC))
	r := newRigApps(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t), fc, apps)
	waitUntil(t, func() bool { return sd.Watching() == 1 })
	tick := func() { fc.Advance(2 * time.Second) }

	r.spawn(t, 4242, "FLATPAK_ID=com.valvesoftware.Steam", "SteamAppId=550")
	waitUntil(t, func() bool { tick(); return len(r.live()) == 1 })
	ev := r.next(t)
	var in Instance
	_ = json.Unmarshal(ev.Data, &in)
	if ev.Type != EventStarted || ev.Source == nil || ev.Source.Kind != sdk.SourceExternal || in.ID != "game" || !in.Front ||
		in.State != StateRunning || in.Match == nil || in.Match.Env != "SteamAppId=550" {
		t.Fatalf("event %s %s", ev.Type, ev.Data)
	}

	// Its processes end: so does the instance.
	_ = os.RemoveAll(filepath.Join(r.proc, "4242"))
	waitUntil(t, func() bool { tick(); return len(r.live()) == 0 })
	if ev := r.next(t); ev.Type != EventExited {
		t.Fatalf("end: %s %s", ev.Type, ev.Data)
	}

	// A process that lingers after the end is not a new start.
	r.spawn(t, 4243, "SteamAppId=550")
	for range 5 {
		tick()
	}
	if live := r.live(); len(live) != 0 {
		t.Fatalf("found again at once: %v", live)
	}
	fc.Advance(30 * time.Second)
	waitUntil(t, func() bool { tick(); return len(r.live()) == 1 })
}

// --- the contract other modules rely on --------------------------------------------

func TestInstanceContract(t *testing.T) {
	// Every field of contract.Instance must come out of the apps module's
	// own Instance, with the same value: renaming a JSON field on either
	// side breaks this test, not the display module at run time.
	in := Instance{ID: "chrome#2", App: "chrome", Name: "Chromium", Runner: RunnerExec, Surface: SurfaceWindow,
		State: StateRunning, Fullscreen: true, Front: true, Action: "incognito",
		Match: &Match{Class: "c", AppID: "a", Title: "t", Env: "K=v"}}
	var got contract.Instance
	if err := json.Unmarshal(sdk.MustJSON(in), &got); err != nil {
		t.Fatal(err)
	}
	full := true
	want := contract.Instance{ID: "chrome#2", App: "chrome", Name: "Chromium", Surface: "window", State: "running",
		Fullscreen: &full, Front: true, Action: "incognito", Match: &contract.Match{Class: "c", AppID: "a", Title: "t", Env: "K=v"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contract view\n%+v\nwant\n%+v", got, want)
	}
}

func TestClosingCountsAsExit(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	waitUntil(t, func() bool { return sd.Watching() == 1 }) // else the exit below can reach no one
	r.start(t, "term")
	r.next(t)
	r.next(t)
	if _, err := r.do(t, contract.ActionInstanceClosing, "term"); err != nil {
		t.Fatal(err)
	}
	sd.Exit(contract.UnitName("term"), 1, 1) // a terminal's shell ends with 1 when its window closes
	ev := r.next(t)
	var in Instance
	_ = json.Unmarshal(ev.Data, &in)
	if ev.Type != EventExited || in.State != StateExited || in.ExitCode == nil || *in.ExitCode != 1 {
		t.Fatalf("event %s %s", ev.Type, ev.Data)
	}
	if _, err := r.do(t, contract.ActionInstanceClosing, "ghost"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown instance: %v", err)
	}
}

func TestHandoffActionGoesToRunningApp(t *testing.T) {
	// Steam's Big Picture action is passed to Steam, never a second
	// instance that would last as long as Steam.
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	ran := make(chan []string, 2)
	r.m.opts.Backends[RunnerExec].(*ExecRunner).RunCommand = func(_ context.Context, argv, _ []string, _ string) error {
		ran <- argv
		return nil
	}
	writeApps(t, r.m.opts.AppsDir, `
-- steam.toml --
runner = { type = "exec", command = ["flatpak", "run", "com.valvesoftware.Steam"], handoff = "FLATPAK_ID=com.valvesoftware.Steam" }
[[actions]]
id = "bigpicture"
name = "Big Picture"
command = ["flatpak", "run", "com.valvesoftware.Steam", "steam://open/bigpicture"]
`)
	r.m.rescan()
	// Steam does not run: the action starts Steam itself (its own
	// command), then hands Steam the request.
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: "app.start", Args: json.RawMessage(`{"id":"steam","action":"bigpicture"}`),
		Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
	if err != nil || !strings.Contains(string(res.Data), `"instance":"steam"`) {
		t.Fatalf("start for an action: %s %v", res.Data, err)
	}
	r.next(t)
	r.next(t)
	sd.Mu.Lock()
	first := sd.Started[0].Argv
	sd.Mu.Unlock()
	if !reflect.DeepEqual(first, []string{"flatpak", "run", "com.valvesoftware.Steam"}) {
		t.Fatalf("started %q", first)
	}
	if argv := <-ran; argv[len(argv)-1] != "steam://open/bigpicture" {
		t.Fatalf("passed %q", argv)
	}
	res, err = r.e.Submit(context.Background(), sdk.Action{Type: "app.start", Args: json.RawMessage(`{"id":"steam","action":"bigpicture"}`),
		Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Data), `"instance":"steam"`) || !strings.Contains(string(res.Data), `"already_running":true`) {
		t.Fatalf("result %s", res.Data)
	}
	if argv := <-ran; argv[len(argv)-1] != "steam://open/bigpicture" {
		t.Fatalf("passed %q", argv)
	}
	sd.Mu.Lock()
	units := len(sd.Started)
	sd.Mu.Unlock()
	if units != 1 {
		t.Fatalf("%d units started, want 1", units)
	}
}

func TestRequiredAppsStartFirst(t *testing.T) {
	// A game requires Steam: starting the game starts Steam first, once.
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	writeApps(t, r.m.opts.AppsDir, `
-- steam.toml --
runner = { type = "exec", command = ["steam"] }
-- game.toml --
requires = ["steam"]
runner = { type = "exec", command = ["game"] }
`)
	r.m.rescan()
	r.start(t, "game")
	sd.Mu.Lock()
	var argv []string
	for _, u := range sd.Started {
		argv = append(argv, u.Argv[0])
	}
	sd.Mu.Unlock()
	if !reflect.DeepEqual(argv, []string{"steam", "game"}) {
		t.Fatalf("started %q, want steam then game", argv)
	}
	r.do(t, "instance.stop", "game")
	r.start(t, "game") // Steam still runs: not started again
	sd.Mu.Lock()
	n := len(sd.Started)
	sd.Mu.Unlock()
	if n != 3 {
		t.Fatalf("%d starts, want 3", n)
	}
}

func TestInstanceLogs(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	var asked []string
	var since []time.Time
	r.m.opts.Journal = func(_ context.Context, q JournalQuery) ([]string, error) {
		asked = append(asked, fmt.Sprintf("%s %d %v", q.Unit, q.Lines, q.Plain))
		since = append(since, q.Since)
		return []string{"2026-10-10T19:00:00 core site[1]: listening"}, nil
	}
	r.start(t, "term")
	got, err := r.m.Read(context.Background(), "logs", map[string]string{"id": "term", "lines": "50"})
	if err != nil {
		t.Fatal(err)
	}
	logs := got.(Logs)
	in, _ := r.m.readInstance("term")
	if logs.Unit != "hostd-term.service" || len(logs.Lines) != 1 || asked[0] != "hostd-term.service 50 false" || !since[0].Equal(in.Started) {
		t.Fatalf("logs %+v, asked %q", logs, asked)
	}
	if _, err := r.m.Read(context.Background(), "logs", map[string]string{"id": "term", "lines": "99999", "plain": "true"}); err != nil || asked[1] != "hostd-term.service 5000 true" {
		t.Fatalf("capped: %q %v", asked, err)
	}
	if _, err := r.m.Read(context.Background(), "logs", map[string]string{"id": "ghost"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown instance: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	r.m.opts.SecretsDir = filepath.Join(t.TempDir(), "secrets")
	writeApps(t, r.m.opts.AppsDir, `
-- api.toml --
runner = { type = "exec", command = ["api"] }
env = { DB = "postgres://app:{secret:db-pass}@db/app", MODE = "prod" }
`)
	r.m.rescan()
	if _, err := r.do(t, "app.start", "api"); sdk.CodeOf(err) != sdk.CodeNotFound || !strings.Contains(err.Error(), "hostctl secret set db-pass") {
		t.Fatalf("missing secret: %v", err)
	}
	put := func(name, value string) error {
		_, err := r.e.Submit(context.Background(), sdk.Action{Type: "secret.set", Args: sdk.MustJSON(map[string]string{"name": name, "value": value}),
			Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
		return err
	}
	if err := put("../x", "v"); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("bad name: %v", err)
	}
	if err := put("db-pass", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(r.m.opts.SecretsDir, "db-pass")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file: %v %v", fi, err)
	}
	r.start(t, "api")
	sd.Mu.Lock()
	env := sd.Started[len(sd.Started)-1].Env
	sd.Mu.Unlock()
	if !slices.Contains(env, "DB=postgres://app:s3cret@db/app") || !slices.Contains(env, "MODE=prod") {
		t.Fatalf("env %q", env)
	}
	if a, _ := r.m.Read(context.Background(), "app", map[string]string{"id": "api"}); strings.Contains(fmt.Sprint(a), "s3cret") {
		t.Fatalf("the catalog shows the value: %v", a)
	}
	list, _ := r.m.Read(context.Background(), "secrets", nil)
	if fmt.Sprint(list) != "{[db-pass]}" {
		t.Fatalf("list %v", list)
	}
	if _, err := r.e.Submit(context.Background(), sdk.Action{Type: "secret.remove", Args: sdk.MustJSON(map[string]string{"name": "db-pass"}),
		Source: sdk.Source{Kind: sdk.SourceManual}}, admin); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.m.Read(context.Background(), "secrets", nil); fmt.Sprint(list) != "{[]}" {
		t.Fatalf("after remove %v", list)
	}
}

func TestSourceSettings(t *testing.T) {
	head := "runner = { type = \"process\", command = [\"site\"] }\n[source]\n"
	f, err := catalog.ParseAppFile("/x/site.toml", []byte(head+"type = \"remote\"\nurl = \"git@github.com:me/site.git\"\npoll = \"5m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := catalog.Build(nil, []catalog.AppFile{f}, nil)
	if app, _ := c.Get("site"); len(c.Problems) != 0 || app.Deploy.URL != "git@github.com:me/site.git" || app.Deploy.Poll != "5m" {
		t.Fatalf("remote source %+v, problems %v", app.Deploy, c.Problems)
	}
	img, err := catalog.ParseAppFile("/x/blog.toml", []byte("runner = { type = \"docker\", image = \"ghcr.io/me/blog:main\" }\n[source]\ntype = \"image\"\nimage = \"ghcr.io/me/blog\"\ntag = \"main\"\ncontainer_port = 80\nauth = \"ghcr\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	c = catalog.Build(nil, []catalog.AppFile{img}, nil)
	if app, _ := c.Get("blog"); len(c.Problems) != 0 || app.Deploy.ContainerPort != 80 || app.Deploy.Auth != "ghcr" {
		t.Fatalf("image source %+v, problems %v", app.Deploy, c.Problems)
	}
	for body, want := range map[string]string{
		"type = \"remote\"\n":                                  "needs url",
		"type = \"remote\"\nurl = \"x\"\npoll = \"1s\"":        "at least 10s",
		"type = \"push\"\nurl = \"x\"":                         "for types remote and webhook",
		"type = \"ftp\"":                                       "push, remote, webhook or image",
		"type = \"image\"\nimage = \"x\"\ncontainer_port = 80": "needs runner type docker",
		"type = \"push\"\nimage = \"x\"":                       "are for type image",
		"type = \"webhook\"\nurl = \"x\"":                      "needs url (to fetch from) and secret",
	} {
		f, err := catalog.ParseAppFile("/x/site.toml", []byte(head+body))
		if err != nil {
			t.Fatal(err)
		}
		if c := catalog.Build(nil, []catalog.AppFile{f}, nil); len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Error, want) {
			t.Errorf("%q: problems %v, want %q", body, c.Problems, want)
		}
	}
}

func TestCheckFolder(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	dir := t.TempDir()
	writeApps(t, dir, `
-- good.toml --
runner = { type = "exec", command = ["good"] }
-- bad.toml --
runner = { type = "warp" }
`)
	check := func(d string) (map[string]any, error) {
		res, err := r.e.Submit(context.Background(), sdk.Action{Type: "app.check", Args: sdk.MustJSON(map[string]string{"dir": d}),
			Source: sdk.Source{Kind: sdk.SourceManual}}, admin)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		_ = json.Unmarshal(res.Data, &out)
		return out, nil
	}
	out, err := check(dir)
	if err != nil {
		t.Fatal(err)
	}
	problems, _ := out["problems"].([]any)
	if out["files"] != float64(2) || len(problems) != 1 || !strings.Contains(fmt.Sprint(problems[0]), "bad.toml") {
		t.Fatalf("check %v", out)
	}
	// The catalog in use is untouched.
	if _, ok := r.m.catalog().Get("good"); ok {
		t.Fatal("app.check changed the catalog")
	}
	if _, err := check(filepath.Join(dir, "nope")); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("missing folder: %v", err)
	}
}

func TestReleaseArgsNeedDeployScope(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	appsOnly := core.Auth{Scopes: []string{contract.ScopeApps}}
	start := func(args string, auth core.Auth) error {
		_, err := r.e.Submit(context.Background(), sdk.Action{Type: "app.start", Args: json.RawMessage(args),
			Source: sdk.Source{Kind: sdk.SourceManual}}, auth)
		return err
	}
	for _, args := range []string{
		`{"id":"term","env":{"LD_PRELOAD":"/tmp/x.so"}}`,
		`{"id":"term","dir":"/tmp"}`,
		`{"id":"term","new":true}`,
		`{"id":"term","front":true}`,
	} {
		if err := start(args, appsOnly); sdk.CodeOf(err) != sdk.CodeForbidden {
			t.Errorf("%s with scope apps only: %v", args, err)
		}
	}
	if err := start(`{"id":"term","front":false,"env":{}}`, appsOnly); err != nil {
		t.Fatalf("unset arguments need no scope: %v", err)
	}
}
