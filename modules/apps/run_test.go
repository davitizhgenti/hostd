package apps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/modules/apps/runner/podman/podmantest"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/sdk"
)

// submit sends an action with these arguments, as auth.
func (r *rig) submit(typ string, args any, auth core.Auth) (map[string]any, error) {
	res, err := r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: sdk.MustJSON(args),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, auth)
	if err != nil {
		return nil, err
	}
	var data map[string]any
	_ = json.Unmarshal(res.Data, &data)
	return data, nil
}

var runner = core.Auth{Name: "laptop", Scopes: []string{contract.ScopeRun}}

func TestRunACommand(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	waitUntil(t, func() bool { return sd.Watching() == 1 })

	// On the screen, full screen by default.
	got, err := r.submit(ActionRun, map[string]any{"command": []string{"imv", "/tmp/plot.png"}}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got["instance"] != "run" || got["state"] != string(StateRunning) {
		t.Fatalf("result %v", got)
	}
	r.next(t)
	r.next(t)
	in := r.m.readInstances(nil)[0]
	if in.App != "run" || in.Name != "imv" || in.Surface != SurfaceWindow || !in.Fullscreen {
		t.Fatalf("instance %+v", in)
	}
	unit := sd.Units[contract.UnitName("run")]
	if !reflect.DeepEqual(unit.Spec.Argv, []string{"imv", "/tmp/plot.png"}) || unit.Spec.Service {
		t.Fatalf("unit %+v", unit.Spec)
	}

	// Another one is a new instance, never the first one focused.
	got, _ = r.submit(ActionRun, map[string]any{"command": []string{"imv", "b.png"}, "fullscreen": false}, runner)
	if got["instance"] != "run#2" {
		t.Fatalf("second: %v", got)
	}
	r.next(t)
	r.next(t)

	// In the background, named, in a directory, with its environment: it
	// ends on its own, and its exit status is reported.
	got, err = r.submit(ActionRun, map[string]any{"command": []string{"pytest", "-q"}, "name": "tests", "background": true,
		"dir": "/home/screen/proj", "env": map[string]string{"CI": "1"}}, runner)
	if err != nil || got["instance"] != "run-tests" {
		t.Fatalf("background: %v %v", got, err)
	}
	r.next(t)
	r.next(t)
	unit = sd.Units[contract.UnitName("run-tests")]
	if !unit.Spec.Service || unit.Spec.Dir != "/home/screen/proj" || !strings.Contains(strings.Join(unit.Spec.Env, " "), "CI=1") {
		t.Fatalf("background unit %+v", unit.Spec)
	}
	if unit.Spec.Restart != "" {
		t.Fatalf("a command that ends must not be restarted: %q", unit.Spec.Restart)
	}
	sd.Exit(contract.UnitName("run-tests"), 1, 1)
	ev := r.next(t)
	var ended Instance
	_ = json.Unmarshal(ev.Data, &ended)
	if ev.Type != EventFailed || ended.ID != "run-tests" || ended.ExitCode == nil || *ended.ExitCode != 1 {
		t.Fatalf("end: %s %s", ev.Type, ev.Data)
	}
	if _, err := r.do(t, contract.ActionInstanceStop, "run"); err != nil {
		t.Fatal(err)
	}
}

func TestRunNeedsItsScope(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	apps := core.Auth{Name: "phone", Scopes: []string{contract.ScopeApps}}
	for _, try := range []struct {
		typ  string
		args any
	}{
		{ActionRun, map[string]any{"command": []string{"sh", "-c", "id"}}},
		{ActionDefine, map[string]any{"id": "x", "file": "runner = { type = \"exec\", command = [\"sh\"] }"}},
		{ActionUndefine, map[string]any{"id": "term"}},
	} {
		if _, err := r.submit(try.typ, try.args, apps); sdk.CodeOf(err) != sdk.CodeForbidden {
			t.Errorf("%s with scope apps: %v", try.typ, err)
		}
	}
	// front needs its own scope here too.
	if _, err := r.submit(ActionRun, map[string]any{"command": []string{"foot"}, "front": true}, runner); sdk.CodeOf(err) != sdk.CodeForbidden {
		t.Errorf("front without display.front: %v", err)
	}
}

func TestRunRefusesBadArguments(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	for name, args := range map[string]map[string]any{
		"no command":    {"command": []string{}},
		"empty program": {"command": []string{""}},
		"relative dir":  {"command": []string{"ls"}, "dir": "proj"},
		"bad variable":  {"command": []string{"ls"}, "env": map[string]string{"A B": "1"}},
		"bad name":      {"command": []string{"ls"}, "name": "../x"},
		"unknown arg":   {"command": []string{"ls"}, "shell": true},
	} {
		if _, err := r.submit(ActionRun, args, runner); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDefineAndUndefineAnApp(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	file := "name = \"Results\"\nrunner = { type = \"url\", url = \"http://192.168.18.60:8000/\" }\n"
	got, err := r.submit(ActionDefine, map[string]any{"id": "results", "file": file}, runner)
	if err != nil || got["replaced"] != false {
		t.Fatalf("define: %v %v", got, err)
	}
	path := filepath.Join(r.m.opts.AppsDir, "results.toml")
	if b, _ := os.ReadFile(path); string(b) != file {
		t.Fatalf("file %q", b)
	}
	if a, ok := r.m.catalog().Get("results"); !ok || a.Name != "Results" || a.Runner.URL != "http://192.168.18.60:8000/" {
		t.Fatalf("not in the catalog at once: %+v", a)
	}
	got, _ = r.submit(ActionDefine, map[string]any{"id": "results", "file": strings.Replace(file, "Results", "Test results", 1)}, runner)
	if got["replaced"] != true {
		t.Fatalf("replace: %v", got)
	}
	if a, _ := r.m.catalog().Get("results"); a.Name != "Test results" {
		t.Fatalf("replaced app %+v", a)
	}

	// A file with a problem is refused, and the one in place stays.
	for name, bad := range map[string]map[string]any{
		"syntax":       {"id": "results", "file": "name = "},
		"typo":         {"id": "results", "file": "nme = \"x\"\nrunner = { type = \"exec\", command = [\"x\"] }"},
		"no runner":    {"id": "results", "file": "name = \"x\""},
		"other id":     {"id": "results", "file": "id = \"other\"\nrunner = { type = \"exec\", command = [\"x\"] }"},
		"bad id":       {"id": "../etc/x", "file": "runner = { type = \"exec\", command = [\"x\"] }"},
		"reserved id":  {"id": "run-x", "file": "runner = { type = \"exec\", command = [\"x\"] }"},
		"missing need": {"id": "results", "file": "requires = [\"ghost\"]\nrunner = { type = \"exec\", command = [\"x\"] }"},
	} {
		if _, err := r.submit(ActionDefine, bad, runner); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
	if a, _ := r.m.catalog().Get("results"); a == nil || a.Name != "Test results" {
		t.Fatalf("a refused file changed the app: %+v", a)
	}
	entries, _ := os.ReadDir(r.m.opts.AppsDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("left a temporary file behind: %s", e.Name())
		}
	}

	if _, err := r.submit(ActionUndefine, map[string]any{"id": "results"}, runner); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.m.catalog().Get("results"); ok {
		t.Fatal("still in the catalog")
	}
	if _, err := r.submit(ActionUndefine, map[string]any{"id": "results"}, runner); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("again: %v", err)
	}
}

// A followed repository owns the app files: they change there.
func TestDefineRefusesAFollowedConfig(t *testing.T) {
	r := newRig(t, unitstest.NewFakeSystemd(), podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "apps")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	r.m.opts.AppsDir = link
	_, err := r.submit(ActionDefine, map[string]any{"id": "x", "file": "runner = { type = \"exec\", command = [\"x\"] }"}, runner)
	if sdk.CodeOf(err) != sdk.CodePreconditionFailed {
		t.Fatalf("err %v", err)
	}
}

func TestStartWithArguments(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := newRig(t, sd, podmantest.NewFakeEngine(), unitstest.FakeSession(t))
	waitUntil(t, func() bool { return sd.Watching() == 1 })
	file := "name = \"Show\"\nrunner = { type = \"exec\", command = [\"imv\", \"-f\", \"{file}\"] }\nargs = { file = \"path\" }\n"
	if _, err := r.submit(ActionDefine, map[string]any{"id": "show", "file": file}, runner); err != nil {
		t.Fatal(err)
	}
	apps := core.Auth{Name: "phone", Scopes: []string{contract.ScopeApps}} // checked values: no run scope needed
	start := func(args map[string]string) (map[string]any, error) {
		return r.submit(contract.ActionAppStart, map[string]any{"id": "show", "args": args}, apps)
	}
	if _, err := start(map[string]string{"file": "/home/screen/plot.png"}); err != nil {
		t.Fatal(err)
	}
	r.next(t)
	r.next(t)
	if argv := sd.Units[contract.UnitName("show")].Spec.Argv; !reflect.DeepEqual(argv, []string{"imv", "-f", "/home/screen/plot.png"}) {
		t.Fatalf("argv %q", argv)
	}
	// New values replace what is on the screen.
	if _, err := start(map[string]string{"file": "/home/screen/next.png"}); err != nil {
		t.Fatal(err)
	}
	for ev := r.next(t); ev.Type != EventStarted; ev = r.next(t) {
	}
	if live := r.live(); !reflect.DeepEqual(live, []string{"show"}) {
		t.Fatalf("live %v", live)
	}
	if argv := sd.Units[contract.UnitName("show")].Spec.Argv; argv[2] != "/home/screen/next.png" {
		t.Fatalf("argv %q", argv)
	}

	for name, args := range map[string]map[string]string{
		"missing":       {},
		"unknown":       {"file": "/a", "url": "http://x"},
		"relative path": {"file": "plot.png"},
		"an option":     {"file": "--help"},
		"a newline":     {"file": "/a\n/b"},
	} {
		if _, err := start(args); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.submit(contract.ActionAppStart, map[string]any{"id": "term", "args": map[string]string{"x": "1"}}, apps); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Errorf("an app without arguments: %v", err)
	}
}

func TestArgumentKinds(t *testing.T) {
	for _, c := range []struct {
		kind, value string
		ok          bool
	}{
		{"url", "http://192.168.18.60:8000/plot.png", true},
		{"url", "https://example.org/", true},
		{"url", "file:///etc/shadow", false},
		{"url", "javascript:alert(1)", false},
		{"url", "http://", false},
		{"path", "/home/screen/a b.png", true},
		{"path", "a.png", false},
		{"text", "Tests: 41 passed", true},
		{"text", "-rf", false},
		{"text", "", false},
	} {
		if err := checkArg(c.kind, c.value); (err == nil) != c.ok {
			t.Errorf("%s %q: %v", c.kind, c.value, err)
		}
	}
}
