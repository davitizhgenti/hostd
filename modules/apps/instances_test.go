package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/systemd"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/sdk"

	"github.com/davitizhgenti/hostd/modules/apps/catalog"
)

func TestNextState(t *testing.T) {
	for _, tc := range []struct {
		from State
		c    change
		code int
		want State
		err  bool
	}{
		{StateStarting, changeStarted, 0, StateRunning, false},
		{StateStarting, changeStartError, 0, StateFailed, false},
		{StateStarting, changeEnded, 0, StateExited, false}, // exited before start was confirmed
		{StateStarting, changeEnded, 2, StateFailed, false},
		{StateStarting, changeStop, 0, StateStopping, false},
		{StateRunning, changeStarted, 0, StateRunning, false},
		{StateRunning, changeEnded, 0, StateExited, false},
		{StateRunning, changeEnded, 1, StateFailed, false},
		{StateRunning, changeStop, 0, StateStopping, false},
		{StateRunning, changeStartError, 0, StateRunning, true},
		{StateStopping, changeEnded, 143, StateExited, false}, // we asked: a signal is fine
		{StateStopping, changeStop, 0, StateStopping, false},
		{StateExited, changeStarted, 0, StateExited, true},
		{StateFailed, changeStop, 0, StateFailed, true},
	} {
		got, err := next(tc.from, tc.c, tc.code)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("next(%s, %d, %d) = %s, %v; want %s, err %v", tc.from, tc.c, tc.code, got, err, tc.want, tc.err)
		}
	}
}

func TestNextID(t *testing.T) {
	used := map[string]bool{}
	inUse := func(id string) bool { return used[id] }
	for _, want := range []string{"firefox", "firefox#2", "firefox#3"} {
		got := nextID("firefox", inUse)
		if got != want {
			t.Fatalf("nextID = %s, want %s", got, want)
		}
		used[got] = true
	}
	delete(used, "firefox#2")
	if got := nextID("firefox", inUse); got != "firefox#2" {
		t.Fatalf("freed number not reused: %s", got)
	}
	if appOf("firefox#3") != "firefox" || appOf("kodi") != "kodi" {
		t.Fatal("appOf wrong")
	}
}

func execApp(id string, argv ...string) *App {
	a := &App{ID: id, Name: id, Runner: Runner{Type: RunnerExec, Command: argv}, Env: map[string]string{"LANG": "C.UTF-8"}}
	catalog.Defaults(a)
	return a
}

func dockerApp(id, image string, restart string) *App {
	a := &App{ID: id, Name: id, Runner: Runner{Type: RunnerDocker, Image: image, Ports: []string{"8096:8096"}},
		Restart: restart, Env: map[string]string{"TZ": "Europe/Tbilisi"}}
	catalog.Defaults(a)
	return a
}

type rig struct {
	e      *core.Engine
	m      *Module
	sd     *unitstest.FakeSystemd
	docker *podmantest.FakeEngine
	events <-chan sdk.Event
	proc   string // the processes the exec runner sees (pid/environ files)
}

const rigApps = `
-- term.toml --
runner = { type = "exec", command = ["foot"] }
-- tabs.toml --
runner = { type = "exec", command = ["firefox"] }
[instance]
policy = "multiple"
-- tv.toml --
runner = { type = "exec", command = ["kodi"] }
[instance]
if_running = "restart"
-- extra.toml --
runner = { type = "exec", command = ["mpv"] }
[instance]
if_running = "new"
-- broken.toml --
runner = { type = "exec", command = ["missing"] }
-- jelly.toml --
runner = { type = "docker", image = "jellyfin:latest" }
-- kodi.toml --
runner = { type = "flatpak", app_id = "tv.kodi.Kodi" }
-- stack.toml --
runner = { type = "compose", file = "compose.yml" }
`

func writeApps(t *testing.T, dir, txt string) {
	t.Helper()
	parts := strings.Split(txt, "-- ")
	for _, p := range parts[1:] {
		name, body, _ := strings.Cut(p, " --\n")
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newRig(t *testing.T, sd *unitstest.FakeSystemd, d *podmantest.FakeEngine, runtimeDir string) *rig {
	t.Helper()
	return newRigClock(t, sd, d, runtimeDir, nil)
}

func newRigClock(t *testing.T, sd *unitstest.FakeSystemd, d *podmantest.FakeEngine, runtimeDir string, clk clock.Clock) *rig {
	t.Helper()
	return newRigApps(t, sd, d, runtimeDir, clk, rigApps)
}

// newRigApps is a rig with these app files (in writeApps' form).
func newRigApps(t *testing.T, sd *unitstest.FakeSystemd, d *podmantest.FakeEngine, runtimeDir string, clk clock.Clock, apps string) *rig {
	t.Helper()
	appsDir, proc := t.TempDir(), t.TempDir()
	writeApps(t, appsDir, apps)
	m := New(Options{AppsDir: appsDir, NoWatch: true, Clock: clk, Backends: map[string]Backend{
		RunnerExec:   &ExecRunner{Systemd: sd, RuntimeDir: runtimeDir, ProcRoot: proc, Clock: clk},
		RunnerDocker: &DockerRunner{Docker: d},
	}})
	reg := core.NewRegistry()
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	e := core.New(reg, core.Options{})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &rig{e: e, m: m, sd: sd, docker: d, events: e.Subscribe(ctx, "instance.*"), proc: proc}
	t.Cleanup(func() {
		cancel()
		_ = e.Stop(context.Background())
	})
	return r
}

var admin = core.Auth{Scopes: []string{sdk.ScopeAdmin}}

func (r *rig) do(t *testing.T, typ, id string) (map[string]any, error) {
	t.Helper()
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: sdk.MustJSON(map[string]string{"id": id}),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, admin)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	_ = json.Unmarshal(res.Data, &data)
	return data, nil
}

func (r *rig) start(t *testing.T, app string) map[string]any {
	t.Helper()
	data, err := r.do(t, "app.start", app)
	if err != nil {
		t.Fatalf("start %s: %v", app, err)
	}
	return data
}

func (r *rig) next(t *testing.T) sdk.Event {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return sdk.Event{}
	}
}

func (r *rig) live() []string {
	var ids []string
	for _, in := range r.m.readInstances(nil) {
		ids = append(ids, in.ID)
	}
	return ids
}

func TestStartAndStop(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	data := r.start(t, "term")
	if data["instance"] != "term" || data["state"] != "running" {
		t.Fatalf("start result %v", data)
	}
	for _, want := range []string{EventStarting, EventStarted} {
		if ev := r.next(t); ev.Type != want || ev.Action == "" || ev.Source.Kind != sdk.SourceManual {
			t.Fatalf("event %+v, want %s linked to the action", ev, want)
		}
	}
	in, _ := r.m.readInstance("term")
	if in.Unit != "hostd-term.service" || in.PID == 0 || in.Runner != RunnerExec || in.Surface != SurfaceWindow {
		t.Fatalf("instance %+v", in)
	}

	if _, err := r.do(t, "instance.stop", "term"); err != nil {
		t.Fatal(err)
	}
	if ev := r.next(t); ev.Type != EventExited || ev.Action == "" {
		t.Fatalf("stop event %+v", ev)
	}
	if len(r.live()) != 0 || len(r.sd.UnitNames()) != 0 {
		t.Fatalf("live %v, units %v", r.live(), r.sd.UnitNames())
	}
	if _, err := r.do(t, "instance.stop", "term"); sdk.CodeOf(err) != sdk.CodeInstanceNotRunning {
		t.Fatalf("second stop: %v", err)
	}
	if _, err := r.do(t, "instance.stop", "ghost"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown instance: %v", err)
	}
	if all := r.m.readInstances(map[string]string{"all": "true"}); len(all) != 1 || all[0].State != StateExited {
		t.Fatalf("ps --all = %+v", all)
	}
}

func TestStartPolicies(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))

	// single + focus (the default): no second copy. Without a display
	// module, focusing is skipped.
	r.start(t, "term")
	if data := r.start(t, "term"); data["already_running"] != true || data["instance"] != "term" {
		t.Fatalf("second start of a single app: %v", data)
	}
	// multiple: numbered copies, lowest free number reused.
	for _, want := range []string{"tabs", "tabs#2", "tabs#3"} {
		if got := r.start(t, "tabs")["instance"]; got != want {
			t.Fatalf("got %v, want %s", got, want)
		}
	}
	if _, err := r.do(t, "instance.stop", "tabs#2"); err != nil {
		t.Fatal(err)
	}
	if got := r.start(t, "tabs")["instance"]; got != "tabs#2" {
		t.Fatalf("got %v, want tabs#2 again", got)
	}
	// single + new: another copy anyway.
	r.start(t, "extra")
	if got := r.start(t, "extra")["instance"]; got != "extra#2" {
		t.Fatalf("if_running=new gave %v", got)
	}
	// single + restart: the old one is stopped (a child action that
	// takes the instance key), then a fresh one starts.
	r.start(t, "tv")
	firstPID := r.instancePID(t, "tv")
	if got := r.start(t, "tv")["instance"]; got != "tv" {
		t.Fatalf("restart gave %v", got)
	}
	if r.instancePID(t, "tv") == firstPID {
		t.Fatal("tv was not restarted")
	}
	want := []string{"extra", "extra#2", "tabs", "tabs#2", "tabs#3", "term", "tv"}
	if got := r.live(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live = %v, want %v", got, want)
	}
}

func (r *rig) instancePID(t *testing.T, id string) int {
	t.Helper()
	in, err := r.m.readInstance(id)
	if err != nil {
		t.Fatal(err)
	}
	return in.PID
}

func TestAppEndsOnItsOwn(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	waitUntil(t, func() bool { return r.sd.Watching() == 1 })
	r.start(t, "term")
	r.start(t, "tabs")
	r.next(t)
	r.next(t)
	r.next(t)
	r.next(t)

	r.sd.Exit(contract.UnitName("term"), 0, 1) // the user closed it
	ev := r.next(t)
	if ev.Type != EventExited || ev.Action != "" || ev.Source.Kind != sdk.SourceExternal || ev.Version == 0 {
		t.Fatalf("closed by the user: %+v", ev)
	}
	r.sd.Exit(contract.UnitName("tabs"), 11, 2) // crashed
	ev = r.next(t)
	var in Instance
	_ = json.Unmarshal(ev.Data, &in)
	if ev.Type != EventFailed || in.Error != "killed by signal 11" || *in.ExitCode != 139 {
		t.Fatalf("crash: %+v %+v", ev, in)
	}
	if len(r.live()) != 0 {
		t.Fatalf("still live: %v", r.live())
	}
	// The ID is free again.
	if got := r.start(t, "term")["instance"]; got != "term" {
		t.Fatalf("restart after exit gave %v", got)
	}
}

func TestStartRefusalsAndFailures(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	for app, code := range map[string]sdk.Code{
		"ghost": sdk.CodeNotFound,
		"stack": sdk.CodeModuleUnavailable, // compose runner not there yet (M4)
	} {
		if _, err := r.do(t, "app.start", app); sdk.CodeOf(err) != code {
			t.Errorf("%s: %v, want %s", app, err, code)
		}
	}
	_, err := r.do(t, "app.start", "broken")
	if sdk.CodeOf(err) != sdk.CodeInternal || !strings.Contains(err.Error(), "starting missing") {
		t.Fatalf("broken: %v", err)
	}
	r.next(t) // starting
	if ev := r.next(t); ev.Type != EventFailed {
		t.Fatalf("event %+v", ev)
	}
	if in, err := r.m.readInstance("broken"); err != nil || in.State != StateFailed || in.Error == "" {
		t.Fatalf("broken instance %+v %v", in, err)
	}

	noSession := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), t.TempDir())
	if _, err := noSession.do(t, "app.start", "term"); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("without Sway: %v", err)
	}
}

func TestContainerApp(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	if got := r.start(t, "jelly")["instance"]; got != "jelly" {
		t.Fatalf("got %v", got)
	}
	in, _ := r.m.readInstance("jelly")
	if in.Container != "hostd-jelly" || in.Surface != SurfaceBackground {
		t.Fatalf("instance %+v", in)
	}
	if _, err := r.do(t, "instance.stop", "jelly"); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.docker.List(context.Background(), podman.LabelInstance); len(list) != 0 {
		t.Fatalf("containers left: %+v", list)
	}
}

func TestAdoptAfterRestart(t *testing.T) {
	sd, d, session := unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t)
	first := newRig(t, sd, d, session)
	first.start(t, "term")
	first.start(t, "tabs")
	first.start(t, "tabs")
	first.start(t, "jelly")
	_ = first.e.Stop(context.Background())     // hostd stops; the apps keep running
	sd.Exit(contract.UnitName("tabs#2"), 0, 1) // and one ends while hostd is away

	second := newRig(t, sd, d, session)
	if got, want := second.live(), []string{"jelly", "tabs", "term"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("adopted %v, want %v", got, want)
	}
	all := second.m.readInstances(map[string]string{"all": "true"})
	found := false
	for _, in := range all {
		if in.ID == "tabs#2" && in.State == StateExited {
			found = true
		}
	}
	if !found {
		t.Fatalf("instance that ended while hostd was away not reported: %+v", all)
	}
	// They keep the time they started, not the time hostd found them.
	for _, in := range all {
		if in.State == StateRunning && in.Started.Year() != 2020 {
			t.Errorf("%s started %v: the time it was found, not its start", in.ID, in.Started)
		}
	}
	// Adopted instances work like any other.
	if _, err := second.do(t, "instance.stop", "tabs"); err != nil {
		t.Fatal(err)
	}
	if got := second.start(t, "term"); got["already_running"] != true {
		t.Fatalf("adopted single app started twice: %v", got)
	}
}

func TestStartRaceWithImmediateExit(t *testing.T) {
	// An app that exits at once can be reported ended before Start returns.
	sd := unitstest.NewFakeSystemd()
	m := New(Options{NoWatch: true})
	b := &quickExit{m: m}
	m.opts.Backends = map[string]Backend{RunnerExec: b}
	m.cat = catalog.Build(nil, []catalog.AppFile{{Path: "x.toml", ID: "blink", Runner: &Runner{Type: RunnerExec, Command: []string{"true"}}}}, nil)
	core := &fakeCore{}
	m.core = core
	res, err := m.handleStart(context.Background(), sdk.Action{ID: "act_1", Args: sdk.MustJSON(map[string]string{"id": "blink"})})
	if err != nil || !strings.Contains(string(res.Data), `"state":"exited"`) {
		t.Fatalf("%s %v", res.Data, err)
	}
	_ = sd
}

type quickExit struct{ m *Module }

func (q *quickExit) Start(_ context.Context, inst Instance, _ *App) (Instance, error) {
	q.m.onEnded(Ended{Instance: inst.ID, ExitCode: 0, Reason: "exit status 0"})
	return inst, nil
}

func (q *quickExit) Stop(context.Context, Instance) error { return nil }

func (q *quickExit) Adopt(context.Context) ([]Instance, error) { return nil, nil }

func (q *quickExit) Watch(ctx context.Context, _ func(Ended)) error { <-ctx.Done(); return nil }

func TestFailedStopCanBeRetried(t *testing.T) {
	m := New(Options{NoWatch: true})
	b := &failingStop{}
	m.opts.Backends = map[string]Backend{RunnerExec: b}
	m.cat = catalog.Build(nil, []catalog.AppFile{{Path: "x.toml", ID: "job", Runner: &Runner{Type: RunnerExec, Command: []string{"job"}}}}, nil)
	m.core = &fakeCore{}
	ctx := context.Background()
	if _, err := m.handleStart(ctx, sdk.Action{ID: "a1", Args: sdk.MustJSON(map[string]string{"id": "job"})}); err != nil {
		t.Fatal(err)
	}
	b.fail = true
	if _, err := m.handleStop(ctx, sdk.Action{ID: "a2", Args: sdk.MustJSON(map[string]string{"id": "job"})}); err == nil {
		t.Fatal("stop error not reported")
	}
	if in, _ := m.readInstance("job"); in.State != StateRunning {
		t.Fatalf("after a failed stop: %s, want running", in.State)
	}
	b.fail = false
	if _, err := m.handleStop(ctx, sdk.Action{ID: "a3", Args: sdk.MustJSON(map[string]string{"id": "job"})}); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

type failingStop struct{ fail bool }

func (f *failingStop) Start(_ context.Context, inst Instance, _ *App) (Instance, error) {
	return inst, nil
}

func (f *failingStop) Stop(context.Context, Instance) error {
	if f.fail {
		return fmt.Errorf("permission denied")
	}
	return nil
}

func (f *failingStop) Adopt(context.Context) ([]Instance, error) { return nil, nil }

func (f *failingStop) Watch(ctx context.Context, _ func(Ended)) error { <-ctx.Done(); return nil }

func TestWatchIgnoresStopsOfReplacedUnits(t *testing.T) {
	// Starting over a unit that ended earlier stops it first; that stop
	// must not be taken for the new instance ending.
	sd := unitstest.NewFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: unitstest.FakeSession(t)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan Ended, 4)
	go func() { _ = r.Watch(ctx, func(e Ended) { ended <- e }) }()
	waitUntil(t, func() bool { return sd.Watching() == 1 })

	inst := Instance{ID: "foot", App: "foot", Surface: SurfaceWindow}
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatal(err)
	}
	sd.Exit(contract.UnitName("foot"), 0, 1)
	<-ended // the first one ended on its own
	sd.Mu.Lock()
	sd.Units[contract.UnitName("foot")] = &unitstest.FakeUnit{Info: systemd.UnitInfo{Name: contract.UnitName("foot"), Description: "hostd instance foot of app foot",
		ActiveState: "active", SubState: "exited"}} // left over: not cleaned up yet
	sd.Mu.Unlock()
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatalf("start over a leftover unit: %v", err)
	}
	select {
	case e := <-ended:
		t.Fatalf("the replaced unit's stop was reported as an exit: %+v", e)
	default:
	}
	if info, ok, _ := sd.Unit(ctx, contract.UnitName("foot")); !ok || info.SubState != "running" {
		t.Fatalf("new unit = %+v %v", info, ok)
	}
}

func TestDockerRelease(t *testing.T) {
	// A container release runs its own image (by digest) on its own port.
	d := podmantest.NewFakeEngine()
	r := &DockerRunner{Docker: d}
	img := "ghcr.io/me/blog@sha256:" + strings.Repeat("a", 64)
	inst := Instance{ID: "blog#2", App: "blog", Image: img, Ports: []string{"127.0.0.1:41000:80"}, Env: map[string]string{"PORT": "41000"}}
	if _, err := r.Start(context.Background(), inst, dockerApp("blog", "ghcr.io/me/blog:main", "never")); err != nil {
		t.Fatal(err)
	}
	spec := d.Created[0]
	if spec.Image != img || !reflect.DeepEqual(spec.Ports, []string{"127.0.0.1:41000:80"}) ||
		!reflect.DeepEqual(spec.Env, []string{"PORT=41000", "TZ=Europe/Tbilisi"}) || !reflect.DeepEqual(d.Pulled, []string{img}) {
		t.Fatalf("spec %+v, pulled %v", spec, d.Pulled)
	}
}
