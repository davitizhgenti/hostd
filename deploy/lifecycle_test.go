package deploy

import (
	"bytes"
	"context"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The add-on engine, updates, the update timer and uninstall, on the fake
// machine of install_test.go.

func newSetup(t *testing.T, m *fakeMachine) (*Setup, *bytes.Buffer) {
	var out bytes.Buffer
	return &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}, &out
}

func (f *fakeMachine) didRun(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.ran, func(l string) bool { return strings.HasPrefix(l, prefix) })
}

func TestAddonEngine(t *testing.T) {
	m := newFakeMachine(t)
	s, out := newSetup(t, m)
	ctx := context.Background()
	screen := screenUser{name: "screen", home: "/home/screen", uid: 1001}

	if err := s.AddAddons(nil); err == nil {
		t.Fatal("added nothing")
	}
	if err := s.AddAddons([]string{"ghost"}); err == nil {
		t.Fatal("added an add-on that does not exist")
	}
	// What dolphin needs comes with it, first.
	if err := s.AddAddons([]string{"dolphin"}); err != nil {
		t.Fatal(err)
	}
	if got := s.installedAddons(); !slices.Equal(got, []string{"flatpak", "controllers", "dolphin"}) {
		t.Fatalf("installed %v", got)
	}
	var list bytes.Buffer
	if err := s.ListAddons(&list); err != nil || !strings.Contains(list.String(), "* dolphin ") || !strings.Contains(list.String(), "  steam ") {
		t.Fatalf("list:\n%s", list.String())
	}

	// The install that follows runs them: Flathub, the app, the device rules.
	m.missing = map[string]bool{"flatpak": true}
	in := &installer{Setup: s, o: Options{Adding: []string{"dolphin"}}, user: screen}
	if err := in.addons(ctx); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"apt-get install -y -q flatpak", "flatpak remote-add --system --if-not-exists flathub",
		"flatpak install --system -y --noninteractive flathub org.DolphinEmu.dolphin-emu", "modprobe uinput"} {
		if !m.didRun(want) {
			t.Errorf("did not run %q\n%q", want, m.ran)
		}
	}
	if !in.exists("/etc/udev/rules.d/60-hostd-controllers.rules") || !in.exists(addonsStamp) {
		t.Fatalf("rules or stamp missing\n%s", out)
	}

	// An update with the same add-on files: nothing runs.
	m.ran = nil
	in = &installer{Setup: s, o: Options{Update: &UpdateOptions{}}, user: screen}
	if err := in.addons(ctx); err != nil || in.addonsSkipped != 3 || m.didRun("flatpak install") {
		t.Fatalf("skipped %d, ran %q, %v", in.addonsSkipped, m.ran, err)
	}
	// One asked for by name runs; the others do not.
	in = &installer{Setup: s, o: Options{Update: &UpdateOptions{Addons: []string{"controllers"}}}, user: screen}
	if err := in.addons(ctx); err != nil || !m.didRun("modprobe uinput") || m.didRun("flatpak install") {
		t.Fatalf("by name: ran %q, %v", m.ran, err)
	}
	// New add-on files (a newer build): every one runs again.
	m.ran = nil
	_ = os.WriteFile(in.path(addonsStamp), []byte("an older build\n"), 0o644)
	in = &installer{Setup: s, o: Options{Update: &UpdateOptions{}}, user: screen}
	if err := in.addons(ctx); err != nil || !m.didRun("flatpak info --system org.DolphinEmu.dolphin-emu") || !m.didRun("modprobe uinput") {
		t.Fatalf("changed files: ran %q, %v", m.ran, err)
	}

	// What another add-on needs cannot go; the add-on that needs it can.
	if err := s.RemoveAddons(ctx, "screen", []string{"controllers"}); err == nil || !strings.Contains(err.Error(), "dolphin needs controllers") {
		t.Fatalf("removed what dolphin needs: %v", err)
	}
	if err := s.RemoveAddons(ctx, "screen", []string{"steam"}); err == nil {
		t.Fatal("removed an add-on that is not installed")
	}
	if err := s.RemoveAddons(ctx, "screen", []string{"dolphin"}); err != nil {
		t.Fatal(err)
	}
	if !m.didRun("flatpak uninstall --system -y --noninteractive org.DolphinEmu.dolphin-emu") {
		t.Errorf("the app stays: %q", m.ran)
	}
	if got := s.installedAddons(); !slices.Equal(got, []string{"flatpak", "controllers"}) {
		t.Fatalf("installed after remove %v", got)
	}
	if !strings.Contains(out.String(), "stays in /home/screen/.var/app/org.DolphinEmu.dolphin-emu") {
		t.Errorf("not told what stays:\n%s", out)
	}
}

func TestEnsurePackages(t *testing.T) {
	m := newFakeMachine(t)
	s, out := newSetup(t, m)
	m.missing = map[string]bool{"jq": true}
	if err := s.ensurePackages(context.Background(), "jq", "curl"); err != nil {
		t.Fatal(err)
	}
	if !m.didRun("apt-get install -y -q jq") || m.didRun("apt-get install -y -q jq curl") || !strings.Contains(out.String(), "installed jq") {
		t.Fatalf("ran %q\n%s", m.ran, out)
	}
	m.ran = nil
	if err := s.ensurePackages(context.Background(), "jq"); err != nil || m.didRun("apt-get") {
		t.Fatalf("installed again: %q %v", m.ran, err)
	}
}

// exec records what Update would run in its place.
type execCall struct {
	path       string
	args, env  []string
	calls      int
	fileExists bool
}

func (e *execCall) run(_ context.Context, path string, args, env []string) error {
	e.calls++
	e.path, e.args, e.env = path, args, env
	_, err := os.Stat(path)
	e.fileExists = err == nil
	return nil
}

func TestUpdateRunsTheNextInstaller(t *testing.T) {
	m := newFakeMachine(t)
	s, _ := newSetup(t, m)
	ctx := context.Background()
	_ = s.setInstalled([]string{"controllers"})

	// From a build at hand (--source).
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "hostd-setup-linux-amd64"), []byte("next"), 0o755)
	var e execCall
	a := UpdateArgs{Options: Options{Update: &UpdateOptions{All: true, Addons: []string{"controllers"}}}, Source: src, Verbose: true, Pass: []string{"--gpu", "other"}}
	if err := s.Update(ctx, a, e.run); err != nil {
		t.Fatal(err)
	}
	if e.path != filepath.Join(src, "hostd-setup-linux-amd64") || !slices.Equal(e.args, []string{"install", "--gpu", "other", "--verbose"}) ||
		!slices.Contains(e.env, "HOSTD_UPDATE_MODE=all") || !slices.Contains(e.env, "HOSTD_FORCE_ADDONS=controllers") {
		t.Fatalf("ran %+v", e)
	}
	// Only installed add-ons can be named.
	a.Update.Addons = []string{"steam"}
	if err := s.Update(ctx, a, e.run); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("named an add-on that is not installed: %v", err)
	}
	if err := s.Update(ctx, UpdateArgs{Source: t.TempDir()}, e.run); err == nil {
		t.Fatal("a source with no installer")
	}

	// From the signed release: fetched, checked, run.
	dir, key, _ := signedRelease(t, map[string]string{"hostd-setup-linux-amd64": "next from the release"})
	e = execCall{}
	a = UpdateArgs{Options: Options{Release: Release{URL: "file://" + dir}, ReleaseKey: key, Update: &UpdateOptions{}, Auto: true}}
	if err := s.Update(ctx, a, e.run); err != nil {
		t.Fatal(err)
	}
	if !e.fileExists || !slices.Contains(e.env, "HOSTD_RELEASE_URL=file://"+dir) || !slices.Contains(e.env, "HOSTD_UPDATE_MODE=quick") ||
		!slices.Contains(e.env, "HOSTD_AUTO=1") {
		t.Fatalf("release: %+v", e)
	}
	// Another key: refused, nothing run.
	_, other, _ := signedRelease(t, map[string]string{"x": "y"})
	e = execCall{}
	a.ReleaseKey = other
	if err := s.Update(ctx, a, e.run); err == nil || e.calls != 0 {
		t.Fatalf("another key: %v, ran %d", err, e.calls)
	}
}

func TestAutoUpdate(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T, apply string) (*fakeMachine, *Setup, *bytes.Buffer, UpdateArgs) {
		m := newFakeMachine(t)
		s, out := newSetup(t, m)
		os.MkdirAll(filepath.Join(m.root, "etc/hostd"), 0o755)
		os.WriteFile(filepath.Join(m.root, "etc/hostd/update.conf"), []byte("CHANNEL=edge\nAPPLY="+apply+"\n"), 0o644)
		dir, key, _ := signedRelease(t, map[string]string{"hostd-setup-linux-amd64": "next", "hostd_0.2.0~edge-abc_amd64.deb": "deb"})
		return m, s, out, UpdateArgs{Options: Options{Release: Release{URL: "file://" + dir}, ReleaseKey: key}}
	}

	// manual: nothing is fetched.
	_, s, out, a := setup(t, "manual")
	var e execCall
	if err := s.AutoUpdate(ctx, a, e.run); err != nil || e.calls != 0 || !strings.Contains(out.String(), "updates are manual") {
		t.Fatalf("manual: %v %d\n%s", err, e.calls, out)
	}

	// notify: a notice once, no update.
	m, s, _, a := setup(t, "notify")
	if err := s.AutoUpdate(ctx, a, e.run); err != nil || e.calls != 0 || !m.didRun("runuser -u screen -- env") {
		t.Fatalf("notify: %v %d %q", err, e.calls, m.ran)
	}
	if !slices.ContainsFunc(m.ran, func(l string) bool {
		return strings.Contains(l, "hostctl display notice hostd 0.2.0~edge-abc is available")
	}) {
		t.Fatalf("no notice: %q", m.ran)
	}
	m.ran = nil
	if err := s.AutoUpdate(ctx, a, e.run); err != nil || slices.ContainsFunc(m.ran, func(l string) bool { return strings.Contains(l, "display notice") }) {
		t.Fatalf("noticed twice: %q", m.ran)
	}

	// auto, someone at the screen: tried again later.
	m, s, out, a = setup(t, "auto")
	m.busy = true
	if err := s.AutoUpdate(ctx, a, e.run); err != nil || e.calls != 0 || !strings.Contains(out.String(), "trying again") {
		t.Fatalf("busy: %v %d\n%s", err, e.calls, out)
	}
	// auto, quiet: the update, unattended, every add-on.
	m.busy = false
	if err := s.AutoUpdate(ctx, a, e.run); err != nil || e.calls != 1 || !slices.Contains(e.env, "HOSTD_AUTO=1") || !slices.Contains(e.env, "HOSTD_UPDATE_MODE=all") {
		t.Fatalf("auto: %v %+v", err, e)
	}
	// Already applied: up to date.
	sums, _ := os.ReadFile(filepath.Join(strings.TrimPrefix(a.Release.URL, "file://"), "SHA256SUMS"))
	os.WriteFile(filepath.Join(m.root, "etc/hostd/applied.sha256"), sums, 0o644)
	e = execCall{}
	out.Reset()
	if err := s.AutoUpdate(ctx, a, e.run); err != nil || e.calls != 0 || !strings.Contains(out.String(), "up to date (edge)") {
		t.Fatalf("applied: %v %d\n%s", err, e.calls, out)
	}
}

func TestUninstall(t *testing.T) {
	m := newFakeMachine(t)
	s, out := newSetup(t, m)
	files := map[string]string{
		"/usr/local/bin/hostctl": "x", "/usr/local/sbin/hostd-setup": "x", "/usr/local/lib/hostd/hostd-menu": "x",
		"/etc/systemd/system/hostd-update.timer": "x", "/etc/hostd/update.conf": "x", "/etc/hostd/addons": "controllers\n",
		"/etc/greetd/config.toml": "hostd's", "/etc/greetd/config.toml.orig": "Debian's", sshdDropIn: sshdKeysOnly,
		"/home/screen/.config/systemd/user/hostd.service": "x", "/home/screen/.local/state/hostd/state.db": "x",
	}
	for p, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(m.root, p)), 0o755)
		os.WriteFile(filepath.Join(m.root, p), []byte(body), 0o644)
	}
	if err := s.Uninstall(context.Background(), UninstallArgs{Purge: true, All: true}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, p := range []string{"/usr/local/bin/hostctl", "/usr/local/sbin/hostd-setup", "/usr/local/lib/hostd", "/etc/systemd/system/hostd-update.timer",
		"/etc/hostd/update.conf", sshdDropIn, "/home/screen/.config/systemd/user/hostd.service", "/home/screen/.local/state/hostd"} {
		if _, err := os.Stat(filepath.Join(m.root, p)); err == nil {
			t.Errorf("%s stays", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(m.root, "etc/greetd/config.toml")); string(b) != "Debian's" {
		t.Errorf("greetd config %q", b)
	}
	if !m.didRun("systemctl reload ssh") {
		t.Errorf("sshd not reloaded: %q", m.ran)
	}
}

// After an update of the menu, a running menu restarts on the new version;
// one in front comes back, one in the background stays stopped.
func TestRestartMenu(t *testing.T) {
	for _, front := range []bool{true, false} {
		m := newFakeMachine(t)
		s, out := newSetup(t, m)
		m.answers = map[string]string{
			"hostctl --json ps":                  `[{"id": "hostd-menu"}, {"id": "foot"}]`,
			"hostctl --json get display windows": `[{"instance": "hostd-menu", "focused": ` + map[bool]string{true: "true", false: "false"}[front] + `}]`,
		}
		os.MkdirAll(filepath.Join(m.root, "home/screen/.config/hostd"), 0o755)
		os.WriteFile(filepath.Join(m.root, "home/screen/.config/hostd/local.token"), []byte("hostd_menu\n"), 0o600)
		in := &installer{Setup: s, menuChange: true, user: screenUser{name: "screen", home: "/home/screen", uid: 1001}}
		if err := in.restartMenu(context.Background(), "/home/screen/.config/hostd/local.token"); err != nil {
			t.Fatal(err)
		}
		started := slices.ContainsFunc(m.ran, func(l string) bool { return strings.HasSuffix(l, "HOSTD_TOKEN=hostd_menu hostctl start hostd-menu") })
		if !slices.ContainsFunc(m.ran, func(l string) bool { return strings.HasSuffix(l, "hostctl stop hostd-menu") }) || started != front {
			t.Fatalf("front %v: ran %q", front, m.ran)
		}
		if !strings.Contains(out.String(), "restarted the menu") {
			t.Fatalf("output:\n%s", out)
		}
	}
}

// Steam's add-on: app files in the screen user's home, its user units,
// a rescan, and its hook; then removed.
func TestSteamAddonEngine(t *testing.T) {
	m := newFakeMachine(t)
	s, out := newSetup(t, m)
	ctx := context.Background()
	os.WriteFile(filepath.Join(m.root, "run/user/1001/hostd.sock"), nil, 0o600)
	screen := screenUser{name: "screen", home: "/home/screen", uid: 1001}
	if err := s.AddAddons([]string{"steam"}); err != nil {
		t.Fatal(err)
	}
	in := &installer{Setup: s, o: Options{Adding: []string{"steam"}}, user: screen}
	if err := in.addons(ctx); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, f := range []string{"/home/screen/.config/hostd/apps/steam.toml", "/home/screen/.config/systemd/user/hostd-steam-games.path",
		"/usr/local/lib/hostd/addons/hostd-steam-games"} {
		if !in.exists(f) {
			t.Errorf("missing %s", f)
		}
	}
	for _, want := range []string{"flatpak install --system -y --noninteractive flathub com.valvesoftware.Steam"} {
		if !m.didRun(want) {
			t.Errorf("did not run %q", want)
		}
	}
	ranScreen := func(cmd string) bool {
		return slices.ContainsFunc(m.ran, func(l string) bool { return strings.HasPrefix(l, "runuser -u screen") && strings.HasSuffix(l, cmd) })
	}
	if !ranScreen("systemctl --user enable --now hostd-steam-games.path hostd-steam-games.timer") ||
		!ranScreen("systemctl --user start hostd-steam-games.service") || !ranScreen("hostctl app rescan") {
		t.Errorf("user units or rescan: %q", m.ran)
	}
	if !slices.ContainsFunc(m.ran, func(l string) bool { return strings.HasPrefix(l, "bash ") && strings.HasSuffix(l, " addon_install") }) {
		t.Errorf("hook not run: %q", m.ran)
	}

	m.ran = nil
	if err := s.RemoveAddons(ctx, "screen", []string{"steam"}); err != nil {
		t.Fatal(err)
	}
	if in.exists("/home/screen/.config/hostd/apps/steam.toml") || in.exists("/home/screen/.config/systemd/user/hostd-steam-games.path") {
		t.Error("files stay")
	}
	if !ranScreen("systemctl --user disable --now hostd-steam-games.path hostd-steam-games.timer") ||
		!slices.ContainsFunc(m.ran, func(l string) bool { return strings.HasSuffix(l, " addon_remove") }) {
		t.Errorf("remove: %q", m.ran)
	}
}

func TestOutputMarkers(t *testing.T) {
	var b bytes.Buffer
	o := NewOutput(&b, false)
	o.Markers = true
	o.Step("A step")
	o.Info("quiet")
	o.Changed("wrote x")
	o.Warn("careful")
	if !o.StepChanged() {
		t.Error("the step did change")
	}
	o.Close()
	got := b.String()
	if !strings.Contains(got, "\001    wrote x") || !strings.Contains(got, "\001    careful") || !strings.Contains(got, "    quiet") {
		t.Fatalf("%q", got)
	}
}

func TestReleaseURL(t *testing.T) {
	for conf, want := range map[UpdateConf]string{
		{Channel: "edge"}:   "https://github.com/davitizhgenti/hostd/releases/download/edge",
		{Channel: "stable"}: "https://github.com/davitizhgenti/hostd/releases/latest/download",
	} {
		if got := conf.ReleaseURL(); got != want {
			t.Errorf("%s: %s", conf.Channel, got)
		}
	}
}

func TestRealChown(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	s := &Setup{Root: t.TempDir()}
	p := filepath.Join(s.Root, "f")
	os.WriteFile(p, nil, 0o644)
	if err := s.chown(p, me.Username); err != nil {
		t.Fatalf("chown to myself: %v", err)
	}
	if err := s.chown(p, "no-such-user-hostd"); err == nil {
		t.Fatal("chown to nobody")
	}
}

func TestHostdThatDoesNotStart(t *testing.T) {
	m := newFakeMachine(t)
	m.hostdBroken = true
	m.answers = map[string]string{"journalctl --user -u hostd -n 20 --no-pager": "hostd: [api] allow: \"x\" is not a network\n"}
	s, out := newSetup(t, m)
	err := s.Install(context.Background(), Options{GPU: "other", From: buildDir(t), SudoUser: "admin"})
	if err == nil || !strings.Contains(err.Error(), "hostd did not start") || !strings.Contains(out.String(), "is not a network") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Stale package lists: one apt-get update, then the install works; with
// the update not helping, the error names the packages.
func TestEnsurePackagesRetries(t *testing.T) {
	m := newFakeMachine(t)
	s, _ := newSetup(t, m)
	m.missing, m.aptFails = map[string]bool{"jq": true}, 1
	if err := s.ensurePackages(context.Background(), "jq"); err != nil || !m.didRun("apt-get update -q") {
		t.Fatalf("%v %q", err, m.ran)
	}
	m.missing, m.aptFails = map[string]bool{"jq": true}, 2
	if err := s.ensurePackages(context.Background(), "jq"); err == nil || !strings.Contains(err.Error(), "could not install jq") {
		t.Fatalf("%v", err)
	}
}

func TestExecRunner(t *testing.T) {
	var streamed bytes.Buffer
	r := ExecRunner{Out: &streamed}
	ctx := context.Background()
	out, err := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo out; echo err >&2"}, Env: []string{"X=1"}})
	if err != nil || string(out) != "out\n" {
		t.Fatalf("captured %q %v", out, err)
	}
	if _, err := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo $X", ""}, Env: []string{"X=streamed"}, Stream: true}); err != nil || streamed.String() != "streamed\n" {
		t.Fatalf("streamed %q %v", streamed.String(), err)
	}
	var w bytes.Buffer
	if _, err := r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo to-w; echo also >&2"}, Stdout: &w}); err != nil || w.String() != "to-w\nalso\n" {
		t.Fatalf("to a writer %q %v", w.String(), err)
	}
	_, err = r.Run(ctx, Cmd{Argv: []string{"sh", "-c", "echo first >&2; echo why it failed >&2; exit 3"}})
	if err == nil || !strings.Contains(err.Error(), "exit status 3: why it failed") {
		t.Fatalf("error %v", err)
	}
	if _, err := r.Run(ctx, Cmd{}); err == nil {
		t.Fatal("ran nothing")
	}
}

func TestBadReleaseKeysAndDocs(t *testing.T) {
	if _, err := parseKey([]byte("not a key")); err == nil || !strings.Contains(err.Error(), "not a PEM public key") {
		t.Fatalf("%v", err)
	}
	rsaPEM := "-----BEGIN PUBLIC KEY-----\nMFwwDQYJKoZIhvcNAQEBBQADSwAwSAJBAMuQ0T7ZiZP0HQ7dv4yDdj+sXjJHd0Zc\nSPnMOUMRsElUqGGv1H8UP6FAyYJyGJE6RyNdvYpNw8jfvuLCPbTB1+0CAwEAAQ==\n-----END PUBLIC KEY-----\n"
	if _, err := parseKey([]byte(rsaPEM)); err == nil || !strings.Contains(err.Error(), "not an Ed25519 key") {
		t.Fatalf("an RSA key: %v", err)
	}
	if err := untarGz([]byte("not gzip"), t.TempDir()); err == nil {
		t.Fatal("unpacked garbage")
	}
}

// The first update of a machine set up by the shell installer: its link
// and its copy of deploy/ give way to this binary.
func TestInstallSelfReplacesTheShellInstaller(t *testing.T) {
	m := newFakeMachine(t)
	s, out := newSetup(t, m)
	old := filepath.Join(m.root, "usr/local/share/hostd-setup")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "install.sh"), []byte("#!/bin/bash"), 0o755)
	os.MkdirAll(filepath.Join(m.root, "usr/local/sbin"), 0o755)
	os.Symlink("/usr/local/share/hostd-setup/install.sh", filepath.Join(m.root, "usr/local/sbin/hostd-setup"))
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the Go installer"), 0o755)
	in := &installer{Setup: s, o: Options{Self: self}}
	if err := in.installSelf(context.Background()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(filepath.Join(m.root, "usr/local/sbin/hostd-setup"))
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("hostd-setup: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.root, "usr/local/sbin/hostd-setup")); string(b) != "the Go installer" {
		t.Fatalf("content %q", b)
	}
	if _, err := os.Stat(old); err == nil || !strings.Contains(out.String(), "removed the shell installer") {
		t.Fatalf("the shell installer stays\n%s", out)
	}
	if err := (&installer{Setup: s, o: Options{Self: "/no/such/file"}}).installSelf(context.Background()); err == nil {
		t.Fatal("installed a binary that cannot be read")
	}
}

// An NVIDIA card: Debian's driver (unless NVIDIA's own is there), kernel
// mode setting, and Sway started through the script that allows the card.
func TestInstallWithNvidia(t *testing.T) {
	m := newFakeMachine(t)
	m.missing = map[string]bool{"nvidia-driver": true}
	os.MkdirAll(filepath.Join(m.root, "etc/apt/sources.list.d"), 0o755)
	os.WriteFile(filepath.Join(m.root, "etc/apt/sources.list.d/debian.sources"),
		[]byte("Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie\nComponents: main\n"), 0o644)
	s, out := newSetup(t, m)
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the installer"), 0o755)
	if err := s.Install(context.Background(), Options{GPU: "nvidia", From: buildDir(t), SudoUser: "admin", Self: self}); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !m.didRun("apt-get install -y -q nvidia-driver") || !m.didRun("update-initramfs -u") {
		t.Errorf("ran %q", m.ran)
	}
	if b, _ := os.ReadFile(filepath.Join(m.root, "etc/apt/sources.list.d/debian.sources")); !strings.Contains(string(b), "Components: main contrib non-free") {
		t.Errorf("non-free not on: %q", b)
	}
	greetd, _ := os.ReadFile(filepath.Join(m.root, "etc/greetd/config.toml"))
	if !strings.Contains(string(greetd), `command = "/usr/local/lib/hostd/start-sway"`) {
		t.Errorf("greetd:\n%s", greetd)
	}
	if b, _ := os.ReadFile(filepath.Join(m.root, "etc/modprobe.d/nvidia-drm-kms.conf")); !strings.Contains(string(b), "modeset=1") {
		t.Errorf("kms %q", b)
	}
	// Again: nothing to do.
	m.ran = nil
	if err := s.Install(context.Background(), Options{GPU: "nvidia", From: buildDir(t), SudoUser: "admin", Self: self, Update: &UpdateOptions{}}); err != nil || m.didRun("update-initramfs") {
		t.Fatalf("second run: %v %q", err, m.ran)
	}
}
