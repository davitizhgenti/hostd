package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// lamp is a fake module whose routes come only from its manifest.
type lamp struct {
	core       sdk.Core
	brightness map[string]int
}

func (l *lamp) Manifest() sdk.Manifest {
	idSchema := `{"type":"object","properties":{"id":{"type":"string"},"brightness":{"type":"integer","minimum":0,"maximum":100},"front":{"type":"boolean"}},"required":["id"]}`
	return sdk.Manifest{
		Name: "lamp", Version: "test", Owns: []string{"lamp.*"},
		Scopes: []sdk.ScopeSpec{{Name: "lamp"}, {Name: "lamp.front"}},
		Actions: []sdk.ActionSpec{
			{Type: "lamp.set", Schema: json.RawMessage(idSchema), Keys: []sdk.KeyTemplate{"lamp:{id}"}, Scope: "lamp",
				ArgScopes: map[string]string{"front": "lamp.front"},
				Route:     &sdk.Route{Method: "POST", Path: "/v1/lamps/{id}/brightness"}},
			{Type: "lamp.flash", Schema: json.RawMessage(idSchema), Scope: "lamp", Slow: true,
				Route: &sdk.Route{Method: "POST", Path: "/v1/lamps/{id}/flash"}},
			{Type: "lamp.fail", Schema: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}}}`), Scope: "lamp",
				Route: &sdk.Route{Method: "POST", Path: "/v1/lamps/fail"}},
		},
		Events: []sdk.EventSpec{{Type: "lamp.changed"}},
	}
}
func (l *lamp) Start(_ context.Context, c sdk.Core) error  { l.core = c; return nil }
func (l *lamp) Stop(context.Context) error                 { return nil }
func (l *lamp) Validate(context.Context, sdk.Action) error { return nil }
func (l *lamp) State(context.Context) (any, error)         { return map[string]any{"lamps": l.brightness}, nil }
func (l *lamp) Handle(_ context.Context, a sdk.Action) (sdk.Result, error) {
	var args struct {
		ID         string
		Brightness int
		Code       string
	}
	_ = a.DecodeArgs(&args)
	if a.Type == "lamp.fail" {
		return sdk.Result{}, sdk.Errorf(sdk.Code(args.Code), "asked to fail with %s", args.Code)
	}
	l.brightness[args.ID] = args.Brightness
	return sdk.Result{Data: json.RawMessage(`{"ok":true}`)}, nil
}

type env struct {
	srv   *Server
	http  *httptest.Server
	store *store.Store
	admin string
	lamp  *lamp
}

func setup(t *testing.T, opts Options) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg := core.NewRegistry()
	l := &lamp{brightness: map[string]int{}}
	if err := reg.Add(l); err != nil {
		t.Fatal(err)
	}
	eng := core.New(reg, core.Options{Audit: st})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	srv, err := New(eng, st, opts)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(ctx)
	ran := make(chan struct{})
	go func() { srv.Run(runCtx); close(ran) }()
	hs := httptest.NewServer(srv.Handler())
	admin, _, err := st.EnsureAdminToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		hs.Close()
		stopRun()
		<-ran
		_ = eng.Stop(ctx)
		_ = st.Close()
	})
	return &env{srv: srv, http: hs, store: st, admin: admin, lamp: l}
}

func (e *env) token(t *testing.T, name string, kind sdk.SourceKind, scopes ...string) string {
	t.Helper()
	_, secret, err := e.store.CreateToken(context.Background(), name, kind, scopes, 0)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

type resp struct {
	status int
	body   []byte
	header http.Header
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
}

func (r resp) errCode(t *testing.T) sdk.Code {
	t.Helper()
	var body struct{ Error *sdk.Error }
	r.json(t, &body)
	if body.Error == nil {
		t.Fatalf("no error object in %s", r.body)
	}
	return body.Error.Code
}

func (e *env) do(t *testing.T, method, path, token, body string) resp {
	t.Helper()
	req, _ := http.NewRequest(method, e.http.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, b, res.Header}
}

func TestFriendlyRouteFromManifest(t *testing.T) {
	e := setup(t, Options{})
	r := e.do(t, "POST", "/v1/lamps/desk/brightness", e.admin, `{"brightness": 40}`)
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var res sdk.Result
	r.json(t, &res)
	if res.Status != sdk.StatusApplied || res.Version != 1 || string(res.Data) != `{"ok":true}` {
		t.Fatalf("result = %+v", res)
	}
	if e.lamp.brightness["desk"] != 40 {
		t.Fatal("path parameter did not reach the module")
	}
	// Path and body disagreeing about the same argument is refused.
	r = e.do(t, "POST", "/v1/lamps/desk/brightness", e.admin, `{"id": "hall", "brightness": 1}`)
	if r.status != 400 || r.errCode(t) != sdk.CodeInvalidArgs {
		t.Fatalf("conflict: %d %s", r.status, r.body)
	}
	// No body at all is fine; the schema decides what is required.
	if r := e.do(t, "POST", "/v1/lamps/desk/brightness", e.admin, ``); r.status != 200 {
		t.Fatalf("empty body: %d %s", r.status, r.body)
	}
}

func TestGenericActionRoute(t *testing.T) {
	e := setup(t, Options{})
	r := e.do(t, "POST", "/v1/actions", e.admin, `{"type":"lamp.set","args":{"id":"hall","brightness":7}}`)
	if r.status != 200 || e.lamp.brightness["hall"] != 7 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	for body, want := range map[string]int{
		`{"args":{}}`:                           400, // no type
		`{"type":"lamp.set","args":{"id":1}}`:   400, // schema
		`{"type":"lamp.nope"}`:                  404,
		`{"type":"lamp.set","extra":1}`:         400, // unknown field in request
		`not json`:                              400,
		`{"type":"lamp.set","args":{"id":"x"}}`: 200,
	} {
		if r := e.do(t, "POST", "/v1/actions", e.admin, body); r.status != want {
			t.Errorf("%s: status %d, want %d (%s)", body, r.status, want, r.body)
		}
	}
}

func TestSlowActionReturns202(t *testing.T) {
	e := setup(t, Options{})
	r := e.do(t, "POST", "/v1/lamps/desk/flash", e.admin, ``)
	if r.status != 202 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var res sdk.Result
	r.json(t, &res)
	if res.Status != sdk.StatusAccepted || res.Action == "" {
		t.Fatalf("%+v", res)
	}
}

func TestErrorCodesMapToStatus(t *testing.T) {
	e := setup(t, Options{})
	for code, status := range map[sdk.Code]int{
		sdk.CodeInvalidArgs:        400,
		sdk.CodeNotFound:           404,
		sdk.CodeInstanceNotRunning: 409,
		sdk.CodePreconditionFailed: 412,
		sdk.CodeForbidden:          403,
		sdk.CodeTimeout:            504,
		sdk.CodeModuleUnavailable:  503,
		sdk.CodeLoopDetected:       409,
		sdk.CodeInternal:           500,
	} {
		r := e.do(t, "POST", "/v1/lamps/fail", e.admin, fmt.Sprintf(`{"code":%q}`, code))
		var body struct {
			Error struct {
				Code    sdk.Code `json:"code"`
				Message string   `json:"message"`
				Action  string   `json:"action"`
			} `json:"error"`
		}
		r.json(t, &body)
		if r.status != status || body.Error.Code != code || body.Error.Message == "" || body.Error.Action == "" {
			t.Errorf("%s: status %d body %s; want %d with code, message and action", code, r.status, r.body, status)
		}
	}
	// Unauthorized comes from the API itself.
	r := e.do(t, "GET", "/v1/version", "", "")
	if r.status != 401 || r.errCode(t) != sdk.CodeUnauthorized || r.header.Get("WWW-Authenticate") == "" {
		t.Fatalf("no token: %d %s", r.status, r.body)
	}
	if r := e.do(t, "GET", "/v1/nothing/here", e.admin, ""); r.status != 404 || r.errCode(t) != sdk.CodeNotFound {
		t.Fatalf("unknown route: %d %s", r.status, r.body)
	}
	if r := e.do(t, "POST", "/v1/actions", e.admin, `{"type":"lamp.set","args":{"id":"a"},"expect_version":5}`); r.status != 412 {
		t.Fatalf("stale expect_version: %d %s", r.status, r.body)
	}
}

func TestScopeMatrix(t *testing.T) {
	e := setup(t, Options{})
	tokens := map[string]string{
		"read":  e.token(t, "reader", sdk.SourceManual, "read"),
		"lamp":  e.token(t, "lamp-only", sdk.SourceManual, "lamp"),
		"both":  e.token(t, "phone", sdk.SourceManual, "read", "lamp"),
		"admin": e.admin,
	}
	routes := []struct {
		method, path, body string
		allowed            []string
	}{
		{"GET", "/v1/version", "", []string{"read", "both", "admin"}},
		{"GET", "/v1/manifests", "", []string{"read", "both", "admin"}},
		{"GET", "/v1/state/lamp", "", []string{"read", "both", "admin"}},
		{"GET", "/v1/actions", "", []string{"read", "both", "admin"}},
		{"POST", "/v1/lamps/a/brightness", `{"brightness":1}`, []string{"lamp", "both", "admin"}},
		{"POST", "/v1/actions", `{"type":"lamp.set","args":{"id":"a"}}`, []string{"lamp", "both", "admin"}},
		{"POST", "/v1/actions", `{"type":"lamp.set","args":{"id":"a","front":true}}`, []string{"admin"}},
		{"GET", "/v1/tokens", "", []string{"admin"}},
		{"POST", "/v1/tokens", `{"name":"x-%s","scopes":["read"]}`, []string{"admin"}},
	}
	for _, rt := range routes {
		for who, tok := range tokens {
			body := rt.body
			if strings.Contains(body, "%s") {
				body = fmt.Sprintf(body, who)
			}
			r := e.do(t, rt.method, rt.path, tok, body)
			allowed := false
			for _, a := range rt.allowed {
				allowed = allowed || a == who
			}
			if allowed && r.status >= 400 {
				t.Errorf("%s %s %s as %s: %d %s", rt.method, rt.path, rt.body, who, r.status, r.body)
			}
			if !allowed && (r.status != 403 || r.errCode(t) != sdk.CodeForbidden) {
				t.Errorf("%s %s %s as %s: %d, want 403 forbidden", rt.method, rt.path, rt.body, who, r.status)
			}
		}
	}
}

func TestActionSourceComesFromToken(t *testing.T) {
	e := setup(t, Options{})
	script := e.token(t, "evening.sh", sdk.SourceAutomation, "lamp", "read")
	r := e.do(t, "POST", "/v1/lamps/a/brightness", script, `{"brightness":5}`)
	var res sdk.Result
	r.json(t, &res)
	r = e.do(t, "GET", "/v1/actions?action="+res.Action, script, "")
	var recs []store.AuditRecord
	r.json(t, &recs)
	if len(recs) != 1 || recs[0].Source.Kind != sdk.SourceAutomation || recs[0].Source.Name != "evening.sh" ||
		!strings.HasPrefix(recs[0].Source.Token, "tok_") {
		t.Fatalf("audit = %+v", recs)
	}
	// A manual token's later change holds the lamp against this script.
	phone := e.token(t, "phone", sdk.SourceManual, "lamp")
	e.do(t, "POST", "/v1/lamps/a/brightness", phone, `{"brightness":30}`)
	r = e.do(t, "POST", "/v1/lamps/a/brightness", script, `{"brightness":70}`)
	r.json(t, &res)
	if r.status != 200 || res.Status != sdk.StatusSkipped || res.HeldBy != sdk.SourceManual {
		t.Fatalf("script after phone: %d %+v", r.status, res)
	}
}

func TestStateAndManifests(t *testing.T) {
	e := setup(t, Options{})
	e.do(t, "POST", "/v1/lamps/desk/brightness", e.admin, `{"brightness":9}`)
	r := e.do(t, "GET", "/v1/state/lamp", e.admin, "")
	if r.status != 200 || !bytes.Contains(r.body, []byte(`"desk":9`)) {
		t.Fatalf("state: %d %s", r.status, r.body)
	}
	if r := e.do(t, "GET", "/v1/state/nope", e.admin, ""); r.status != 404 {
		t.Fatalf("unknown module: %d", r.status)
	}
	var mans []sdk.Manifest
	e.do(t, "GET", "/v1/manifests", e.admin, "").json(t, &mans)
	if len(mans) != 1 || mans[0].Name != "lamp" || len(mans[0].Actions) != 3 {
		t.Fatalf("manifests = %+v", mans)
	}
	var ver map[string]any
	e.do(t, "GET", "/v1/version", e.admin, "").json(t, &ver)
	if ver["os"] != "linux" || ver["arch"] == "" || ver["version"] == "" {
		t.Fatalf("version = %v", ver)
	}
}

func TestTokenEndpoints(t *testing.T) {
	e := setup(t, Options{})
	r := e.do(t, "POST", "/v1/tokens", e.admin, `{"name":"phone","scopes":["read","lamp"]}`)
	if r.status != 201 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	var created struct {
		store.Token
		Secret string `json:"secret"`
	}
	r.json(t, &created)
	if created.Kind != sdk.SourceManual || !strings.HasPrefix(created.Secret, store.TokenPrefix) {
		t.Fatalf("created = %+v", created)
	}
	if r := e.do(t, "GET", "/v1/version", created.Secret, ""); r.status != 200 {
		t.Fatalf("new token does not work: %d", r.status)
	}
	// Listing never includes secrets.
	r = e.do(t, "GET", "/v1/tokens", e.admin, "")
	if bytes.Contains(r.body, []byte(created.Secret)) || bytes.Contains(r.body, []byte("secret")) {
		t.Fatalf("token list leaks secrets: %s", r.body)
	}
	for body, code := range map[string]sdk.Code{
		`{"name":"x","scopes":["lights"]}`:                 sdk.CodeInvalidArgs, // unknown scope
		`{"name":"phone","scopes":["read"]}`:               sdk.CodeInvalidArgs, // name taken
		`{"name":"","scopes":["read"]}`:                    sdk.CodeInvalidArgs,
		`{"name":"y","scopes":["read"],"kind":"external"}`: sdk.CodeInvalidArgs,
		`{"name":"z","scopes":["read"],"ttl":"forever"}`:   sdk.CodeInvalidArgs,
		`{"name":"s","scopes":["read"],"color":"blue"}`:    sdk.CodeInvalidArgs,
	} {
		if r := e.do(t, "POST", "/v1/tokens", e.admin, body); r.errCode(t) != code {
			t.Errorf("%s: %d %s", body, r.status, r.body)
		}
	}
	// A short-lived script token.
	r = e.do(t, "POST", "/v1/tokens", e.admin, `{"name":"run-1","kind":"automation","scopes":["lamp"],"ttl":"60s"}`)
	var short store.Token
	r.json(t, &short)
	if short.Expires == nil || short.Kind != sdk.SourceAutomation {
		t.Fatalf("short-lived token = %+v", short)
	}

	if r := e.do(t, "DELETE", "/v1/tokens/"+created.ID, e.admin, ""); r.status != 204 {
		t.Fatalf("revoke: %d %s", r.status, r.body)
	}
	if r := e.do(t, "GET", "/v1/version", created.Secret, ""); r.status != 401 {
		t.Fatalf("revoked token still works: %d", r.status)
	}
	if r := e.do(t, "DELETE", "/v1/tokens/"+created.ID, e.admin, ""); r.status != 404 {
		t.Fatalf("second revoke: %d", r.status)
	}
}

func TestAdminTokenFileDeletedOnFirstUse(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hostd-admin-token")
	e := setup(t, Options{AdminTokenFile: file})
	if err := os.WriteFile(file, []byte(e.admin), 0o600); err != nil {
		t.Fatal(err)
	}
	phone := e.token(t, "phone", sdk.SourceManual, "read")
	e.do(t, "GET", "/v1/version", phone, "")
	if _, err := os.Stat(file); err != nil {
		t.Fatal("deleted by another token's use")
	}
	e.do(t, "GET", "/v1/version", e.admin, "")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("admin token file still there: %v", err)
	}
}

func TestRequestID(t *testing.T) {
	e := setup(t, Options{})
	r := e.do(t, "GET", "/v1/version", e.admin, "")
	if !strings.HasPrefix(r.header.Get("X-Request-ID"), "req_") {
		t.Fatalf("X-Request-ID = %q", r.header.Get("X-Request-ID"))
	}
	req, _ := http.NewRequest("GET", e.http.URL+"/v1/version", nil)
	req.Header.Set("Authorization", "Bearer "+e.admin)
	req.Header.Set("X-Request-ID", "laptop-42")
	res, _ := http.DefaultClient.Do(req)
	res.Body.Close()
	if res.Header.Get("X-Request-ID") != "laptop-42" {
		t.Fatalf("caller's request ID not kept: %q", res.Header.Get("X-Request-ID"))
	}
}

func TestSourceCheck(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:5000":                 true,
		"[::1]:5000":                     true,
		"10.1.2.3:5000":                  true,
		"172.16.0.9:5000":                true,
		"172.31.255.1:5000":              true,
		"192.168.18.60:5000":             true,
		"169.254.1.1:5000":               true,
		"[fe80::1%eth0]:5000":            true,
		"[fd00::1]:5000":                 true,
		"[::ffff:192.168.1.5]:5000":      true,
		"8.8.8.8:5000":                   false,
		"172.32.0.1:5000":                false,
		"[::ffff:8.8.8.8]:5000":          false,
		"[2001:db8::1]:5000":             false,
		"[2a00:1450:4001:80b::200e]:443": false,
		"garbage":                        false,
	} {
		if got := allowedPeer(addr); got != ok {
			t.Errorf("allowedPeer(%s) = %v, want %v", addr, got, ok)
		}
	}
	// The middleware answers 403 before authentication.
	h := privateOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) }))
	for addr, want := range map[string]int{"8.8.8.8:1": 403, "192.168.1.2:1": 299} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/version", nil)
		req.RemoteAddr = addr
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", addr, rec.Code, want)
		}
	}
}

func TestRouteConflictIsAnError(t *testing.T) {
	reg := core.NewRegistry()
	m := &conflicting{}
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	eng := core.New(reg, core.Options{})
	_, err := New(eng, nil, Options{})
	if err == nil || !strings.Contains(err.Error(), "GET /v1/version") {
		t.Fatalf("err = %v", err)
	}
	_ = eng.Stop(context.Background())
}

type conflicting struct{}

func (conflicting) Manifest() sdk.Manifest {
	return sdk.Manifest{Name: "clash", Version: "test", Owns: []string{"clash.*"},
		Actions: []sdk.ActionSpec{{Type: "clash.x", Scope: "read", Route: &sdk.Route{Method: "GET", Path: "/v1/version"}}}}
}
func (conflicting) Start(context.Context, sdk.Core) error                  { return nil }
func (conflicting) Stop(context.Context) error                             { return nil }
func (conflicting) Validate(context.Context, sdk.Action) error             { return nil }
func (conflicting) Handle(context.Context, sdk.Action) (sdk.Result, error) { return sdk.Result{}, nil }

// --- events ---------------------------------------------------------------

func (e *env) dial(t *testing.T, query string, header http.Header, protocols ...string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.http.URL, "http") + "/v1/events" + query
	return websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header, Subprotocols: protocols})
}

func readEvent(t *testing.T, c *websocket.Conn) sdk.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var ev sdk.Event
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestEventStreamFilterAndAuth(t *testing.T) {
	e := setup(t, Options{})
	hdr := http.Header{"Authorization": {"Bearer " + e.admin}}
	c, _, err := e.dial(t, "?type=lamp.*", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	e.do(t, "POST", "/v1/lamps/a/brightness", e.admin, `{"brightness":1}`) // only action.* events
	e.lamp.core.Emit(sdk.Event{Type: "lamp.changed", Data: json.RawMessage(`{"id":"a"}`)})
	if ev := readEvent(t, c); ev.Type != "lamp.changed" {
		t.Fatalf("got %s through a lamp.* filter", ev.Type)
	}

	// Token as a subprotocol, the way a browser sends it. The server
	// answers with hostd.v1 and never echoes the token.
	c2, res, err := e.dial(t, "", nil, wsProtocol, wsTokenPrefix+e.admin)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Header.Get("Sec-WebSocket-Protocol"); got != wsProtocol || c2.Subprotocol() != wsProtocol {
		t.Fatalf("server chose %q", got)
	}
	c2.CloseNow()

	for name, tc := range map[string]struct {
		header    http.Header
		protocols []string
	}{
		"no token":      {nil, nil},
		"bad header":    {http.Header{"Authorization": {"Bearer hostd_nope"}}, nil},
		"bad protocol":  {nil, []string{wsProtocol, wsTokenPrefix + "hostd_nope"}},
		"no read scope": {http.Header{"Authorization": {"Bearer " + e.token(t, "lamp-only", sdk.SourceManual, "lamp")}}, nil},
	} {
		_, res, err := e.dial(t, "", tc.header, tc.protocols...)
		if err == nil {
			t.Errorf("%s: connected", name)
			continue
		}
		if res == nil || (res.StatusCode != 401 && res.StatusCode != 403) {
			t.Errorf("%s: response %v", name, res)
		}
	}
}

func TestEventStreamReplaySince(t *testing.T) {
	e := setup(t, Options{})
	hdr := http.Header{"Authorization": {"Bearer " + e.admin}}
	c, _, err := e.dial(t, "?type=lamp.*", hdr)
	if err != nil {
		t.Fatal(err)
	}
	e.lamp.core.Emit(sdk.Event{Type: "lamp.changed", Data: json.RawMessage(`{"n":1}`)})
	first := readEvent(t, c)
	c.CloseNow() // client drops off...

	for n := 2; n <= 4; n++ { // ...and misses three events
		e.lamp.core.Emit(sdk.Event{Type: "lamp.changed", Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))})
	}
	waitRing(t, e, 4)

	c, _, err = e.dial(t, "?type=lamp.*&since="+first.ID, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	for n := 2; n <= 4; n++ {
		ev := readEvent(t, c)
		if string(ev.Data) != fmt.Sprintf(`{"n":%d}`, n) {
			t.Fatalf("replay #%d = %s", n, ev.Data)
		}
	}
	// Live events continue after the replay, with no duplicates.
	e.lamp.core.Emit(sdk.Event{Type: "lamp.changed", Data: json.RawMessage(`{"n":5}`)})
	if ev := readEvent(t, c); string(ev.Data) != `{"n":5}` {
		t.Fatalf("after replay got %s", ev.Data)
	}

	// A since the server no longer has gets a gap notice, then the window.
	c3, _, err := e.dial(t, "?type=lamp.*&since=evt_GONE", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer c3.CloseNow()
	if ev := readEvent(t, c3); ev.Type != "bus.gap" {
		t.Fatalf("want bus.gap, got %s", ev.Type)
	}
}

func waitRing(t *testing.T, e *env, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		evs, _ := e.srv.ring.since("")
		count := 0
		for _, ev := range evs {
			if ev.Type == "lamp.changed" {
				count++
			}
		}
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ring has %d lamp events, want %d", count, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRing(t *testing.T) {
	r := newRing(3)
	for i := 1; i <= 5; i++ {
		r.add(sdk.Event{ID: fmt.Sprintf("evt_%d", i)})
	}
	got, found := r.since("evt_3")
	if !found || len(got) != 2 || got[0].ID != "evt_4" {
		t.Fatalf("since(evt_3) = %v %v", got, found)
	}
	got, found = r.since("evt_1") // fell out of the window
	if found || len(got) != 3 {
		t.Fatalf("since(evt_1) = %v %v", got, found)
	}
}

func TestServeOnUnixSocket(t *testing.T) {
	e := setup(t, Options{})
	sock := filepath.Join(t.TempDir(), "hostd.sock")
	if err := os.WriteFile(sock, nil, 0o644); err != nil { // a stale file from a crash
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- e.srv.Serve(ctx, sock, "127.0.0.1:0") }()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	deadline := time.Now().Add(5 * time.Second)
	var res *http.Response
	for {
		req, _ := http.NewRequest("GET", "http://hostd/v1/version", nil)
		req.Header.Set("Authorization", "Bearer "+e.admin)
		var err error
		if res, err = client.Do(req); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket never answered: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatalf("Serve after cancel: %v", err)
	}
	client.CloseIdleConnections()
}

func TestServeFailsOnBusyAddress(t *testing.T) {
	e := setup(t, Options{})
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	sock := filepath.Join(t.TempDir(), "hostd.sock")
	if err := e.srv.Serve(context.Background(), sock, ln.Addr().String()); err == nil {
		t.Fatal("Serve on a busy port succeeded")
	}
}
