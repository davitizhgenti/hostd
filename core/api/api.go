// Package api serves hostd's HTTP API: generic routes for any module's
// actions and state, friendly routes generated from module manifests,
// tokens, the audit trail, and the WebSocket event stream.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/internal/ids"
	"github.com/davitizhgenti/hostd/internal/version"
	"github.com/davitizhgenti/hostd/sdk"
)

const maxBody = 1 << 20 // 1 MiB

// Options configure the API server.
type Options struct {
	Logger *slog.Logger
	// AdminTokenFile, if set, is deleted the first time the admin token is
	// used: it only exists to hand the first token to hostctl login.
	AdminTokenFile string
	// ReplayEvents is how many recent events are kept for ?since= (default
	// 1,000).
	ReplayEvents int
	// OnListening, if set, runs once every listener is open, before
	// requests are served: the moment hostd can tell systemd it is ready.
	OnListening func()
}

// Server is the HTTP API.
type Server struct {
	engine *core.Engine
	store  *store.Store
	log    *slog.Logger
	opts   Options
	mux    *http.ServeMux
	ring   *ring

	adminFileOnce sync.Once
}

// New builds the server and its routes, including one route per action
// that declares one in its module's manifest.
func New(e *core.Engine, s *store.Store, opts Options) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ReplayEvents == 0 {
		opts.ReplayEvents = 1000
	}
	srv := &Server{engine: e, store: s, log: opts.Logger, opts: opts, mux: http.NewServeMux(), ring: newRing(opts.ReplayEvents)}

	routes := []struct {
		pattern string
		scope   string
		h       func(http.ResponseWriter, *http.Request, store.Token) error
	}{
		// Any valid token may read these: they describe the API, not the
		// machine, and hostctl needs them to build its commands.
		{"GET /v1/version", "", srv.getVersion},
		{"GET /v1/manifests", "", srv.getManifests},
		{"GET /v1/state/{module}", sdk.ScopeRead, srv.getState},
		{"POST /v1/actions", "", srv.postAction}, // scope depends on the action
		{"GET /v1/actions", sdk.ScopeRead, srv.getAudit},
		{"GET /v1/events", sdk.ScopeRead, srv.getEvents},
		{"GET /v1/tokens", sdk.ScopeAdmin, srv.listTokens},
		{"POST /v1/tokens", sdk.ScopeAdmin, srv.createToken},
		{"DELETE /v1/tokens/{id}", sdk.ScopeAdmin, srv.revokeToken},
	}
	for _, r := range routes {
		if err := srv.handle(r.pattern, r.scope, r.h); err != nil {
			return nil, err
		}
	}
	for _, m := range e.Registry().Manifests() {
		for _, spec := range m.Actions {
			if spec.Route == nil {
				continue
			}
			pattern := spec.Route.Method + " " + spec.Route.Path
			if err := srv.handle(pattern, "", srv.friendly(spec)); err != nil {
				return nil, fmt.Errorf("module %q, action %q: %w", m.Name, spec.Type, err)
			}
		}
	}
	for _, m := range e.Registry().Manifests() {
		for _, rd := range m.Reads {
			if err := srv.handle("GET "+rd.Path, sdk.ScopeRead, srv.read(m.Name, rd)); err != nil {
				return nil, fmt.Errorf("module %q, read %q: %w", m.Name, rd.Name, err)
			}
		}
	}
	srv.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, sdk.CodeNotFound, "no route "+r.Method+" "+r.URL.Path)
	})
	return srv, nil
}

// handle registers a route behind authentication. ServeMux panics on
// conflicting patterns; that becomes an error here, since manifests can
// declare routes that collide.
func (s *Server) handle(pattern, scope string, h func(http.ResponseWriter, *http.Request, store.Token) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("route %q: %v", pattern, p)
		}
	}()
	s.mux.Handle(pattern, s.authenticated(scope, h))
	return nil
}

// Handler returns the API with request IDs and panic recovery, for the
// unix socket. TCP listeners also need the home-network check; see Serve.
func (s *Server) Handler() http.Handler { return s.withBasics(s.mux) }

func (s *Server) withBasics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rid := r.Header.Get("X-Request-ID")
		if rid == "" || len(rid) > 64 {
			rid = ids.New("req")
		}
		w.Header().Set("X-Request-ID", rid)
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic in API handler", "panic", p, "path", r.URL.Path, "request", rid)
				writeError(w, r, http.StatusInternalServerError, sdk.CodeInternal, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// Run collects events for replay until ctx ends. Start it before serving.
func (s *Server) Run(ctx context.Context) {
	for ev := range s.engine.Subscribe(ctx, "*") {
		s.ring.add(ev)
	}
}

// Serve starts the ring collector and serves on a unix socket (mode 0600)
// and, if tcpAddr is not empty, on TCP for the home network, until ctx
// ends; then it shuts down gracefully.
func (s *Server) Serve(ctx context.Context, unixPath, tcpAddr string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.Run(ctx)

	var lc net.ListenConfig
	var listeners []net.Listener
	var servers []*http.Server
	if unixPath != "" {
		_ = os.Remove(unixPath) // a stale socket from a crash
		ln, err := lc.Listen(ctx, "unix", unixPath)
		if err != nil {
			return err
		}
		if err := os.Chmod(unixPath, 0o600); err != nil {
			ln.Close()
			return err
		}
		listeners = append(listeners, ln)
		servers = append(servers, &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second})
	}
	if tcpAddr != "" {
		ln, err := lc.Listen(ctx, "tcp", tcpAddr)
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return err
		}
		listeners = append(listeners, ln)
		servers = append(servers, &http.Server{Handler: s.withBasics(privateOnly(s.mux)), ReadHeaderTimeout: 10 * time.Second})
	}

	if s.opts.OnListening != nil {
		s.opts.OnListening()
	}
	errc := make(chan error, len(servers))
	for i := range servers {
		go func() { errc <- servers[i].Serve(listeners[i]) }()
	}
	select {
	case err := <-errc:
		cancel()
		s.shutdown(servers)
		return err
	case <-ctx.Done():
		s.shutdown(servers)
		return nil
	}
}

func (s *Server) shutdown(servers []*http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(ctx)
	}
}

// authenticated checks the bearer token (or, for WebSocket clients, the
// hostd.token.<secret> subprotocol) and the route's scope.
func (s *Server) authenticated(scope string, h func(http.ResponseWriter, *http.Request, store.Token) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := bearer(r)
		if secret == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hostd"`)
			writeError(w, r, http.StatusUnauthorized, sdk.CodeUnauthorized, "a token is required")
			return
		}
		tok, err := s.store.VerifyToken(r.Context(), secret)
		if err != nil {
			if errors.Is(err, store.ErrInvalidToken) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="hostd", error="invalid_token"`)
				writeError(w, r, http.StatusUnauthorized, sdk.CodeUnauthorized, "invalid, expired or revoked token")
				return
			}
			s.log.Error("verifying token", "err", err)
			writeError(w, r, http.StatusInternalServerError, sdk.CodeInternal, "internal error")
			return
		}
		if tok.Name == "admin" && s.opts.AdminTokenFile != "" {
			s.adminFileOnce.Do(func() { _ = os.Remove(s.opts.AdminTokenFile) })
		}
		if scope != "" && !tok.HasScope(scope) {
			writeError(w, r, http.StatusForbidden, sdk.CodeForbidden, fmt.Sprintf("this needs scope %q", scope))
			return
		}
		if err := h(w, r, tok); err != nil {
			writeErr(w, r, err)
		}
	})
}

func bearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if after, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(after)
		}
		return ""
	}
	for _, p := range websocketProtocols(r) {
		if after, ok := strings.CutPrefix(p, wsTokenPrefix); ok {
			return after
		}
	}
	return ""
}

func authOf(tok store.Token) core.Auth {
	return core.Auth{TokenID: tok.ID, Name: tok.Name, Scopes: tok.Scopes}
}

func sourceOf(tok store.Token) sdk.Source {
	return sdk.Source{Kind: tok.Kind, Name: tok.Name, Token: tok.ID}
}

// --- actions --------------------------------------------------------------

type actionRequest struct {
	Type          string          `json:"type"`
	Args          json.RawMessage `json:"args,omitempty"`
	ExpectVersion *uint64         `json:"expect_version,omitempty"`
	Cause         string          `json:"cause,omitempty"`
}

func (s *Server) postAction(w http.ResponseWriter, r *http.Request, tok store.Token) error {
	var req actionRequest
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if req.Type == "" {
		return sdk.Errorf(sdk.CodeInvalidArgs, "type is required")
	}
	return s.submit(w, r, tok, sdk.Action{
		Type: req.Type, Args: req.Args, ExpectVersion: req.ExpectVersion, Cause: req.Cause,
	})
}

// friendly serves an action's own route. The JSON body (optional) gives
// the arguments; path parameters are added to them as strings.
func (s *Server) friendly(spec sdk.ActionSpec) func(http.ResponseWriter, *http.Request, store.Token) error {
	params := pathParams(spec.Route.Path)
	return func(w http.ResponseWriter, r *http.Request, tok store.Token) error {
		args := map[string]json.RawMessage{}
		body, err := readBody(r)
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := json.Unmarshal(body, &args); err != nil {
				return sdk.Errorf(sdk.CodeInvalidArgs, "body must be a JSON object of arguments")
			}
		}
		for _, p := range params {
			v, _ := json.Marshal(r.PathValue(p))
			if prev, ok := args[p]; ok && !bytes.Equal(bytes.TrimSpace(prev), v) {
				return sdk.Errorf(sdk.CodeInvalidArgs, "argument %q is in both the path and the body, with different values", p)
			}
			args[p] = v
		}
		raw, _ := json.Marshal(args)
		a := sdk.Action{Type: spec.Type, Args: raw}
		if v := r.URL.Query().Get("expect_version"); v != "" {
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return sdk.Errorf(sdk.CodeInvalidArgs, "expect_version must be a number")
			}
			a.ExpectVersion = &n
		}
		return s.submit(w, r, tok, a)
	}
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, tok store.Token, a sdk.Action) error {
	a.Source = sourceOf(tok)
	res, err := s.engine.Submit(r.Context(), a, authOf(tok))
	if err != nil {
		return err
	}
	status := http.StatusOK
	if res.Status == sdk.StatusAccepted {
		status = http.StatusAccepted
	}
	writeJSON(w, status, res)
	return nil
}

// --- reads ----------------------------------------------------------------

// read serves a module's declared read route.
func (s *Server) read(module string, rd sdk.ReadSpec) func(http.ResponseWriter, *http.Request, store.Token) error {
	params := pathParams(rd.Path)
	return func(w http.ResponseWriter, r *http.Request, _ store.Token) error {
		args := map[string]string{}
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				args[k] = v[0]
			}
		}
		for _, p := range params {
			args[p] = r.PathValue(p)
		}
		v, err := s.engine.ModuleRead(r.Context(), module, rd.Name, args)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, v)
		return nil
	}
}

func (s *Server) getVersion(w http.ResponseWriter, _ *http.Request, _ store.Token) error {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": version.Version, "dev": version.IsDev(),
		"os": runtime.GOOS, "arch": runtime.GOARCH, "schema": store.SchemaVersion,
	})
	return nil
}

func (s *Server) getManifests(w http.ResponseWriter, _ *http.Request, _ store.Token) error {
	writeJSON(w, http.StatusOK, s.engine.Registry().Manifests())
	return nil
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request, _ store.Token) error {
	st, err := s.engine.ModuleState(r.Context(), r.PathValue("module"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, st)
	return nil
}

func (s *Server) getAudit(w http.ResponseWriter, r *http.Request, _ store.Token) error {
	q := r.URL.Query()
	query := store.AuditQuery{Type: q.Get("type"), Action: q.Get("action")}
	var err error
	if v := q.Get("limit"); v != "" {
		if query.Limit, err = strconv.Atoi(v); err != nil {
			return sdk.Errorf(sdk.CodeInvalidArgs, "limit must be a number")
		}
	}
	if v := q.Get("before"); v != "" {
		if query.Before, err = strconv.ParseInt(v, 10, 64); err != nil {
			return sdk.Errorf(sdk.CodeInvalidArgs, "before must be a number")
		}
	}
	if err := s.store.Flush(r.Context()); err != nil { // include actions that just finished
		return err
	}
	recs, err := s.store.QueryAudit(r.Context(), query)
	if err != nil {
		return err
	}
	if recs == nil {
		recs = []store.AuditRecord{}
	}
	writeJSON(w, http.StatusOK, recs)
	return nil
}

// --- tokens ---------------------------------------------------------------

type tokenRequest struct {
	Name   string         `json:"name"`
	Kind   sdk.SourceKind `json:"kind,omitempty"`
	Scopes []string       `json:"scopes"`
	TTL    sdk.Duration   `json:"ttl,omitempty"`
}

func (s *Server) knownScopes() map[string]bool {
	known := map[string]bool{sdk.ScopeRead: true, sdk.ScopeAdmin: true}
	for _, m := range s.engine.Registry().Manifests() {
		for _, sc := range m.Scopes {
			known[sc.Name] = true
		}
	}
	return known
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, caller store.Token) error {
	var req tokenRequest
	if err := decodeBody(r, &req); err != nil {
		return err
	}
	if req.Kind == "" {
		req.Kind = sdk.SourceManual
	}
	known := s.knownScopes()
	for _, sc := range req.Scopes {
		if !known[sc] {
			names := make([]string, 0, len(known))
			for k := range known {
				names = append(names, k)
			}
			sort.Strings(names)
			return sdk.Errorf(sdk.CodeInvalidArgs, "unknown scope %q; known scopes: %s", sc, strings.Join(names, ", "))
		}
	}
	if !store.ScopesWithin(req.Scopes, caller.Scopes) {
		return sdk.Errorf(sdk.CodeForbidden, "a token cannot grant scopes its creator does not have")
	}
	tok, secret, err := s.store.CreateToken(r.Context(), req.Name, req.Kind, req.Scopes, time.Duration(req.TTL))
	if err != nil {
		if errors.Is(err, store.ErrNameTaken) || errors.Is(err, store.ErrBadRequest) {
			return &sdk.Error{Code: sdk.CodeInvalidArgs, Message: err.Error()}
		}
		return err
	}
	writeJSON(w, http.StatusCreated, struct {
		store.Token
		Secret string `json:"secret"`
	}{tok, secret})
	return nil
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, _ store.Token) error {
	toks, err := s.store.ListTokens(r.Context())
	if err != nil {
		return err
	}
	if toks == nil {
		toks = []store.Token{}
	}
	writeJSON(w, http.StatusOK, toks)
	return nil
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, _ store.Token) error {
	err := s.store.RevokeToken(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		return sdk.Errorf(sdk.CodeNotFound, "no active token %q", r.PathValue("id"))
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// --- helpers --------------------------------------------------------------

func readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, sdk.Errorf(sdk.CodeInvalidArgs, "reading body: %v", err)
	}
	if len(b) > maxBody {
		return nil, sdk.Errorf(sdk.CodeInvalidArgs, "body is larger than %d bytes", maxBody)
	}
	return b, nil
}

func decodeBody(r *http.Request, v any) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return sdk.Errorf(sdk.CodeInvalidArgs, "body: %v", err)
	}
	return nil
}

func pathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}"))
		}
	}
	return out
}

// HTTPStatus maps an error code to its HTTP status.
func HTTPStatus(c sdk.Code) int {
	switch c {
	case sdk.CodeInvalidArgs:
		return http.StatusBadRequest
	case sdk.CodeUnauthorized:
		return http.StatusUnauthorized
	case sdk.CodeForbidden:
		return http.StatusForbidden
	case sdk.CodeNotFound:
		return http.StatusNotFound
	case sdk.CodeInstanceNotRunning, sdk.CodeLoopDetected:
		return http.StatusConflict
	case sdk.CodePreconditionFailed:
		return http.StatusPreconditionFailed
	case sdk.CodeModuleUnavailable:
		return http.StatusServiceUnavailable
	case sdk.CodeTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	se := sdk.AsError(err)
	if se.Code == sdk.CodeInternal {
		slog.Default().Error("API request failed", "path", r.URL.Path, "err", err)
	}
	writeJSON(w, HTTPStatus(se.Code), map[string]*sdk.Error{"error": se})
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code sdk.Code, msg string) {
	_ = r
	writeJSON(w, status, map[string]*sdk.Error{"error": {Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
