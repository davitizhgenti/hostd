package units

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/internal/systemd"
	"github.com/davitizhgenti/hostd/modules/apps/runner/units/unitstest"
	"github.com/davitizhgenti/hostd/modules/apps/spec"
	"github.com/davitizhgenti/hostd/sdk"
)

// execApp is an exec app as the catalog makes it (a window, not restarted).
func execApp(id string, argv ...string) *spec.App {
	return &spec.App{ID: id, Name: id, Surface: spec.SurfaceWindow, Restart: "never",
		Runner: spec.Runner{Type: spec.RunnerExec, Command: argv}, Env: map[string]string{"LANG": "C.UTF-8"}}
}

func TestSessionEnv(t *testing.T) {
	dir := unitstest.FakeSession(t)
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

func TestExecRunnerStartStop(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: unitstest.FakeSession(t), HomeDir: "/home/screen"}
	ctx := context.Background()
	app := execApp("foot", "foot", "--fullscreen")

	inst, err := r.Start(ctx, spec.Instance{ID: "foot#2", App: "foot", Surface: spec.SurfaceWindow}, app)
	if err != nil {
		t.Fatal(err)
	}
	if inst.Unit != `hostd-foot\x232.service` || inst.PID == 0 {
		t.Fatalf("instance = %+v", inst)
	}
	spec := sd.Started[0]
	if !reflect.DeepEqual(spec.Argv, []string{"foot", "--fullscreen"}) || spec.Dir != "/home/screen" ||
		!slices.Contains(spec.Env, "WAYLAND_DISPLAY=wayland-1") || !slices.Contains(spec.Env, "LANG=C.UTF-8") ||
		spec.Description != "hostd instance foot#2 of app foot" {
		t.Fatalf("spec = %+v", spec)
	}
	if err := r.Stop(ctx, inst); err != nil {
		t.Fatal(err)
	}
	if len(sd.UnitNames()) != 0 {
		t.Fatalf("units left after stop: %v", sd.UnitNames())
	}
}

func TestExecRunnerNeedsASessionForWindows(t *testing.T) {
	r := &ExecRunner{Systemd: unitstest.NewFakeSystemd(), RuntimeDir: t.TempDir()}
	_, err := r.Start(context.Background(), spec.Instance{ID: "foot", Surface: spec.SurfaceWindow}, execApp("foot", "foot"))
	if sdk.CodeOf(err) != sdk.CodeModuleUnavailable || !strings.Contains(err.Error(), "Sway is not running") {
		t.Fatalf("err = %v", err)
	}
	// A background app needs no session.
	if _, err := r.Start(context.Background(), spec.Instance{ID: "job", Surface: spec.SurfaceBackground}, execApp("job", "job")); err != nil {
		t.Fatal(err)
	}
}

func TestExecRunnerClearsStaleUnitAndReportsExecFailure(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: unitstest.FakeSession(t)}
	ctx := context.Background()
	inst := spec.Instance{ID: "foot", App: "foot", Surface: spec.SurfaceWindow}
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatal(err)
	}
	sd.Exit(contract.UnitName("foot"), 0, 1) // ended while nobody watched: unit stays (RemainAfterExit)
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err != nil {
		t.Fatalf("stale unit blocked a new start: %v", err)
	}
	// A unit that is really running is never replaced.
	if _, err := r.Start(ctx, inst, execApp("foot", "foot")); err == nil {
		t.Fatal("started over a running unit")
	}
	// The binary does not exist: an error, and no unit left behind.
	_, err := r.Start(ctx, spec.Instance{ID: "nope", App: "nope", Surface: spec.SurfaceWindow}, execApp("nope", "missing"))
	if err == nil || !strings.Contains(err.Error(), "starting missing") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(sd.UnitNames(), contract.UnitName("nope")) {
		t.Fatal("failed unit left behind")
	}
}

func TestExecRunnerAdopt(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: unitstest.FakeSession(t)}
	ctx := context.Background()
	for _, id := range []string{"foot", "foot#2", "kodi"} {
		if _, err := r.Start(ctx, spec.Instance{ID: id, App: spec.AppOf(id), Surface: spec.SurfaceWindow}, execApp(spec.AppOf(id), spec.AppOf(id))); err != nil {
			t.Fatal(err)
		}
	}
	sd.Exit(contract.UnitName("kodi"), 3, 1) // ended with status 3 while hostd was away
	_ = sd.StartTransient(ctx, "hostd-check.service", systemd.UnitSpec{Description: "[systemd-run] /usr/bin/foot", Argv: []string{"foot"}})

	got, err := r.Adopt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]spec.Instance{}
	for _, in := range got {
		byID[in.ID] = in
	}
	if len(got) != 3 || byID["foot#2"].State != spec.StateRunning || byID["foot#2"].App != "foot" || byID["foot#2"].PID == 0 {
		t.Fatalf("adopted %+v", got)
	}
	if k := byID["kodi"]; k.State != spec.StateFailed || k.ExitCode == nil || *k.ExitCode != 3 {
		t.Fatalf("kodi = %+v", k)
	}
	if slices.Contains(sd.UnitNames(), contract.UnitName("kodi")) || !slices.Contains(sd.UnitNames(), "hostd-check.service") {
		t.Fatalf("units after adopt: %v (kodi cleaned up, foreign unit untouched)", sd.UnitNames())
	}
}

func TestExecRunnerWatch(t *testing.T) {
	sd := unitstest.NewFakeSystemd()
	r := &ExecRunner{Systemd: sd, RuntimeDir: unitstest.FakeSession(t)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Ended, 10)
	go func() { _ = r.Watch(ctx, func(e Ended) { got <- e }) }()
	waitUntil(t, func() bool { return sd.Watching() == 1 })

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
		if _, err := r.Start(ctx, spec.Instance{ID: tc.id, App: tc.id, Surface: spec.SurfaceBackground}, execApp(tc.id, tc.id)); err != nil {
			t.Fatal(err)
		}
		sd.Exit(contract.UnitName(tc.id), tc.status, tc.code)
		e := <-got
		if e.Instance != tc.id || e.ExitCode != tc.want || e.Reason != tc.reason {
			t.Errorf("%s: %+v", tc.id, e)
		}
	}
	// hostd's own Stop is not reported as an exit.
	inst, _ := r.Start(ctx, spec.Instance{ID: "d", App: "d", Surface: spec.SurfaceBackground}, execApp("d", "d"))
	_ = r.Stop(ctx, inst)
	select {
	case e := <-got:
		t.Fatalf("stop reported as an exit: %+v", e)
	default:
	}
	if len(sd.UnitNames()) != 0 {
		t.Fatalf("ended units not cleaned up: %v", sd.UnitNames())
	}
}

// --- docker runner ---------------------------------------------------------
