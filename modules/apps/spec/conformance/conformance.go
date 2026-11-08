// Package conformance is the test every runner (spec.Backend) must pass. It
// states what the apps module relies on a runner for, once, so a new runner
// shows it is right by passing it and nothing else is needed:
//
//	func TestMyRunner(t *testing.T) {
//		conformance.Run(t, func(t *testing.T) conformance.Harness { ... })
//	}
//
// The suite drives the runner through its Backend interface; the Harness
// supplies what only the runner's fake knows: how a process ends.
package conformance

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/modules/apps/spec"
)

// Harness is a runner and its machine, made for one test.
type Harness struct {
	// Backend is the runner.
	Backend spec.Backend
	// App returns an app this runner can start, by ID.
	App func(id string) *spec.App
	// Exit makes the instance's process end by itself with this exit code,
	// and the runner's watch is told.
	Exit func(inst spec.Instance, code int)
	// Gone makes the process end while nobody watches: the event is lost,
	// as when hostd is down.
	Gone func(inst spec.Instance, code int)
	// Watching is how many watches are running now.
	Watching func() int
	// Restart is hostd starting again: a new runner over the same machine.
	Restart func() spec.Backend
}

// Run runs every check against a runner made by newHarness.
func Run(t *testing.T, newHarness func(t *testing.T) Harness) {
	t.Helper()
	for name, check := range map[string]func(*testing.T, Harness){
		"a started instance is running and can be found again": startIsRunning,
		"instances of one app are told apart":                  instancesAreTold,
		"stop ends an instance and leaves nothing behind":      stopEnds,
		"stopping what is already gone does not hang":          stopTwice,
		"watch reports an instance that ends by itself":        watchSelfExit,
		"watch reports each end once, with its exit code":      watchExitCodes,
		"watch ends with its context":                          watchEnds,
		"adopt finds what runs and what ended while away":      adoptAfterRestart,
		"adopt does not return an instance that was stopped":   adoptSkipsStopped,
	} {
		t.Run(name, func(t *testing.T) { check(t, newHarness(t)) })
	}
}

func instance(id string) spec.Instance {
	return spec.Instance{ID: id, App: spec.AppOf(id), Name: id, Surface: spec.SurfaceBackground, State: spec.StateStarting}
}

func start(t *testing.T, h Harness, id string) spec.Instance {
	t.Helper()
	inst, err := h.Backend.Start(context.Background(), instance(id), h.App(spec.AppOf(id)))
	if err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	return inst
}

func wait(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// watch starts a watch and collects what it reports.
func watch(t *testing.T, h Harness) <-chan spec.Ended {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ended := make(chan spec.Ended, 16)
	go func() { _ = h.Backend.Watch(ctx, func(e spec.Ended) { ended <- e }) }()
	wait(t, "the watch to run", func() bool { return h.Watching() == 1 })
	return ended
}

func adopt(t *testing.T, b spec.Backend) map[string]spec.Instance {
	t.Helper()
	got, err := b.Adopt(context.Background())
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	out := map[string]spec.Instance{}
	for _, in := range got {
		out[in.ID] = in
	}
	return out
}

func startIsRunning(t *testing.T, h Harness) {
	inst := start(t, h, "web")
	if inst.ID != "web" || inst.App != "web" {
		t.Errorf("the instance changed its identity: %+v", inst)
	}
	if inst.Unit == "" && inst.Container == "" {
		t.Errorf("nothing says where it runs (Unit or Container): %+v", inst)
	}
	if inst.State.Ended() {
		t.Errorf("state %q right after start", inst.State)
	}
	if got := adopt(t, h.Restart()); got["web"].State != spec.StateRunning {
		t.Errorf("after hostd restarts it is %+v, want running", got["web"])
	}
}

func instancesAreTold(t *testing.T, h Harness) {
	a, b := start(t, h, "web"), start(t, h, "web#2")
	if a.Unit == b.Unit && a.Container == b.Container {
		t.Errorf("two instances share one process: %+v %+v", a, b)
	}
	got := adopt(t, h.Restart())
	if len(got) != 2 || got["web"].App != "web" || got["web#2"].App != "web" {
		t.Errorf("adopted %+v", got)
	}
}

func stopEnds(t *testing.T, h Harness) {
	a, b := start(t, h, "web"), start(t, h, "db")
	if err := h.Backend.Stop(context.Background(), a); err != nil {
		t.Fatalf("stop: %v", err)
	}
	got := adopt(t, h.Restart())
	if _, found := got["web"]; found {
		t.Errorf("a stopped instance is still found: %+v", got["web"])
	}
	if got["db"].State != spec.StateRunning {
		t.Errorf("stopping one stopped another: %+v", got["db"])
	}
	_ = b
}

func stopTwice(t *testing.T, h Harness) {
	inst := start(t, h, "web")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.Backend.Stop(context.Background(), inst)
		_ = h.Backend.Stop(context.Background(), inst) // an error is fine, a hang is not
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping twice hangs")
	}
}

func watchSelfExit(t *testing.T, h Harness) {
	ended := watch(t, h)
	inst := start(t, h, "web")
	h.Exit(inst, 0)
	select {
	case e := <-ended:
		if e.Instance != "web" || e.ExitCode != 0 {
			t.Errorf("ended = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the end was not reported")
	}
}

func watchExitCodes(t *testing.T, h Harness) {
	ended := watch(t, h)
	a, b := start(t, h, "web"), start(t, h, "db")
	h.Exit(a, 3)
	h.Exit(b, 0)
	got := map[string]int{}
	for range 2 {
		select {
		case e := <-ended:
			if _, again := got[e.Instance]; again {
				t.Errorf("%s reported twice", e.Instance)
			}
			got[e.Instance] = e.ExitCode
		case <-time.After(5 * time.Second):
			t.Fatalf("reported %v, want two", got)
		}
	}
	if got["web"] != 3 || got["db"] != 0 {
		t.Errorf("exit codes = %v", got)
	}
	select {
	case e := <-ended:
		t.Errorf("an extra report: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func watchEnds(t *testing.T, h Harness) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = h.Backend.Watch(ctx, func(spec.Ended) {}) }()
	wait(t, "the watch to run", func() bool { return h.Watching() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch does not return when its context ends")
	}
}

func adoptAfterRestart(t *testing.T, h Harness) {
	up, away := start(t, h, "web"), start(t, h, "db")
	_ = up
	h.Gone(away, 3) // ended while hostd was away
	got := adopt(t, h.Restart())
	if got["web"].State != spec.StateRunning {
		t.Errorf("web = %+v, want running", got["web"])
	}
	db, ok := got["db"]
	if !ok || !db.State.Ended() || db.ExitCode == nil || *db.ExitCode != 3 {
		t.Errorf("db = %+v, want ended with exit code 3", db)
	}
	// Reported once: a second adopt does not find it again.
	if again := adopt(t, h.Restart()); slices.Contains(keys(again), "db") {
		t.Errorf("adopted an ended instance twice: %+v", again["db"])
	}
}

func adoptSkipsStopped(t *testing.T, h Harness) {
	inst := start(t, h, "web")
	if err := h.Backend.Stop(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if got := adopt(t, h.Restart()); len(got) != 0 {
		t.Errorf("adopted %+v after the only instance was stopped", got)
	}
}

func keys(m map[string]spec.Instance) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
