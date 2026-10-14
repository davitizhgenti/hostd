package podman_test

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

	"github.com/davitizhgenti/hostd/modules/apps/runner/podman"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/sdk"
)

// dockerApp is a container app as the catalog makes it (in the background).
func dockerApp(id, image string, restart string) *spec.App {
	if restart == "" {
		restart = "on-failure"
	}
	return &spec.App{ID: id, Name: id, Surface: spec.SurfaceBackground, Restart: restart,
		Runner: spec.Runner{Type: spec.RunnerDocker, Image: image, Ports: []string{"8096:8096"}}, Env: map[string]string{"TZ": "Europe/Tbilisi"}}
}

func TestDockerRunnerStartStop(t *testing.T) {
	d := podmantest.NewFakeEngine()
	r := &podman.DockerRunner{Docker: d}
	ctx := context.Background()
	inst, err := r.Start(ctx, spec.Instance{ID: "jellyfin#2", App: "jellyfin"}, dockerApp("jellyfin", "jellyfin:latest", "always"))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Container != "hostd-jellyfin_2" || inst.PID == 0 || !reflect.DeepEqual(d.Pulled, []string{"jellyfin:latest"}) {
		t.Fatalf("inst = %+v, pulled %v", inst, d.Pulled)
	}
	cs := d.Created[0]
	if cs.Restart != "always" || cs.Labels[podman.LabelInstance] != "jellyfin#2" || cs.Labels[podman.LabelApp] != "jellyfin" ||
		!reflect.DeepEqual(cs.Env, []string{"TZ=Europe/Tbilisi"}) || !reflect.DeepEqual(cs.Ports, []string{"8096:8096"}) {
		t.Fatalf("container spec = %+v", cs)
	}
	// The image is there now: no second pull.
	if _, err := r.Start(ctx, spec.Instance{ID: "jellyfin", App: "jellyfin"}, dockerApp("jellyfin", "jellyfin:latest", "never")); err != nil {
		t.Fatal(err)
	}
	if len(d.Pulled) != 1 || d.Created[1].Restart != "no" {
		t.Fatalf("pulled %v, restart %q", d.Pulled, d.Created[1].Restart)
	}
	if err := r.Stop(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.Inspect(ctx, "hostd-jellyfin_2"); ok {
		t.Fatal("container not removed")
	}
}

func TestDockerRunnerRefusals(t *testing.T) {
	r := &podman.DockerRunner{Docker: podmantest.NewFakeEngine()}
	ctx := context.Background()
	build := dockerApp("blog", "", "always")
	build.Runner.Build = "."
	if _, err := r.Start(ctx, spec.Instance{ID: "blog"}, build); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("build without image: %v", err)
	}
	if _, err := r.Start(ctx, spec.Instance{ID: "x"}, dockerApp("x", "missing:latest", "never")); err == nil ||
		!strings.Contains(err.Error(), "pulling missing:latest") {
		t.Fatalf("pull failure: %v", err)
	}
}

func TestDockerRunnerAdoptAndWatch(t *testing.T) {
	d := podmantest.NewFakeEngine()
	r := &podman.DockerRunner{Docker: d}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range []string{"web", "db", "old"} {
		if _, err := r.Start(ctx, spec.Instance{ID: id, App: id}, dockerApp(id, "img", "on-failure")); err != nil {
			t.Fatal(err)
		}
	}
	d.Mu.Lock()
	c := d.Find("hostd-old")
	c.Info.Running, c.Info.ExitCode = false, 2
	d.Mu.Unlock()

	got, err := r.Adopt(ctx)
	if err != nil || len(got) != 3 {
		t.Fatalf("adopt = %+v %v", got, err)
	}
	for _, in := range got {
		if in.ID == "old" && (in.State != spec.StateFailed || *in.ExitCode != 2) {
			t.Fatalf("old = %+v", in)
		}
	}
	if _, ok, _ := d.Inspect(ctx, "hostd-old"); ok {
		t.Fatal("exited container not cleaned up")
	}

	ended := make(chan spec.Ended, 10)
	go func() { _ = r.Watch(ctx, func(e spec.Ended) { ended <- e }) }()
	waitUntil(t, func() bool { return d.Watching() == 1 })
	d.Exit("hostd-db", 1, true) // the engine restarts it: still alive
	d.Exit("hostd-web", 0, false)
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
	id, err := e.Create(ctx, "hostd-web", podman.ContainerSpec{Image: "nginx:latest", Ports: []string{"8080:80"}, Restart: "always",
		Labels: map[string]string{podman.LabelInstance: "web"}})
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
	if err != nil || !ok || info.Name != "hostd-web" || info.ExitCode != 3 || info.Labels[podman.LabelInstance] != "web" {
		t.Fatalf("Inspect = %+v %v %v", info, ok, err)
	}
	if _, ok, err := e.Inspect(ctx, "gone"); ok || err != nil {
		t.Fatalf("missing container: %v %v", ok, err)
	}
	var dead []string
	_ = e.Events(ctx, podman.LabelInstance, func(id string) { dead = append(dead, id) })
	if !reflect.DeepEqual(dead, []string{"abc123", "def456"}) {
		t.Fatalf("die events = %v", dead)
	}
	if _, err := e.Create(ctx, "other", podman.ContainerSpec{Image: "x"}); err == nil {
		t.Fatal("engine error not reported")
	}

	nowhere := &podman.EngineAPI{Socket: filepath.Join(t.TempDir(), "none.sock")}
	if _, err := nowhere.ImageExists(ctx, "x"); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("unreachable engine: %v", err)
	}
}

// --- the module, through the real pipeline ---------------------------------

func TestComposeRunner(t *testing.T) {
	d := podmantest.NewFakeEngine()
	file := filepath.Join(t.TempDir(), "compose.yaml")
	_ = os.WriteFile(file, []byte("services: {}\n"), 0o644)
	var mu sync.Mutex
	var ran [][]string
	var envs [][]string
	r := &podman.ComposeRunner{Docker: d, Socket: "/run/user/1000/podman/podman.sock", Command: []string{"podman", "compose"},
		Run: func(ctx context.Context, argv, env []string) error { // brings up two containers
			mu.Lock()
			ran, envs = append(ran, argv), append(envs, env)
			mu.Unlock()
			project := argv[3]
			for _, svc := range []string{"web", "db"} {
				id, _ := d.Create(ctx, project+"-"+svc+"-1", podman.ContainerSpec{Labels: map[string]string{podman.LabelProject: project}})
				_ = d.Start(ctx, id)
			}
			return nil
		}}
	app := &spec.App{ID: "media", Runner: spec.Runner{Type: spec.RunnerCompose, File: file}, Env: map[string]string{"TZ": "Europe/Tbilisi"}}
	ctx := context.Background()
	inst, err := r.Start(ctx, spec.Instance{ID: "media#2", App: "media", Env: map[string]string{"PORT": "41000"}}, app)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Container != "hostd-media__2" ||
		strings.Join(ran[0], " ") != "podman compose -p hostd-media__2 -f "+file+" up -d --remove-orphans" ||
		!slices.Contains(envs[0], "DOCKER_HOST=unix:///run/user/1000/podman/podman.sock") ||
		!slices.Contains(envs[0], "TZ=Europe/Tbilisi") || !slices.Contains(envs[0], "PORT=41000") {
		t.Fatalf("inst %+v, ran %q", inst, ran)
	}

	// After a hostd restart: one project, running.
	got, err := r.Adopt(ctx)
	if err != nil || len(got) != 1 || got[0].ID != "media#2" || got[0].App != "media" || got[0].State != spec.StateRunning {
		t.Fatalf("adopt %+v %v", got, err)
	}

	// It ends once every container has stopped.
	ended := make(chan spec.Ended, 1)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Watch(wctx, func(e spec.Ended) { ended <- e }) }()
	for d.Watching() == 0 {
		time.Sleep(time.Millisecond)
	}
	d.Exit("hostd-media__2-web-1", 1, false)
	select {
	case e := <-ended:
		t.Fatalf("ended while db still runs: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	d.Exit("hostd-media__2-db-1", 0, false)
	select {
	case e := <-ended:
		if e.Instance != "media#2" {
			t.Fatalf("ended %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end")
	}
	if list, _ := d.List(ctx, podman.LabelProject); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}

	// Stop: every container stopped and removed.
	if _, err := r.Start(ctx, spec.Instance{ID: "media", App: "media"}, app); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(ctx, spec.Instance{ID: "media"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.List(ctx, podman.LabelProject); len(list) != 0 {
		t.Fatalf("after stop %+v", list)
	}
	if _, err := r.Start(ctx, spec.Instance{ID: "x"}, &spec.App{ID: "x", Runner: spec.Runner{Type: spec.RunnerCompose, File: "/nope.yaml"}}); err == nil {
		t.Fatal("a missing file started")
	}
}

// fakeEngine serves the Engine API on a unix socket, recording requests.
func fakeEngine(t *testing.T, h http.HandlerFunc) *podman.EngineAPI {
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
	return &podman.EngineAPI{Socket: sock}
}

// waitUntil polls cond for up to 5 seconds.
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
