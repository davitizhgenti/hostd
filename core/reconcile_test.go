package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/core/audit"
	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

// lampKinds is a module with a kind, lamp, and an action on a lamp that
// holds its key until the test lets it go.
type lampKinds struct {
	*testutil.Module
	mu      sync.Mutex
	applied []string
	fail    bool
}

func (l *lampKinds) Apply(_ context.Context, r sdk.Resource) (sdk.ResourceStatus, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail {
		return sdk.ResourceStatus{}, errors.New("not a lamp")
	}
	l.applied = append(l.applied, "apply "+r.Name)
	return sdk.ResourceStatus{}, nil
}

func (l *lampKinds) Delete(_ context.Context, _, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applied = append(l.applied, "delete "+name)
	return nil
}

func (l *lampKinds) log() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.applied...)
}

func TestResourcesGoThroughLocksVersionsAndAudit(t *testing.T) {
	ctx := context.Background()
	m := &lampKinds{Module: testutil.NewModule("lamps", nil, nil, nil)}
	m.M.Kinds = []sdk.KindSpec{{Kind: "lamp", Plural: "lamps", Schema: []byte(`{"type":"object"}`)}}
	m.M.Actions = []sdk.ActionSpec{{Type: "lamps.hold", Scope: sdk.ScopeAdmin, Keys: []sdk.KeyTemplate{"lamp:{name}"},
		Schema: []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)}}
	held, release := make(chan struct{}), make(chan struct{})
	m.HandleFunc = func(context.Context, sdk.Action) (sdk.Result, error) {
		close(held)
		<-release
		m.mu.Lock()
		m.applied = append(m.applied, "action done")
		m.mu.Unlock()
		return sdk.Result{}, nil
	}
	reg := NewRegistry()
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	log := audit.NewMemory(100)
	e := New(reg, Options{Audit: log})
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(ctx)

	// An action on the lamp holds it: the file change waits for it.
	submitted := make(chan error, 1)
	go func() {
		_, err := e.Submit(ctx, sdk.Action{Type: "lamps.hold", Args: []byte(`{"name":"desk"}`), Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, Auth{Scopes: []string{sdk.ScopeAdmin}})
		submitted <- err
	}()
	select {
	case <-held:
	case err := <-submitted:
		t.Fatalf("the action did not run: %v", err)
	}
	done := make(chan error)
	go func() {
		_, err := e.Reconcile(ctx, sdk.Resource{Kind: "lamp", Name: "desk"})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("applied while an action held the lamp")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := m.log(); len(got) != 2 || got[0] != "action done" || got[1] != "apply desk" {
		t.Fatalf("order %q", got)
	}
	if v := e.Version("lamp:desk"); v != 2 { // the action's change, then the file's
		t.Fatalf("version %d", v)
	}
	if err := e.Unreconcile(ctx, "lamp", "desk"); err != nil || e.Version("lamp:desk") != 3 {
		t.Fatalf("delete: %v, version %d", err, e.Version("lamp:desk"))
	}
	m.mu.Lock()
	m.fail = true
	m.mu.Unlock()
	if _, err := e.Reconcile(ctx, sdk.Resource{Kind: "lamp", Name: "bad"}); err == nil {
		t.Fatal("a failed apply")
	}
	var seen []string
	for _, en := range log.Entries() {
		if en.Resource != "" {
			seen = append(seen, en.Type+" "+en.Resource+" "+en.Status)
		}
	}
	want := []string{"lamps.hold lamp:desk applied", "resource.applied lamp:desk applied", "resource.deleted lamp:desk applied", "resource.applied lamp:bad failed"}
	if len(seen) != len(want) {
		t.Fatalf("audit %q", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("audit %q", seen)
		}
	}
	if _, err := e.Reconcile(ctx, sdk.Resource{Kind: "plant", Name: "fern"}); err == nil {
		t.Fatal("a kind nobody owns")
	}
	if err := e.Unreconcile(ctx, "plant", "fern"); err != nil {
		t.Fatal(err)
	}
}
