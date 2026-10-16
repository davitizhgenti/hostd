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

	"github.com/davitizhgenti/hostd/sdk"
)

// fakeHostd records the actions sent and answers each type as told
// (default: applied, no data); /v1/version answers versions in turn.
type fakeHostd struct {
	mu       sync.Mutex
	actions  []map[string]any
	answers  map[string]string
	versions []string
	uploaded []byte
}

func newFakeHostd(t *testing.T) *fakeHostd {
	t.Helper()
	f := &fakeHostd{answers: map[string]string{}, versions: []string{`{"version":"v1","arch":"amd64"}`}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/manifests", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	mux.HandleFunc("POST /v1/actions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		f.mu.Lock()
		f.actions = append(f.actions, req)
		answer, ok := f.answers[req["type"].(string)]
		f.mu.Unlock()
		if !ok {
			answer = `{"action":"act_1","status":"applied"}`
		}
		_, _ = w.Write([]byte(answer))
	})
	mux.HandleFunc("GET /v1/audio", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"percent":40,"muted":false,"description":"GP107GL HDMI"}`))
	})
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		v := f.versions[0]
		if len(f.versions) > 1 {
			f.versions = f.versions[1:]
		}
		f.mu.Unlock()
		_, _ = w.Write([]byte(v))
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.uploaded = b
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"version":"v2","previous":"v1","restarting":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("HOSTD_URL", srv.URL)
	t.Setenv("HOSTD_TOKEN", "hostd_test")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	return f
}

func (f *fakeHostd) last(t *testing.T) (string, string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.actions) == 0 {
		t.Fatal("no action sent")
	}
	a := f.actions[len(f.actions)-1]
	args, _ := json.Marshal(a["args"])
	return a["type"].(string), string(args)
}

func TestVolumeAndMute(t *testing.T) {
	f := newFakeHostd(t)
	f.answers["audio.volume.set"] = `{"status":"applied","data":{"percent":35,"description":"GP107GL HDMI"}}`
	f.answers["audio.mute.set"] = `{"status":"applied","data":{"percent":35,"muted":true,"description":"GP107GL HDMI"}}`

	if out, _, code := hostctl(t, "volume"); code != 0 || out != "volume 40% on GP107GL HDMI\n" {
		t.Fatalf("volume: %d %q", code, out)
	}
	for arg, want := range map[string]string{"40": `{"percent":40}`, "-5": `{"percent":"-5"}`, "+5": `{"percent":"+5"}`} {
		out, errs, code := hostctl(t, "volume", arg)
		typ, args := f.last(t)
		if code != 0 || typ != "audio.volume.set" || args != want || out != "volume 35% on GP107GL HDMI\n" {
			t.Errorf("volume %s: %d %s %s %q %s", arg, code, typ, args, out, errs)
		}
	}
	if out, _, _ := hostctl(t, "volume", "1", "2"); !strings.Contains(out, "Usage:") {
		t.Errorf("two values: %q", out)
	}
	for arg, want := range map[string]string{"": `{"muted":"toggle"}`, "on": `{"muted":true}`, "off": `{"muted":false}`} {
		args := []string{"mute"}
		if arg != "" {
			args = append(args, arg)
		}
		out, _, code := hostctl(t, args...)
		_, sent := f.last(t)
		if code != 0 || sent != want || out != "volume 35% (muted) on GP107GL HDMI\n" {
			t.Errorf("mute %q: %d %s %q", arg, code, sent, out)
		}
	}
	if _, _, code := hostctl(t, "mute", "loud"); code != exitUsage {
		t.Errorf("mute loud: %d", code)
	}
	// Held by someone at the screen: said so, not an error.
	f.answers["audio.volume.set"] = `{"status":"skipped","held_by":"local","reason":"held"}`
	if out, _, code := hostctl(t, "volume", "90"); code != 0 || !strings.Contains(out, "local") {
		t.Errorf("held: %d %q", code, out)
	}
}

func TestFocus(t *testing.T) {
	f := newFakeHostd(t)
	if out, _, code := hostctl(t, "focus", "firefox", "--front"); code != 0 || out != "focused firefox\n" {
		t.Fatalf("focus: %d %q", code, out)
	}
	if typ, args := f.last(t); typ != "window.focus" || args != `{"front":true,"instance":"firefox"}` {
		t.Fatalf("sent %s %s", typ, args)
	}
	f.answers["window.focus"] = `{"status":"skipped","reason":"in_use"}`
	if out, _, _ := hostctl(t, "focus", "firefox"); !strings.Contains(out, "someone is using the screen") {
		t.Fatalf("in use: %q", out)
	}
}

func TestUpdatePushOverTheAPI(t *testing.T) {
	f := newFakeHostd(t)
	f.versions = []string{`{"version":"v1"}`, `{"version":"v2"}`}
	bin := filepath.Join(t.TempDir(), "hostd")
	_ = os.WriteFile(bin, []byte("a hostd build"), 0o755)
	out, errs, code := hostctl(t, "update", "push", bin)
	if code != 0 || !strings.Contains(out, "installed hostd v2 (was v1)") || !strings.Contains(out, "hostd v2 is running") {
		t.Fatalf("push: %d %q %q", code, out, errs)
	}
	if string(f.uploaded) != "a hostd build" {
		t.Fatalf("uploaded %q", f.uploaded)
	}
	// A build that does not start: systemd rolls back, and hostctl says so.
	f.versions = []string{`{"version":"v1"}`, `{"version":"v1","rolled_back_from":"v2"}`}
	if _, errs, code := hostctl(t, "update", "push", bin); code == 0 || !strings.Contains(errs, "did not start; systemd rolled back to v1") {
		t.Fatalf("rollback: %d %q", code, errs)
	}
	// Outside a checkout, without a binary: says what is missing.
	t.Chdir(t.TempDir())
	if _, errs, code := hostctl(t, "update", "push"); code != exitUsage || !strings.Contains(errs, "not in a hostd source checkout") {
		t.Fatalf("no binary: %d %q", code, errs)
	}
}

func TestConvert(t *testing.T) {
	for _, c := range []struct {
		typ, in, want string
		ok            bool
	}{
		{"integer", "40", "40", true},
		{"integer", "forty", "", false},
		{"number", "0.5", "0.5", true},
		{"number", "x", "", false},
		{"boolean", "true", "true", true},
		{"boolean", "yes", "", false},
		{"array", "a,b", `["a","b"]`, true},
		{"array", `["a b"]`, `["a b"]`, true},
		{"object", `{"a":1}`, `{"a":1}`, true},
		{"object", "a=1", "", false},
		{"string", "40", `"40"`, true},
		{"", "+5", `"+5"`, true},
		{"", "40", "40", true},
		{"", "hello", `"hello"`, true},
	} {
		got, err := convert("x", c.typ, c.in)
		if (err == nil) != c.ok || (c.ok && string(got) != c.want) {
			t.Errorf("%s %q: %s %v", c.typ, c.in, got, err)
		}
		if err != nil && sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%s %q: code %s", c.typ, c.in, sdk.CodeOf(err))
		}
	}
}

func TestLoginAddress(t *testing.T) {
	for in, want := range map[string]string{
		"core":                   "https://core:7300",
		"192.168.18.33":          "https://192.168.18.33:7300",
		"core:8000":              "https://core:8000",
		"http://core":            "http://core:7300",
		"fd00::1":                "https://[fd00::1]:7300",
		"unix:///run/hostd.sock": "unix:///run/hostd.sock",
		"https://[fd00::1]:7301": "https://[fd00::1]:7301",
	} {
		if got := loginAddress(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}
