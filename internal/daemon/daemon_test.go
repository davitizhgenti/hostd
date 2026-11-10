package daemon

import (
	"bytes"
	"context"
	"errors"
	"github.com/davitizhgenti/hostd/core/store"
	"github.com/davitizhgenti/hostd/sdk"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/client"
	"github.com/davitizhgenti/hostd/modules/apps"
	"github.com/davitizhgenti/hostd/modules/display"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hostd.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	c, err := LoadConfig(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil || !reflect.DeepEqual(c.Modules, defaultModules()) || c.Listen != nil {
		t.Fatalf("missing file: %+v %v", c, err)
	}

	c, err = LoadConfig(writeConfig(t, `
modules = ["apps"]
listen = ""
hold_window = "90s"
[hold_windows]
"audio." = "1m"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Modules, []string{"apps"}) || c.Listen == nil || *c.Listen != "" ||
		time.Duration(c.HoldWindow) != 90*time.Second || c.holdWindows()["audio."] != time.Minute {
		t.Fatalf("config = %+v", c)
	}

	// Each module's own section; display checks its key bindings.
	c, err = LoadConfig(writeConfig(t, `
[display]
idle_after = "10m"
hold = "1s"
[display.keys]
"F1" = "display.menu"
"Super+Q" = ""
[display.buttons]
guide = "window.back"
[apps]
browser = ["firefox", "{url}"]
`))
	if err != nil {
		t.Fatal(err)
	}
	confs, err := moduleConfigs(c)
	if err != nil {
		t.Fatalf("module settings: %v", err)
	}
	d := confs["display"].(*display.Config)
	keys, buttons, _ := display.Bindings(d.Keys, d.Buttons)
	if _, ok := keys["Super+Q"]; ok || keys["F1"] != "display.menu" || keys["Super+Tab"] != "window.next" ||
		buttons["guide"] != "window.back" || time.Duration(d.IdleAfter) != 10*time.Minute || time.Duration(d.Hold) != time.Second {
		t.Fatalf("display: %+v %v %v", d, keys, buttons)
	}
	if a := confs["apps"].(*apps.Config); !reflect.DeepEqual(a.Browser, []string{"firefox", "{url}"}) {
		t.Fatalf("apps: %+v", a)
	}

	for body, want := range map[string]string{
		"[input.keys]\n\"F2\" = \"display.menu\"":    "input is now [display]: hold, [display.keys] and [display.buttons]",
		"idle_after = \"7m\"":                        "idle_after is now [display] idle_after",
		`gamescope = ["gamescope"]`:                  "gamescope is now [apps] gamescope",
		"[display.keys]\n\"F2\" = \"window.fly\"":    `[display] keys: F2: "window.fly" cannot be bound`,
		"[display.buttons]\nturbo = \"window.next\"": `[display] buttons: button "turbo"`,
		"[display]\nmice = 1":                        "unknown key(s): display.mice",
		"[display]\nidle_after = \"-1m\"":            "idle_after is negative",
		"[display]\nidle_after = \"soon\"":           "not a duration",
		"[audio]\nvolume = 5":                        "the audio module has no settings",
		"[api]\ntls = false":                         "insecure_http",
		"[api]\nallow = [\"tailscale\"]":             "not a network",
		"[api]\nnames = [\"my core\"]":               "not a host name",
		"[api]\nport = 1":                            "unknown key(s): api.port",
		`modules = ["apps", "lights"]`:               `no module "lights"`,
		`modules = ["apps", "apps"]`:                 "listed twice",
		`listn = ":7300"`:                            "unknown key(s): listn",
		`hold_window = "three minutes"`:              "not a duration",
		`hold_window = "-1m"`:                        "negative",
		`modules = "apps"`:                           "",
		"[hold_windows]\n\"audio.\" = 5":             "",
		`this is not toml`:                           "",
		"[external.lights]\ncommand = [\"x\"]":       "is now a file in modules/",
	} {
		_, err := LoadConfig(writeConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
}

// runtimeDir returns a short directory: unix socket paths are limited.
func runtimeDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "hrt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// syncBuffer is a bytes.Buffer safe for hostd's concurrent log writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRunHeadless(t *testing.T) {
	rt := runtimeDir(t)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "") // never touch the machine's real session bus
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg := writeConfig(t, `modules = ["apps"]`+"\n"+`listen = ""`)
	ctx, cancel := context.WithCancel(context.Background())
	var stderr syncBuffer
	exit := make(chan int, 1)
	go func() {
		exit <- Run(ctx, []string{"-config", cfg, "-state", t.TempDir(), "-runtime", rt}, &stderr)
	}()

	tokenFile := filepath.Join(rt, "hostd-admin-token")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(tokenFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("hostd did not start:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	token, _ := os.ReadFile(tokenFile)
	c, err := client.New("unix://"+filepath.Join(rt, "hostd.sock"), strings.TrimSpace(string(token)))
	if err != nil {
		t.Fatal(err)
	}
	mans, err := c.Manifests(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(mans) != 2 || mans[0].Name != "hostd" || mans[1].Name != "apps" {
		t.Fatalf("modules loaded: %+v", mans)
	}

	cancel()
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("exit %d:\n%s", code, stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("hostd did not stop")
	}
	if !strings.Contains(stderr.String(), "hostd stopping") {
		t.Fatalf("no clean shutdown in log:\n%s", stderr.String())
	}
}

func TestRunRefusesBadConfig(t *testing.T) {
	rt := runtimeDir(t)
	cfg := writeConfig(t, `modules = ["apps", "lights"]`)
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{"-config", cfg, "-state", t.TempDir(), "-runtime", rt}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), `no module "lights"`) {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(rt, "hostd.sock")); err == nil {
		t.Fatal("started serving despite a bad config")
	}
}

// External modules are files in modules/: read in order, and one that is
// not valid is kept with its error.
func TestLoadExternals(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "modules"), 0o755)
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, "modules", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lights.toml", "command = [\"python3\", \"lights.py\"]\nenv = { BRIDGE = \"10.0.0.2\" }\ntransport = \"stdio\"\n")
	write("apps.toml", "command = [\"x\"]\n")
	write("noword.toml", "env = {}\n")
	write("typo.toml", "commnd = [\"x\"]\n")
	write("README", "not a module")
	write(".hidden.toml", "command = [\"x\"]\n")
	got := loadExternals(dir)
	names := make([]string, len(got))
	for i, x := range got {
		names[i] = x.name
	}
	if !reflect.DeepEqual(names, []string{"apps", "lights", "noword", "typo"}) {
		t.Fatalf("names = %v", names)
	}
	l := got[1]
	if l.err != nil || len(l.file.Command) != 2 || l.file.Env["BRIDGE"] != "10.0.0.2" || l.file.Transport != "stdio" {
		t.Fatalf("lights: %+v", l)
	}
	for i, want := range map[int]string{0: "built-in module", 2: "needs a command", 3: "unknown key(s): commnd"} {
		if got[i].err == nil || !strings.Contains(got[i].err.Error(), want) {
			t.Errorf("%s: %v, want %q", got[i].name, got[i].err, want)
		}
		if st := got[i].state(nil); st.Err == "" || st.Name != got[i].name {
			t.Errorf("%s: state %+v", got[i].name, st)
		}
	}
	if st := l.state(errors.New("did not answer")); st.Err != "did not answer" || !bytes.Equal(st.Raw, l.raw) {
		t.Errorf("a module that did not start: %+v", st)
	}
	if got := loadExternals(t.TempDir()); len(got) != 0 {
		t.Errorf("no modules/ folder: %v", got)
	}
}

// Files in hostd.d/ add to hostd.toml: an add-on, or a person, plugs in a
// module's settings without editing hostd.toml.
func TestDropIns(t *testing.T) {
	setup := func(t *testing.T, main string, drops map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "hostd.toml")
		if main != "" {
			_ = os.WriteFile(path, []byte(main), 0o644)
		}
		_ = os.MkdirAll(DropInDir(path), 0o755)
		for name, body := range drops {
			_ = os.WriteFile(filepath.Join(DropInDir(path), name), []byte(body), 0o644)
		}
		return path
	}

	// Settings from several files come together; other files are ignored.
	path := setup(t, "modules = [\"apps\", \"display\"]\n[display]\nidle_after = \"9m\"\n", map[string]string{
		"20-keys.toml": "[display.keys]\n\"F1\" = \"display.menu\"\n[apps]\nbrowser = [\"firefox\", \"{url}\"]\n",
		"README":       "not a config file",
		"old.toml~":    "not one either",
	})
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Modules, []string{"apps", "display"}) {
		t.Fatalf("config %+v", c)
	}
	confs, err := moduleConfigs(c)
	if err != nil {
		t.Fatal(err)
	}
	d := confs["display"].(*display.Config)
	if time.Duration(d.IdleAfter) != 9*time.Minute || d.Keys["F1"] != "display.menu" ||
		!reflect.DeepEqual(confs["apps"].(*apps.Config).Browser, []string{"firefox", "{url}"}) {
		t.Fatalf("merged: %+v %+v", d, confs["apps"])
	}

	// [api] may come from a drop-in too (the Tailscale add-on's).
	c, err = LoadConfig(setup(t, "", map[string]string{"tailscale.toml": "[api]\nallow = [\"100.64.0.0/10\"]\nnames = [\"core.tail1234.ts.net\"]\n"}))
	if err != nil || !c.API.TLSOn() || len(c.API.AllowPrefixes()) != 1 || c.API.Names[0] != "core.tail1234.ts.net" {
		t.Fatalf("[api] from a drop-in: %+v %v", c.API, err)
	}
	if c, err := LoadConfig(setup(t, "[api]\ntls = false\ninsecure_http = true\n", nil)); err != nil || c.API.TLSOn() {
		t.Fatalf("plain HTTP, asked for: %+v %v", c.API, err)
	}

	// Drop-ins alone, with no hostd.toml.
	if c, err := LoadConfig(setup(t, "", map[string]string{"x.toml": "[display]\nidle_after = \"7m\"\n"})); err != nil || len(c.sections) != 1 {
		t.Fatalf("no hostd.toml: %+v %v", c, err)
	}

	for name, tc := range map[string]struct {
		main  string
		drops map[string]string
		want  []string
	}{
		"a setting in two files": {"[display]\nidle_after = \"1m\"\n", map[string]string{"a.toml": "[display]\nidle_after = \"2m\"\n"},
			[]string{"display.idle_after is set in both", "hostd.toml", "a.toml"}},
		"an external module in a drop-in": {"", map[string]string{"a.toml": "[external.x]\ncommand = [\"x\"]\n"},
			[]string{"a.toml: external belongs in", "hostd.toml"}},
		"a top-level key": {"", map[string]string{"a.toml": "listen = \"\"\n"},
			[]string{"a.toml: listen belongs in", "hostd.toml"}},
		"a mistake in a drop-in": {"", map[string]string{"a.toml": "[display.keys]\n\"F2\" = \"window.fly\"\n"},
			[]string{"[display] keys: F2"}},
		"not TOML": {"", map[string]string{"a.toml": "this is not toml"}, []string{"a.toml"}},
	} {
		_, err := LoadConfig(setup(t, tc.main, tc.drops))
		for _, w := range tc.want {
			if err == nil || !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %v, want %q", name, err, w)
			}
		}
	}
}

// hostd will not start on the layout before 0.2, and -migrate fixes it.
func TestRunRefusesTheOldLayoutAndMigrates(t *testing.T) {
	cfg := writeConfig(t, "")
	dir := filepath.Dir(cfg)
	old := "[[rule]]\nname = \"bedtime\"\non = \"instance.started\"\ndo = { scene = \"quiet\" }\n"
	if err := os.WriteFile(filepath.Join(dir, "rules.toml"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	run := t.TempDir()
	var errb bytes.Buffer
	args := []string{"-config", cfg, "-state", t.TempDir(), "-runtime", run, "-listen", ""}
	if code := Run(context.Background(), args, &errb); code != 2 || !strings.Contains(errb.String(), "hostd -migrate") {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	errb.Reset()
	if code := Run(context.Background(), []string{"-config", cfg, "-migrate"}, &errb); code != 0 || !strings.Contains(errb.String(), "rules/bedtime.toml") {
		t.Fatalf("migrate: exit %d: %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "rules", "bedtime.toml")); err != nil {
		t.Fatal(err)
	}
	errb.Reset()
	if code := Run(context.Background(), []string{"-config", cfg, "-migrate"}, &errb); code != 0 || !strings.Contains(errb.String(), "nothing to migrate") {
		t.Fatalf("second migrate: exit %d: %s", code, errb.String())
	}
}

// The tokens of a rule's script: made for the run, gone when it ends.
func TestScriptTokensAreRevokedWithTheRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	secret, revoke, err := scriptTokens{st}.Mint(ctx, "script night #1", []string{"audio"}, time.Minute)
	if err != nil || !strings.HasPrefix(secret, "hostd_") {
		t.Fatalf("mint: %q %v", secret, err)
	}
	if tok, err := st.VerifyToken(ctx, secret); err != nil || tok.Kind != sdk.SourceAutomation {
		t.Fatalf("verify: %+v %v", tok, err)
	}
	revoke()
	if _, err := st.VerifyToken(ctx, secret); err == nil {
		t.Fatal("the token works after the run")
	}
}

func TestContains(t *testing.T) {
	if !contains([]string{"a", "demo"}, "demo") || contains([]string{"a"}, "demo") || contains(nil, "x") {
		t.Fatal("contains is wrong")
	}
}
