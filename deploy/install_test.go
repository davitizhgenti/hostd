package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	sshdBad  bool            // sshd -t refuses the config
	missing  map[string]bool // packages not installed (until apt-get installs them)
	busy     bool            // someone uses the screen: hostctl wait-quiet fails
	flatpaks map[string]bool // Flatpak apps installed
	// bare: a machine with no screen user yet, no render group, greetd
	// not enabled; useradd makes the user.
	bare, userMade bool
	answers        map[string]string // output for a command line (as the screen user: what it runs)
	hostdBroken    bool              // hostd does not start
	aptFails       int               // apt-get install fails this many times (stale package lists)
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
	if out, ok := f.answers[strings.Join(argv, " ")]; ok {
		return []byte(out), nil
	}
	if f.bare {
		switch {
		case line == "id screen" && !f.userMade:
			return nil, fail
		case strings.HasPrefix(line, "useradd "):
			f.userMade = true
			os.MkdirAll(filepath.Join(f.root, "home/screen"), 0o755)
			return nil, nil
		case line == "id -nG screen":
			return []byte("screen\n"), nil
		case line == "getent group render", line == "systemctl is-enabled --quiet greetd":
			return nil, fail
		}
	}
	switch {
	case line == "dpkg --print-architecture":
		return []byte("amd64\n"), nil
	case argv[0] == "dpkg-query":
		if f.missing[argv[len(argv)-1]] {
			return []byte("unknown ok not-installed"), fail
		}
		return []byte("install ok installed"), nil
	case len(argv) > 3 && argv[0] == "apt-get" && argv[1] == "install" && f.aptFails > 0:
		f.aptFails--
		return nil, fail
	case f.hostdBroken && strings.Join(argv, " ") == "systemctl --user restart hostd":
		return nil, fail
	case len(argv) > 3 && argv[0] == "apt-get" && argv[1] == "install":
		for _, p := range argv[4:] {
			delete(f.missing, p)
		}
		return nil, nil
	case argv[0] == "hostctl" && len(argv) > 1 && argv[1] == "wait-quiet" && f.busy:
		return nil, fail
	case strings.HasPrefix(line, "flatpak info --system "):
		if !f.flatpaks[argv[3]] {
			return nil, fail
		}
		return nil, nil
	case strings.HasPrefix(line, "flatpak install --system "):
		if f.flatpaks == nil {
			f.flatpaks = map[string]bool{}
		}
		f.flatpaks[argv[len(argv)-1]] = true
		return nil, nil
	case strings.HasPrefix(line, "flatpak uninstall --system "):
		delete(f.flatpaks, argv[len(argv)-1])
		return nil, nil
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
	os.WriteFile(filepath.Join(dir, "hostd-docs.tar.gz"), docsTarball(t, "<h1>hostd docs</h1>"), 0o644)
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

// docsTarball is a docs site as a build carries it.
func docsTarball(t *testing.T, index string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{"index.html": index, "api/index.html": "<h1>API</h1>"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// A bare Debian: the user, its groups, autologin and the packages are
// made, and hostd comes from the signed release.
func TestInstallFromScratch(t *testing.T) {
	m := newFakeMachine(t)
	m.bare = true
	m.missing = map[string]bool{"sway": true, "greetd": true}
	os.Remove(filepath.Join(m.root, "etc/subuid"))
	os.Remove(filepath.Join(m.root, "var/lib/systemd/linger/screen"))
	os.RemoveAll(filepath.Join(m.root, "home/screen"))
	dir, key, _ := signedRelease(t, map[string]string{"hostd-linux-amd64": "hostd from the release", "hostctl-linux-amd64": "hostctl",
		"hostd-docs.tar.gz": string(docsTarball(t, "<h1>docs</h1>"))})
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the installer"), 0o755)
	s, out := newSetup(t, m)
	o := Options{GPU: "other", Release: Release{URL: "file://" + dir}, ReleaseKey: key, SudoUser: "admin", Self: self}
	if err := s.Install(context.Background(), o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"apt-get update -q", "apt-get install -y -q sway greetd", "useradd -m -s /bin/bash screen", "groupadd -r render",
		"usermod -aG video screen", "usermod -aG input screen", "usermod --add-subuids 200000-265535", "loginctl enable-linger screen",
		"systemctl enable greetd"} {
		if !m.didRun(want) {
			t.Errorf("did not run %q", want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(m.root, "usr/local/share/hostd/docs/index.html")); err != nil || string(b) != "<h1>docs</h1>" {
		t.Errorf("docs %q %v", b, err)
	}
	if !strings.Contains(out.String(), "created user screen") || !strings.Contains(out.String(), "Reboot") {
		t.Errorf("output:\n%s", out)
	}
}
