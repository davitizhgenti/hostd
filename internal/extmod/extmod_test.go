package extmod

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

var admin = core.Auth{Scopes: []string{sdk.ScopeAdmin}}

func python(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	return p
}

func example(t *testing.T) []string {
	path, _ := filepath.Abs(filepath.Join("..", "..", "examples", "python-module", "lights.py"))
	return []string{python(t), path}
}

type rig struct {
	e     *core.Engine
	m     *Module
	audit *core.MemoryAudit
	apps  *testutil.Module
}

func newRig(t *testing.T) *rig { return newRigWith(t, TransportSocket) }

func newRigWith(t *testing.T, transport string) *rig {
	t.Helper()
	m, err := Load(context.Background(), Options{Name: "lights", Command: example(t), SocketDir: t.TempDir(), Transport: transport,
		Backoff: 50 * time.Millisecond, ConnectWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	apps := testutil.NewModule("apps", nil, nil, []string{"instance.started"})
	apps.M.Owns = []string{"instance.*"}
	reg := core.NewRegistry()
	for _, mod := range []sdk.Module{apps, m} {
		if err := reg.Add(mod); err != nil {
			t.Fatal(err)
		}
	}
	audit := core.NewMemoryAudit(1000)
	e := core.New(reg, core.Options{Audit: audit})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return &rig{e: e, m: m, audit: audit, apps: apps}
}

func (r *rig) do(t *testing.T, typ, args string) (sdk.Result, error) {
	t.Helper()
	return r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: json.RawMessage(args),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, admin)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPythonModule(t *testing.T) {
	r := newRig(t)
	man := r.m.Manifest()
	if man.Name != "lights" || len(man.Actions) != 2 || man.Actions[0].Route.Path != "/v1/lights/{id}" {
		t.Fatalf("manifest %+v", man)
	}

	// An action through the pipeline, with its event tied to it.
	events := r.e.Subscribe(context.Background(), "lights.*")
	res, err := r.do(t, "lights.set", `{"id":"porch","brightness":40}`)
	if err != nil || res.Status != sdk.StatusApplied || !strings.Contains(string(res.Data), `"brightness": 40`) && !strings.Contains(string(res.Data), `"brightness":40`) {
		t.Fatalf("set: %+v %v", res, err)
	}
	select {
	case ev := <-events:
		if ev.Type != "lights.changed" || ev.Action != res.Action || ev.Source == nil || ev.Source.Kind != sdk.SourceManual {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no lights.changed")
	}

	// The schema is the core's to check; validate is the module's.
	if _, err := r.do(t, "lights.set", `{"id":"porch","brightness":400}`); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("out of range: %v", err)
	}
	if _, err := r.do(t, "lights.set", `{"id":"garage","brightness":4}`); sdk.CodeOf(err) != sdk.CodeNotFound || !strings.Contains(err.Error(), "garage") {
		t.Fatalf("unknown light: %v", err)
	}

	// Child actions from inside an action ("within").
	res, err = r.do(t, "lights.dim_all", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	children := 0
	for _, e := range r.audit.Entries() {
		if e.Type == "lights.set" && e.Parent == res.Action && e.Source.Kind == sdk.SourceManual {
			children++
		}
	}
	if children != 2 {
		t.Fatalf("%d child actions in the audit: %+v", children, r.audit.Entries())
	}

	// A read, and the state.
	raw, err := r.e.ModuleRead(context.Background(), "lights", "lights", nil)
	if err != nil || !strings.Contains(string(raw.(json.RawMessage)), `"brightness": 10`) {
		t.Fatalf("read %s %v", raw, err)
	}

	// Subscriptions: the module counts the apps started.
	r.apps.Core().Emit(sdk.Event{Type: "instance.started", Data: json.RawMessage(`{"id":"tv"}`)})
	waitFor(t, "the module to see the event", func() bool {
		st, err := r.m.State(context.Background())
		return err == nil && strings.Contains(string(st.(json.RawMessage)), `"apps_started": 1`)
	})
}

func TestModuleRestarts(t *testing.T) {
	r := newRig(t)
	r.m.mu.Lock()
	pid := r.m.proc.Process.Pid
	r.m.mu.Unlock()
	_ = syscall.Kill(pid, syscall.SIGKILL)
	waitFor(t, "the module to be down", func() bool {
		_, err := r.do(t, "lights.set", `{"id":"porch","brightness":1}`)
		return sdk.CodeOf(err) == sdk.CodeModuleUnavailable
	})
	waitFor(t, "the module to come back", func() bool {
		_, err := r.do(t, "lights.set", `{"id":"porch","brightness":2}`)
		return err == nil
	})
	r.m.mu.Lock()
	again := r.m.proc.Process.Pid
	r.m.mu.Unlock()
	if again == pid {
		t.Fatal("same process")
	}
	// It subscribed again in start.
	r.apps.Core().Emit(sdk.Event{Type: "instance.started", Data: json.RawMessage(`{"id":"tv"}`)})
	waitFor(t, "the restarted module to see events", func() bool {
		st, err := r.m.State(context.Background())
		return err == nil && strings.Contains(string(st.(json.RawMessage)), `"apps_started": 1`)
	})
}

func TestLoadFailures(t *testing.T) {
	py := python(t)
	dir := t.TempDir()
	script := func(name, body string) []string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return []string{py, p}
	}
	const connect = "import json, os, socket\ns = socket.socket(socket.AF_UNIX)\ns.connect(os.environ['HOSTD_MODULE_SOCKET'])\nf = s.makefile('r')\n"
	reply := func(result string) string {
		return "m = json.loads(f.readline())\ns.sendall((json.dumps({'jsonrpc': '2.0', 'id': m['id'], 'result': " + result + "}) + '\\n').encode())\n"
	}
	for name, c := range map[string]struct {
		cmd  []string
		want string
	}{
		"never connects": {script("sleep.py", "import time\ntime.sleep(30)\n"), "did not connect"},
		"exits":          {script("exit.py", "raise SystemExit(1)\n"), "ended before connecting"},
		"protocol 2":     {script("p2.py", connect+reply("{'protocol': 2}")+"f.readline()\n"), "protocol 2"},
		"wrong name": {script("name.py", connect+reply("{'protocol': 1}")+reply("{'name': 'other', 'version': '1'}")+"f.readline()\n"),
			`calls itself "other"`},
		"bad manifest": {script("bad.py", connect+reply("{'protocol': 1}")+reply("{'name': 'x', 'version': '1', 'owns': ['apps.*'], 'actions': [{'type': 'lights.set', 'scope': 'admin'}]}")+"f.readline()\n"),
			"manifest"},
		"no command": {nil, "no command"},
	} {
		_, err := Load(context.Background(), Options{Name: "x", Command: c.cmd, SocketDir: filepath.Join(dir, "sock"), ConnectWait: 500 * time.Millisecond})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The same module over standard input and output: no socket, and what it
// prints on standard error still reaches the log.
func TestPythonModuleStdio(t *testing.T) {
	sockets := t.TempDir()
	m, err := Load(context.Background(), Options{Name: "lights", Command: example(t), SocketDir: sockets, Transport: TransportStdio,
		Backoff: 50 * time.Millisecond, ConnectWait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Stop(context.Background())
	if entries, _ := os.ReadDir(sockets); len(entries) != 0 {
		t.Fatalf("the stdio transport made files: %v", entries)
	}

	r := newRigWith(t, TransportStdio)
	res, err := r.do(t, "lights.set", `{"id":"porch","brightness":40}`)
	if err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("set: %+v %v", res, err)
	}
	if _, err := r.do(t, "lights.set", `{"id":"garage","brightness":4}`); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown light: %v", err)
	}
	// Child actions, a read, and a subscription: the calls the module makes.
	if _, err := r.do(t, "lights.dim_all", `{}`); err != nil {
		t.Fatal(err)
	}
	raw, err := r.e.ModuleRead(context.Background(), "lights", "lights", nil)
	if err != nil || !strings.Contains(string(raw.(json.RawMessage)), `"brightness": 10`) {
		t.Fatalf("read %s %v", raw, err)
	}
	r.apps.Core().Emit(sdk.Event{Type: "instance.started", Data: json.RawMessage(`{"id":"tv"}`)})
	waitFor(t, "the module to see the event", func() bool {
		st, err := r.m.State(context.Background())
		return err == nil && strings.Contains(string(st.(json.RawMessage)), `"apps_started": 1`)
	})

	// It is started again after it dies, as with a socket.
	r.m.mu.Lock()
	pid := r.m.proc.Process.Pid
	r.m.mu.Unlock()
	_ = syscall.Kill(pid, syscall.SIGKILL)
	waitFor(t, "the module to come back", func() bool {
		r.m.mu.Lock()
		again := r.m.proc.Process.Pid
		r.m.mu.Unlock()
		_, err := r.do(t, "lights.set", `{"id":"porch","brightness":2}`)
		return again != pid && err == nil
	})
}

// Closing hostd's end ends a module that reads until end of file, with no
// signal sent: that is what stdio adds.
func TestStdioModuleEndsWhenHostdLetsGo(t *testing.T) {
	py := python(t)
	script := filepath.Join(t.TempDir(), "m.py")
	body := "import sys\nfor line in sys.stdin:\n    pass\nsys.exit(0)\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(py, script)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	_ = (&stdioConn{r: out, w: in}).Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the module ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the module did not end when its input closed")
	}
}

func TestUnknownTransport(t *testing.T) {
	_, err := Load(context.Background(), Options{Name: "x", Command: []string{"true"}, SocketDir: t.TempDir(), Transport: "pipe"})
	if err == nil || !strings.Contains(err.Error(), `transport "pipe"`) {
		t.Fatalf("%v", err)
	}
}
