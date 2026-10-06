package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var t0 = time.Date(2026, 10, 7, 19, 0, 0, 0, time.UTC)

func open(t *testing.T, opts Options) (*Store, *clock.Fake, string) {
	t.Helper()
	fc := clock.NewFake(t0)
	if opts.Clock == nil {
		opts.Clock = fc
	}
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(context.Background(), path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, fc, path
}

var ctx = context.Background()

func TestMigrateFromEmptyAndReopen(t *testing.T) {
	s, _, path := open(t, Options{})
	if v, err := s.Version(ctx); err != nil || v != SchemaVersion {
		t.Fatalf("version = %d, %v; want %d", v, err, SchemaVersion)
	}
	if _, _, err := s.CreateToken(ctx, "phone", sdk.SourceManual, []string{"read"}, 0); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopening runs no migration again and keeps the data.
	s2, err := Open(ctx, path, Options{Clock: clock.NewFake(t0)})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	toks, err := s2.ListTokens(ctx)
	if err != nil || len(toks) != 1 || toks[0].Name != "phone" {
		t.Fatalf("after reopen: %+v %v", toks, err)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Simulate a database written by a future hostd.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schema_version SET version = ?`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = Open(ctx, path, Options{})
	if err == nil || !strings.Contains(err.Error(), "newer than this hostd") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsOddPaths(t *testing.T) {
	if _, err := Open(ctx, "/tmp/state.db?mode=memory", Options{}); err == nil {
		t.Fatal("path with ? accepted")
	}
}

func TestTokenLifecycle(t *testing.T) {
	s, fc, _ := open(t, Options{})
	tok, secret, err := s.CreateToken(ctx, "phone", sdk.SourceManual, []string{"read", "apps", "audio"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, TokenPrefix) || len(secret) < 40 || !strings.HasPrefix(tok.ID, "tok_") {
		t.Fatalf("token %+v secret %q", tok, secret)
	}

	// The secret is never stored: only its hash.
	var stored string
	if err := s.db.QueryRow(`SELECT hash FROM tokens WHERE id = ?`, tok.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret || strings.Contains(stored, secret[len(TokenPrefix):]) || stored != hashSecret(secret) {
		t.Fatalf("stored %q for secret %q", stored, secret)
	}

	fc.Advance(time.Minute)
	got, err := s.VerifyToken(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tok.ID || got.Kind != sdk.SourceManual || !got.HasScope("audio") || got.HasScope("display") {
		t.Fatalf("verified %+v", got)
	}
	if got.LastUsed == nil || !got.LastUsed.Equal(t0.Add(time.Minute)) {
		t.Fatalf("last used = %v", got.LastUsed)
	}

	for _, bad := range []string{"", "hostd_", secret + "x", "Bearer " + secret, strings.TrimPrefix(secret, TokenPrefix)} {
		if _, err := s.VerifyToken(ctx, bad); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("VerifyToken(%q) = %v", bad, err)
		}
	}

	if err := s.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyToken(ctx, secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked token verified: %v", err)
	}
	if err := s.RevokeToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	// The name is free again once revoked.
	if _, _, err := s.CreateToken(ctx, "phone", sdk.SourceManual, []string{"read"}, 0); err != nil {
		t.Fatalf("reusing a revoked name: %v", err)
	}
	toks, _ := s.ListTokens(ctx)
	if len(toks) != 2 || toks[0].Revoked == nil || toks[1].Revoked != nil {
		t.Fatalf("list = %+v", toks)
	}
}

func TestTokenExpiry(t *testing.T) {
	s, fc, _ := open(t, Options{})
	tok, secret, err := s.CreateToken(ctx, "evening.sh run", sdk.SourceAutomation, []string{"audio"}, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Expires == nil || !tok.Expires.Equal(t0.Add(time.Minute)) {
		t.Fatalf("expires = %v", tok.Expires)
	}
	fc.Advance(59 * time.Second)
	if _, err := s.VerifyToken(ctx, secret); err != nil {
		t.Fatalf("valid before expiry: %v", err)
	}
	fc.Advance(time.Second)
	if _, err := s.VerifyToken(ctx, secret); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("valid at expiry: %v", err)
	}

	// Expired tokens are purged after the keep period.
	fc.Advance(time.Hour)
	n, err := s.PurgeExpired(ctx, 30*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
}

func TestCreateTokenRefusals(t *testing.T) {
	s, _, _ := open(t, Options{})
	if _, _, err := s.CreateToken(ctx, "phone", sdk.SourceManual, []string{"read"}, 0); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		kind   sdk.SourceKind
		scopes []string
		ttl    time.Duration
		want   string
	}{
		{"  ", sdk.SourceManual, []string{"read"}, 0, "name is required"},
		{"x", sdk.SourceExternal, []string{"read"}, 0, "kind must be"},
		{"x", "", []string{"read"}, 0, "kind must be"},
		{"x", sdk.SourceManual, nil, 0, "at least one scope"},
		{"x", sdk.SourceManual, []string{"read"}, -time.Second, "negative"},
		{"phone", sdk.SourceManual, []string{"read"}, 0, "already exists"},
	} {
		if _, _, err := s.CreateToken(ctx, tc.name, tc.kind, tc.scopes, tc.ttl); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CreateToken(%q, %q): err = %v, want %q", tc.name, tc.kind, err, tc.want)
		}
	}
}

func TestEnsureAdminTokenOnlyOnce(t *testing.T) {
	s, _, _ := open(t, Options{})
	secret, created, err := s.EnsureAdminToken(ctx)
	if err != nil || !created || secret == "" {
		t.Fatalf("first: %q %v %v", secret, created, err)
	}
	tok, err := s.VerifyToken(ctx, secret)
	if err != nil || !tok.HasScope("anything") || tok.Name != "admin" {
		t.Fatalf("admin token: %+v %v", tok, err)
	}
	again, created, err := s.EnsureAdminToken(ctx)
	if err != nil || created || again != "" {
		t.Fatalf("second call minted a token: %q %v %v", again, created, err)
	}
	// Even after the admin token is revoked: tokens exist, so no new one.
	_ = s.RevokeToken(ctx, tok.ID)
	if _, created, _ := s.EnsureAdminToken(ctx); created {
		t.Fatal("minted a new admin token after revocation")
	}
}

func TestScopesWithin(t *testing.T) {
	for _, tc := range []struct {
		want, have []string
		ok         bool
	}{
		{[]string{"audio"}, []string{"audio", "apps"}, true},
		{[]string{"audio", "display"}, []string{"audio"}, false},
		{[]string{"display.front"}, []string{"admin"}, true},
		{nil, []string{"read"}, true},
		{[]string{"admin"}, []string{"audio"}, false},
	} {
		if got := ScopesWithin(tc.want, tc.have); got != tc.ok {
			t.Errorf("ScopesWithin(%v, %v) = %v", tc.want, tc.have, got)
		}
	}
}

func entry(i int, typ, status string) core.AuditEntry {
	return core.AuditEntry{
		Time: t0.Add(time.Duration(i) * time.Second), Action: fmt.Sprintf("act_%04d", i), Type: typ,
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "phone"}, Status: status,
	}
}

func TestAuditWriteAndQuery(t *testing.T) {
	s, _, _ := open(t, Options{})
	types := []string{"audio.volume.set", "app.start", "audio.mute.set", "window.focus"}
	for i := 0; i < 40; i++ {
		s.Record(entry(i, types[i%4], "applied"))
	}
	held := entry(40, "audio.volume.set", "skipped")
	until := t0.Add(time.Hour)
	held.Reason, held.HeldBy, held.Until = "held", sdk.SourceManual, &until
	s.Record(held)
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	all, err := s.QueryAudit(ctx, AuditQuery{})
	if err != nil || len(all) != 41 {
		t.Fatalf("all: %d %v", len(all), err)
	}
	if all[0].Action != "act_0040" || all[0].Status != "skipped" || all[0].HeldBy != sdk.SourceManual ||
		all[0].Until == nil || !all[0].Until.Equal(until) {
		t.Fatalf("newest = %+v", all[0])
	}

	page, _ := s.QueryAudit(ctx, AuditQuery{Limit: 10})
	next, _ := s.QueryAudit(ctx, AuditQuery{Limit: 10, Before: page[9].Seq})
	if len(page) != 10 || len(next) != 10 || next[0].Seq != page[9].Seq-1 {
		t.Fatalf("paging broken: %d %d", len(page), len(next))
	}

	audio, _ := s.QueryAudit(ctx, AuditQuery{Type: "audio.*"})
	if len(audio) != 21 {
		t.Fatalf("audio.* matched %d, want 21", len(audio))
	}
	for _, r := range audio {
		if !sdk.MatchType("audio.*", r.Type) {
			t.Fatalf("audio.* matched %s", r.Type)
		}
	}
	exact, _ := s.QueryAudit(ctx, AuditQuery{Type: "app.start"})
	one, _ := s.QueryAudit(ctx, AuditQuery{Action: "act_0007"})
	if len(exact) != 10 || len(one) != 1 || one[0].Type != "window.focus" {
		t.Fatalf("exact %d, by action %+v", len(exact), one)
	}
}

func TestAuditLikePatternEscaping(t *testing.T) {
	s, _, _ := open(t, Options{})
	s.Record(entry(1, "a_b.set", "applied"))
	s.Record(entry(2, "axb.set", "applied"))
	_ = s.Flush(ctx)
	got, _ := s.QueryAudit(ctx, AuditQuery{Type: "a_b.*"})
	if len(got) != 1 || got[0].Type != "a_b.set" {
		t.Fatalf("_ treated as a wildcard: %+v", got)
	}
}

func TestAuditTrimmedToMax(t *testing.T) {
	s, _, _ := open(t, Options{AuditMax: 50})
	for i := 0; i < 120; i++ {
		s.Record(entry(i, "audio.volume.set", "applied"))
		if i%17 == 0 {
			_ = s.Flush(ctx) // several batches
		}
	}
	_ = s.Flush(ctx)
	all, _ := s.QueryAudit(ctx, AuditQuery{Limit: 1000})
	if len(all) != 50 || all[0].Action != "act_0119" || all[49].Action != "act_0070" {
		t.Fatalf("kept %d entries, newest %s oldest %s", len(all), all[0].Action, all[len(all)-1].Action)
	}
}

func TestAuditWrittenOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		s.Record(entry(i, "app.start", "applied"))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	all, _ := s.QueryAudit(ctx, AuditQuery{})
	if len(all) != 25 {
		t.Fatalf("%d entries survived Close, want 25", len(all))
	}
}

func TestStoreIsAnAuditor(t *testing.T) {
	var _ core.Auditor = (*Store)(nil)
}

func TestEngineWritesAuditToStore(t *testing.T) {
	// The real wiring: the engine's audit trail lands in SQLite.
	s, _, _ := open(t, Options{})
	reg := core.NewRegistry()
	m := &auditModule{}
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	e := core.New(reg, core.Options{Clock: clock.NewFake(t0), Audit: s})
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Stop(ctx) }()
	res, err := e.Submit(ctx, sdk.Action{Type: "probe.ping", Source: sdk.Source{Kind: sdk.SourceManual, Name: "cli"}},
		core.Auth{Scopes: []string{sdk.ScopeAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Flush(ctx)
	got, _ := s.QueryAudit(ctx, AuditQuery{Action: res.Action})
	if len(got) != 1 || got[0].Status != "applied" || got[0].Source.Name != "cli" {
		t.Fatalf("audit = %+v", got)
	}
}

type auditModule struct{}

func (auditModule) Manifest() sdk.Manifest {
	return sdk.Manifest{Name: "probe", Version: "test", Owns: []string{"probe.*"},
		Actions: []sdk.ActionSpec{{Type: "probe.ping", Scope: sdk.ScopeAdmin}}}
}
func (auditModule) Start(context.Context, sdk.Core) error                  { return nil }
func (auditModule) Validate(context.Context, sdk.Action) error             { return nil }
func (auditModule) Stop(context.Context) error                             { return nil }
func (auditModule) Handle(context.Context, sdk.Action) (sdk.Result, error) { return sdk.Result{}, nil }
