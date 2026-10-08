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
	if err != nil || !reflect.DeepEqual(c.Modules, DefaultModules) || c.Listen != nil {
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

	c, err = LoadConfig(writeConfig(t, `
[input.keys]
"F1" = "display.menu"
"Super+Q" = ""
[input.buttons]
guide = "window.back"
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.keys["Super+Q"]; ok || c.keys["F1"] != "display.menu" || c.keys["Super+Tab"] != "window.next" ||
		c.buttons["guide"] != "window.back" {
		t.Fatalf("bindings: %v %v", c.keys, c.buttons)
	}

	for body, want := range map[string]string{
		"[input.keys]\n\"F2\" = \"window.fly\"":    `input.keys: F2: "window.fly" cannot be bound`,
		"[input.buttons]\nturbo = \"window.next\"": `input.buttons: button "turbo"`,
		"[input]\nmice = {}":                       "unknown key(s): input.mice",
		`modules = ["apps", "lights"]`:             `no module "lights"`,
		`modules = ["apps", "apps"]`:               "listed twice",
		`modules = ["apps", "automation"]`:         "later version (M5)",
		`listn = ":7300"`:                          "unknown key(s): listn",
		`hold_window = "three minutes"`:            "not a duration",
		`hold_window = "-1m"`:                      "negative",
		`modules = "apps"`:                         "",
		"[hold_windows]\n\"audio.\" = 5":           "",
		`this is not toml`:                         "",
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
