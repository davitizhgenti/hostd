package system

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/testutil"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/tlsca"
	"github.com/davitizhgenti/hostd/sdk"
)

func newModule(t *testing.T, o Options) (*Module, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	o.Store = st
	return New(o), st
}

func act(typ, args string) sdk.Action {
	return sdk.Action{Type: typ, Args: json.RawMessage(args)}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	m, st := newModule(t, Options{})
	if man := m.Manifest(); man.Validate() != nil {
		t.Fatal(man.Validate())
	}
	res, err := m.Handle(ctx, act("token.create", `{"name":"phone","scopes":["read"]}`))
	var created struct {
		Kind   sdk.SourceKind
		Secret string
	}
	if err != nil || json.Unmarshal(res.Data, &created) != nil || created.Kind != sdk.SourceManual || created.Secret == "" {
		t.Fatalf("create: %v %s", err, res.Data)
	}
	for args, code := range map[string]sdk.Code{
		`{"name":"x","scopes":["lights"]}`:             sdk.CodeInvalidArgs,
		`{"name":"phone","scopes":["read"]}`:           sdk.CodeInvalidArgs,
		`{"name":"y","scopes":["read"],"ttl":"never"}`: sdk.CodeInvalidArgs,
		`{"name":"z","scopes":["read"],"ttl":"-1h"}`:   sdk.CodeInvalidArgs,
	} {
		if _, err := m.Handle(ctx, act("token.create", args)); sdk.CodeOf(err) != code {
			t.Errorf("%s: %v", args, err)
		}
	}
	// An expired token is shown as such.
	if _, err := m.Handle(ctx, act("token.create", `{"name":"brief","scopes":["read"],"ttl":"1ns"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	got, err := m.Read(ctx, "tokens", nil)
	if err != nil {
		t.Fatal(err)
	}
	expired := map[string]bool{}
	for _, v := range got.([]tokenView) {
		expired[v.Name] = v.Expired
	}
	if !expired["brief"] || expired["phone"] {
		t.Fatalf("expired: %v", expired)
	}

	// Revoked by name; then the name has no active token.
	if _, err := m.Handle(ctx, act("token.revoke", `{"id":"phone"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Handle(ctx, act("token.revoke", `{"id":"phone"}`)); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("second revoke by name: %v", err)
	}
	if _, err := m.Handle(ctx, act("token.revoke", `{"id":"tok_nope"}`)); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	if _, _, err := st.CreateToken(ctx, "spare", sdk.SourceManual, []string{"read"}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Handle(ctx, act("token.nope", `{}`)); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown action: %v", err)
	}
}

func TestLog(t *testing.T) {
	ctx := context.Background()
	m, st := newModule(t, Options{})
	st.Record(core.AuditEntry{Time: time.Now(), Action: "act_1", Type: "lamp.set", Args: json.RawMessage(`{"lamp":"desk","brightness":40}`), Status: "applied"})
	got, err := m.Read(ctx, "log", map[string]string{"limit": "5", "type": "lamp.*"})
	if err != nil {
		t.Fatal(err)
	}
	if recs := got.([]auditView); len(recs) != 1 || recs[0].Summary != "desk 40" {
		t.Fatalf("log: %+v", recs)
	}
	for _, p := range []map[string]string{{"limit": "x"}, {"before": "y"}} {
		if _, err := m.Read(ctx, "log", p); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%v: %v", p, err)
		}
	}
	if argSummary(json.RawMessage(`[1]`)) != "" || argSummary(json.RawMessage(`{"a":[1,2]}`)) != "[1,2]" {
		t.Fatal("argSummary")
	}
}

func TestQuietRunningAndTLS(t *testing.T) {
	ctx := context.Background()
	m, _ := newModule(t, Options{Every: time.Millisecond})
	if res, err := m.Handle(ctx, act("quiet.wait", `{"max":"1s","idle":true}`)); err != nil || string(res.Data) != `{"quiet":true}` {
		t.Fatalf("quiet: %s %v", res.Data, err)
	}
	if _, err := m.Handle(ctx, act("quiet.wait", `{"max":"soon"}`)); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("bad max: %v", err)
	}
	if got, err := m.Read(ctx, "running", nil); err != nil || len(got.([]core.RunningAction)) != 0 {
		t.Fatalf("running: %v %v", got, err)
	}
	if _, err := m.Read(ctx, "tls", nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("tls without a CA: %v", err)
	}
	if _, err := m.Read(ctx, "nope", nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown read: %v", err)
	}

	ca, err := tlsca.Open(tlsca.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m, _ = newModule(t, Options{TLS: ca})
	got, err := m.Read(ctx, "tls", nil)
	if err != nil || got.(map[string]any)["fingerprint"] != ca.Fingerprint() {
		t.Fatalf("tls: %v %v", got, err)
	}
	if man := m.Manifest(); man.Reads[len(man.Reads)-1].Scope != sdk.ScopePublic {
		t.Fatal("tls is not public")
	}
}

// quiet.wait counts what modules mark Busy, whatever its name: a module
// of the test's own holds it, and a plain action of another does not.
func TestQuietWaitsForBusyActions(t *testing.T) {
	ctx := context.Background()
	sys, _ := newModule(t, Options{Every: 5 * time.Millisecond})
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	work := testutil.NewModule("work", nil, nil, nil)
	work.M.Actions = []sdk.ActionSpec{
		{Type: "work.long", Scope: sdk.ScopeAdmin, Busy: true},
		{Type: "work.brief", Scope: sdk.ScopeAdmin},
	}
	work.HandleFunc = func(_ context.Context, a sdk.Action) (sdk.Result, error) {
		started <- struct{}{}
		<-release
		return sdk.Result{}, nil
	}
	reg := core.NewRegistry()
	for _, mod := range []sdk.Module{sys, work} {
		if err := reg.Add(mod); err != nil {
			t.Fatal(err)
		}
	}
	e := core.New(reg, core.Options{Privileged: []string{Name}})
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop(ctx) })
	admin := core.Auth{Scopes: []string{sdk.ScopeAdmin}}
	submit := func(typ, args string) (sdk.Result, error) {
		return e.Submit(ctx, sdk.Action{Type: typ, Args: json.RawMessage(args), Source: sdk.Source{Kind: sdk.SourceManual, Name: "t"}}, admin)
	}
	quiet := func() error { _, err := submit("quiet.wait", `{"max":"60ms"}`); return err }

	go func() { _, _ = submit("work.brief", `{}`) }()
	<-started
	if err := quiet(); err != nil {
		t.Fatalf("an action nobody marked busy held quiet.wait: %v", err)
	}
	release <- struct{}{}
	go func() { _, _ = submit("work.long", `{}`) }()
	<-started
	if err := quiet(); sdk.CodeOf(err) != sdk.CodePreconditionFailed || !strings.Contains(err.Error(), "work.long") {
		t.Fatalf("a busy action did not hold quiet.wait: %v", err)
	}
	release <- struct{}{}
	if err := quiet(); err != nil {
		t.Fatalf("after it ended: %v", err)
	}
}
