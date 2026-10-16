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

	"github.com/coder/websocket"

	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/sdk"
)

// runAPI is a hostd with the apps module's manifest that answers app.run
// with the instance run-tests, which ends with exit status 1. With events,
// its stream replays an earlier run-tests that ended (to be ignored), then
// this one; without, hostctl asks for the instance until it has ended.
type runAPI struct {
	mu      sync.Mutex
	actions []map[string]any
	polls   int
}

func newRunAPI(t *testing.T, events bool) *runAPI {
	t.Helper()
	api := &runAPI{}
	manifests := []sdk.Manifest{apps.New(apps.Options{}).Manifest()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/manifests", func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(manifests) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		api.mu.Lock()
		api.actions = append(api.actions, req)
		api.mu.Unlock()
		_, _ = w.Write([]byte(`{"action":"act_7","status":"applied","data":{"instance":"run-tests","state":"running"}}`))
	})
	ended := `{"id":"run-tests","app":"run-tests","state":"failed","ended":"2026-10-16T10:00:00Z","exit_code":1}`
	mux.HandleFunc("GET /v1/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		api.polls++
		n := api.polls
		api.mu.Unlock()
		if n < 3 {
			_, _ = w.Write([]byte(`{"id":"run-tests","app":"run-tests","state":"running"}`))
			return
		}
		_, _ = w.Write([]byte(ended))
	})
	if events {
		mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("since") != "start" {
				t.Errorf("events not replayed: %s", r.URL.RawQuery)
			}
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hostd.v1"}})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			for _, ev := range []string{
				`{"id":"ev1","type":"instance.exited","action":"act_3","data":{"id":"run-tests","state":"exited","ended":"2026-10-16T09:00:00Z","exit_code":0}}`,
				`{"id":"ev2","type":"instance.starting","action":"act_7","data":{"id":"run-tests","state":"starting"}}`,
				`{"id":"ev3","type":"instance.started","action":"act_7","data":{"id":"run-tests","state":"running"}}`,
				`{"id":"ev4","type":"instance.failed","data":` + ended + `}`,
			} {
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(ev))
			}
			<-r.Context().Done()
		})
	}
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

func TestRunWaitsAndExitsWithTheCommandsStatus(t *testing.T) {
	for _, events := range []bool{true, false} {
		api := newRunAPI(t, events)
		out, errs, code := hostctl(t, "run", "-b", "--name", "tests", "--env", "CI=1", "--wait", "--", "pytest", "-q", "--maxfail=1")
		if code != 1 || out != "collected 2 items\n1 failed, 1 passed\n" || errs != "" {
			t.Fatalf("events %v: exit %d\n%s\n%s", events, code, out, errs)
		}
		got := api.actions[0]
		args, _ := json.Marshal(got["args"])
		if got["type"] != "app.run" || string(args) != `{"background":true,"command":["pytest","-q","--maxfail=1"],"env":{"CI":"1"},"name":"tests"}` {
			t.Fatalf("sent %v %s", got["type"], args)
		}
		// With the stream, the instance is never asked for: its end comes as an event.
		if events && api.polls != 0 {
			t.Errorf("polled %d times with an event stream", api.polls)
		}
	}
}

func TestRunWithoutWait(t *testing.T) {
	api := newRunAPI(t, false)
	out, errs, code := hostctl(t, "run", "--fullscreen=false", "foot", "-e", "htop")
	if code != 0 || out != "started run-tests (running)\n" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	args, _ := json.Marshal(api.actions[0]["args"])
	if string(args) != `{"command":["foot","-e","htop"],"fullscreen":false}` {
		t.Fatalf("sent %s", args)
	}
	if _, errs, code := hostctl(t, "run", "--env", "nope", "--", "ls"); code != exitUsage || !strings.Contains(errs, "key=value") {
		t.Fatalf("bad --env: exit %d %s", code, errs)
	}
	if _, _, code := hostctl(t, "run"); code != exitUsage {
		t.Fatalf("no command: exit %d", code)
	}
}

func TestStartAndStopFromTheManifest(t *testing.T) {
	api := newRunAPI(t, false)
	out, errs, code := hostctl(t, "start", "show", "--arg", "file=/home/screen/plot.png", "--front")
	if code != 0 || out != "started run-tests (running)\n" {
		t.Fatalf("exit %d\n%s\n%s", code, out, errs)
	}
	args, _ := json.Marshal(api.actions[0]["args"])
	if api.actions[0]["type"] != "app.start" || string(args) != `{"args":{"file":"/home/screen/plot.png"},"front":true,"id":"show"}` {
		t.Fatalf("sent %s", args)
	}
}

func TestDoneLines(t *testing.T) {
	done := []string{"{instance} is already running if already_running", "started {instance} ({state})"}
	for data, want := range map[string]string{
		`{"instance":"tabs","state":"running","already_running":true}`:  "tabs is already running",
		`{"instance":"tabs","state":"starting"}`:                        "started tabs (starting)",
		`{"instance":"tabs","state":"running","already_running":false}`: "started tabs (running)",
	} {
		if got, ok := doneLine(done, json.RawMessage(data)); !ok || got != want {
			t.Errorf("%s: %q", data, got)
		}
	}
	if _, ok := doneLine(nil, json.RawMessage(`{}`)); ok {
		t.Error("no lines, a line")
	}
}

// An argument that is a document (an app file) can come from a file or
// from standard input.
func TestDocumentArguments(t *testing.T) {
	api := newRunAPI(t, false)
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
