package daemon

import (
	"bytes"
	"context"
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
	confs, notes, err := moduleConfigs(c)
	if err != nil || len(notes) != 0 {
		t.Fatalf("module settings: %v %v", notes, err)
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

	// The keys of earlier versions still work, for one release, with a note.
	c, err = LoadConfig(writeConfig(t, `
idle_after = "7m"
gamescope = ["gamescope", "-f"]
[input]
hold = "2s"
[input.keys]
"F1" = "display.menu"
`))
	if err != nil {
		t.Fatal(err)
	}
	confs, notes, _ = moduleConfigs(c)
	d = confs["display"].(*display.Config)
	if time.Duration(d.IdleAfter) != 7*time.Minute || time.Duration(d.Hold) != 2*time.Second || d.Keys["F1"] != "display.menu" ||
		!reflect.DeepEqual(confs["apps"].(*apps.Config).Gamescope, []string{"gamescope", "-f"}) {
		t.Fatalf("old keys: %+v %+v", d, confs["apps"])
	}
	if len(notes) != 4 || !strings.Contains(strings.Join(notes, "\n"), "idle_after is now [display] idle_after") {
		t.Fatalf("notes: %q", notes)
	}

	for body, want := range map[string]string{
		"[input.keys]\n\"F2\" = \"window.fly\"":                      `[display] keys: F2: "window.fly" cannot be bound`,
		"[display.keys]\n\"F2\" = \"window.fly\"":                    `[display] keys: F2: "window.fly" cannot be bound`,
		"[display.buttons]\nturbo = \"window.next\"":                 `[display] buttons: button "turbo"`,
		"[input]\nmice = {}":                                         "unknown key(s): input.mice",
		"[display]\nmice = 1":                                        "unknown key(s): display.mice",
		"[display]\nidle_after = \"-1m\"":                            "idle_after is negative",
		"[display]\nidle_after = \"soon\"":                           "not a duration",
		"idle_after = \"5m\"\n[display]\nidle_after = \"6m\"":        "same setting",
		"[audio]\nvolume = 5":                                        "the audio module has no settings",
		`modules = ["apps", "lights"]`:                               `no module "lights"`,
		`modules = ["apps", "apps"]`:                                 "listed twice",
		`listn = ":7300"`:                                            "unknown key(s): listn",
		`hold_window = "three minutes"`:                              "not a duration",
		`hold_window = "-1m"`:                                        "negative",
		`modules = "apps"`:                                           "",
		"[hold_windows]\n\"audio.\" = 5":                             "",
		`this is not toml`:                                           "",
		"[external.apps]\ncommand = [\"x\"]":                         "a built-in module's name",
		"[external.Lights]\ncommand = [\"x\"]":                       "use lowercase",
		"[external.lights]\nenv = {}":                                "needs a command",
		"[external.lights]\ncommand = [\"x\"]\ntransport = \"pipe\"": `transport "pipe"`,
		"[external.lights]\ncommnd = [\"x\"]":                        "unknown key(s): external.lights.commnd",
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
	if len(mans) != 1 || mans[0].Name != "apps" {
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

func TestLoadConfigExternal(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, "[external.lights]\ncommand = [\"python3\", \"lights.py\"]\nenv = { BRIDGE = \"10.0.0.2\" }\n"))
	if err != nil || len(c.External["lights"].Command) != 2 || c.External["lights"].Env["BRIDGE"] != "10.0.0.2" {
		t.Fatalf("%+v %v", c.External, err)
	}
}

// Files in hostd.d/ add to hostd.toml: an add-on, or a person, plugs in an
// external module or a module's settings without editing hostd.toml.
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
		"10-lights.toml": "[external.lights]\ncommand = [\"python3\", \"lights.py\"]\ntransport = \"stdio\"\n",
		"20-keys.toml":   "[display.keys]\n\"F1\" = \"display.menu\"\n[apps]\nbrowser = [\"firefox\", \"{url}\"]\n",
		"README":         "not a config file",
		"old.toml~":      "not one either",
	})
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Modules, []string{"apps", "display"}) || c.External["lights"].Transport != "stdio" {
		t.Fatalf("config %+v", c)
	}
	confs, _, err := moduleConfigs(c)
	if err != nil {
		t.Fatal(err)
	}
	d := confs["display"].(*display.Config)
	if time.Duration(d.IdleAfter) != 9*time.Minute || d.Keys["F1"] != "display.menu" ||
		!reflect.DeepEqual(confs["apps"].(*apps.Config).Browser, []string{"firefox", "{url}"}) {
		t.Fatalf("merged: %+v %+v", d, confs["apps"])
	}

	// Drop-ins alone, with no hostd.toml.
	if c, err := LoadConfig(setup(t, "", map[string]string{"x.toml": "[external.x]\ncommand = [\"x\"]\n"})); err != nil || len(c.External) != 1 {
		t.Fatalf("no hostd.toml: %+v %v", c, err)
	}

	for name, tc := range map[string]struct {
		main  string
		drops map[string]string
		want  []string
	}{
		"a setting in two files": {"[display]\nidle_after = \"1m\"\n", map[string]string{"a.toml": "[display]\nidle_after = \"2m\"\n"},
			[]string{"display.idle_after is set in both", "hostd.toml", "a.toml"}},
		"a module in two drop-ins": {"", map[string]string{"a.toml": "[external.x]\ncommand = [\"x\"]\n", "b.toml": "[external.x]\nenv = {A = \"1\"}\n"},
			[]string{"external.x is set in both", "a.toml", "b.toml"}},
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
