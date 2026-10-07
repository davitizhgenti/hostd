package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/sdk"
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

func TestUnitNameAndDescription(t *testing.T) {
	if got := unitName("firefox#2"); got != `hostd-firefox\x232.service` {
		t.Fatalf("unitName = %q", got)
	}
	m := reDescription.FindStringSubmatch(description(Instance{ID: "firefox#2", App: "firefox"}))
	if m == nil || m[1] != "firefox#2" || m[2] != "firefox" {
		t.Fatalf("description does not parse back: %v", m)
	}
}

// fakeSession makes a runtime dir with a Wayland socket and a Sway socket.
func fakeSession(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rt-") // short: socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, name := range []string{"wayland-1", "sway-ipc.1000.42.sock"} {
		ln, err := net.Listen("unix", filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
	}
	if err := os.WriteFile(filepath.Join(dir, "wayland-1.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSessionEnv(t *testing.T) {
	dir := fakeSession(t)
	env, ok := SessionEnv(dir)
	if !ok {
		t.Fatal("no session found")
	}
	for _, want := range []string{"WAYLAND_DISPLAY=wayland-1", "XDG_RUNTIME_DIR=" + dir,
		"SWAYSOCK=" + filepath.Join(dir, "sway-ipc.1000.42.sock")} {
		if !slices.Contains(env, want) {
			t.Errorf("env %v lacks %s", env, want)
		}
	}
	if _, ok := SessionEnv(t.TempDir()); ok {
		t.Fatal("found a session in an empty directory")
	}
}

// --- exec runner -----------------------------------------------------------

func execApp(id string, argv ...string) *App {
	a := &App{ID: id, Name: id, Runner: Runner{Type: RunnerExec, Command: argv}, Env: map[string]string{"LANG": "C.UTF-8"}}
	a.applyDefaults()
	return a
}

func TestExecRunnerStartStop(t *testing.T) {
	sd := newFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: fakeSession(t), HomeDir: "/home/screen"}
	ctx := context.Background()
	app := execApp("foot", "foot", "--fullscreen")

	inst, err := r.Start(ctx, Instance{ID: "foot#2", App: "foot", Surface: SurfaceWindow}, app)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Unit != `hostd-foot\x232.service` || inst.PID == 0 {
		t.Fatalf("instance = %+v", inst)
	}
	spec := sd.started[0]
	if !reflect.DeepEqual(spec.Argv, []string{"foot", "--fullscreen"}) || spec.Dir != "/home/screen" ||
		!slices.Contains(spec.Env, "WAYLAND_DISPLAY=wayland-1") || !slices.Contains(spec.Env, "LANG=C.UTF-8") ||
		spec.Description != "hostd instance foot#2 of app foot" {
		t.Fatalf("spec = %+v", spec)
	}
	if err := r.Stop(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if len(sd.unitNames()) != 0 {
		t.Fatalf("units left after stop: %v", sd.unitNames())
	}
}

func TestExecRunnerNeedsASessionForWindows(t *testing.T) {
	r := &ExecRunner{Systemd: newFakeSystemd(), RuntimeDir: t.TempDir()}
	_, err := r.Start(context.Background(), Instance{ID: "foot", Surface: SurfaceWindow}, execApp("foot", "foot"))
	if sdk.CodeOf(err) != sdk.CodeModuleUnavailable || !strings.Contains(err.Error(), "Sway is not running") {
		t.Fatalf("err = %v", err)
	}
	// A background app needs no session.
	if _, err := r.Start(context.Background(), Instance{ID: "job", Surface: SurfaceBackground}, execApp("job", "job")); err != nil {
		t.Fatal(err)
	}
}

func TestExecRunnerClearsStaleUnitAndReportsExecFailure(t *testing.T) {
	sd := newFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: fakeSession(t)}
	ctx := context.Background()
	inst := Instance{ID: "foot", App: "foot", Surface: SurfaceWindow}
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatal(err)
	}
	sd.exit(unitName("foot"), 0, 1) // ended while nobody watched: unit stays (RemainAfterExit)
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatalf("stale unit blocked a new start: %v", err)
	}
	// A unit that is really running is never replaced.
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err == nil {
		t.Fatal("started over a running unit")
	}
	// The binary does not exist: an error, and no unit left behind.
	_, err := r.Start(ctx, Instance{ID: "nope", App: "nope", Surface: SurfaceWindow}, execApp("nope", "missing"))
	if err == nil || !strings.Contains(err.Error(), "starting missing") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(sd.unitNames(), unitName("nope")) {
		t.Fatal("failed unit left behind")
	}
}

func TestExecRunnerAdopt(t *testing.T) {
	sd := newFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: fakeSession(t)}
	ctx := context.Background()
	for _, id := range []string{"foot", "foot#2", "kodi"} {
		if _, err := r.Start(ctx, Instance{ID: id, App: appOf(id), Surface: SurfaceWindow}, execApp(appOf(id), appOf(id))); err != nil {
			t.Fatal(err)
		}
	}
	sd.exit(unitName("kodi"), 3, 1) // ended with status 3 while hostd was away
	_ = sd.StartTransient(ctx, "hostd-check.service", UnitSpec{Description: "[systemd-run] /usr/bin/foot", Argv: []string{"foot"}})

	got, err := r.Adopt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Instance{}
	for _, in := range got {
		byID[in.ID] = in
	}
	if len(got) != 3 || byID["foot#2"].State != StateRunning || byID["foot#2"].App != "foot" || byID["foot#2"].PID == 0 {
		t.Fatalf("adopted %+v", got)
	}
	if k := byID["kodi"]; k.State != StateFailed || k.ExitCode == nil || *k.ExitCode != 3 {
		t.Fatalf("kodi = %+v", k)
	}
	if slices.Contains(sd.unitNames(), unitName("kodi")) || !slices.Contains(sd.unitNames(), "hostd-check.service") {
		t.Fatalf("units after adopt: %v (kodi cleaned up, foreign unit untouched)", sd.unitNames())
	}
}

func TestExecRunnerWatch(t *testing.T) {
	sd := newFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: fakeSession(t)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Ended, 10)
	go func() { _ = r.Watch(ctx, func(e Ended) { got <- e }) }()
	waitUntil(t, func() bool { return sd.watching() == 1 })

	for _, tc := range []struct {
		id           string
		status, code int
		want         int
		reason       string
	}{
		{"a", 0, 1, 0, "exit status 0"},
		{"b", 3, 1, 3, "exit status 3"},
		{"c", 9, 2, 137, "killed by signal 9"},
	} {
		if _, err := r.Start(ctx, Instance{ID: tc.id, App: tc.id, Surface: SurfaceBackground}, execApp(tc.id, tc.id)); err != nil {
			t.Fatal(err)
		}
		sd.exit(unitName(tc.id), tc.status, tc.code)
		e := <-got
		if e.Instance != tc.id || e.ExitCode != tc.want || e.Reason != tc.reason {
			t.Errorf("%s: %+v", tc.id, e)
		}
	}
	// hostd's own Stop is not reported as an exit.
	inst, _ := r.Start(ctx, Instance{ID: "d", App: "d", Surface: SurfaceBackground}, execApp("d", "d"))
	_ = r.Stop(ctx, inst)
	select {
	case e := <-got:
		t.Fatalf("stop reported as an exit: %+v", e)
	default:
	}
	if len(sd.unitNames()) != 0 {
		t.Fatalf("ended units not cleaned up: %v", sd.unitNames())
	}
}

// --- docker runner ---------------------------------------------------------

func dockerApp(id, image string, restart string) *App {
	a := &App{ID: id, Name: id, Runner: Runner{Type: RunnerDocker, Image: image, Ports: []string{"8096:8096"}},
		Restart: restart, Env: map[string]string{"TZ": "Europe/Tbilisi"}}
	a.applyDefaults()
	return a
}

func TestDockerRunnerStartStop(t *testing.T) {
	d := newFakeDocker()
	r := &DockerRunner{Docker: d}
	ctx := context.Background()
	inst, err := r.Start(ctx, Instance{ID: "jellyfin#2", App: "jellyfin"}, dockerApp("jellyfin", "jellyfin:latest", "always"))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Container != "hostd-jellyfin_2" || inst.PID == 0 || !reflect.DeepEqual(d.pulled, []string{"jellyfin:latest"}) {
		t.Fatalf("inst = %+v, pulled %v", inst, d.pulled)
	}
	spec := d.created[0]
	if spec.Restart != "always" || spec.Labels[labelInstance] != "jellyfin#2" || spec.Labels[labelApp] != "jellyfin" ||
		!reflect.DeepEqual(spec.Env, []string{"TZ=Europe/Tbilisi"}) || !reflect.DeepEqual(spec.Ports, []string{"8096:8096"}) {
		t.Fatalf("spec = %+v", spec)
	}
	// The image is there now: no second pull.
	if _, err := r.Start(ctx, Instance{ID: "jellyfin", App: "jellyfin"}, dockerApp("jellyfin", "jellyfin:latest", "never")); err != nil {
		t.Fatal(err)
	}
	if len(d.pulled) != 1 || d.created[1].Restart != "no" {
		t.Fatalf("pulled %v, restart %q", d.pulled, d.created[1].Restart)
	}
	if err := r.Stop(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Inspect(ctx, "hostd-jellyfin_2"); ok {
		t.Fatal("container not removed")
	}
}

func TestDockerRunnerRefusals(t *testing.T) {
	r := &DockerRunner{Docker: newFakeDocker()}
	ctx := context.Background()
	build := dockerApp("blog", "", "always")
	build.Runner.Build = "."
	if _, err := r.Start(ctx, Instance{ID: "blog"}, build); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("build without image: %v", err)
	}
	if _, err := r.Start(ctx, Instance{ID: "x"}, dockerApp("x", "missing:latest", "never")); err == nil ||
		!strings.Contains(err.Error(), "pulling missing:latest") {
		t.Fatalf("pull failure: %v", err)
	}
}

func TestDockerRunnerAdoptAndWatch(t *testing.T) {
	d := newFakeDocker()
	r := &DockerRunner{Docker: d}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []string{"web", "db", "old"} {
		if _, err := r.Start(ctx, Instance{ID: id, App: id}, dockerApp(id, "img", "on-failure")); err != nil {
			t.Fatal(err)
		}
	}
	d.mu.Lock()
	c := d.find("hostd-old")
	c.info.Running, c.info.ExitCode = false, 2
	d.mu.Unlock()

	got, err := r.Adopt(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("adopt = %+v %v", got, err)
	}
	for _, in := range got {
		if in.ID == "old" && (in.State != StateFailed || *in.ExitCode != 2) {
			t.Fatalf("old = %+v", in)
		}
	}
	if _, ok, _ := d.Inspect(ctx, "hostd-old"); ok {
		t.Fatal("exited container not cleaned up")
	}

	ended := make(chan Ended, 10)
	go func() { _ = r.Watch(ctx, func(e Ended) { ended <- e }) }()
	waitUntil(t, func() bool { return d.watching() == 1 })
	d.exit("hostd-db", 1, true) // the engine restarts it: still alive
	d.exit("hostd-web", 0, false)
	e := <-ended
	if e.Instance != "web" || e.ExitCode != 0 {
		t.Fatalf("ended = %+v", e)
	}
	select {
	case e := <-ended:
		t.Fatalf("restarting container reported as ended: %+v", e)
	default:
	}
}

func TestParsePort(t *testing.T) {
	for in, want := range map[string][3]string{
		"8080:80":           {"", "8080", "80/tcp"},
		"127.0.0.1:8080:80": {"127.0.0.1", "8080", "80/tcp"},
		"53:53/udp":         {"", "53", "53/udp"},
	} {
		ip, hp, cp, err := parsePort(in)
		if err != nil || [3]string{ip, hp, cp} != want {
			t.Errorf("parsePort(%q) = %q %q %q %v", in, ip, hp, cp, err)
		}
	}
	for _, bad := range []string{"80", "a:b", "1:2:3:4", "0:80", "8080:70000"} {
		if _, _, _, err := parsePort(bad); err == nil {
			t.Errorf("parsePort(%q) accepted", bad)
		}
	}
}

// fakeEngine serves the Engine API on a unix socket, recording requests.
func fakeEngine(t *testing.T, h http.HandlerFunc) *EngineAPI {
	t.Helper()
	dir, _ := os.MkdirTemp("", "eng-")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "engine.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Close() })
	return &EngineAPI{Socket: sock}
}

func TestEngineAPI(t *testing.T) {
	var mu sync.Mutex
	var created map[string]any
	e := fakeEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.41/images/nginx:latest/json":
			w.WriteHeader(404)
		case "/v1.41/images/create":
			io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"toomanyrequests: rate limit"}`+"\n")
		case "/v1.41/containers/create":
			if r.URL.Query().Get("name") != "hostd-web" {
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&created)
			mu.Unlock()
			w.WriteHeader(201)
			io.WriteString(w, `{"Id":"abc123"}`)
		case "/v1.41/containers/abc123/json":
			io.WriteString(w, `{"Id":"abc123","Name":"/hostd-web","Config":{"Labels":{"hostd.instance":"web"}},
				"State":{"Status":"exited","Running":false,"ExitCode":3,"Pid":0}}`)
		case "/v1.41/containers/gone/json":
			w.WriteHeader(404)
		case "/v1.41/containers/abc123/start":
			w.WriteHeader(304) // already started is fine
		case "/v1.41/events":
			// Docker's format, then Podman's older one, then noise.
			io.WriteString(w, `{"Type":"container","Action":"die","Actor":{"ID":"abc123"}}`+"\n")
			io.WriteString(w, `{"status":"die","id":"def456"}`+"\n")
			io.WriteString(w, `{"Action":"start","Actor":{"ID":"zzz"}}`+"\nnot json\n")
		default:
			w.WriteHeader(500)
			io.WriteString(w, `{"message":"unexpected `+r.URL.Path+`"}`)
		}
	})
	ctx := context.Background()

	if ok, err := e.ImageExists(ctx, "nginx:latest"); ok || err != nil {
		t.Fatalf("ImageExists = %v %v", ok, err)
	}
	if err := e.Pull(ctx, "nginx:latest"); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("pull error not reported: %v", err)
	}
	id, err := e.Create(ctx, "hostd-web", ContainerSpec{Image: "nginx:latest", Ports: []string{"8080:80"}, Restart: "always",
		Labels: map[string]string{labelInstance: "web"}})
	if err != nil || id != "abc123" {
		t.Fatalf("Create = %q %v", id, err)
	}
	mu.Lock()
	hc := created["HostConfig"].(map[string]any)
	if fmt.Sprint(hc["PortBindings"]) != "map[80/tcp:[map[HostIp: HostPort:8080]]]" ||
		fmt.Sprint(hc["RestartPolicy"]) != "map[Name:always]" || fmt.Sprint(created["Labels"]) != "map[hostd.instance:web]" {
		t.Fatalf("create body = %v", created)
	}
	mu.Unlock()
	if err := e.Start(ctx, "abc123"); err != nil {
		t.Fatalf("304 should be fine: %v", err)
	}
	info, ok, err := e.Inspect(ctx, "abc123")
	if err != nil || !ok || info.Name != "hostd-web" || info.ExitCode != 3 || info.Labels[labelInstance] != "web" {
		t.Fatalf("Inspect = %+v %v %v", info, ok, err)
	}
	if _, ok, err := e.Inspect(ctx, "gone"); ok || err != nil {
		t.Fatalf("missing container: %v %v", ok, err)
	}
	var dead []string
	_ = e.Events(ctx, labelInstance, func(id string) { dead = append(dead, id) })
	if !reflect.DeepEqual(dead, []string{"abc123", "def456"}) {
		t.Fatalf("die events = %v", dead)
	}
	if _, err := e.Create(ctx, "other", ContainerSpec{Image: "x"}); err == nil {
		t.Fatal("engine error not reported")
	}

	nowhere := &EngineAPI{Socket: filepath.Join(t.TempDir(), "none.sock")}
	if _, err := nowhere.ImageExists(ctx, "x"); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("unreachable engine: %v", err)
	}
}

// --- the module, through the real pipeline ---------------------------------

type rig struct {
	e      *core.Engine
	m      *Module
	sd     *fakeSystemd
	docker *fakeDocker
	events <-chan sdk.Event
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

func newRig(t *testing.T, sd *fakeSystemd, d *fakeDocker, runtimeDir string) *rig {
	t.Helper()
	appsDir := t.TempDir()
	writeApps(t, appsDir, rigApps)
	m := New(Options{AppsDir: appsDir, NoWatch: true, Backends: map[string]Backend{
		RunnerExec:   &ExecRunner{Systemd: sd, RuntimeDir: runtimeDir},
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
	r := &rig{e: e, m: m, sd: sd, docker: d, events: e.Subscribe(ctx, "instance.*")}
	t.Cleanup(func() {
		cancel()
		_ = e.Stop(context.Background())
	})
	return r
}

var admin = core.Auth{Scopes: []string{sdk.ScopeAdmin}}

func (r *rig) do(t *testing.T, typ, id string) (map[string]any, error) {
	t.Helper()
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: mustJSON(map[string]string{"id": id}),
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
	r := newRig(t, newFakeSystemd(), newFakeDocker(), fakeSession(t))
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
	if len(r.live()) != 0 || len(r.sd.unitNames()) != 0 {
		t.Fatalf("live %v, units %v", r.live(), r.sd.unitNames())
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
	r := newRig(t, newFakeSystemd(), newFakeDocker(), fakeSession(t))

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
	r := newRig(t, newFakeSystemd(), newFakeDocker(), fakeSession(t))
	waitUntil(t, func() bool { return r.sd.watching() == 1 })
	r.start(t, "term")
	r.start(t, "tabs")
	r.next(t)
	r.next(t)
	r.next(t)
	r.next(t)

	r.sd.exit(unitName("term"), 0, 1) // the user closed it
	ev := r.next(t)
	if ev.Type != EventExited || ev.Action != "" || ev.Source.Kind != sdk.SourceExternal || ev.Version == 0 {
		t.Fatalf("closed by the user: %+v", ev)
	}
	r.sd.exit(unitName("tabs"), 11, 2) // crashed
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
	r := newRig(t, newFakeSystemd(), newFakeDocker(), fakeSession(t))
	for app, code := range map[string]sdk.Code{
		"ghost": sdk.CodeNotFound,
		"kodi":  sdk.CodeModuleUnavailable, // flatpak runner not there yet
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

	noSession := newRig(t, newFakeSystemd(), newFakeDocker(), t.TempDir())
	if _, err := noSession.do(t, "app.start", "term"); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("without Sway: %v", err)
	}
}

func TestContainerApp(t *testing.T) {
	r := newRig(t, newFakeSystemd(), newFakeDocker(), fakeSession(t))
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
	if list, _ := r.docker.List(context.Background(), labelInstance); len(list) != 0 {
		t.Fatalf("containers left: %+v", list)
	}
}

func TestAdoptAfterRestart(t *testing.T) {
	sd, d, session := newFakeSystemd(), newFakeDocker(), fakeSession(t)
	first := newRig(t, sd, d, session)
	first.start(t, "term")
	first.start(t, "tabs")
	first.start(t, "tabs")
	first.start(t, "jelly")
	_ = first.e.Stop(context.Background()) // hostd stops; the apps keep running
	sd.exit(unitName("tabs#2"), 0, 1)      // and one ends while hostd is away

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
	sd := newFakeSystemd()
	m := New(Options{NoWatch: true})
	b := &quickExit{m: m}
	m.opts.Backends = map[string]Backend{RunnerExec: b}
	m.cat = Build(nil, []AppFile{{Path: "x.toml", ID: "blink", Runner: &Runner{Type: RunnerExec, Command: []string{"true"}}}}, nil)
	core := &fakeCore{}
	m.core = core
	res, err := m.handleStart(context.Background(), sdk.Action{ID: "act_1", Args: mustJSON(map[string]string{"id": "blink"})})
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
func (q *quickExit) Stop(context.Context, Instance) error           { return nil }
func (q *quickExit) Adopt(context.Context) ([]Instance, error)      { return nil, nil }
func (q *quickExit) Watch(ctx context.Context, _ func(Ended)) error { <-ctx.Done(); return nil }

func TestFailedStopCanBeRetried(t *testing.T) {
	m := New(Options{NoWatch: true})
	b := &failingStop{}
	m.opts.Backends = map[string]Backend{RunnerExec: b}
	m.cat = Build(nil, []AppFile{{Path: "x.toml", ID: "job", Runner: &Runner{Type: RunnerExec, Command: []string{"job"}}}}, nil)
	m.core = &fakeCore{}
	ctx := context.Background()
	if _, err := m.handleStart(ctx, sdk.Action{ID: "a1", Args: mustJSON(map[string]string{"id": "job"})}); err != nil {
		t.Fatal(err)
	}
	b.fail = true
	if _, err := m.handleStop(ctx, sdk.Action{ID: "a2", Args: mustJSON(map[string]string{"id": "job"})}); err == nil {
		t.Fatal("stop error not reported")
	}
	if in, _ := m.readInstance("job"); in.State != StateRunning {
		t.Fatalf("after a failed stop: %s, want running", in.State)
	}
	b.fail = false
	if _, err := m.handleStop(ctx, sdk.Action{ID: "a3", Args: mustJSON(map[string]string{"id": "job"})}); err != nil {
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
func (f *failingStop) Adopt(context.Context) ([]Instance, error)      { return nil, nil }
func (f *failingStop) Watch(ctx context.Context, _ func(Ended)) error { <-ctx.Done(); return nil }

func TestUserSystemdNeverAutolaunchesABus(t *testing.T) {
	// With no bus address and no user bus, connecting fails at once: it
	// must not fall back to dbus-launch and a stray bus.
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	t.Setenv("PATH", t.TempDir()) // and if it tried, there is no dbus-launch to run
	s := &UserSystemd{RuntimeDir: t.TempDir()}
	start := time.Now()
	_, err := s.List(context.Background(), "hostd-*.service")
	if err == nil || !strings.Contains(err.Error(), "no user bus") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("took %v to give up", time.Since(start))
	}
}

func TestWatchIgnoresStopsOfReplacedUnits(t *testing.T) {
	// Starting over a unit that ended earlier stops it first; that stop
	// must not be taken for the new instance ending.
	sd := newFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: fakeSession(t)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended := make(chan Ended, 4)
	go func() { _ = r.Watch(ctx, func(e Ended) { ended <- e }) }()
	waitUntil(t, func() bool { return sd.watching() == 1 })

	inst := Instance{ID: "foot", App: "foot", Surface: SurfaceWindow}
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatal(err)
	}
	sd.exit(unitName("foot"), 0, 1)
	<-ended // the first one ended on its own
	sd.mu.Lock()
	sd.units[unitName("foot")] = &fakeUnit{info: UnitInfo{Name: unitName("foot"), Description: "hostd instance foot of app foot",
		ActiveState: "active", SubState: "exited"}} // left over: not cleaned up yet
	sd.mu.Unlock()
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatalf("start over a leftover unit: %v", err)
	}
	select {
	case e := <-ended:
		t.Fatalf("the replaced unit's stop was reported as an exit: %+v", e)
	default:
	}
	if info, ok, _ := sd.Unit(ctx, unitName("foot")); !ok || info.SubState != "running" {
		t.Fatalf("new unit = %+v %v", info, ok)
	}
}
