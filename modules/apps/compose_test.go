package apps

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProjectName(t *testing.T) {
	for _, id := range []string{"jellyfin", "jellyfin#2", "my_app.v2#10", "a-b", "x__y"} {
		p := projectName(id)
		if strings.ContainsAny(strings.TrimPrefix(p, "hostd-"), ".#") {
			t.Errorf("%s: project %s", id, p)
		}
		if back, ok := instanceOfProject(p); !ok || back != id {
			t.Errorf("%s → %s → %s %v", id, p, back, ok)
		}
	}
	for _, p := range []string{"other", "hostd-", "hostd-a_x", "hostd-a_"} {
		if _, ok := instanceOfProject(p); ok {
			t.Errorf("%s taken for one of hostd's", p)
		}
	}
}

func TestComposeRunner(t *testing.T) {
	d := newFakeDocker()
	file := filepath.Join(t.TempDir(), "compose.yaml")
	_ = os.WriteFile(file, []byte("services: {}\n"), 0o644)
	var mu sync.Mutex
	var ran [][]string
	var envs [][]string
	r := &ComposeRunner{Docker: d, Socket: "/run/user/1000/podman/podman.sock", Command: []string{"podman", "compose"},
		Run: func(ctx context.Context, argv, env []string) error { // brings up two containers
			mu.Lock()
			ran, envs = append(ran, argv), append(envs, env)
			mu.Unlock()
			project := argv[3]
			for _, svc := range []string{"web", "db"} {
				id, _ := d.Create(ctx, project+"-"+svc+"-1", ContainerSpec{Labels: map[string]string{labelProject: project}})
				_ = d.Start(ctx, id)
			}
			return nil
		}}
	app := &App{ID: "media", Runner: Runner{Type: RunnerCompose, File: file}, Env: map[string]string{"TZ": "Europe/Tbilisi"}}
	ctx := context.Background()
	inst, err := r.Start(ctx, Instance{ID: "media#2", App: "media", Env: map[string]string{"PORT": "41000"}}, app)
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
	if err != nil || len(got) != 1 || got[0].ID != "media#2" || got[0].App != "media" || got[0].State != StateRunning {
		t.Fatalf("adopt %+v %v", got, err)
	}

	// It ends once every container has stopped.
	ended := make(chan Ended, 1)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = r.Watch(wctx, func(e Ended) { ended <- e }) }()
	for d.watching() == 0 {
		time.Sleep(time.Millisecond)
	}
	d.exit("hostd-media__2-web-1", 1, false)
	select {
	case e := <-ended:
		t.Fatalf("ended while db still runs: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	d.exit("hostd-media__2-db-1", 0, false)
	select {
	case e := <-ended:
		if e.Instance != "media#2" {
			t.Fatalf("ended %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end")
	}
	if list, _ := d.List(ctx, labelProject); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}

	// Stop: every container stopped and removed.
	if _, err := r.Start(ctx, Instance{ID: "media", App: "media"}, app); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(ctx, Instance{ID: "media"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.List(ctx, labelProject); len(list) != 0 {
		t.Fatalf("after stop %+v", list)
	}
	if _, err := r.Start(ctx, Instance{ID: "x"}, &App{ID: "x", Runner: Runner{Type: RunnerCompose, File: "/nope.yaml"}}); err == nil {
		t.Fatal("a missing file started")
	}
}

func TestComposeFileBesideAppFile(t *testing.T) {
	f, err := ParseAppFile("/cfg/apps/media.toml", []byte("runner = { type = \"compose\", file = \"media/compose.yaml\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Runner.File != "/cfg/apps/media/compose.yaml" {
		t.Fatalf("file %q", f.Runner.File)
	}
}
