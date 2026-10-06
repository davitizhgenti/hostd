package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

var t0 = time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)

// resModule is a module owning "res.*" whose actions set integer values on
// named resources, shaped like a real module's volume or focus actions.
type resModule struct {
	*testutil.Module
	mu     sync.Mutex
	values map[string]int
	gone   map[string]bool // keys Validate reports as not running
}

const setSchema = `{"type":"object","properties":{"key":{"type":"string"},"value":{"type":"integer"},"front":{"type":"boolean"}},"required":["key"]}`

func newResModule() *resModule {
	m := &resModule{values: map[string]int{}, gone: map[string]bool{}}
	m.Module = &testutil.Module{M: sdk.Manifest{
		Name: "res", Version: "test", Owns: []string{"res.*"},
		Scopes: []sdk.ScopeSpec{{Name: "res"}, {Name: "res.front"}},
		Actions: []sdk.ActionSpec{
			{Type: "res.set", Schema: json.RawMessage(setSchema), Keys: []sdk.KeyTemplate{"res:{key}"},
				Scope: "res", ArgScopes: map[string]string{"front": "res.front"}},
			{Type: "res.swap", Schema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"]}`),
				Keys: []sdk.KeyTemplate{"res:{a}", "res:{b}"}, Scope: "res"},
			{Type: "res.ping", Scope: "res"},
			{Type: "res.slow", Schema: json.RawMessage(setSchema), Keys: []sdk.KeyTemplate{"res:{key}"}, Scope: "res", Slow: true},
			{Type: "res.quick", Schema: json.RawMessage(setSchema), Keys: []sdk.KeyTemplate{"res:{key}"}, Scope: "res",
				Timeout: sdk.Duration(time.Second)},
		},
		Events: []sdk.EventSpec{{Type: "res.changed"}, {Type: "res.note"}},
	}}
	m.ValidateFunc = func(_ context.Context, a sdk.Action) error {
		var args struct{ Key string }
		_ = a.DecodeArgs(&args)
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.gone[args.Key] {
			return sdk.Errorf(sdk.CodeInstanceNotRunning, "%s is not running", args.Key)
		}
		return nil
	}
	m.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args struct {
			Key   string
			Value int
		}
		_ = a.DecodeArgs(&args)
		m.mu.Lock()
		m.values[args.Key] = args.Value
		m.mu.Unlock()
		return sdk.Result{}, nil
	}
	return m
}

func (m *resModule) value(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[key]
}

type harness struct {
	e     *Engine
	clock *clock.Fake
	audit *MemoryAudit
	res   *resModule
}

func newHarness(t *testing.T, opts Options, extra ...sdk.Module) *harness {
	t.Helper()
	h := &harness{clock: clock.NewFake(t0), audit: NewMemoryAudit(1000), res: newResModule()}
	opts.Clock, opts.Audit = h.clock, h.audit
	reg := NewRegistry()
	if err := reg.Add(h.res); err != nil {
		t.Fatal(err)
	}
	for _, m := range extra {
		if err := reg.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	h.e = New(reg, opts)
	if err := h.e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.e.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return h
}

var admin = Auth{Name: "test", Scopes: []string{sdk.ScopeAdmin}}

func act(typ string, args string, kind sdk.SourceKind) sdk.Action {
	a := sdk.Action{Type: typ, Source: sdk.Source{Kind: kind, Name: "test"}}
	if args != "" {
		a.Args = json.RawMessage(args)
	}
	return a
}

func set(key string, value int, kind sdk.SourceKind) sdk.Action {
	return act("res.set", fmt.Sprintf(`{"key":%q,"value":%d}`, key, value), kind)
}

func (h *harness) submit(t *testing.T, a sdk.Action) (sdk.Result, error) {
	t.Helper()
	return h.e.Submit(context.Background(), a, admin)
}

func wantCode(t *testing.T, err error, code sdk.Code) {
	t.Helper()
	if sdk.CodeOf(err) != code {
		t.Fatalf("err = %v, want code %s", err, code)
	}
	var se *sdk.Error
	if errors.As(err, &se) && se.Action == "" {
		t.Fatalf("error has no action ID: %#v", se)
	}
}

func TestSubmitAppliesAndPublishes(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.e.Subscribe(ctx, "action.*")

	res, err := h.submit(t, set("vol", 40, sdk.SourceManual))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != sdk.StatusApplied || res.Version != 1 || !strings.HasPrefix(res.Action, "act_") {
		t.Fatalf("result = %+v", res)
	}
	if h.res.value("vol") != 40 {
		t.Fatal("handler did not run")
	}
	ev := <-events
	if ev.Type != EventActionDone || ev.Action != res.Action || ev.Source.Kind != sdk.SourceManual {
		t.Fatalf("event = %+v", ev)
	}
	entries := h.audit.Entries()
	if len(entries) != 1 || entries[0].Status != "applied" || entries[0].Version != 1 || entries[0].Resource != "res:vol" {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestEarlyRefusals(t *testing.T) {
	h := newHarness(t, Options{})
	limited := Auth{Name: "phone", Scopes: []string{sdk.ScopeRead, "res"}}
	for _, tc := range []struct {
		name string
		a    sdk.Action
		auth Auth
		code sdk.Code
	}{
		{"unknown type", act("res.nope", "", sdk.SourceManual), admin, sdk.CodeNotFound},
		{"unowned type", act("audio.volume.set", "", sdk.SourceManual), admin, sdk.CodeNotFound},
		{"bad args", act("res.set", `{"key":"x","value":"high"}`, sdk.SourceManual), admin, sdk.CodeInvalidArgs},
		{"unknown arg", act("res.set", `{"key":"x","vlaue":1}`, sdk.SourceManual), admin, sdk.CodeInvalidArgs},
		{"external source", set("x", 1, sdk.SourceExternal), admin, sdk.CodeInvalidArgs},
		{"no source", set("x", 1, ""), admin, sdk.CodeInvalidArgs},
		{"missing scope", set("x", 1, sdk.SourceManual), Auth{Scopes: []string{sdk.ScopeRead}}, sdk.CodeForbidden},
		{"arg scope", act("res.set", `{"key":"x","front":true}`, sdk.SourceManual), limited, sdk.CodeForbidden},
		{"expect_version on two keys", func() sdk.Action {
			a := act("res.swap", `{"a":"x","b":"y"}`, sdk.SourceManual)
			v := uint64(0)
			a.ExpectVersion = &v
			return a
		}(), admin, sdk.CodeInvalidArgs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.e.Submit(context.Background(), tc.a, tc.auth)
			wantCode(t, err, tc.code)
		})
	}
	// Allowed: the arg scope only applies when the argument is true.
	if _, err := h.e.Submit(context.Background(), act("res.set", `{"key":"x","front":false}`, sdk.SourceManual), limited); err != nil {
		t.Fatalf("front=false needs no extra scope: %v", err)
	}
	// Every refusal is in the audit trail.
	failed := 0
	for _, e := range h.audit.Entries() {
		if e.Status == StatusFailed {
			failed++
		}
	}
	if failed != 9 {
		t.Fatalf("%d failed entries in audit, want 9", failed)
	}
}

func TestOrderingOnOneKey(t *testing.T) {
	h := newHarness(t, Options{})
	var mu sync.Mutex
	var order []int
	gate := make(chan struct{})
	first := true
	h.res.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args struct{ Value int }
		_ = a.DecodeArgs(&args)
		mu.Lock()
		blockFirst := first
		first = false
		order = append(order, args.Value)
		mu.Unlock()
		if blockFirst {
			<-gate
		}
		return sdk.Result{}, nil
	}

	const n = 1000
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = h.submit(t, set("k", -1, sdk.SourceManual)) }()
	waitFor(t, "first action to hold the key", func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 1 })
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = h.submit(t, set("k", i, sdk.SourceManual)) }()
		waitFor(t, "action to queue", func() bool { return h.e.locks.waiting("res:k") == i+1 })
	}
	close(gate)
	wg.Wait()
	for i := 1; i <= n; i++ {
		if order[i] != i-1 {
			t.Fatalf("position %d ran action %d; not in arrival order", i, order[i])
		}
	}
	if v := h.e.Version("res:k"); v != n+1 {
		t.Fatalf("version = %d, want %d", v, n+1)
	}
}

func TestDifferentKeysRunInParallel(t *testing.T) {
	h := newHarness(t, Options{})
	var started sync.WaitGroup
	started.Add(2)
	release := make(chan struct{})
	h.res.HandleFunc = func(context.Context, sdk.Action) (sdk.Result, error) {
		started.Done()
		<-release // both must be inside Handle at once to get past this test
		return sdk.Result{}, nil
	}
	var wg sync.WaitGroup
	for _, k := range []string{"a", "b"} {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = h.submit(t, set(k, 1, sdk.SourceManual)) }()
	}
	both := make(chan struct{})
	go func() { started.Wait(); close(both) }()
	select {
	case <-both:
	case <-time.After(5 * time.Second):
		t.Fatal("actions on different keys did not run concurrently")
	}
	close(release)
	wg.Wait()
}

func TestNoDeadlockRandomMultiKeyActions(t *testing.T) {
	h := newHarness(t, Options{})
	keys := []string{"k1", "k2", "k3", "k4", "k5"}
	var wg sync.WaitGroup
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				a, b := keys[(w+i)%5], keys[(w*3+i*7+1)%5]
				if _, err := h.submit(t, act("res.swap", fmt.Sprintf(`{"a":%q,"b":%q}`, a, b), sdk.SourceManual)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock among multi-key actions")
	}
	if h.e.locks.size() != 0 {
		t.Fatal("keys still held after all actions finished")
	}
}

func TestLateValidation(t *testing.T) {
	h := newHarness(t, Options{})
	gate := make(chan struct{})
	h.res.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args struct{ Value int }
		_ = a.DecodeArgs(&args)
		if args.Value == 0 { // the "stop": blocks, then marks x gone
			<-gate
			h.res.mu.Lock()
			h.res.gone["x"] = true
			h.res.mu.Unlock()
		}
		return sdk.Result{}, nil
	}
	stopped := make(chan error, 1)
	go func() { _, err := h.submit(t, set("x", 0, sdk.SourceManual)); stopped <- err }()
	waitFor(t, "stop to run", func() bool { return h.e.isRunningAny() })
	queued := make(chan error, 1)
	go func() { _, err := h.submit(t, set("x", 5, sdk.SourceManual)); queued <- err }()
	waitFor(t, "second action to queue", func() bool { return h.e.locks.waiting("res:x") == 1 })
	close(gate)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	// Valid when sent, but the target was gone when its turn came.
	wantCode(t, <-queued, sdk.CodeInstanceNotRunning)
}

func (e *Engine) isRunningAny() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running) > 0
}

func TestVersionsAndExpectVersion(t *testing.T) {
	h := newHarness(t, Options{})
	if _, err := h.submit(t, set("vol", 30, sdk.SourceManual)); err != nil {
		t.Fatal(err)
	}
	v := h.e.Version("res:vol")
	if v != 1 {
		t.Fatalf("version = %d", v)
	}

	fresh := set("vol", 35, sdk.SourceManual)
	fresh.ExpectVersion = &v
	res, err := h.submit(t, fresh)
	if err != nil || res.Version != 2 {
		t.Fatalf("fresh expect_version: %+v %v", res, err)
	}

	stale := set("vol", 99, sdk.SourceManual)
	stale.ExpectVersion = &v // still 1, resource is at 2
	_, err = h.submit(t, stale)
	wantCode(t, err, sdk.CodePreconditionFailed)
	if !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("error should tell the current version: %v", err)
	}
	if h.res.value("vol") != 35 {
		t.Fatal("stale action ran")
	}

	// An outside change bumps the version too.
	h.res.Core().Emit(sdk.Event{Type: "res.changed", Resource: "res:vol", Data: json.RawMessage(`{"value":55}`)})
	if v := h.e.Version("res:vol"); v != 3 {
		t.Fatalf("version after observed change = %d, want 3", v)
	}
}

func TestHolds(t *testing.T) {
	kinds := []sdk.SourceKind{sdk.SourceLocal, sdk.SourceManual, sdk.SourceAutomation}
	for _, first := range kinds {
		for _, second := range kinds {
			for _, inside := range []bool{true, false} {
				name := fmt.Sprintf("%s then %s %s window", first, second, map[bool]string{true: "inside", false: "after"}[inside])
				t.Run(name, func(t *testing.T) {
					h := newHarness(t, Options{})
					if _, err := h.submit(t, set("vol", 1, first)); err != nil {
						t.Fatal(err)
					}
					if inside {
						h.clock.Advance(2*time.Minute + 59*time.Second)
					} else {
						h.clock.Advance(3 * time.Minute)
					}
					res, err := h.submit(t, set("vol", 2, second))
					if err != nil {
						t.Fatal(err)
					}
					wantSkip := inside && second.Priority() < first.Priority()
					if wantSkip {
						if res.Status != sdk.StatusSkipped || res.Reason != "held" || res.HeldBy != first || res.Until == nil ||
							!res.Until.Equal(t0.Add(3*time.Minute)) {
							t.Fatalf("want skipped, held by %s until %v; got %+v", first, t0.Add(3*time.Minute), res)
						}
						if h.res.value("vol") != 1 {
							t.Fatal("skipped action ran")
						}
					} else if res.Status != sdk.StatusApplied || h.res.value("vol") != 2 {
						t.Fatalf("want applied, got %+v", res)
					}
				})
			}
		}
	}
}

func TestHoldRenewsAtNewPriority(t *testing.T) {
	h := newHarness(t, Options{})
	mustApply := func(a sdk.Action) {
		t.Helper()
		res, err := h.submit(t, a)
		if err != nil || res.Status != sdk.StatusApplied {
			t.Fatalf("%+v %v", res, err)
		}
	}
	mustApply(set("vol", 1, sdk.SourceAutomation))
	mustApply(set("vol", 2, sdk.SourceManual)) // higher: applies, holds at manual
	h.clock.Advance(2 * time.Minute)
	mustApply(set("vol", 3, sdk.SourceManual)) // equal: applies, renews the window
	h.clock.Advance(2 * time.Minute)           // 4 min after the first manual, 2 after the renewal
	res, _ := h.submit(t, set("vol", 4, sdk.SourceAutomation))
	if res.Status != sdk.StatusSkipped {
		t.Fatalf("renewed hold did not apply: %+v", res)
	}
	h.clock.Advance(time.Minute)
	mustApply(set("vol", 5, sdk.SourceAutomation)) // renewal expired
}

func TestExternalChangesNeverHold(t *testing.T) {
	h := newHarness(t, Options{})
	h.res.Core().Emit(sdk.Event{Type: "res.changed", Resource: "res:vol"})
	res, err := h.submit(t, set("vol", 70, sdk.SourceAutomation))
	if err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("automation after an outside change: %+v %v", res, err)
	}
	var observed *AuditEntry
	for _, e := range h.audit.Entries() {
		if e.Status == "observed" {
			observed = &e
		}
	}
	if observed == nil || observed.Source.Kind != sdk.SourceExternal || observed.Version != 1 {
		t.Fatalf("observed audit entry = %+v", observed)
	}
}

func TestHoldWindowOverride(t *testing.T) {
	h := newHarness(t, Options{HoldWindows: map[string]time.Duration{"res:short": 10 * time.Second}})
	if _, err := h.submit(t, set("short", 1, sdk.SourceManual)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.submit(t, set("long", 1, sdk.SourceManual)); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(11 * time.Second)
	if res, _ := h.submit(t, set("short", 2, sdk.SourceAutomation)); res.Status != sdk.StatusApplied {
		t.Fatalf("short window not applied: %+v", res)
	}
	if res, _ := h.submit(t, set("long", 2, sdk.SourceAutomation)); res.Status != sdk.StatusSkipped {
		t.Fatalf("default window not applied: %+v", res)
	}
	if got := h.e.HeldKeys(); len(got) != 2 {
		t.Fatalf("HeldKeys = %v", got)
	}
}

// sceneModule applies a "scene": one action that sends child actions to
// res through Core.Do, like the automation module will.
func newSceneModule(handle func(ctx context.Context, core sdk.Core, a sdk.Action) (sdk.Result, error)) *testutil.Module {
	m := &testutil.Module{M: sdk.Manifest{
		Name: "scene", Version: "test", Owns: []string{"scene.*"},
		Actions: []sdk.ActionSpec{
			{Type: "scene.apply", Schema: json.RawMessage(`{"type":"object","properties":{"key":{"type":"string"}}}`),
				Keys: []sdk.KeyTemplate{"res:{key}"}, Scope: sdk.ScopeAdmin},
			{Type: "scene.free", Scope: sdk.ScopeAdmin},
		},
	}}
	m.HandleFunc = func(ctx context.Context, a sdk.Action) (sdk.Result, error) { return handle(ctx, m.Core(), a) }
	return m
}

func TestChildActionReusesParentKey(t *testing.T) {
	var child sdk.Result
	var childErr error
	scene := newSceneModule(func(ctx context.Context, core sdk.Core, a sdk.Action) (sdk.Result, error) {
		// Parent holds res:k; the child needs res:k too (like if_running
		// = restart stopping the instance it is starting).
		child, childErr = core.Do(ctx, set("k", 7, ""))
		return sdk.Result{}, childErr
	})
	h := newHarness(t, Options{}, scene)
	done := make(chan error, 1)
	go func() { _, err := h.submit(t, act("scene.apply", `{"key":"k"}`, sdk.SourceManual)); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: child could not reuse its parent's key")
	}
	if childErr != nil || child.Status != sdk.StatusApplied || h.res.value("k") != 7 {
		t.Fatalf("child = %+v %v", child, childErr)
	}
	// The child is linked to its parent and runs at the chain's origin.
	var parentID string
	for _, e := range h.audit.Entries() {
		if e.Type == "scene.apply" {
			parentID = e.Action
		}
	}
	for _, e := range h.audit.Entries() {
		if e.Type == "res.set" {
			if e.Parent != parentID || e.Source.Kind != sdk.SourceManual || e.Source.Module != "scene" {
				t.Fatalf("child audit = %+v (parent %s)", e, parentID)
			}
		}
	}
}

func TestChildWaitsOnlyForKeysItAdds(t *testing.T) {
	gate := make(chan struct{})
	scene := newSceneModule(func(ctx context.Context, core sdk.Core, a sdk.Action) (sdk.Result, error) {
		// Holds res:a; the child also needs res:b, which another action holds.
		return core.Do(ctx, act("res.swap", `{"a":"a","b":"b"}`, ""))
	})
	h := newHarness(t, Options{}, scene)
	h.res.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		if a.Type == "res.set" {
			<-gate // the action holding res:b
		}
		return sdk.Result{}, nil
	}
	go func() { _, _ = h.submit(t, set("b", 1, sdk.SourceManual)) }()
	waitFor(t, "res:b to be held", func() bool { return h.e.isRunningAny() })
	done := make(chan error, 1)
	go func() { _, err := h.submit(t, act("scene.apply", `{"key":"a"}`, sdk.SourceManual)); done <- err }()
	waitFor(t, "child to wait for res:b", func() bool { return h.e.locks.waiting("res:b") == 1 })
	if h.e.locks.waiting("res:a") != 0 {
		t.Fatal("child queued for res:a, which its parent holds")
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGrandchildReusesGrandparentKey(t *testing.T) {
	scene := newSceneModule(func(ctx context.Context, core sdk.Core, a sdk.Action) (sdk.Result, error) {
		if a.Type == "scene.apply" {
			return core.Do(ctx, act("scene.free", "", "")) // child holds nothing new
		}
		return core.Do(ctx, set("k", 9, "")) // grandchild needs the grandparent's key
	})
	h := newHarness(t, Options{}, scene)
	done := make(chan error, 1)
	go func() { _, err := h.submit(t, act("scene.apply", `{"key":"k"}`, sdk.SourceManual)); done <- err }()
	select {
	case err := <-done:
		if err != nil || h.res.value("k") != 9 {
			t.Fatalf("err=%v value=%d", err, h.res.value("k"))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock: grandchild could not reuse its grandparent's key")
	}
}

func TestUnrelatedActionWaitsForParent(t *testing.T) {
	gate := make(chan struct{})
	scene := newSceneModule(func(ctx context.Context, core sdk.Core, a sdk.Action) (sdk.Result, error) {
		<-gate
		return sdk.Result{}, nil
	})
	h := newHarness(t, Options{}, scene)
	go func() { _, _ = h.submit(t, act("scene.apply", `{"key":"k"}`, sdk.SourceManual)) }()
	waitFor(t, "scene to hold res:k", func() bool { return h.e.isRunningAny() })
	done := make(chan struct{})
	go func() { _, _ = h.submit(t, set("k", 1, sdk.SourceManual)); close(done) }()
	waitFor(t, "unrelated action to queue", func() bool { return h.e.locks.waiting("res:k") == 1 })
	close(gate)
	<-done
}

func TestEventTriggeredActionDoesNotReenter(t *testing.T) {
	gate := make(chan struct{})
	var evID string
	var mu sync.Mutex
	scene := newSceneModule(func(ctx context.Context, core sdk.Core, a sdk.Action) (sdk.Result, error) {
		<-gate
		return sdk.Result{}, nil
	})
	h := newHarness(t, Options{}, scene)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.e.Subscribe(ctx, "res.note")
	go func() { _, _ = h.submit(t, act("scene.apply", `{"key":"k"}`, sdk.SourceManual)) }()
	waitFor(t, "scene to hold res:k", func() bool { return h.e.isRunningAny() })
	h.res.Core().Emit(sdk.Event{Type: "res.note"})
	ev := <-events
	mu.Lock()
	evID = ev.ID
	mu.Unlock()

	// A rule reacting to that event, while the scene still holds res:k:
	// it has a cause, not a parent, so it queues like anyone else.
	reaction := set("k", 2, sdk.SourceAutomation)
	reaction.Cause = evID
	done := make(chan struct{})
	go func() { _, _ = h.submit(t, reaction); close(done) }()
	waitFor(t, "reaction to queue", func() bool { return h.e.locks.waiting("res:k") == 1 })
	close(gate)
	<-done
}

func TestCauseChainDepthLimit(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.e.Subscribe(ctx, "action.*")

	// Step 1 is the origin; each later step reacts to the action.done event
	// of the previous one, as a rule would. Five rule-triggered steps (2-6)
	// run; the sixth (step 7) is stopped.
	cause := ""
	for step := 1; step <= 7; step++ {
		a := set("k", step, sdk.SourceAutomation)
		a.Cause = cause
		_, err := h.submit(t, a)
		if step <= 6 {
			if err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
		} else {
			wantCode(t, err, sdk.CodeLoopDetected)
		}
		for ev := range events {
			if ev.Type == EventActionDone {
				cause = ev.ID
				break
			}
			if ev.Type == EventLoopDetected {
				if step != 7 {
					t.Fatalf("loop detected at step %d", step)
				}
				return
			}
		}
	}
	t.Fatal("no loop_detected event")
}

func TestTimeoutHoldsKeysUntilHandlerReturns(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.e.Subscribe(ctx, "action.*")

	gate := make(chan struct{})
	h.res.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		if a.Type == "res.quick" {
			<-gate
		}
		return sdk.Result{}, nil
	}
	errc := make(chan error, 1)
	go func() {
		_, err := h.submit(t, act("res.quick", `{"key":"k","value":1}`, sdk.SourceManual))
		errc <- err
	}()
	h.clock.BlockUntil(1) // the action's timer
	h.clock.Advance(time.Second)
	err := <-errc
	wantCode(t, err, sdk.CodeTimeout)

	// The handler is still running, so the key is still held.
	next := make(chan error, 1)
	go func() { _, err := h.submit(t, set("k", 2, sdk.SourceManual)); next <- err }()
	waitFor(t, "next action to queue behind the timed-out one", func() bool { return h.e.locks.waiting("res:k") == 1 })

	close(gate) // handler finishes: outcome published, key released
	if err := <-next; err != nil {
		t.Fatal(err)
	}
	var timedOutID string
	var se *sdk.Error
	if errors.As(err, &se) {
		timedOutID = se.Action
	}
	for ev := range events {
		if ev.Type == EventActionDone && ev.Action == timedOutID {
			return // late outcome arrived
		}
	}
}

func TestTimeoutThenCancelsStuckHandler(t *testing.T) {
	h := newHarness(t, Options{})
	cancelled := make(chan struct{})
	h.res.HandleFunc = func(ctx context.Context, a sdk.Action) (sdk.Result, error) {
		<-ctx.Done()
		close(cancelled)
		return sdk.Result{}, ctx.Err()
	}
	errc := make(chan error, 1)
	go func() { _, err := h.submit(t, act("res.quick", `{"key":"k"}`, sdk.SourceManual)); errc <- err }()
	h.clock.BlockUntil(1)
	h.clock.Advance(time.Second)
	wantCode(t, <-errc, sdk.CodeTimeout)
	h.clock.BlockUntil(1) // the hard deadline
	h.clock.Advance(time.Second)
	<-cancelled
	waitFor(t, "key release", func() bool { return h.e.locks.size() == 0 })
	if entries := h.audit.Entries(); entries[len(entries)-1].Status != StatusFailed {
		t.Fatalf("stuck action should end as failed: %+v", entries[len(entries)-1])
	}
}

func TestSlowActionAccepted(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.e.Subscribe(ctx, "action.done")
	res, err := h.submit(t, act("res.slow", `{"key":"k","value":3}`, sdk.SourceManual))
	if err != nil || res.Status != sdk.StatusAccepted {
		t.Fatalf("%+v %v", res, err)
	}
	ev := <-events
	if ev.Action != res.Action {
		t.Fatalf("done event for %s, want %s", ev.Action, res.Action)
	}
	var done sdk.Result
	_ = json.Unmarshal(ev.Data, &done)
	if done.Status != sdk.StatusApplied || h.res.value("k") != 3 {
		t.Fatalf("outcome = %+v", done)
	}
}

func TestHandlerErrorsAndPanics(t *testing.T) {
	h := newHarness(t, Options{})
	h.res.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		var args struct{ Value int }
		_ = a.DecodeArgs(&args)
		switch args.Value {
		case 1:
			return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "no output hdmi-9")
		case 2:
			return sdk.Result{}, errors.New("wpctl: exit status 1")
		default:
			panic("boom")
		}
	}
	_, err := h.submit(t, set("k", 1, sdk.SourceManual))
	wantCode(t, err, sdk.CodeNotFound)
	_, err = h.submit(t, set("k", 2, sdk.SourceManual))
	wantCode(t, err, sdk.CodeInternal)
	_, err = h.submit(t, set("k", 3, sdk.SourceManual))
	wantCode(t, err, sdk.CodeInternal)
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("panic not reported: %v", err)
	}
	if h.e.Version("res:k") != 0 {
		t.Fatal("failed actions bumped the version")
	}
	if len(h.e.HeldKeys()) != 0 {
		t.Fatal("failed actions created a hold")
	}
}

func TestModuleUnavailable(t *testing.T) {
	// Registered but never started (as display is before Sway appears in
	// a failed start, or after Stop).
	reg := NewRegistry()
	res := newResModule()
	if err := reg.Add(res); err != nil {
		t.Fatal(err)
	}
	if err := reg.Resolve(); err != nil {
		t.Fatal(err)
	}
	e := New(reg, Options{Clock: clock.NewFake(t0)})
	_, err := e.Submit(context.Background(), set("k", 1, sdk.SourceManual), admin)
	wantCode(t, err, sdk.CodeModuleUnavailable)
	_ = e.Stop(context.Background())
}

func TestShutdownRefusesNewActions(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Add(newResModule()); err != nil {
		t.Fatal(err)
	}
	e := New(reg, Options{Clock: clock.NewFake(t0)})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := e.Submit(context.Background(), set("k", 1, sdk.SourceManual), admin)
	wantCode(t, err, sdk.CodeModuleUnavailable)
}

func TestEmitRules(t *testing.T) {
	h := newHarness(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := h.e.Subscribe(ctx, "res.*")

	h.res.Core().Emit(sdk.Event{Type: "res.undeclared"}) // dropped
	h.res.Core().Emit(sdk.Event{Type: "res.note", Data: json.RawMessage(`{"n":1}`)})
	ev := <-events
	if ev.Type != "res.note" || ev.ID == "" || ev.Time.IsZero() {
		t.Fatalf("event = %+v", ev)
	}

	// An event with a resource, emitted while handling an action, is
	// published after the action with the version the action produced.
	h.res.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		h.res.Core().Emit(sdk.Event{Type: "res.changed", Action: a.ID, Resource: "res:k"})
		return sdk.Result{}, nil
	}
	res, err := h.submit(t, set("k", 1, sdk.SourceLocal))
	if err != nil {
		t.Fatal(err)
	}
	ev = <-events
	if ev.Type != "res.changed" || ev.Version != res.Version || ev.Action != res.Action || ev.Source.Kind != sdk.SourceLocal {
		t.Fatalf("changed event = %+v, result %+v", ev, res)
	}
}

func TestStopWaitsForRunningActions(t *testing.T) {
	reg := NewRegistry()
	res := newResModule()
	gate := make(chan struct{})
	res.HandleFunc = func(context.Context, sdk.Action) (sdk.Result, error) { <-gate; return sdk.Result{}, nil }
	if err := reg.Add(res); err != nil {
		t.Fatal(err)
	}
	e := New(reg, Options{Clock: clock.NewFake(t0)})
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.Submit(context.Background(), set("k", 1, sdk.SourceManual), admin); done <- err }()
	waitFor(t, "action to run", func() bool { return e.isRunningAny() })
	stopped := make(chan struct{})
	go func() { _ = e.Stop(context.Background()); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop returned while an action was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("running action failed during Stop: %v", err)
	}
	<-stopped
}

func TestModuleOwnAction(t *testing.T) {
	// A module acting on its own (not inside a handler) sends a trusted
	// root action, with source automation unless it says otherwise.
	h := newHarness(t, Options{})
	core := h.res.Core()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := core.Subscribe(ctx, "action.done")
	res, err := core.Do(context.Background(), set("k", 4, ""))
	if err != nil || res.Status != sdk.StatusApplied {
		t.Fatalf("%+v %v", res, err)
	}
	ev := <-events
	if ev.Source.Kind != sdk.SourceAutomation || ev.Source.Name != "res" || ev.Source.Module != "res" {
		t.Fatalf("source = %+v", ev.Source)
	}
	res, err = core.Do(context.Background(), set("k", 5, sdk.SourceLocal))
	if err != nil {
		t.Fatal(err)
	}
	if ev := <-events; ev.Source.Kind != sdk.SourceLocal {
		t.Fatalf("explicit source not kept: %+v", ev.Source)
	}
	if h.e.Registry() == nil || h.e.Limiter() == nil {
		t.Fatal("accessors returned nil")
	}
}

func TestChainMemoryIsBounded(t *testing.T) {
	h := newHarness(t, Options{ChainMemory: 3})
	for i := 0; i < 10; i++ {
		h.res.Core().Emit(sdk.Event{Type: "res.note"})
	}
	h.e.mu.Lock()
	n, order := len(h.e.eventDepth), len(h.e.eventsOrder)
	h.e.mu.Unlock()
	if n != 3 || order != 3 {
		t.Fatalf("remembered %d events (order %d), want 3", n, order)
	}
}
