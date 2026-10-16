package deploy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
	port      int            // the service's public port
	source    map[string]any // extra [source] settings
	rescans   int
	tmp       string
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
	if args.Image != "" { // a container release: serves its image name
		if len(args.Ports) != 1 || !strings.HasPrefix(args.Ports[0], "127.0.0.1:"+args.Env["PORT"]+":") {
			return "", fmt.Errorf("ports %q", args.Ports)
		}
		dir, err := os.MkdirTemp(f.tmp, "image-")
		if err != nil {
			return "", err
		}
		_ = os.WriteFile(filepath.Join(dir, "built.html"), []byte(args.Image), 0o644)
		cmd.Dir = dir
	}
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
		{Type: "app.check", Scope: "apps", Schema: json.RawMessage(`{"type":"object","additionalProperties":true}`)},
		{Type: "app.rescan", Scope: "apps"},
	}
	apps.M.Reads = []sdk.ReadSpec{{Name: "apps", Path: "/v1/apps"}, {Name: "instance", Path: "/v1/instances/{id}"}}
	apps.ReadFunc = func(_ context.Context, name string, params map[string]string) (any, error) {
		if name == "apps" {
			src := map[string]any{"type": "push", "port": f.port, "build": []string{"sh", "-c", "test ! -f FAIL && cp index.html built.html"}}
			for k, v := range f.source {
				src[k] = v
			}
			return map[string]any{"apps": []map[string]any{{"id": "site", "deploy": src}}}, nil
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
		case "app.check": // a file saying BROKEN is a problem
			var args struct{ Dir string }
			_ = a.DecodeArgs(&args)
			problems := []map[string]string{}
			entries, _ := os.ReadDir(args.Dir)
			for _, e := range entries {
				if b, _ := os.ReadFile(filepath.Join(args.Dir, e.Name())); strings.Contains(string(b), "BROKEN") {
					problems = append(problems, map[string]string{"file": e.Name(), "error": "broken"})
				}
			}
			return sdk.Result{Data: sdk.MustJSON(map[string]any{"problems": problems})}, nil
		case "app.rescan":
			f.mu.Lock()
			f.rescans++
			f.mu.Unlock()
			return sdk.Result{}, nil
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
	f    *fakeApps
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

func (g *gate) out(dir string, args ...string) string {
	g.t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		g.t.Fatalf("%v: %v", args, err)
	}
	return string(out)
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

// newGate runs the deploy module with fake apps whose service is "site".
func newGate(t *testing.T, source map[string]any, opts Options) *gate {
	t.Helper()
	for _, tool := range []string{"git", "python3", "tar"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("no %s", tool)
		}
	}
	f := &fakeApps{instances: map[string]*fakeInstance{}, port: freeTestPort(t), source: source, tmp: t.TempDir()}
	t.Cleanup(f.stopAll)
	opts.Root, opts.ListenHost, opts.Drain, opts.HealthWait = t.TempDir(), "127.0.0.1", 2*time.Second, 20*time.Second
	m := New(opts)
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
	return &gate{t: t, e: e, m: m, f: f, work: t.TempDir(), url: fmt.Sprintf("http://127.0.0.1:%d/built.html", f.port)}
}

func TestDeployGate(t *testing.T) {
	g := newGate(t, nil, Options{})
	m := g.m

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
	g.f.mu.Lock()
	first := g.f.instances["site"].state
	g.f.mu.Unlock()
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

// waitFor polls cond for up to 30s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 300 {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRemoteSource(t *testing.T) {
	upstream := filepath.Join(t.TempDir(), "up.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", upstream).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	g := newGate(t, map[string]any{"type": "remote", "url": upstream, "poll": "100ms"}, Options{PollTick: 50 * time.Millisecond})
	g.sh(g.work, "git", "init", "-q", "-b", "main")
	g.sh(g.work, "git", "remote", "add", "origin", upstream)

	serves := func(want string) func() bool {
		return func() bool { code, body := g.get(); return code == 200 && body == want }
	}
	g.commit("v1", false)
	waitFor(t, "v1 deployed by polling", serves("v1"))

	// A broken commit fails once and is not retried; v1 keeps serving.
	g.commit("v2", true)
	failed := func() int {
		st, _ := g.m.load("site")
		n := 0
		for _, r := range st.Releases {
			if r.Result == "failed" {
				n++
			}
		}
		return n
	}
	waitFor(t, "v2 refused", func() bool { return failed() == 1 })
	time.Sleep(500 * time.Millisecond) // several polls
	if n := failed(); n != 1 {
		t.Fatalf("the broken commit was tried %d times", n)
	}
	if !serves("v1")() {
		t.Fatal("v1 stopped serving")
	}

	g.commit("v3", false)
	waitFor(t, "v3 deployed", serves("v3"))
}

func TestDeployKey(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	g := newGate(t, map[string]any{"type": "remote", "url": "/nonexistent", "poll": "1h"}, Options{})
	key := func() string {
		res, err := g.do("deploy.key")
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			PublicKey string `json:"public_key"`
		}
		_ = json.Unmarshal(res.Data, &out)
		return out.PublicKey
	}
	first := key()
	if !strings.HasPrefix(first, "ssh-ed25519 ") || key() != first {
		t.Fatalf("keys %q", first)
	}
	if fi, err := os.Stat(g.m.keyPath("site")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key: %v %v", fi, err)
	}
	env := strings.Join(g.m.gitEnv("site"), "\n")
	if !strings.Contains(env, "GIT_SSH_COMMAND=ssh -i "+g.m.keyPath("site")) {
		t.Fatalf("env %s", env)
	}
}

func TestWebhookSource(t *testing.T) {
	upstream := filepath.Join(t.TempDir(), "up.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", upstream).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	secrets := t.TempDir()
	g := newGate(t, map[string]any{"type": "webhook", "url": upstream, "secret": "gh-hook"}, Options{SecretsDir: secrets})
	g.sh(g.work, "git", "init", "-q", "-b", "main")
	g.sh(g.work, "git", "remote", "add", "origin", upstream)
	g.commit("v1", false)
	head := strings.TrimSpace(g.out(g.work, "git", "rev-parse", "HEAD"))

	key := []byte("s3cret")
	n := 0
	hook := func(event string, body string, sign func([]byte) string) (map[string]string, error) {
		n++
		h := http.Header{}
		h.Set("X-GitHub-Event", event)
		h.Set("X-GitHub-Delivery", fmt.Sprintf("d-%d", n))
		if sign != nil {
			h.Set("X-Hub-Signature-256", sign([]byte(body)))
		}
		v, err := g.e.ModuleHook(context.Background(), "deploy", "git", sdk.HookRequest{Params: map[string]string{"app": "site"}, Header: h, Body: []byte(body)})
		if err != nil {
			return nil, err
		}
		return v.(map[string]string), nil
	}
	good := func(b []byte) string {
		mac := hmac.New(sha256.New, key)
		mac.Write(b)
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	push := fmt.Sprintf(`{"ref":"refs/heads/main","after":%q}`, head)

	if _, err := hook("push", push, good); sdk.CodeOf(err) != sdk.CodeModuleUnavailable || !strings.Contains(err.Error(), "hostctl secret set gh-hook") {
		t.Fatalf("no secret yet: %v", err)
	}
	_ = os.WriteFile(filepath.Join(secrets, "gh-hook"), key, 0o600)
	for name, sign := range map[string]func([]byte) string{
		"missing": nil,
		"wrong key": func(b []byte) string {
			mac := hmac.New(sha256.New, []byte("x"))
			mac.Write(b)
			return "sha256=" + hex.EncodeToString(mac.Sum(nil))
		},
		"no prefix":  func(b []byte) string { return strings.TrimPrefix(good(b), "sha256=") },
		"not hex":    func([]byte) string { return "sha256=zz" },
		"other body": func([]byte) string { return good([]byte(push + " ")) },
	} {
		if _, err := hook("push", push, sign); sdk.CodeOf(err) != sdk.CodeUnauthorized {
			t.Errorf("%s signature: %v", name, err)
		}
	}
	if v, err := hook("ping", `{}`, good); err != nil || v["status"] != "pong" {
		t.Fatalf("ping: %v %v", v, err)
	}
	if v, _ := hook("push", `{"ref":"refs/heads/dev","after":"`+head+`"}`, good); v["status"] != "ignored" {
		t.Fatalf("other branch: %v", v)
	}
	if v, _ := hook("push", `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000"}`, good); v["status"] != "ignored" {
		t.Fatalf("deleted branch: %v", v)
	}
	if v, err := hook("push", push, good); err != nil || v["status"] != "deploying" {
		t.Fatalf("push: %v %v", v, err)
	}
	delivered := fmt.Sprintf("d-%d", n)
	waitFor(t, "v1 deployed by the webhook", func() bool { code, body := g.get(); return code == 200 && body == "v1" })

	// The same delivery again (a replay) is ignored.
	h := http.Header{}
	h.Set("X-GitHub-Event", "push")
	h.Set("X-GitHub-Delivery", delivered)
	h.Set("X-Hub-Signature-256", good([]byte(push)))
	v, err := g.e.ModuleHook(context.Background(), "deploy", "git", sdk.HookRequest{Params: map[string]string{"app": "site"}, Header: h, Body: []byte(push)})
	if err != nil || v.(map[string]string)["reason"] != "this delivery was already handled" {
		t.Fatalf("replay: %v %v", v, err)
	}
}

func TestConfigFollow(t *testing.T) {
	upstream := filepath.Join(t.TempDir(), "config.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", upstream).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	appsDir := filepath.Join(t.TempDir(), "hostd", "apps")
	_ = os.MkdirAll(appsDir, 0o755)
	_ = os.WriteFile(filepath.Join(appsDir, "mine.toml"), []byte("hand-written"), 0o644)
	g := newGate(t, nil, Options{AppsDir: appsDir})
	work := g.work
	g.sh(work, "git", "init", "-q", "-b", "main")
	g.sh(work, "git", "remote", "add", "origin", upstream)
	commit := func(files map[string]string) {
		_ = os.RemoveAll(filepath.Join(work, "apps"))
		_ = os.MkdirAll(filepath.Join(work, "apps"), 0o755)
		for name, body := range files {
			_ = os.WriteFile(filepath.Join(work, "apps", name), []byte(body), 0o644)
		}
		g.sh(work, "git", "add", "-A")
		g.sh(work, "git", "commit", "-q", "--allow-empty", "-m", "config")
		g.sh(work, "git", "push", "-q", "origin", "main")
	}
	do := func(typ, args string) (sdk.Result, error) {
		return g.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(args),
			Source: sdk.Source{Kind: sdk.SourceManual}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(appsDir, name))
		if err != nil {
			return ""
		}
		return string(b)
	}

	commit(map[string]string{"site.toml": "v1"})
	if _, err := do("config.follow", fmt.Sprintf(`{"url":%q}`, upstream)); err != nil {
		t.Fatal(err)
	}
	if read("site.toml") != "v1" || read("mine.toml") != "" {
		t.Fatalf("applied: site=%q mine=%q", read("site.toml"), read("mine.toml"))
	}
	if b, _ := os.ReadFile(filepath.Join(appsDir+".before-follow", "mine.toml")); string(b) != "hand-written" {
		t.Fatal("the hand-written app files were not kept")
	}

	// An invalid commit is rejected; the previous config stays.
	commit(map[string]string{"site.toml": "BROKEN"})
	_, err := do("config.sync", `{}`)
	if sdk.CodeOf(err) != sdk.CodePreconditionFailed || !strings.Contains(err.Error(), "previous config stays") {
		t.Fatalf("broken config: %v", err)
	}
	if read("site.toml") != "v1" {
		t.Fatalf("after the rejection: %q", read("site.toml"))
	}
	f, _ := g.m.loadFollow()
	if f.Rejected == "" || len(f.Problems) != 1 || f.Applied == f.Rejected {
		t.Fatalf("state %+v", f)
	}

	// A good commit after it.
	commit(map[string]string{"site.toml": "v2", "blog.toml": "b"})
	if _, err := do("config.sync", `{}`); err != nil {
		t.Fatal(err)
	}
	if read("site.toml") != "v2" || read("blog.toml") != "b" {
		t.Fatalf("v2: %q %q", read("site.toml"), read("blog.toml"))
	}
	g.f.mu.Lock()
	rescans := g.f.rescans
	g.f.mu.Unlock()
	if rescans != 2 {
		t.Fatalf("rescans %d", rescans)
	}

	// No apps/ folder: rejected.
	g.sh(work, "git", "rm", "-q", "-r", "apps")
	g.sh(work, "git", "commit", "-q", "-m", "oops")
	g.sh(work, "git", "push", "-q", "origin", "main")
	if _, err := do("config.sync", `{}`); err == nil || !strings.Contains(err.Error(), "no apps/ folder") {
		t.Fatalf("no apps folder: %v", err)
	}

	// Unfollow: the files in use become an ordinary folder.
	if _, err := do("config.unfollow", `{}`); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(appsDir); err != nil || fi.Mode()&os.ModeSymlink != 0 || read("site.toml") != "v2" {
		t.Fatalf("after unfollow: %v %v %q", fi, err, read("site.toml"))
	}
	if _, err := do("config.sync", `{}`); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("sync after unfollow: %v", err)
	}
}

func TestImageSource(t *testing.T) {
	reg := newFakeRegistry(t, true)
	reg.set("main", digestOf('1'))
	secrets := t.TempDir()
	_ = os.WriteFile(filepath.Join(secrets, "ghcr"), []byte("me:pat\n"), 0o600)
	g := newGate(t, map[string]any{"type": "image", "image": reg.image(), "tag": "main", "container_port": 80,
		"auth": "ghcr", "poll": "100ms", "build": nil}, Options{SecretsDir: secrets, PollTick: 50 * time.Millisecond})
	serves := func(digest string) func() bool {
		return func() bool { code, body := g.get(); return code == 200 && body == reg.image()+"@"+digest }
	}
	waitFor(t, "the first image deployed by polling", serves(digestOf('1')))
	reg.set("main", digestOf('2'))
	waitFor(t, "the new digest deployed", serves(digestOf('2')))

	if _, err := g.do("deploy.rollback"); err != nil {
		t.Fatal(err)
	}
	if !serves(digestOf('1'))() {
		t.Fatal("rollback to the first image")
	}
	time.Sleep(400 * time.Millisecond) // polls: the newer digest is not deployed again
	if !serves(digestOf('1'))() {
		t.Fatal("polling undid the rollback")
	}
}

// After a reboot the live release is not running: it is started again,
// and the service answers as before. The module's reads say where it is.
func TestLiveReleaseComesBack(t *testing.T) {
	g := newGate(t, nil, Options{})
	m := g.m
	ctx := context.Background()
	// As a push deploys: not a person's changes, so nothing holds the app
	// (after a reboot no hold would remain either).
	auto := func(typ string) {
		t.Helper()
		res, err := g.e.Submit(ctx, sdk.Action{Type: typ, Args: json.RawMessage(`{"app":"site"}`),
			Source: sdk.Source{Kind: sdk.SourceAutomation, Name: "git push"}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}})
		if err != nil || res.Status == sdk.StatusSkipped {
			t.Fatalf("%s: %v %s", typ, err, res.Status)
		}
	}
	auto("deploy.init")
	g.sh(g.work, "git", "init", "-q", "-b", "main")
	g.sh(g.work, "git", "remote", "add", "origin", m.repo("site"))
	g.commit("v1", false)
	auto("deploy.run")
	var cur Release
	waitFor(t, "the release to be live", func() bool {
		st, err := m.load("site")
		if err != nil {
			return false
		}
		c, ok := st.current()
		cur = c
		return ok
	})
	g.f.stop(cur.Instance) // the machine rebooted: nothing runs
	if code, _ := g.get(); code == 200 {
		t.Fatal("still answering with nothing running")
	}
	m.sync(ctx)
	if code, body := g.get(); code != 200 || body != "v1" {
		t.Fatalf("after the restart: %d %q", code, body)
	}
	st, _ := m.load("site")
	if again, _ := st.current(); again.Instance == cur.Instance || again.Rev != cur.Rev {
		t.Fatalf("revived %+v, was %+v", again, cur)
	}

	all, err := m.Read(ctx, "deploys", nil)
	if err != nil || len(all.([]*State)) != 1 {
		t.Fatalf("deploys %v %v", all, err)
	}
	if one, err := m.Read(ctx, "deploy", map[string]string{"app": "site"}); err != nil || one.(*State).App != "site" {
		t.Fatalf("deploy %v %v", one, err)
	}
	if _, err := m.Read(ctx, "deploy", map[string]string{"app": "ghost"}); err == nil {
		t.Fatal("a deploy of an app that is not a service")
	}
	if _, err := m.Read(ctx, "nothing", nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown read: %v", err)
	}
	if s, err := m.State(ctx); err != nil || len(s.([]*State)) != 1 {
		t.Fatalf("state %v %v", s, err)
	}
}

// A followed config is applied by polling: a new commit on its branch,
// not one already applied or rejected.
func TestConfigFollowPolls(t *testing.T) {
	upstream := filepath.Join(t.TempDir(), "config.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "-b", "main", upstream).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	appsDir := filepath.Join(t.TempDir(), "hostd", "apps")
	g := newGate(t, nil, Options{AppsDir: appsDir})
	g.sh(g.work, "git", "init", "-q", "-b", "main")
	g.sh(g.work, "git", "remote", "add", "origin", upstream)
	commit := func(body string) {
		_ = os.MkdirAll(filepath.Join(g.work, "apps"), 0o755)
		_ = os.WriteFile(filepath.Join(g.work, "apps", "site.toml"), []byte(body), 0o644)
		g.sh(g.work, "git", "add", "-A")
		g.sh(g.work, "git", "commit", "-q", "-m", body)
		g.sh(g.work, "git", "push", "-q", "origin", "main")
	}
	ctx := context.Background()
	commit("v1")
	// Followed by automation: a person's follow would hold the config for
	// its hold window, and a poll in it would wait for the next one.
	if _, err := g.e.Submit(ctx, sdk.Action{Type: "config.follow", Args: json.RawMessage(fmt.Sprintf(`{"url":%q,"poll":"30s"}`, upstream)),
		Source: sdk.Source{Kind: sdk.SourceAutomation, Name: "setup"}}, core.Auth{Scopes: []string{sdk.ScopeAdmin}}); err != nil {
		t.Fatal(err)
	}
	commit("v2")
	g.m.pollConfig(ctx)
	if b, _ := os.ReadFile(filepath.Join(appsDir, "site.toml")); string(b) != "v2" {
		t.Fatalf("not applied by polling: %q", b)
	}
	if read, err := g.m.readFollow(); err != nil || read == nil {
		t.Fatalf("follow read %v %v", read, err)
	}
	f, err := g.m.loadFollow()
	if err != nil || f == nil {
		t.Fatalf("follow %v %v", f, err)
	}
	if f.pollEvery() != 30*time.Second || (&Follow{}).pollEvery() != time.Minute {
		t.Fatalf("poll %v", f.pollEvery())
	}
	// Nothing new: nothing done.
	before, _ := os.Stat(filepath.Join(appsDir, "site.toml"))
	g.m.pollConfig(ctx)
	if after, _ := os.Stat(filepath.Join(appsDir, "site.toml")); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("applied again with nothing new")
	}
	if _, err := g.m.Read(ctx, "config_follow", nil); err != nil {
		t.Fatal(err)
	}
}

// What the deploy module refuses, and the actions not exercised elsewhere.
func TestDeployRefusals(t *testing.T) {
	appsDir := filepath.Join(t.TempDir(), "hostd", "apps")
	_ = os.MkdirAll(appsDir, 0o755)
	keys := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	g := newGate(t, nil, Options{AppsDir: appsDir, AuthorizedKeys: keys, GitShell: "/usr/local/bin/hostctl git-shell"})
	ctx := context.Background()
	do := func(typ, args string) (sdk.Result, error) {
		return g.e.Submit(ctx, sdk.Action{Type: typ, Args: json.RawMessage(args), Source: sdk.Source{Kind: sdk.SourceManual}},
			core.Auth{Scopes: []string{sdk.ScopeAdmin}})
	}
	for _, c := range []struct {
		typ, args string
		code      sdk.Code
	}{
		{"deploy.run", `{"app":"ghost"}`, sdk.CodeNotFound},
		{"deploy.rollback", `{"app":"site"}`, sdk.CodeNotFound}, // no earlier release
		{"config.follow", `{"url":"--upload-pack=evil"}`, sdk.CodeInvalidArgs},
		{"config.follow", `{"url":"/r.git","poll":"1s"}`, sdk.CodeInvalidArgs},
		{"config.sync", `{}`, sdk.CodeNotFound},
	} {
		if _, err := do(c.typ, c.args); sdk.CodeOf(err) != c.code {
			t.Errorf("%s %s: %v, want %s", c.typ, c.args, err, c.code)
		}
	}
	// A key for a private config repository, and for a service's.
	res, err := do("config.key", `{}`)
	if err != nil || !strings.Contains(string(res.Data), "ssh-ed25519") {
		t.Fatalf("config.key: %s %v", res.Data, err)
	}
	first, err := do("deploy.key", `{"app":"site"}`)
	if err != nil || !strings.Contains(string(first.Data), "ssh-ed25519") {
		t.Fatalf("deploy.key: %s %v", first.Data, err)
	}
	// Asked again: the same key, not a new one (the repository has it).
	if again, err := do("deploy.key", `{"app":"site"}`); err != nil || string(again.Data) != string(first.Data) {
		t.Fatalf("a second deploy.key: %s %v", again.Data, err)
	}
	if again, _ := do("config.key", `{}`); string(again.Data) != string(res.Data) {
		t.Fatalf("a second config.key: %s", again.Data)
	}
	// A push key, through the action: added with its forced command, then gone.
	pub := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl laptop"
	if _, err := do("deploy.authorize", fmt.Sprintf(`{"name":"laptop","key":%q}`, pub)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(keys); !strings.Contains(string(b), `command="/usr/local/bin/hostctl git-shell"`) || !strings.Contains(string(b), "hostd-push:laptop") {
		t.Fatalf("authorized_keys:\n%s", b)
	}
	if _, err := do("deploy.authorize", `{"name":"bad","key":"not a key"}`); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("a bad key: %v", err)
	}
	if _, err := do("deploy.revoke", `{"name":"laptop"}`); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(keys); strings.Contains(string(b), "hostd-push:laptop") {
		t.Fatalf("revoked key stays:\n%s", b)
	}
	// init gives the remote to push to.
	if res, err := do("deploy.init", `{"app":"site"}`); err != nil || !strings.Contains(string(res.Data), `"remote":"ssh://`) {
		t.Fatalf("init: %s %v", res.Data, err)
	}
	// Unfollowing what is not followed changes nothing.
	if _, err := do("config.unfollow", `{}`); err != nil && sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unfollow: %v", err)
	}
}

func TestTeeTail(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var tail bytes.Buffer
	w := &teeTail{w: f, tail: &tail}
	big := strings.Repeat("x", 70<<10)
	if _, err := w.Write([]byte(big + "\nend\n")); err != nil {
		t.Fatal(err)
	}
	if tail.Len() != 64<<10 || !strings.HasSuffix(tail.String(), "end\n") {
		t.Fatalf("kept %d bytes", tail.Len())
	}
	if fi, _ := f.Stat(); fi.Size() != int64(len(big)+5) {
		t.Fatalf("the log has %d bytes", fi.Size())
	}
	if got := lastLines("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("lastLines %q", got)
	}
}

func TestSmallEdges(t *testing.T) {
	var s, plain service
	_ = json.Unmarshal([]byte(`{"id":"site","deploy":{"type":"remote","poll":"15s"}}`), &s)
	_ = json.Unmarshal([]byte(`{"id":"site","deploy":{"type":"remote"}}`), &plain)
	if s.pollEvery() != 15*time.Second || plain.pollEvery() != time.Minute {
		t.Errorf("poll %v %v", s.pollEvery(), plain.pollEvery())
	}
	if s.branch() != "main" {
		t.Errorf("default branch %q", s.branch())
	}
	m := New(Options{Root: t.TempDir()})
	if _, err := m.readFollow(); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("nothing followed: %v", err)
	}
	// Webhook secrets: none configured, not set, empty, a bad name.
	if _, err := m.readSecret("hook"); sdk.CodeOf(err) != sdk.CodeModuleUnavailable {
		t.Fatalf("no secrets folder: %v", err)
	}
	dir := t.TempDir()
	m = New(Options{Root: t.TempDir(), SecretsDir: dir})
	if _, err := m.readSecret("hook"); err == nil || !strings.Contains(err.Error(), "hostctl secret set hook") {
		t.Fatalf("not set: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "hook"), nil, 0o600)
	if _, err := m.readSecret("hook"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty: %v", err)
	}
	if _, err := m.readSecret("../etc"); err == nil {
		t.Fatal("read outside the secrets")
	}
	_ = os.WriteFile(filepath.Join(dir, "hook"), []byte("s3cret"), 0o600)
	if b, err := m.readSecret("hook"); err != nil || string(b) != "s3cret" {
		t.Fatalf("%q %v", b, err)
	}
}

// A replaced release drains: it takes no new requests, and the drain ends
// when the last one has (or when the time is up).
func TestUpstreamDrain(t *testing.T) {
	u := newUpstream(1)
	for range 2 { // two requests in flight
		if !u.acquire() {
			t.Fatal("a fresh upstream refused a request")
		}
	}
	done := make(chan struct{})
	go func() { drain(u, 5*time.Second); close(done) }()
	waitFor(t, "draining", func() bool { u.mu.Lock(); defer u.mu.Unlock(); return u.draining })
	if u.acquire() {
		t.Fatal("a draining upstream took a request")
	}
	u.release()
	select {
	case <-done:
		t.Fatal("drained with a request left")
	case <-time.After(50 * time.Millisecond):
	}
	u.release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the drain did not end")
	}
	// The time is up before the requests are.
	slow := newUpstream(2)
	slow.acquire()
	start := time.Now()
	drain(slow, 50*time.Millisecond)
	if time.Since(start) > 2*time.Second {
		t.Fatal("waited past the drain time")
	}
	drain(nil, time.Second) // nothing to drain
}

// What the webhook ignores or refuses, before anything is fetched.
func TestWebhookIgnores(t *testing.T) {
	secrets := t.TempDir()
	_ = os.WriteFile(filepath.Join(secrets, "hook"), []byte("s3cret"), 0o600)
	g := newGate(t, map[string]any{"type": "webhook", "url": "/nowhere.git", "secret": "hook"}, Options{SecretsDir: secrets})
	ctx := context.Background()
	sign := func(body string) string {
		mac := hmac.New(sha256.New, []byte("s3cret"))
		mac.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	hook := func(event, delivery, body string) (any, error) {
		h := map[string][]string{"X-Hub-Signature-256": {sign(body)}, "X-Github-Event": {event}}
		if delivery != "" {
			h["X-Github-Delivery"] = []string{delivery}
		}
		return g.m.Hook(ctx, "git", sdk.HookRequest{Params: map[string]string{"app": "site"}, Header: h, Body: []byte(body)})
	}
	reason := func(v any) string { m, _ := v.(map[string]string); return m["status"] + ": " + m["reason"] }

	if _, err := g.m.Hook(ctx, "other", sdk.HookRequest{}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("another hook: %v", err)
	}
	if _, err := g.m.Hook(ctx, "git", sdk.HookRequest{Params: map[string]string{"app": "ghost"}}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("not a service: %v", err)
	}
	for _, c := range []struct{ event, delivery, body, want string }{
		{"issues", "", `{}`, "ignored: not a push"},
		{"push", "", `{"ref":"refs/heads/dev","after":"1234"}`, "ignored: a push to refs/heads/dev, not main"},
		{"push", "", `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000"}`, "ignored: the branch was deleted"},
		{"ping", "d1", `{}`, "pong: "},
		{"ping", "d1", `{}`, "ignored: this delivery was already handled"},
	} {
		got, err := hook(c.event, c.delivery, c.body)
		if err != nil || reason(got) != c.want {
			t.Errorf("%s %s: %q %v, want %q", c.event, c.body, reason(got), err, c.want)
		}
	}
	if _, err := hook("push", "", `not json`); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("a broken push: %v", err)
	}
}
