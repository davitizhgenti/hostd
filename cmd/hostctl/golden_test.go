package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/modules/display"
	"github.com/davitizhgenti/hostd/sdk"
)

// fakeAPI serves what hostctl asks for: the manifests of the modules given,
// and canned answers to reads.
func fakeAPI(t *testing.T, manifests []sdk.Manifest, reads map[string]string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/manifests", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(manifests)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := reads[r.URL.RequestURI()]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no read ` + r.URL.RequestURI() + `"}}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("HOSTD_URL", strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("HOSTD_TOKEN", "hostd_test")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
}

// hostctl runs a command line and returns what it printed.
func hostctl(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(""), &out, &errb)
	return out.String(), errb.String(), code
}

// The output of the commands that print a read as a table, as it was before
// those moved into the manifest: a migration must keep every byte.
func TestGoldenTables(t *testing.T) {
	displayManifest := display.New(display.Options{}).Manifest()
	fakeAPI(t, []sdk.Manifest{displayManifest}, map[string]string{
		"/v1/windows": `[
			{"id":1,"instance":"firefox","app_id":"firefox","class":"","title":"Mozilla Firefox","workspace":"hostd:firefox","focused":true,"fullscreen":true},
			{"id":2,"instance":"","app_id":"","class":"Steam","title":"A very long window title that goes on well past forty characters","workspace":"1","focused":false,"fullscreen":false},
			{"id":3,"instance":"","app_id":"","class":"","title":"","workspace":"2","focused":false,"fullscreen":false}]`,
		"/v1/controllers": `[
			{"device":"/dev/input/event9","name":"Xbox","vendor":"045e","product":"028e","profile":"xbox","kind":"gamepad","buttons":["guide","start","select"]},
			{"device":"/dev/hidraw5","name":"DolphinBar","vendor":"057e","product":"0306","profile":"dolphinbar","kind":"raw","raw":true,"app":"dolphin"}]`,
		"/v1/instances": `[
			{"id":"firefox","app":"firefox","runner":"exec","state":"running","pid":4242,"started":"2026-10-14T10:15:30Z"},
			{"id":"term","app":"term","runner":"exec","state":"exited","pid":4300,"started":"2026-10-14T10:00:00Z","ended":"2026-10-14T10:20:00Z","exit_code":1}]`,
	})
	for name, args := range map[string][]string{"windows": {"windows"}, "controllers": {"controllers"}, "ps": {"ps"}} {
		out, errs, code := hostctl(t, args...)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, errs)
		}
		path := filepath.Join("testdata", "golden", name+".txt")
		if os.Getenv("UPDATE_GOLDEN") != "" {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (UPDATE_GOLDEN=1 writes it)", name, err)
		}
		if out != string(want) {
			t.Errorf("%s output changed:\n%s\nwant:\n%s", name, out, want)
		}
	}
	// An empty list.
	fakeAPI(t, []sdk.Manifest{displayManifest}, map[string]string{"/v1/windows": `[]`, "/v1/controllers": `[]`})
	for name, want := range map[string]string{"windows": "INSTANCE  APP ID  WORKSPACE  FOCUSED  FULLSCREEN  TITLE\n", "controllers": "no controllers connected\n"} {
		if out, _, code := hostctl(t, name); code != 0 || out != want {
			t.Errorf("empty %s: %q (exit %d), want %q", name, out, code, want)
		}
	}
}
