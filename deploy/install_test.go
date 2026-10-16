package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeMachine answers the installer's commands as a fresh Debian 13 machine
// with a screen user would, keeping what changes (hostd versions, a running
// hostd) in its files under root.
type fakeMachine struct {
	t    *testing.T
	root string

	mu       sync.Mutex
	ran      []string
	hostdUp  bool
	tokens   int
	restarts int
	sshdBad  bool // sshd -t refuses the config
}

func (f *fakeMachine) Run(_ context.Context, c Cmd) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(c.Argv, " ")
	f.ran = append(f.ran, line)
	argv := c.Argv
	if argv[0] == "runuser" { // as the screen user: what it runs
		i := 0
		for i < len(argv) && argv[i] != "--" {
			i++
		}
		argv = argv[i+1:]
		for len(argv) > 0 && (argv[0] == "env" || strings.Contains(argv[0], "=")) {
			argv = argv[1:]
		}
	}
	fail := fmt.Errorf("exit status 1")
	switch {
	case line == "dpkg --print-architecture":
		return []byte("amd64\n"), nil
	case argv[0] == "dpkg-query":
		return []byte("install ok installed"), nil
	case line == "id screen", strings.HasPrefix(line, "getent group "):
		return nil, nil
	case line == "id -nG screen":
		return []byte("screen video render input\n"), nil
	case line == "id -u screen":
		return []byte("1001\n"), nil
	case line == "getent passwd screen":
		return []byte("screen:x:1001:1001::/home/screen:/bin/bash\n"), nil
	case line == "getent passwd admin":
		return []byte("admin:x:1000:1000::/home/admin:/bin/bash\n"), nil
	case strings.HasSuffix(argv[0], "/hostd") && argv[1] == "-version":
		if c.Stdout == nil {
			return nil, nil // like hostd, which says it on standard error
		}
		_, err := io.WriteString(c.Stdout, "hostd v1\n")
		return nil, err
	case strings.HasSuffix(argv[0], "/hostd") && argv[1] == "-install":
		lib := filepath.Join(f.root, argv[4])
		b, _ := os.ReadFile(argv[2])
		os.MkdirAll(filepath.Join(lib, "versions/v1"), 0o755)
		os.WriteFile(filepath.Join(lib, "versions/v1/hostd"), b, 0o755)
		os.Remove(filepath.Join(lib, "current"))
		os.Symlink("versions/v1", filepath.Join(lib, "current"))
		return []byte("installed hostd v1\n"), nil
	case strings.HasPrefix(strings.Join(argv, " "), "systemctl --user is-active --quiet hostd"):
		if f.hostdUp {
			return nil, nil
		}
		return nil, fail
	case strings.HasPrefix(strings.Join(argv, " "), "systemctl --user restart hostd"):
		f.hostdUp = true
		f.restarts++
		os.WriteFile(filepath.Join(f.root, "run/user/1001/hostd-admin-token"), []byte("hostd_admin\n"), 0o600)
		return nil, nil
	case argv[0] == "hostctl" && argv[1] == "login":
		os.MkdirAll(filepath.Join(f.root, "home/screen/.config/hostctl"), 0o755)
		os.WriteFile(filepath.Join(f.root, "home/screen/.config/hostctl/config.toml"), []byte("x"), 0o600)
		return nil, nil
	case strings.Join(argv, " ") == "hostctl --json token create menu --kind local --scopes read,apps":
		f.tokens++
		return []byte(`{"secret": "hostd_menu"}`), nil
	case strings.Join(argv, " ") == "hostctl --json ps":
		return []byte(`[]`), nil
	case line == "hostname -I":
		return []byte("10.0.0.5 fd00::5\n"), nil
	case line == "sshd -t" && f.sshdBad:
		return []byte("/etc/ssh/sshd_config line 12: bad option"), fail
	case line == "systemctl is-enabled --quiet greetd", line == "systemctl is-enabled --quiet hostd-update.timer":
		return nil, nil
	}
	return nil, nil // anything else succeeds quietly
}

func newFakeMachine(t *testing.T) *fakeMachine {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"etc", "run/user/1001", "var/lib/systemd/linger", "home/screen", "home/admin/.ssh"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "etc/os-release"), []byte("PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nID=debian\nVERSION_CODENAME=trixie\n"), 0o644)
	os.WriteFile(filepath.Join(root, "etc/subuid"), []byte("screen:200000:65536\n"), 0o644)
	os.WriteFile(filepath.Join(root, "etc/subgid"), []byte("screen:200000:65536\n"), 0o644)
	os.WriteFile(filepath.Join(root, "var/lib/systemd/linger/screen"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "run/user/1001/bus"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "home/admin/.ssh/authorized_keys"), []byte("ssh-ed25519 AAAA admin@laptop\n"), 0o600)
	return &fakeMachine{t: t, root: root}
}

func buildDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hostd-linux-amd64"), []byte("hostd binary v1"), 0o755)
	os.WriteFile(filepath.Join(dir, "hostctl-linux-amd64"), []byte("hostctl binary v1"), 0o755)
	return dir
}

func TestInstallOnAFreshMachine(t *testing.T) {
	m := newFakeMachine(t)
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the installer"), 0o755)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	o := Options{GPU: "other", From: buildDir(t), SudoUser: "admin", Self: self}
	if err := s.Install(context.Background(), o); err != nil {
		t.Fatalf("install: %v\n%s", err, out.String())
	}
	s.Out.Close()
	for _, p := range []string{"/home/screen/.config/sway/config", "/home/screen/.config/mako/config", "/etc/greetd/config.toml",
		"/home/screen/.config/systemd/user/hostd.service", "/home/screen/.local/lib/hostd/rollback.sh", "/usr/local/bin/hostctl",
		"/usr/local/lib/hostd/hostd-menu", "/home/screen/.config/hostd/apps/hostd-menu.toml", "/usr/local/sbin/hostd-setup",
		"/etc/systemd/system/hostd-update.timer", "/etc/hostd/update.conf", "/home/screen/.config/hostd/local.token"} {
		if _, err := os.Stat(filepath.Join(m.root, p)); err != nil {
			t.Errorf("not installed: %s", p)
		}
	}
	greetd, _ := os.ReadFile(filepath.Join(m.root, "etc/greetd/config.toml"))
	if !bytes.Contains(greetd, []byte(`command = "sway"`)) || !bytes.Contains(greetd, []byte(`user = "screen"`)) {
		t.Errorf("greetd config:\n%s", greetd)
	}
	keys, _ := os.ReadFile(filepath.Join(m.root, "home/screen/.ssh/authorized_keys"))
	if string(keys) != "ssh-ed25519 AAAA admin@laptop\n" {
		t.Errorf("SSH keys %q", keys)
	}
	if link, _ := os.Readlink(filepath.Join(m.root, "home/screen/.config/systemd/user/default.target.wants/hostd.service")); link != "../hostd.service" {
		t.Errorf("hostd does not start at boot: %q", link)
	}
	for _, want := range []string{"installed hostd v1", "hostd is running", "logged the screen user's hostctl in", "created the menu's token",
		"copied 1 SSH key(s) from admin", "ssh screen@10.0.0.5 hostctl apps --all"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if m.restarts != 1 || m.tokens != 1 {
		t.Fatalf("restarts %d, tokens %d", m.restarts, m.tokens)
	}

	// A second run, as an update: nothing to change, and it says so.
	out.Reset()
	s.Out = NewOutput(&out, false)
	o.Update = &UpdateOptions{}
	if err := s.Install(context.Background(), o); err != nil {
		t.Fatalf("second run: %v\n%s", err, out.String())
	}
	s.Out.Close()
	if !strings.Contains(out.String(), "No changes needed") || strings.Contains(out.String(), "==> Checking the system") ||
		!strings.Contains(out.String(), "hostd v1 is running.") || !strings.Contains(out.String(), "steps already up to date") {
		t.Fatalf("second run output:\n%s", out.String())
	}
	if m.restarts != 1 || m.tokens != 1 {
		t.Fatalf("second run restarted hostd or made a token: restarts %d, tokens %d", m.restarts, m.tokens)
	}
}

func TestInstallRefusesOtherSystems(t *testing.T) {
	m := newFakeMachine(t)
	os.WriteFile(filepath.Join(m.root, "etc/os-release"), []byte("PRETTY_NAME=\"Ubuntu 24.04\"\nID=ubuntu\n"), 0o644)
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(io.Discard, true)}
	if err := s.Install(context.Background(), Options{From: buildDir(t)}); err == nil || !strings.Contains(err.Error(), "this is Ubuntu 24.04") {
		t.Fatalf("%v", err)
	}
}

func TestNvidiaDetection(t *testing.T) {
	m := newFakeMachine(t)
	gpu := filepath.Join(m.root, "sys/bus/pci/devices/0000:01:00.0")
	os.MkdirAll(gpu, 0o755)
	os.WriteFile(filepath.Join(gpu, "class"), []byte("0x030000\n"), 0o644)
	os.WriteFile(filepath.Join(gpu, "vendor"), []byte("0x10de\n"), 0o644)
	os.MkdirAll(filepath.Join(m.root, "etc/apt/sources.list.d"), 0o755)
	os.WriteFile(filepath.Join(m.root, "etc/apt/sources.list.d/debian.sources"),
		[]byte("Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main\n"), 0o644)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	in := &installer{Setup: s, o: Options{GPU: "auto"}}
	ctx := context.Background()
	if err := in.checkSystem(ctx); err != nil || in.gpu != "nvidia" {
		t.Fatalf("gpu %q %v", in.gpu, err)
	}
	if err := in.enableNonFree(ctx); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(filepath.Join(m.root, "etc/apt/sources.list.d/debian.sources"))
	if !strings.Contains(string(src), "Components: main contrib non-free\n") {
		t.Fatalf("sources:\n%s", src)
	}
	if err := in.nvidiaSway(ctx); err != nil || in.swayCommand != "/usr/local/lib/hostd/start-sway" || !in.needReboot {
		t.Fatalf("nvidia sway: %v %q %v", err, in.swayCommand, in.needReboot)
	}
}

func TestSSHKeysOnly(t *testing.T) {
	setup := func(t *testing.T, o Options) (*fakeMachine, *installer, *bytes.Buffer) {
		m := newFakeMachine(t)
		os.MkdirAll(filepath.Join(m.root, "usr/sbin"), 0o755)
		os.WriteFile(filepath.Join(m.root, "usr/sbin/sshd"), nil, 0o755)
		var out bytes.Buffer
		s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
		return m, &installer{Setup: s, o: o}, &out
	}
	dropIn := func(m *fakeMachine) string {
		b, _ := os.ReadFile(filepath.Join(m.root, sshdDropIn))
		return string(b)
	}
	ran := func(m *fakeMachine, line string) bool { return slices.Contains(m.ran, line) }

	// The admin has a key: passwords go, after sshd has checked the result.
	m, in, out := setup(t, Options{SudoUser: "admin"})
	if err := in.sshKeysOnly(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dropIn(m), "PasswordAuthentication no") || !ran(m, "sshd -t") || !ran(m, "systemctl reload ssh") {
		t.Fatalf("drop-in %q, ran %q", dropIn(m), m.ran)
	}
	if !strings.Contains(out.String(), "admin logs in with one of its 1 key(s)") {
		t.Fatalf("output:\n%s", out)
	}
	m.ran = nil
	if err := in.sshKeysOnly(context.Background()); err != nil || ran(m, "systemctl reload ssh") {
		t.Fatalf("second run reloaded sshd: %v %q", err, m.ran)
	}

	// No key: nothing changes, and it says how to add one.
	m, in, out = setup(t, Options{SudoUser: "admin"})
	os.WriteFile(filepath.Join(m.root, "home/admin/.ssh/authorized_keys"), []byte("# none yet\n"), 0o600)
	if err := in.sshKeysOnly(context.Background()); err != nil || dropIn(m) != "" || !strings.Contains(out.String(), "ssh-copy-id admin@") {
		t.Fatalf("no key: %v %q\n%s", err, dropIn(m), out)
	}

	// The update timer (no admin), and --ssh-passwords keep: nothing changes.
	for _, o := range []Options{{}, {SudoUser: "admin", SSHPasswords: "keep"}} {
		m, in, _ = setup(t, o)
		if err := in.sshKeysOnly(context.Background()); err != nil || dropIn(m) != "" {
			t.Fatalf("%+v: %v %q", o, err, dropIn(m))
		}
	}

	// sshd refuses the result: it is taken back, sshd is not reloaded.
	m, in, out = setup(t, Options{SudoUser: "admin"})
	m.sshdBad = true
	if err := in.sshKeysOnly(context.Background()); err != nil || dropIn(m) != "" || ran(m, "systemctl reload ssh") ||
		!strings.Contains(out.String(), "left as it was") {
		t.Fatalf("refused: %v %q %q\n%s", err, dropIn(m), m.ran, out)
	}
}
