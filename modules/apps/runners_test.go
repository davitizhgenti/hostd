package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
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
	// Or replaces them all (Steam's desktop actions run outside its session).
	files[0].ReplaceActions = true
	files[0].Actions = files[0].Actions[:1]
	app, _ = Build(found, files, nil).Get("chromium")
	if !reflect.DeepEqual(app.Actions, []AppAction{{ID: "kiosk", Name: "Kiosk", Command: []string{"chromium", "--kiosk"}}}) {
		t.Fatalf("replaced actions %+v", app.Actions)
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

func TestFlatpakAppsFollowTheirSandbox(t *testing.T) {
	c := Build(nil, []AppFile{
		mustParse(t, "/x/kodi.toml", `runner = { type = "flatpak", app_id = "tv.kodi.Kodi" }`),
		mustParse(t, "/x/many.toml", "runner = { type = \"flatpak\", app_id = \"org.x.Many\" }\n[instance]\npolicy = \"multiple\""),
	}, nil)
	if len(c.Problems) != 0 {
		t.Fatal(c.Problems)
	}
	kodi, _ := c.Get("kodi")
	many, _ := c.Get("many")
	if kodi.Runner.Handoff != "FLATPAK_ID=tv.kodi.Kodi" || many.Runner.Handoff != "" {
		t.Fatalf("handoffs %q %q", kodi.Runner.Handoff, many.Runner.Handoff)
	}
}

func mustParse(t *testing.T, path, body string) AppFile {
	t.Helper()
	f, err := ParseAppFile(path, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return f
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
	r.m.opts.Journal = func(_ context.Context, unit string, n int) ([]string, error) {
		asked = append(asked, fmt.Sprintf("%s %d", unit, n))
		return []string{"2026-10-10T19:00:00 core site[1]: listening"}, nil
	}
	r.start(t, "term")
	got, err := r.m.Read(context.Background(), "logs", map[string]string{"id": "term", "lines": "50"})
	if err != nil {
		t.Fatal(err)
	}
	logs := got.(Logs)
	if logs.Unit != "hostd-term.service" || len(logs.Lines) != 1 || asked[0] != "hostd-term.service 50" {
		t.Fatalf("logs %+v, asked %q", logs, asked)
	}
	if _, err := r.m.Read(context.Background(), "logs", map[string]string{"id": "term", "lines": "99999"}); err != nil || asked[1] != "hostd-term.service 5000" {
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
	f, err := ParseAppFile("/x/site.toml", []byte(head+"type = \"remote\"\nurl = \"git@github.com:me/site.git\"\npoll = \"5m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	c := Build(nil, []AppFile{f}, nil)
	if app, _ := c.Get("site"); len(c.Problems) != 0 || app.Deploy.URL != "git@github.com:me/site.git" || app.Deploy.Poll != "5m" {
		t.Fatalf("remote source %+v, problems %v", app.Deploy, c.Problems)
	}
	img, err := ParseAppFile("/x/blog.toml", []byte("runner = { type = \"docker\", image = \"ghcr.io/me/blog:main\" }\n[source]\ntype = \"image\"\nimage = \"ghcr.io/me/blog\"\ntag = \"main\"\ncontainer_port = 80\nauth = \"ghcr\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	c = Build(nil, []AppFile{img}, nil)
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
		f, err := ParseAppFile("/x/site.toml", []byte(head+body))
		if err != nil {
			t.Fatal(err)
		}
		if c := Build(nil, []AppFile{f}, nil); len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Error, want) {
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
