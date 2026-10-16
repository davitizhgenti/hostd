package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/sdk"
)

// runAPI answers app.run and the instance it starts, which ends with exit
// status code after polls polls; it records the actions sent.
type runAPI struct {
	mu      sync.Mutex
	actions []map[string]any
	polls   int
}

func newRunAPI(t *testing.T, code int, polls int, manifests []sdk.Manifest) *runAPI {
	t.Helper()
	api := &runAPI{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/manifests", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(manifests) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		api.mu.Lock()
		api.actions = append(api.actions, req)
		api.mu.Unlock()
		_, _ = w.Write([]byte(`{"action":"act_1","status":"applied","data":{"instance":"run-tests","state":"running"}}`))
	})
	mux.HandleFunc("GET /v1/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.polls++
		n := api.polls
		api.mu.Unlock()
		if r.PathValue("id") != "run-tests" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if n < polls {
			_, _ = w.Write([]byte(`{"id":"run-tests","app":"run-tests","state":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"run-tests","app":"run-tests","state":"failed","ended":"2026-10-16T10:00:00Z","exit_code":` + itoa(code) + `}`))
	})
	mux.HandleFunc("GET /v1/instances/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("plain") != "true" {
			t.Errorf("logs not asked plain: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"instance":"run-tests","lines":["collected 2 items","1 failed, 1 passed"]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("HOSTD_URL", srv.URL)
	t.Setenv("HOSTD_TOKEN", "hostd_test")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return api
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRunWaitsAndExitsWithTheCommandsStatus(t *testing.T) {
	api := newRunAPI(t, 1, 2, nil)
	out, errs, code := hostctl(t, "run", "--background", "--name", "tests", "--env", "CI=1", "--wait", "--", "pytest", "-q", "--maxfail=1")
	if code != 1 || out != "collected 2 items\n1 failed, 1 passed\n" || errs != "" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	got := api.actions[0]
	args, _ := json.Marshal(got["args"])
	if got["type"] != "app.run" || string(args) != `{"background":true,"command":["pytest","-q","--maxfail=1"],"env":{"CI":"1"},"name":"tests"}` {
		t.Fatalf("sent %v %s", got["type"], args)
	}
}

func TestRunWithoutWait(t *testing.T) {
	newRunAPI(t, 0, 1, nil)
	out, errs, code := hostctl(t, "run", "--windowed", "foot")
	if code != 0 || out != "started run-tests (running)\n" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	if _, errs, code := hostctl(t, "run", "--env", "nope", "--", "ls"); code != exitUsage || !strings.Contains(errs, "NAME=value") {
		t.Fatalf("bad --env: exit %d %s", code, errs)
	}
}

// An argument that is a document (an app file) can come from a file or
// from standard input.
func TestDocumentArguments(t *testing.T) {
	m := apps.New(apps.Options{}).Manifest()
	api := newRunAPI(t, 0, 1, []sdk.Manifest{m})
	file := filepath.Join(t.TempDir(), "results.toml")
	body := "name = \"Results\"\nrunner = { type = \"url\", url = \"http://laptop:8000/\" }\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, errs, code := hostctl(t, "app", "define", "results", "@"+file); code != 0 {
		t.Fatalf("exit %d %s", code, errs)
	}
	args, _ := api.actions[0]["args"].(map[string]any)
	if api.actions[0]["type"] != "app.define" || args["id"] != "results" || args["file"] != body {
		t.Fatalf("sent %v", api.actions[0])
	}
	if _, errs, code := hostctl(t, "app", "define", "results", "@/no/such/file"); code != exitUsage {
		t.Fatalf("missing file: exit %d %s", code, errs)
	}
}
