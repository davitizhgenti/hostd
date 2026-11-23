// Package system is the module "hostd": hostd's own features (tokens, the
// audit trail, the actions running now, its certificate, waiting until it
// is quiet) offered as a module offers its own, so they come with routes,
// scopes, the audit trail and generated hostctl commands like any other.
package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/clock"
	"github.com/davitizhgenti/hostd/internal/tlsca"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

// Name is the module's name.
const Name = "hostd"

// Options are what the module serves from.
type Options struct {
	Store *store.Store
	// TLS is the machine's CA, if the API is served over HTTPS.
	TLS *tlsca.Authority
	// Every is how often quiet.wait looks again (default 1s).
	Every time.Duration
	// Clock times quiet.wait (default: the real one).
	Clock clock.Clock
	// Externals are the external modules hostd found when it started, and
	// Reserved the names of the built-in modules: for the kind "module".
	Externals []ExternalState
	Reserved  []string
	Logger    *slog.Logger
}

// Module is hostd's own module.
type Module struct {
	o Options

	actions *kit.Actions
	reads   *kit.Reads

	mu    sync.Mutex
	core  sdk.Core
	intro sdk.Introspector // nil until Start, and in tests that set none
}

func New(o Options) *Module {
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Every == 0 {
		o.Every = time.Second
	}
	m := &Module{o: o}
	m.actions, m.reads = m.declare()
	return m
}

// The arguments of hostd's own actions.
type (
	createArgs struct {
		Name   string         `json:"name" desc:"what it is for: a device or a script"`
		Scopes []string       `json:"scopes" desc:"what it may do (hostctl get hostd scopes)"`
		Kind   sdk.SourceKind `json:"kind,omitempty" enum:"manual,automation,local" default:"manual" desc:"manual (a person's device), automation (scripts) or local (the on-screen menu)"`
		TTL    string         `json:"ttl,omitempty" desc:"how long it lasts, such as 720h; default: until revoked"`
	}
	revokeArgs struct {
		ID string `json:"id" desc:"the token's ID (tok_...) or name"`
	}
	quietArgs struct {
		Types string `json:"types,omitempty" desc:"action types that count as busy, comma-separated (default: the actions modules mark busy: deploys, scenes)"`
		Idle  bool   `json:"idle,omitempty" desc:"also wait until nobody is using the screen"`
		Max   string `json:"max,omitempty" desc:"how long to wait at most (default 10m)"`
	}
	// createdToken is a new token with its secret, shown only now: the
	// audit trail keeps arguments, never results.
	createdToken struct {
		store.Token
		Secret string `json:"secret"`
	}
	revoked struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	quiet struct {
		Quiet bool `json:"quiet"`
	}
	tlsInfo struct {
		Fingerprint string     `json:"fingerprint"`
		Pin         string     `json:"pin"`
		Leaf        tlsca.Leaf `json:"leaf"`
	}
)

// declare is hostd's own actions and reads.
func (m *Module) declare() (*kit.Actions, *kit.Reads) {
	t := kit.NewActions(Name)
	kit.On(t, sdk.ActionSpec{Type: "token.create", Description: "Create an API token; its secret is shown only now",
		Scope: sdk.ScopeAdmin, Keys: []sdk.KeyTemplate{"token:{name}"},
		Route: &sdk.Route{Method: "POST", Path: "/v1/tokens"},
		CLI: &sdk.CLI{Done: []string{
			"Created token \"{name}\" ({kind}, scopes {scopes}). Its secret, shown only now:\n\n    {secret}"}}}, m.createToken)
	kit.On(t, sdk.ActionSpec{Type: "token.revoke", Description: "Revoke a token, by ID or name",
		Scope: sdk.ScopeAdmin, Keys: []sdk.KeyTemplate{"token:{id}"},
		Route: &sdk.Route{Method: "DELETE", Path: "/v1/tokens/{id}"},
		CLI:   &sdk.CLI{Done: []string{"Revoked {id}"}}}, m.revokeToken)
	kit.On(t, sdk.ActionSpec{Type: "quiet.wait", Description: "Wait until no deploy or scene is in progress (and, with idle, nobody is at the screen); fails if one still is after max",
		Scope: sdk.ScopeRead, Timeout: sdk.Duration(time.Hour),
		Route: &sdk.Route{Method: "POST", Path: "/v1/quiet"},
		CLI:   &sdk.CLI{Alias: []string{"wait-quiet"}, Done: []string{"quiet"}}}, m.waitQuiet)

	r := kit.NewReads(Name)
	kit.Serve(r, sdk.ReadSpec{Name: "tokens", Description: "The API tokens (never their secrets)", Path: "/v1/tokens", Scope: sdk.ScopeAdmin,
		CLI: &sdk.CLI{Alias: []string{"token list"}, Columns: []sdk.Column{{Title: "NAME", Path: "name"}, {Title: "KIND", Path: "kind"}, {Title: "SCOPES", Path: "scopes"},
			{Title: "LAST USED", Text: []string{"{last_used:date} if last_used", "never"}},
			{Title: "STATE", Text: []string{"revoked if revoked", "expired if expired", "expires {expires:date} if expires", "active"}},
			{Title: "ID", Path: "id"}}}}, m.tokens)
	kit.Serve(r, sdk.ReadSpec{Name: "log", Description: "The audit trail: recent actions, their sources and results, newest first (?limit, ?before, ?type, ?action)",
		Path: "/v1/actions",
		CLI: &sdk.CLI{Alias: []string{"log"}, Reverse: true, NoHeader: true, Gap: 3,
			Params: []sdk.Param{{Name: "limit", Value: "N", Default: "50", Description: "how many entries"},
				{Name: "type", Value: "pattern", Description: "only these types, e.g. 'audio.*'"},
				{Name: "action", Value: "ID", Description: "only this action's entries"}},
			Columns: []sdk.Column{{Title: "TIME", Path: "time", Format: "time"},
				{Title: "ACTION", Text: []string{"{type} {summary}"}, Max: 60},
				{Title: "SOURCE", Text: []string{"{source.kind} ({source.name}) if source.name", "{source.kind}"}},
				{Title: "STATUS", Path: "status"},
				{Title: "DETAIL", Text: []string{"held by {held_by} until {until:time} if until", "{reason} if status=skipped",
					"{code}: {reason} if status=failed", "v{version} if version", " "}, Max: 100}}}}, m.audit)
	kit.Serve(r, sdk.ReadSpec{Name: "running", Description: "The actions being handled now, oldest first", Path: "/v1/actions/running",
		CLI: &sdk.CLI{Empty: "nothing is running", Columns: []sdk.Column{{Title: "ACTION", Path: "id"}, {Title: "TYPE", Path: "type"},
			{Title: "SOURCE", Text: []string{"{source.kind} ({source.name}) if source.name", "{source.kind}"}},
			{Title: "SINCE", Path: "started", Format: "time"}}}}, m.running)
	if m.o.TLS != nil {
		kit.Serve(r, sdk.ReadSpec{Name: "tls", Path: "/v1/tls", Scope: sdk.ScopePublic,
			Description: "The machine's CA (its fingerprint, which clients pin) and the certificate the API serves",
			CLI: &sdk.CLI{Alias: []string{"tls"}, Done: []string{
				tlsText + "\nLeft out: {leaf.skipped} (the CA was made before they were configured; to remake it, stop hostd, remove ~/.local/state/hostd/tls, start it, and log in again everywhere) if leaf.skipped",
				tlsText}}}, m.tls)
	}
	return t, r
}

func (m *Module) Manifest() sdk.Manifest {
	return sdk.Manifest{
		Name: Name, Version: "0.1.0",
		Owns:    []string{"token.*", "quiet.*"},
		Kinds:   []sdk.KindSpec{moduleKind()},
		Actions: m.actions.Specs(),
		Reads:   m.reads.Specs(),
	}
}

// tlsText is how hostctl tls prints the CA and the certificate.
const tlsText = "Certificate authority (pin):\n    {pin}\n    file: GET /v1/tls/ca.pem, or ~/.local/state/hostd/tls/ca.pem on the machine\n" +
	"Certificate: {leaf.names}, {leaf.ips}\n    valid until {leaf.not_after:date}"

func (m *Module) Start(_ context.Context, c sdk.Core) error {
	m.mu.Lock()
	m.core = c
	m.intro = c.Introspect() // hostd's own module is the one that gets it
	m.mu.Unlock()
	return nil
}

func (m *Module) Stop(context.Context) error { return nil }

func (m *Module) Validate(ctx context.Context, a sdk.Action) error { return m.actions.Validate(ctx, a) }

func (m *Module) Handle(ctx context.Context, a sdk.Action) (sdk.Result, error) {
	return m.actions.Handle(ctx, a)
}

func (m *Module) knownScopes() []string {
	known := []string{sdk.ScopeRead, sdk.ScopeAdmin}
	m.mu.Lock()
	in := m.intro
	m.mu.Unlock()
	if in != nil {
		known = append(known, in.Scopes()...)
	}
	sort.Strings(known)
	return slices.Compact(known)
}

func (m *Module) createToken(ctx context.Context, _ sdk.Action, args createArgs) (createdToken, error) {
	if args.Kind == "" {
		args.Kind = sdk.SourceManual
	}
	known := m.knownScopes()
	for _, sc := range args.Scopes {
		if !slices.Contains(known, sc) {
			return createdToken{}, sdk.Errorf(sdk.CodeInvalidArgs, "unknown scope %q; known scopes: %s", sc, strings.Join(known, ", "))
		}
	}
	var ttl time.Duration
	if args.TTL != "" {
		d, err := time.ParseDuration(args.TTL)
		if err != nil || d <= 0 {
			return createdToken{}, sdk.Errorf(sdk.CodeInvalidArgs, "ttl %q: a duration such as 720h", args.TTL)
		}
		ttl = d
	}
	tok, secret, err := m.o.Store.CreateToken(ctx, args.Name, args.Kind, args.Scopes, ttl)
	if err != nil {
		if errors.Is(err, store.ErrNameTaken) || errors.Is(err, store.ErrBadRequest) {
			return createdToken{}, &sdk.Error{Code: sdk.CodeInvalidArgs, Message: err.Error()}
		}
		return createdToken{}, err
	}
	return createdToken{Token: tok, Secret: secret}, nil
}

func (m *Module) revokeToken(ctx context.Context, _ sdk.Action, args revokeArgs) (revoked, error) {
	id := args.ID
	if !strings.HasPrefix(id, "tok_") { // a name: its active token
		toks, err := m.o.Store.ListTokens(ctx)
		if err != nil {
			return revoked{}, err
		}
		id = ""
		for _, t := range toks {
			if t.Name == args.ID && t.Revoked == nil {
				id = t.ID
			}
		}
		if id == "" {
			return revoked{}, sdk.Errorf(sdk.CodeNotFound, "no active token named %q", args.ID)
		}
	}
	if err := m.o.Store.RevokeToken(ctx, id); errors.Is(err, store.ErrNotFound) {
		return revoked{}, sdk.Errorf(sdk.CodeNotFound, "no active token %q", args.ID)
	} else if err != nil {
		return revoked{}, err
	}
	return revoked{ID: args.ID, Token: id}, nil
}

// waitQuiet returns once no action of the given types runs (and, with
// idle, nobody is at the screen); it fails if that has not happened by
// max.
func (m *Module) waitQuiet(ctx context.Context, _ sdk.Action, args quietArgs) (quiet, error) {
	m.mu.Lock()
	in := m.intro
	m.mu.Unlock()
	if args.Types == "" && in != nil {
		args.Types = strings.Join(in.BusyTypes(), ",")
	}
	maxWait := 10 * time.Minute
	if args.Max != "" {
		d, err := time.ParseDuration(args.Max)
		if err != nil || d < 0 {
			return quiet{}, sdk.Errorf(sdk.CodeInvalidArgs, "max %q: a duration such as 5m", args.Max)
		}
		maxWait = d
	}
	deadline := m.o.Clock.Now().Add(maxWait)
	for {
		busy := m.busy(ctx, strings.Split(args.Types, ","), args.Idle)
		if len(busy) == 0 {
			return quiet{Quiet: true}, nil
		}
		if m.o.Clock.Now().After(deadline) {
			return quiet{}, sdk.Errorf(sdk.CodePreconditionFailed, "still busy after %s: %s", maxWait, strings.Join(busy, "; "))
		}
		select {
		case <-ctx.Done():
			return quiet{}, ctx.Err()
		case <-m.o.Clock.After(m.o.Every):
		}
	}
}

func (m *Module) busy(ctx context.Context, patterns []string, idle bool) []string {
	m.mu.Lock()
	in, c := m.intro, m.core
	m.mu.Unlock()
	var busy []string
	if in != nil {
		for _, r := range in.Running() {
			for _, p := range patterns {
				if sdk.MatchType(strings.TrimSpace(p), r.Type) {
					busy = append(busy, fmt.Sprintf("%s (%s, since %s)", r.Type, r.ID, r.Started.Format("15:04:05")))
					break
				}
			}
		}
	}
	if idle && c != nil && c.Handles("display.menu") {
		var d struct {
			Present bool `json:"present"`
		}
		if raw, err := c.Read(ctx, "display", "display", nil); err == nil && json.Unmarshal(raw, &d) == nil && d.Present {
			busy = append(busy, "someone is using the screen")
		}
	}
	return busy
}

func (m *Module) Read(ctx context.Context, name string, params map[string]string) (any, error) {
	return m.reads.Read(ctx, name, params)
}

func (m *Module) tokens(ctx context.Context, _ struct{}) ([]tokenView, error) {
	toks, err := m.o.Store.ListTokens(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]tokenView, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenView{Token: t, Expired: t.Revoked == nil && t.Expires != nil && !t.Expires.After(now)})
	}
	return out, nil
}

func (m *Module) running(context.Context, struct{}) ([]sdk.RunningAction, error) {
	m.mu.Lock()
	in := m.intro
	m.mu.Unlock()
	if in == nil {
		return []sdk.RunningAction{}, nil
	}
	return in.Running(), nil
}

func (m *Module) tls(context.Context, struct{}) (tlsInfo, error) {
	fp := m.o.TLS.Fingerprint()
	return tlsInfo{Fingerprint: fp, Pin: tlsca.Pretty(fp), Leaf: m.o.TLS.Leaf()}, nil
}

// tokenView is a token as the tokens read shows it.
type tokenView struct {
	store.Token
	Expired bool `json:"expired,omitempty"`
}

// auditView is an audit entry as the log read shows it: with its
// arguments in short, in the order they were sent.
type auditView struct {
	store.AuditRecord
	Summary string `json:"summary,omitempty"`
}

func (m *Module) audit(ctx context.Context, params map[string]string) ([]auditView, error) {
	q := store.AuditQuery{Type: params["type"], Action: params["action"]}
	var err error
	if v := params["limit"]; v != "" {
		if q.Limit, err = strconv.Atoi(v); err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "limit must be a number")
		}
	}
	if v := params["before"]; v != "" {
		if q.Before, err = strconv.ParseInt(v, 10, 64); err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidArgs, "before must be a number")
		}
	}
	if err := m.o.Store.Flush(ctx); err != nil { // include actions that just finished
		return nil, err
	}
	recs, err := m.o.Store.QueryAudit(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]auditView, 0, len(recs))
	for _, r := range recs {
		out = append(out, auditView{AuditRecord: r, Summary: argSummary(r.Args)})
	}
	return out, nil
}

// argSummary shows argument values compactly, in the order they were
// sent: {"lamp":"desk","brightness":40} becomes "desk 40".
func argSummary(raw json.RawMessage) string {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	var parts []string
	for dec.More() {
		if _, err := dec.Token(); err != nil { // key
			break
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			break
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			parts = append(parts, s)
		} else {
			parts = append(parts, string(v))
		}
	}
	return strings.Join(parts, " ")
}
