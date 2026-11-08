package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/core/resources"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
)

type noteSpec struct {
	Text string `json:"text"`
}

// notesEnv serves the API over a module that owns the kind "note".
type notesEnv struct {
	*env
	root  string
	mu    sync.Mutex
	shown []string
}

func notesSetup(t *testing.T) *notesEnv {
	t.Helper()
	ctx := context.Background()
	n := &notesEnv{root: t.TempDir()}
	m := kit.New("notes", "1").Owns("notes.*").Scope("notes", "Show notes")
	kit.Action(m, kit.Spec{Type: "notes.show", Scope: "notes"},
		func(_ context.Context, _ kit.Ctx, a struct {
			Name string `json:"name"`
			Loud bool   `json:"loud,omitempty"`
		}) (struct{}, error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			n.shown = append(n.shown, a.Name)
			return struct{}{}, nil
		})
	kit.Kind(m, kit.KindSpec{Kind: "note", Plural: "notes", Description: "A note",
		Verbs: []sdk.VerbSpec{{Name: "show", Description: "Show it", Action: "notes.show"}}},
		func(_ context.Context, _ kit.Ctx, r kit.Res[noteSpec]) (sdk.ResourceStatus, error) {
			if r.Spec.Text == "poison" {
				return sdk.ResourceStatus{}, errors.New("no poison")
			}
			return sdk.ResourceStatus{Conditions: []sdk.Condition{{Type: sdk.ConditionReady, Status: sdk.ConditionTrue}}}, nil
		}, nil)

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	reg := core.NewRegistry()
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	eng := core.New(reg, core.Options{Audit: st})
	runCtx, cancel := context.WithCancel(ctx)
	if err := eng.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	rs := resources.New(eng, resources.Options{Root: n.root, Debounce: 20 * time.Millisecond})
	if err := rs.Start(runCtx); err != nil {
		t.Fatal(err)
	}
	srv, err := New(eng, st, Options{Resources: rs})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	admin, _, err := st.EnsureAdminToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { hs.Close(); cancel(); _ = eng.Stop(ctx); _ = st.Close() })
	n.env = &env{srv: srv, http: hs, store: st, admin: admin}
	return n
}

func TestKindRoutes(t *testing.T) {
	n := notesSetup(t)
	const path = "/v1/notes/hello"

	if r := n.do(t, "GET", "/v1/notes", n.admin, ""); r.status != 200 || strings.TrimSpace(string(r.body)) != "[]" {
		t.Fatalf("empty list: %d %s", r.status, r.body)
	}
	if r := n.do(t, "GET", path, n.admin, ""); r.status != 404 {
		t.Fatalf("missing: %d %s", r.status, r.body)
	}

	// PUT from TOML and from JSON; the check writes nothing.
	if r := n.do(t, "PUT", path+"?check=1", n.admin, `text = "hi"`); r.status != 200 {
		t.Fatalf("check: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(n.root, "notes", "hello.toml")); err == nil {
		t.Fatal("check wrote a file")
	}
	r := n.do(t, "PUT", path, n.admin, `text = "hi"`)
	if r.status != 200 {
		t.Fatalf("put: %d %s", r.status, r.body)
	}
	var it resources.Item
	r.json(t, &it)
	if it.Name != "hello" || it.Generation != 1 || !it.Status.Ready() || string(it.Spec) != `{"text":"hi"}` {
		t.Fatalf("item = %+v", it)
	}
	if r := n.do(t, "PUT", path, n.admin, `{"text":"hi again"}`); r.status != 200 {
		t.Fatalf("put json: %d %s", r.status, r.body)
	}

	// Reads: the list, the item, the file as written.
	var list []resources.Item
	n.do(t, "GET", "/v1/notes", n.admin, "").json(t, &list)
	if len(list) != 1 || list[0].Generation != 2 {
		t.Fatalf("list = %+v", list)
	}
	r = n.do(t, "GET", path+"/file", n.admin, "")
	if r.status != 200 || !strings.Contains(string(r.body), `text = "hi again"`) || !strings.Contains(r.header.Get("Content-Type"), "toml") {
		t.Fatalf("file: %d %s %v", r.status, r.body, r.header)
	}

	// Refusals: the module's, the schema's, the syntax, the name.
	for name, c := range map[string]struct{ path, body string }{
		"module": {path, `text = "poison"`}, "schema": {path, `text = 5`},
		"syntax": {path, `text = `}, "name": {"/v1/notes/Bad_Name", `text = "x"`},
	} {
		if r := n.do(t, "PUT", c.path, n.admin, c.body); r.status != 400 || r.errCode(t) != sdk.CodeInvalidArgs {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}

	// The verb sends the action with the name filled in.
	r = n.do(t, "POST", path+"/show", n.admin, "")
	if r.status != 200 {
		t.Fatalf("verb: %d %s", r.status, r.body)
	}
	n.mu.Lock()
	shown := append([]string(nil), n.shown...)
	n.mu.Unlock()
	if len(shown) != 1 || shown[0] != "hello" {
		t.Fatalf("shown = %v", shown)
	}
	if r := n.do(t, "POST", path+"/show", n.admin, `{"name":"other"}`); r.status != 400 {
		t.Fatalf("a body naming another resource: %d %s", r.status, r.body)
	}

	// Delete.
	if r := n.do(t, "DELETE", path, n.admin, ""); r.status != 204 {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	if r := n.do(t, "DELETE", path, n.admin, ""); r.status != 404 {
		t.Fatalf("second delete: %d", r.status)
	}
}

func TestKindRoutesNeedTheRightScope(t *testing.T) {
	n := notesSetup(t)
	reader := n.token(t, "reader", sdk.SourceManual, sdk.ScopeRead)
	notes := n.token(t, "notes", sdk.SourceManual, sdk.ScopeRead, "notes")
	if r := n.do(t, "PUT", "/v1/notes/x", n.admin, `text = "x"`); r.status != 200 {
		t.Fatal(r.status)
	}
	for _, path := range []string{"/v1/notes", "/v1/notes/x"} {
		if r := n.do(t, "GET", path, reader, ""); r.status != 200 {
			t.Errorf("reading with read scope: %d", r.status)
		}
	}
	// Writing needs admin (the kind's default); the verb needs the action's scope.
	if r := n.do(t, "PUT", "/v1/notes/y", notes, `text = "y"`); r.status != http.StatusForbidden {
		t.Errorf("put with a notes token: %d", r.status)
	}
	if r := n.do(t, "DELETE", "/v1/notes/x", reader, ""); r.status != http.StatusForbidden {
		t.Errorf("delete with read scope: %d", r.status)
	}
	if r := n.do(t, "POST", "/v1/notes/x/show", reader, ""); r.status != http.StatusForbidden {
		t.Errorf("verb without its scope: %d", r.status)
	}
	if r := n.do(t, "POST", "/v1/notes/x/show", notes, ""); r.status != 200 {
		t.Errorf("verb with its scope: %d", r.status)
	}
	if r := n.do(t, "GET", "/v1/notes", "", ""); r.status != http.StatusUnauthorized {
		t.Errorf("no token: %d", r.status)
	}
}

func TestKindsAreInTheOpenAPI(t *testing.T) {
	n := notesSetup(t)
	r := n.do(t, "GET", "/v1/openapi.json", n.admin, "")
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	r.json(t, &doc)
	for path, methods := range map[string][]string{
		"/v1/notes":             {"get"},
		"/v1/notes/{name}":      {"get", "put", "delete"},
		"/v1/notes/{name}/file": {"get"},
		"/v1/notes/{name}/show": {"post"},
	} {
		for _, m := range methods {
			if _, ok := doc.Paths[path][m]; !ok {
				t.Errorf("%s %s missing from the OpenAPI document", strings.ToUpper(m), path)
			}
		}
	}
}
