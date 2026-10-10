package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

// fakeApps runs releases for real (a small web server per release), as
// the apps module would through systemd.
type fakeApps struct {
	mu        sync.Mutex
	instances map[string]*fakeInstance
	n         int
	port      int // the service's public port
}

type fakeInstance struct {
	cmd   *exec.Cmd
	state string
}

func (f *fakeApps) start(args contract.AppStart) (string, error) {
	f.mu.Lock()
	f.n++
	id := "site"
	if f.n > 1 {
		id = fmt.Sprintf("site#%d", f.n)
	}
	f.mu.Unlock()
	cmd := exec.Command("python3", "-m", "http.server", "--bind", "127.0.0.1", args.Env["PORT"])
	cmd.Dir = args.Dir
	if err := cmd.Start(); err != nil {
		return "", err
	}
	in := &fakeInstance{cmd: cmd, state: "starting"}
	f.mu.Lock()
	f.instances[id] = in
	f.mu.Unlock()
	go func() { // healthy once its port answers
		for range 100 {
			if c, err := net.Dial("tcp", "127.0.0.1:"+args.Env["PORT"]); err == nil {
				_ = c.Close()
				f.mu.Lock()
				if in.state == "starting" {
					in.state = "running"
				}
				f.mu.Unlock()
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return id, nil
}

func (f *fakeApps) stop(id string) {
	f.mu.Lock()
	in := f.instances[id]
	f.mu.Unlock()
	if in == nil {
		return
	}
	_ = in.cmd.Process.Kill()
	_ = in.cmd.Wait()
	f.mu.Lock()
	in.state = "exited"
	f.mu.Unlock()
}

func (f *fakeApps) stopAll() {
	f.mu.Lock()
	ids := make([]string, 0, len(f.instances))
	for id := range f.instances {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.stop(id)
	}
}

func (f *fakeApps) module(t *testing.T) *testutil.Module {
	apps := testutil.NewModule("apps", nil, nil, nil)
	apps.M.Owns = []string{"app.*", "instance.*"}
	apps.M.Scopes = []sdk.ScopeSpec{{Name: "apps"}, {Name: contract.ScopeDeploy}}
	apps.M.Actions = []sdk.ActionSpec{
		{Type: "app.start", Scope: "apps", Schema: json.RawMessage(`{"type":"object","additionalProperties":true}`), Keys: []sdk.KeyTemplate{"app:{id}"}},
		{Type: "instance.stop", Scope: "apps", Schema: json.RawMessage(`{"type":"object","additionalProperties":true}`), Keys: []sdk.KeyTemplate{"instance:{id}"}},
	}
	apps.M.Reads = []sdk.ReadSpec{{Name: "apps", Path: "/v1/apps"}, {Name: "instance", Path: "/v1/instances/{id}"}}
	apps.ReadFunc = func(_ context.Context, name string, params map[string]string) (any, error) {
		if name == "apps" {
			return map[string]any{"apps": []map[string]any{{"id": "site", "deploy": map[string]any{
				"type": "push", "port": f.port, "build": []string{"sh", "-c", "test ! -f FAIL && cp index.html built.html"}}}}}, nil
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		in := f.instances[params["id"]]
		if in == nil {
			return nil, sdk.Errorf(sdk.CodeNotFound, "no instance %q", params["id"])
		}
		return map[string]string{"id": params["id"], "state": in.state}, nil
	}
	apps.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		switch a.Type {
		case "app.start":
			var args contract.AppStart
			_ = a.DecodeArgs(&args)
			id, err := f.start(args)
			if err != nil {
				return sdk.Result{}, err
			}
			return sdk.Result{Data: sdk.MustJSON(map[string]string{"instance": id, "state": "starting"})}, nil
		default:
			var args struct{ ID string }
			_ = a.DecodeArgs(&args)
			f.stop(args.ID)
			return sdk.Result{}, nil
		}
	}
	return apps
}

func freeTestPort(t *testing.T) int {
	p, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type gate struct {
	t    *testing.T
	e    *core.Engine
	m    *Module
	work string // a clone that pushes to the service's repo
	url  string
}

func (g *gate) sh(dir string, args ...string) {
	g.t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "PATH=/usr/bin:/bin")
	if out, err := cmd.CombinedOutput(); err != nil {
		g.t.Fatalf("%v: %v\n%s", args, err, out)
	}
}

// commit writes index.html (and FAIL to break the build) and pushes.
func (g *gate) commit(text string, breakBuild bool) {
	g.t.Helper()
	_ = os.WriteFile(filepath.Join(g.work, "index.html"), []byte(text), 0o644)
	if breakBuild {
		_ = os.WriteFile(filepath.Join(g.work, "FAIL"), nil, 0o644)
	} else {
		_ = os.Remove(filepath.Join(g.work, "FAIL"))
	}
	g.sh(g.work, "git", "add", "-A")
	g.sh(g.work, "git", "commit", "-q", "-m", text)
	g.sh(g.work, "git", "push", "-q", "origin", "main")
}

func (g *gate) do(typ string) (sdk.Result, error) {
	return g.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(`{"app":"site"}`),
		Source: sdk.Source{Kind: sdk.SourceManual}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
}

func (g *gate) get() (int, string) {
	resp, err := http.Get(g.url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestDeployGate(t *testing.T) {
	for _, tool := range []string{"git", "python3", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	f := &fakeApps{instances: map[string]*fakeInstance{}, port: freeTestPort(t)}
	t.Cleanup(f.stopAll)
	root := t.TempDir()
	m := New(Options{Root: root, ListenHost: "127.0.0.1", Drain: 2 * time.Second, HealthWait: 20 * time.Second})
	reg := core.NewRegistry()
	for _, mod := range []sdk.Module{f.module(t), m} {
		if err := reg.Add(mod); err != nil {
			t.Fatal(err)
		}
	}
	e := core.New(reg, core.Options{})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	g := &gate{t: t, e: e, m: m, work: t.TempDir(), url: fmt.Sprintf("http://127.0.0.1:%d/built.html", f.port)}

	if _, err := g.do("deploy.init"); err != nil {
		t.Fatal(err)
	}
	if code, _ := g.get(); code != http.StatusServiceUnavailable {
		t.Fatalf("before any release: %d", code)
	}
	g.sh(g.work, "git", "init", "-q", "-b", "main")
	g.sh(g.work, "git", "remote", "add", "origin", m.repo("site"))

	// v1
	g.commit("v1", false)
	if _, err := g.do("deploy.run"); err != nil {
		t.Fatal(err)
	}
	if code, body := g.get(); code != 200 || body != "v1" {
		t.Fatalf("v1: %d %q", code, body)
	}

	// v2 under load: no request fails.
	var failures, total atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			total.Add(1)
			if code, body := g.get(); code != 200 || (body != "v1" && body != "v2") {
				failures.Add(1)
			}
		}
	}()
	g.commit("v2", false)
	if _, err := g.do("deploy.run"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d of %d requests failed during the switch", failures.Load(), total.Load())
	}
	if code, body := g.get(); code != 200 || body != "v2" {
		t.Fatalf("v2: %d %q", code, body)
	}
	f.mu.Lock()
	first := f.instances["site"].state
	f.mu.Unlock()
	if first != "exited" {
		t.Fatalf("v1's release still %s after the switch", first)
	}

	// v3's build fails: v2 keeps serving.
	g.commit("v3", true)
	_, err := g.do("deploy.run")
	if err == nil || !strings.Contains(err.Error(), "keeps serving") || !strings.Contains(err.Error(), "build") {
		t.Fatalf("broken build: %v", err)
	}
	if code, body := g.get(); code != 200 || body != "v2" {
		t.Fatalf("after the failed deploy: %d %q", code, body)
	}
	st, _ := m.load("site")
	if st.Releases[0].Result != "failed" {
		t.Fatalf("history %+v", st.Releases)
	}

	// Back to v1.
	if _, err := g.do("deploy.rollback"); err != nil {
		t.Fatal(err)
	}
	if code, body := g.get(); code != 200 || body != "v1" {
		t.Fatalf("after rollback: %d %q", code, body)
	}
	st, _ = m.load("site")
	revs := map[string]int{}
	for _, r := range st.Releases {
		revs[r.Rev]++
	}
	for rev, n := range revs {
		if n > 1 {
			t.Fatalf("release %s listed %d times: %+v", rev[:8], n, st.Releases)
		}
	}
	if st.Releases[0].Result != "live" || st.Releases[0].Rev != st.Current {
		t.Fatalf("history after rollback %+v", st.Releases)
	}
}
