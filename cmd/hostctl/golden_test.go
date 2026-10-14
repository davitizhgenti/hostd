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
		"/v1/apps": `{"apps": [
			{"id":"firefox","name":"Firefox","source":"desktop","runner":{"type":"exec"},"surface":"window"},
			{"id":"jelly","name":"Jellyfin","source":"file","files":["/home/screen/.config/hostd/apps/jelly.toml"],"runner":{"type":"docker"},"surface":"background"}],
			"problems": [{"file":"bad.toml","error":"runner.type \"vm\" is not known"}]}`,
		"/v1/tokens": `[
			{"id":"tok_1","name":"admin","kind":"manual","scopes":["admin"],"created":"2026-10-01T10:00:00Z","last_used":"2026-10-14T09:30:00Z"},
			{"id":"tok_2","name":"phone","kind":"manual","scopes":["read","apps"],"created":"2026-10-02T10:00:00Z","expires":"2099-01-01T00:00:00Z"},
			{"id":"tok_3","name":"old","kind":"automation","scopes":["read"],"created":"2026-01-01T10:00:00Z","expires":"2026-02-01T00:00:00Z"},
			{"id":"tok_4","name":"gone","kind":"local","scopes":["read"],"created":"2026-01-01T10:00:00Z","revoked":"2026-03-01T00:00:00Z"}]`,
		"/v1/actions?limit=50": `[
			{"seq":3,"time":"2026-10-14T10:02:00Z","action":"act_3","type":"audio.volume.set","args":{"percent":70},"source":{"kind":"automation","name":"evening"},"status":"skipped","held_by":"manual","until":"2026-10-14T10:05:00Z"},
			{"seq":2,"time":"2026-10-14T10:01:00Z","action":"act_2","type":"app.start","args":{"id":"nope"},"source":{"kind":"manual","name":"phone"},"status":"failed","code":"not_found","reason":"no app \"nope\""},
			{"seq":1,"time":"2026-10-14T10:00:00Z","action":"act_1","type":"audio.volume.set","args":{"percent":40},"source":{"kind":"manual","name":"admin"},"status":"applied","version":3}]`,
		"/v1/rules": `{"rules": [
			{"name":"evening","on":"display.idle","do":{"scene":"dim"},"fired":2,"last_fired":"2026-10-14T10:00:00Z","last_result":"applied"},
			{"name":"quiet","on":"instance.started","do":{"action":"audio.mute.set"},"fired":0,"paused_until":"9999-01-01T00:00:00Z","paused_why":"paused by hand"}],
			"problems": [{"file":"rules.toml","error":"rule 3: needs on"}]}`,
		"/v1/scenes": `{"scenes": [
			{"id":"movie","name":"Movie night","audio":{"volume":40},"apps":{"running":["jelly"]}},
			{"id":"lights","name":"Lights only","do":[{"action":"lights.scene.set"}]}],
			"problems": []}`,
		"/v1/instances": `[
			{"id":"firefox","app":"firefox","runner":"exec","state":"running","pid":4242,"started":"2026-10-14T10:15:30Z"},
			{"id":"term","app":"term","runner":"exec","state":"exited","pid":4300,"started":"2026-10-14T10:00:00Z","ended":"2026-10-14T10:20:00Z","exit_code":1}]`,
	})
	for name, args := range map[string][]string{"windows": {"windows"}, "controllers": {"controllers"}, "ps": {"ps"},
		"apps": {"apps"}, "tokens": {"token", "list"}, "log": {"log"}, "rules": {"rules"}, "scenes": {"scenes"}} {
		out, errs, code := hostctl(t, args...)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, errs)
		}
		if errs != "" { // problems with files are reported on stderr, after the table
			out += "--- stderr\n" + errs
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
