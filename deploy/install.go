package deploy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Options for one run of install (and update, which is install run by the
// newest hostd-setup).
type Options struct {
	ScreenUser string // default screen
	GPU        string // auto (default), nvidia or other
	From       string // take hostd and hostctl from this directory, not the release
	Release    Release
	ReleaseKey []byte // nil: hostd's
	SudoUser   string // the admin who ran sudo: their SSH keys go to the screen user
	Self       string // this hostd-setup binary, installed as /usr/local/sbin/hostd-setup
	// Update is set when this run is an update: its summary is short, and
	// add-ons run only when needed (see addons.go).
	Update *UpdateOptions
	Adding []string // add-ons just added (hostd-setup add): run them now
	Auto   bool     // run by the update timer: add-ons that change data hold back
}

// UpdateOptions say which add-ons an update runs anyway.
type UpdateOptions struct {
	All    bool     // every installed one
	Addons []string // these
}

// installer is one install run: the options and what it learns.
type installer struct {
	*Setup
	o Options

	arch, gpu   string
	user        screenUser
	swayCommand string
	needReboot  bool
	work        string // downloads
	version     string // the hostd being installed
	hostdChange bool   // hostd must restart
	menuChange  bool   // the menu must restart
	vncChanged  bool   // the VNC server must restart

	addonsSkipped int
}

type screenUser struct {
	name, home string
	uid        int
}

func (u screenUser) runtime() string { return "/run/user/" + strconv.Itoa(u.uid) }

// Install sets up the machine; the steps are those of deploy/install.sh
// before it moved here, in the same order and with the same messages.
func (s *Setup) Install(ctx context.Context, o Options) error {
	if o.ScreenUser == "" {
		o.ScreenUser = "screen"
	}
	if o.GPU == "" {
		o.GPU = "auto"
	}
	in := &installer{Setup: s, o: o}
	work, err := os.MkdirTemp("", "hostd-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	in.work = work
	for _, step := range []func(context.Context) error{
		in.checkSystem, in.enableNonFree, in.packages, in.nvidiaSway, in.screenUser,
		in.autologin, in.installHostd, in.installSelf, in.addons, in.updates,
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	in.summary(ctx)
	return nil
}

// --- helpers --------------------------------------------------------------

// run runs a command and returns what it printed.
func (s *Setup) run(ctx context.Context, argv ...string) ([]byte, error) {
	return s.Run.Run(ctx, Cmd{Argv: argv})
}

// ok reports whether a command succeeds.
func (s *Setup) ok(ctx context.Context, argv ...string) bool {
	_, err := s.run(ctx, argv...)
	return err == nil
}

// stream runs a command whose output is worth seeing (apt).
func (s *Setup) stream(ctx context.Context, env []string, argv ...string) error {
	_, err := s.Run.Run(ctx, Cmd{Argv: argv, Env: env, Stream: true})
	return err
}

// asScreen runs a command as the screen user, inside its systemd session.
func (in *installer) asScreen(ctx context.Context, argv ...string) ([]byte, error) {
	rt := in.user.runtime()
	full := append([]string{"runuser", "-u", in.user.name, "--", "env", "HOME=" + in.user.home, "USER=" + in.user.name,
		"XDG_RUNTIME_DIR=" + rt, "DBUS_SESSION_BUS_ADDRESS=unix:path=" + rt + "/bus"}, argv...)
	return in.run(ctx, full...)
}

// screenOK reports whether a command run as the screen user succeeds.
func (in *installer) screenOK(ctx context.Context, argv ...string) bool {
	_, err := in.asScreen(ctx, argv...)
	return err == nil
}

func (in *installer) exists(p string) bool {
	_, err := os.Lstat(in.path(p))
	return err == nil
}

// --- the steps ------------------------------------------------------------

func (in *installer) checkSystem(ctx context.Context) error {
	in.Out.Step("Checking the system")
	osr, _ := os.ReadFile(in.path("/etc/os-release"))
	vals := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(osr))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			vals[k] = strings.Trim(v, `"`)
		}
	}
	if vals["ID"] != "debian" || vals["VERSION_CODENAME"] != "trixie" {
		name := vals["PRETTY_NAME"]
		if name == "" {
			name = "unknown"
		}
		return fmt.Errorf("needs Debian 13 (trixie); this is %s", name)
	}
	out, err := in.run(ctx, "dpkg", "--print-architecture")
	if err != nil {
		return err
	}
	in.arch = strings.TrimSpace(string(out))
	in.Out.Info("Debian 13 (%s)", in.arch)

	in.gpu = in.o.GPU
	if in.gpu == "auto" {
		in.gpu = "other"
		devs, _ := filepath.Glob(in.path("/sys/bus/pci/devices/*"))
		for _, d := range devs {
			class, err := os.ReadFile(filepath.Join(d, "class"))
			if err != nil || !strings.HasPrefix(string(class), "0x03") {
				continue
			}
			if vendor, _ := os.ReadFile(filepath.Join(d, "vendor")); strings.TrimSpace(string(vendor)) == "0x10de" {
				in.gpu = "nvidia"
			}
		}
	}
	if in.gpu == "nvidia" && in.arch != "amd64" {
		return errors.New("the NVIDIA setup needs amd64")
	}
	in.Out.Info("GPU setup: %s", in.gpu)
	return nil
}

var (
	reComponents = regexp.MustCompile(`^Components:`)
	reDebLine    = regexp.MustCompile(`^deb(-src)? .* main( |$)`)
)

// enableNonFree adds contrib and non-free to Debian's sources (the NVIDIA
// driver is there).
func (in *installer) enableNonFree(context.Context) error {
	if in.gpu != "nvidia" {
		return nil
	}
	in.Out.Step("Enabling contrib and non-free (NVIDIA driver)")
	src, match := "/etc/apt/sources.list.d/debian.sources", reComponents
	if !in.exists(src) {
		src, match = "/etc/apt/sources.list", reDebLine
	}
	b, err := os.ReadFile(in.path(src))
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if !match.MatchString(l) {
			continue
		}
		for _, c := range []string{"contrib", "non-free"} {
			if !slices.Contains(strings.Fields(l), c) {
				l += " " + c
			}
		}
		lines[i] = l
	}
	changed, err := in.installFile([]byte(strings.Join(lines, "\n")), src, 0o644, "root")
	if err == nil && !changed {
		in.Out.Info("already enabled")
	}
	return err
}

// packages are what a hostd machine needs from Debian.
var packages = []string{
	"sway", "swaybg", "xwayland", "foot", "greetd", "mako-notifier", "libnotify-bin", "wayvnc", "grim",
	"pipewire", "pipewire-pulse", "wireplumber", "rtkit", "dbus-user-session",
	"podman", "uidmap", "fuse-overlayfs", "passt", "catatonit", "nftables",
	"jq", "curl", "ca-certificates", "git", "openssl",
	"python3-gi", "gir1.2-gtk-4.0",
}

// missingPackages returns the ones of pkgs that are not installed.
func (s *Setup) missingPackages(ctx context.Context, pkgs []string) []string {
	var missing []string
	for _, p := range pkgs {
		out, err := s.run(ctx, "dpkg-query", "-W", "-f=${Status}", p)
		if err != nil || strings.TrimSpace(string(out)) != "install ok installed" {
			missing = append(missing, p)
		}
	}
	return missing
}

// ensurePackages installs the packages that are missing.
func (s *Setup) ensurePackages(ctx context.Context, pkgs ...string) error {
	missing := s.missingPackages(ctx, pkgs)
	if len(missing) == 0 {
		return nil
	}
	s.Out.Info("installing: %s", strings.Join(missing, " "))
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	install := append([]string{"apt-get", "install", "-y", "-q"}, missing...)
	if err := s.stream(ctx, env, install...); err != nil {
		if err := s.stream(ctx, env, "apt-get", "update", "-q"); err != nil {
			return err
		}
		if err := s.stream(ctx, env, install...); err != nil {
			return fmt.Errorf("could not install %s: %w", strings.Join(missing, " "), err)
		}
	}
	s.Out.Changed("installed %s", strings.Join(missing, " "))
	return nil
}

func (in *installer) packages(ctx context.Context) error {
	in.Out.Step("Installing packages")
	pkgs := slices.Clone(packages)
	if in.gpu == "nvidia" {
		pkgs = append(pkgs, "linux-headers-amd64", "firmware-misc-nonfree")
		// NVIDIA's own driver (the nvidia add-on) replaces Debian's: keep it.
		if !in.exists("/usr/bin/nvidia-uninstall") {
			pkgs = append(pkgs, "nvidia-driver")
		}
	}
	missing := in.missingPackages(ctx, pkgs)
	if len(missing) == 0 {
		in.Out.Info("all %d packages already installed", len(pkgs))
		return nil
	}
	in.Out.Info("installing: %s", strings.Join(missing, " "))
	env := []string{"DEBIAN_FRONTEND=noninteractive"}
	if err := in.stream(ctx, env, "apt-get", "update", "-q"); err != nil {
		return err
	}
	if err := in.stream(ctx, env, append([]string{"apt-get", "install", "-y", "-q"}, missing...)...); err != nil {
		return err
	}
	in.Out.Changed("installed %s", strings.Join(missing, " "))
	if slices.Contains(missing, "nvidia-driver") || slices.Contains(missing, "linux-headers-amd64") {
		in.needReboot = true
	}
	return nil
}

// nvidiaSway sets up NVIDIA's driver for Sway: kernel mode setting, and
// the start script that picks the renderer.
func (in *installer) nvidiaSway(ctx context.Context) error {
	in.swayCommand = "sway"
	if in.gpu != "nvidia" {
		return nil
	}
	in.Out.Step("Setting up the NVIDIA driver for Sway")
	changed, err := in.installFile([]byte("options nvidia-drm modeset=1 fbdev=1\n"), "/etc/modprobe.d/nvidia-drm-kms.conf", 0o644, "root")
	switch {
	case err != nil:
		return err
	case changed:
		if err := in.stream(ctx, nil, "update-initramfs", "-u"); err != nil {
			return err
		}
		in.needReboot = true
	default:
		in.Out.Info("kernel mode setting already on")
	}
	changed, err = in.installFile(file("start-sway"), "/usr/local/lib/hostd/start-sway", 0o755, "root")
	switch {
	case err != nil:
		return err
	case changed:
		in.needReboot = true
	default:
		in.Out.Info("Sway start script up to date")
	}
	in.swayCommand = "/usr/local/lib/hostd/start-sway"
	return nil
}

func (in *installer) screenUser(ctx context.Context) error {
	name := in.o.ScreenUser
	in.Out.Step("Setting up the " + name + " user")
	if in.ok(ctx, "id", name) {
		in.Out.Info("user exists")
	} else {
		if _, err := in.run(ctx, "useradd", "-m", "-s", "/bin/bash", name); err != nil {
			return err
		}
		in.Out.Changed("created user %s", name)
	}
	groups, _ := in.run(ctx, "id", "-nG", name)
	for _, g := range []string{"video", "render", "input"} {
		if !in.ok(ctx, "getent", "group", g) {
			if _, err := in.run(ctx, "groupadd", "-r", g); err != nil {
				return err
			}
		}
		if !slices.Contains(strings.Fields(string(groups)), g) {
			if _, err := in.run(ctx, "usermod", "-aG", g, name); err != nil {
				return err
			}
			in.Out.Changed("added %s to %s", name, g)
		}
	}
	if !in.listed("/etc/subuid", name) || !in.listed("/etc/subgid", name) {
		if _, err := in.run(ctx, "usermod", "--add-subuids", "200000-265535", "--add-subgids", "200000-265535", name); err != nil {
			return err
		}
		in.Out.Changed("added subordinate IDs for rootless Podman")
	}
	if in.exists("/var/lib/systemd/linger/" + name) {
		in.Out.Info("lingering on")
	} else {
		if _, err := in.run(ctx, "loginctl", "enable-linger", name); err != nil {
			return err
		}
		in.Out.Changed("enabled lingering (user services start at boot)")
	}
	home, err := in.homeOf(ctx, name)
	if err != nil {
		return err
	}
	out, err := in.run(ctx, "id", "-u", name)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("id -u %s: %q", name, out)
	}
	in.user = screenUser{name: name, home: home, uid: uid}

	if err := in.copySSHKeys(ctx); err != nil {
		return err
	}

	// Session config. A running session picks up changes at once (exec
	// lines do not run again).
	changed, err := in.installFile(file("sway/config"), home+"/.config/sway/config", 0o644, name)
	if err != nil {
		return err
	}
	if changed {
		socks, _ := filepath.Glob(in.path(in.user.runtime() + "/sway-ipc.*.sock"))
		for _, sock := range socks {
			if _, err := in.asScreen(ctx, "env", "SWAYSOCK="+strings.TrimPrefix(sock, filepath.Clean(in.Root)), "swaymsg", "reload"); err == nil {
				in.Out.Info("reloaded the running Sway")
			}
		}
	} else {
		in.Out.Info("sway config up to date")
	}
	changed, err = in.installFile(file("mako/config"), home+"/.config/mako/config", 0o644, name)
	if err != nil {
		return err
	}
	if changed {
		if _, err := in.asScreen(ctx, "makoctl", "reload"); err == nil {
			in.Out.Info("reloaded mako")
		}
	} else {
		in.Out.Info("mako config up to date")
	}
	vncChanged, err := in.installFile(file("systemd/wayvnc.service"), home+"/.config/systemd/user/wayvnc.service", 0o644, name)
	if err != nil {
		return err
	}
	if !vncChanged {
		in.Out.Info("VNC service up to date")
	}
	in.vncChanged = vncChanged

	// Rootless Podman's API socket, for hostd's container runners.
	wants := home + "/.config/systemd/user/sockets.target.wants"
	if in.exists(wants + "/podman.socket") {
		in.Out.Info("Podman socket enabled")
		return nil
	}
	if err := in.mkdirs(wants, name); err != nil {
		return err
	}
	if err := os.Symlink("/usr/lib/systemd/user/podman.socket", in.path(wants+"/podman.socket")); err != nil {
		return err
	}
	if err := in.chown(in.path(wants+"/podman.socket"), name); err != nil {
		return err
	}
	in.Out.Changed("enabled the Podman API socket")
	if in.exists(in.user.runtime() + "/systemd") { // its user manager runs: start it now
		_, _ = in.run(ctx, "systemctl", "--user", "--machine="+name+"@", "start", "podman.socket")
	}
	return nil
}

// listed reports whether a file of /etc has a line for user (subuid).
func (in *installer) listed(file, user string) bool {
	b, _ := os.ReadFile(in.path(file))
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, user+":") {
			return true
		}
	}
	return false
}

func (s *Setup) homeOf(ctx context.Context, name string) (string, error) {
	out, err := s.run(ctx, "getent", "passwd", name)
	if err != nil {
		return "", fmt.Errorf("no user %s: %w", name, err)
	}
	f := strings.Split(strings.TrimSpace(string(out)), ":")
	if len(f) < 6 {
		return "", fmt.Errorf("getent passwd %s: %q", name, out)
	}
	return f[5], nil
}

// copySSHKeys gives the screen user the keys of the admin who ran sudo,
// so whoever reaches the admin account reaches the screen user too.
func (in *installer) copySSHKeys(ctx context.Context) error {
	admin := in.o.SudoUser
	if admin == "" || admin == "root" {
		return nil
	}
	adminHome, err := in.homeOf(ctx, admin)
	if err != nil {
		return err
	}
	theirs, err := os.ReadFile(in.path(adminHome + "/.ssh/authorized_keys"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bytes.TrimSpace(theirs)) == 0 {
		in.Out.Warn("no SSH keys for %s to copy (add yours with ssh-copy-id first)", admin)
		return nil
	}
	keys := in.user.home + "/.ssh/authorized_keys"
	if err := in.mkdirs(in.user.home+"/.ssh", in.user.name); err != nil {
		return err
	}
	_ = os.Chmod(in.path(in.user.home+"/.ssh"), 0o700)
	have, _ := os.ReadFile(in.path(keys))
	lines := strings.Split(string(have), "\n")
	added := 0
	for _, k := range strings.Split(string(theirs), "\n") {
		if strings.TrimSpace(k) == "" || slices.Contains(lines, k) {
			continue
		}
		if len(have) > 0 && !bytes.HasSuffix(have, []byte("\n")) {
			have = append(have, '\n')
		}
		have = append(have, k+"\n"...)
		lines = append(lines, k)
		added++
	}
	if err := os.WriteFile(in.path(keys), have, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(in.path(keys), 0o600)
	if err := in.chown(in.path(keys), in.user.name); err != nil {
		return err
	}
	if added > 0 {
		in.Out.Changed("copied %d SSH key(s) from %s", added, admin)
	} else {
		in.Out.Info("SSH keys in place")
	}
	return nil
}

func (in *installer) autologin(ctx context.Context) error {
	in.Out.Step("Setting up automatic login to Sway")
	conf := strings.NewReplacer("@SWAY_COMMAND@", in.swayCommand, "@SCREEN_USER@", in.user.name).Replace(string(file("greetd/config.toml")))
	changed, err := in.installFile([]byte(conf), "/etc/greetd/config.toml", 0o644, "root")
	switch {
	case err != nil:
		return err
	case changed:
		in.needReboot = true
	default:
		in.Out.Info("greetd config up to date")
	}
	if in.ok(ctx, "systemctl", "is-enabled", "--quiet", "greetd") {
		in.Out.Info("greetd enabled")
		return nil
	}
	if _, err := in.run(ctx, "systemctl", "enable", "greetd"); err != nil {
		return err
	}
	in.Out.Changed("enabled greetd")
	in.needReboot = true
	return nil
}

// --- hostd ----------------------------------------------------------------

func (in *installer) installHostd(ctx context.Context) error {
	in.Out.Step("Installing hostd")
	if err := in.getBinaries(ctx); err != nil {
		return err
	}
	var out bytes.Buffer // hostd prints its version on standard error
	_, err := in.Run.Run(ctx, Cmd{Argv: []string{filepath.Join(in.work, "hostd"), "-version"}, Stdout: &out})
	f := strings.Fields(out.String())
	if err != nil || len(f) < 2 {
		return errors.New("the hostd binary does not run on this machine")
	}
	in.version = f[1]

	// Versions sit side by side; "current" points at the running one,
	// which is what updates and rollbacks switch. hostd itself switches
	// (hostd -install, the same code an upload through the API runs): it
	// keeps the state and the config of the version that runs now, so a
	// rollback brings them back too.
	lib := in.user.home + "/.local/lib/hostd"
	cur, _ := os.Readlink(in.path(lib + "/current"))
	same := false
	if cur == "versions/"+in.version {
		old, _ := os.ReadFile(in.path(lib + "/versions/" + in.version + "/hostd"))
		now, _ := os.ReadFile(filepath.Join(in.work, "hostd"))
		same = bytes.Equal(old, now)
	}
	if same {
		in.Out.Info("hostd %s already installed", in.version)
	} else {
		_ = os.Chmod(in.work, 0o755) // the screen user runs the new binary from here
		out, err := in.asScreen(ctx, filepath.Join(in.work, "hostd"), "-install", filepath.Join(in.work, "hostd"), "-lib", lib, "-no-restart")
		if err != nil {
			return fmt.Errorf("could not install hostd %s: %w", in.version, err)
		}
		in.Out.Changed("%s", strings.TrimSpace(string(out)))
		in.hostdChange = true
	}

	if ch, err := in.installFile(file("rollback.sh"), lib+"/rollback.sh", 0o755, in.user.name); err != nil {
		return err
	} else if !ch {
		in.Out.Info("rollback script up to date")
	}
	if ch, err := in.installFile(file("systemd/hostd-rollback.service"), in.user.home+"/.config/systemd/user/hostd-rollback.service", 0o644, in.user.name); err != nil {
		return err
	} else if !ch {
		in.Out.Info("rollback service up to date")
	}
	hostctl, err := os.ReadFile(filepath.Join(in.work, "hostctl"))
	if err != nil {
		return err
	}
	if ch, err := in.installFile(hostctl, "/usr/local/bin/hostctl", 0o755, "root"); err != nil {
		return err
	} else if !ch {
		in.Out.Info("hostctl up to date")
	}
	if err := in.installDocs(); err != nil {
		return err
	}
	if err := in.installMenu(); err != nil {
		return err
	}
	if ch, err := in.installFile(file("systemd/hostd.service"), in.user.home+"/.config/systemd/user/hostd.service", 0o644, in.user.name); err != nil {
		return err
	} else if ch {
		in.hostdChange = true
	} else {
		in.Out.Info("hostd service up to date")
	}
	wants := in.user.home + "/.config/systemd/user/default.target.wants"
	if !in.exists(wants + "/hostd.service") {
		if err := in.mkdirs(wants, in.user.name); err != nil {
			return err
		}
		if err := os.Symlink("../hostd.service", in.path(wants+"/hostd.service")); err != nil {
			return err
		}
		if err := in.chown(in.path(wants+"/hostd.service"), in.user.name); err != nil {
			return err
		}
		in.Out.Changed("hostd starts at boot")
	}
	return in.startHostd(ctx)
}

// getBinaries puts hostd, hostctl and the docs (if any) in the work
// directory: from --from, or downloaded from the release and verified.
func (in *installer) getBinaries(ctx context.Context) error {
	if in.o.From != "" {
		for _, cmd := range []string{"hostd", "hostctl"} {
			src := filepath.Join(in.o.From, cmd+"-linux-"+in.arch)
			if _, err := os.Stat(src); err != nil {
				src = filepath.Join(in.o.From, cmd)
			}
			if err := copyFile(src, filepath.Join(in.work, cmd), 0o755); err != nil {
				return fmt.Errorf("%s has no %s or %s-linux-%s", in.o.From, cmd, cmd, in.arch)
			}
		}
		_ = copyFile(filepath.Join(in.o.From, "hostd-docs.tar.gz"), filepath.Join(in.work, "hostd-docs.tar.gz"), 0o644)
		in.Out.Info("using the binaries in %s", in.o.From)
		return nil
	}
	names := []string{"hostd-linux-" + in.arch, "hostctl-linux-" + in.arch}
	if err := in.o.Release.Fetch(ctx, in.work, append([]string{"SHA256SUMS", "SHA256SUMS.sig"}, names...)...); err != nil {
		return err
	}
	if Lists(in.work, "hostd-docs.tar.gz") {
		if err := in.o.Release.Fetch(ctx, in.work, "hostd-docs.tar.gz"); err != nil {
			return err
		}
		names = append(names, "hostd-docs.tar.gz")
	}
	if err := Verify(in.work, in.o.ReleaseKey, names...); err != nil {
		return err
	}
	for _, cmd := range []string{"hostd", "hostctl"} {
		if err := os.Rename(filepath.Join(in.work, cmd+"-linux-"+in.arch), filepath.Join(in.work, cmd)); err != nil {
			return err
		}
		_ = os.Chmod(filepath.Join(in.work, cmd), 0o755)
	}
	in.Out.Info("downloaded from %s (signed; checksums match)", in.o.Release.URL)
	return nil
}

// installDocs unpacks the docs site, served by hostd at /docs/.
func (in *installer) installDocs() error {
	tarball, err := os.ReadFile(filepath.Join(in.work, "hostd-docs.tar.gz"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil // a build without docs
	} else if err != nil {
		return err
	}
	docs := "/usr/local/share/hostd/docs"
	h := sha256.Sum256(tarball)
	sum := hex.EncodeToString(h[:])
	if b, _ := os.ReadFile(in.path(docs + "/.build")); strings.TrimSpace(string(b)) == sum {
		in.Out.Info("docs up to date")
		return nil
	}
	if !in.exists(docs) {
		in.hostdChange = true // hostd looks for them when it starts
	}
	next := in.path(docs + ".new")
	_ = os.RemoveAll(next)
	if err := untarGz(tarball, next); err != nil {
		return fmt.Errorf("cannot unpack the docs: %w", err)
	}
	if err := os.WriteFile(filepath.Join(next, ".build"), []byte(sum+"\n"), 0o644); err != nil {
		return err
	}
	_ = os.RemoveAll(in.path(docs + ".old"))
	if in.exists(docs) {
		if err := os.Rename(in.path(docs), in.path(docs+".old")); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(in.path(docs)), 0o755); err != nil {
		return err
	}
	if err := os.Rename(next, in.path(docs)); err != nil {
		return err
	}
	_ = os.RemoveAll(in.path(docs + ".old"))
	in.Out.Changed("installed the docs (http://<machine>:7300/docs/)")
	return nil
}

// installMenu puts hostd's on-screen menu in place: a hidden app in the
// catalog, opened by display.menu (Super, or a held Guide button).
func (in *installer) installMenu() error {
	// The files of its old name (the "switcher", hostd-overlay) go.
	_ = os.Remove(in.path("/usr/local/lib/hostd/hostd-overlay"))
	_ = os.Remove(in.path(in.user.home + "/.config/hostd/apps/hostd-overlay.toml"))
	ch, err := in.installFile(file("menu/hostd-menu"), "/usr/local/lib/hostd/hostd-menu", 0o755, "root")
	if err != nil {
		return err
	}
	in.menuChange = in.menuChange || ch
	if !ch {
		in.Out.Info("menu up to date")
	}
	_ = os.Remove(in.path("/usr/local/bin/hostd-switch")) // replaced by hostd's own key bindings
	// Backups that older installers made of hostd's own files.
	for _, pattern := range []string{"/usr/local/bin/hostctl.orig", "/usr/local/bin/hostd-steam-session.orig", "/usr/local/lib/hostd/*.orig",
		"/usr/local/lib/hostd/addons/*.orig", in.user.home + "/.config/hostd/apps/*.orig"} {
		matches, _ := filepath.Glob(in.path(pattern))
		for _, m := range matches {
			rel := "/" + strings.TrimPrefix(m, filepath.Clean(in.Root)+"/")
			if in.hostdOwned(rel) {
				_ = os.Remove(m)
				in.Out.Changed("removed %s (a backup of hostd's own file)", rel)
			}
		}
	}
	ch, err = in.installFile(file("menu/hostd-menu.toml"), in.user.home+"/.config/hostd/apps/hostd-menu.toml", 0o644, in.user.name)
	if err != nil {
		return err
	}
	in.menuChange = in.menuChange || ch
	if !ch {
		in.Out.Info("menu app file up to date")
	}
	return nil
}

// startHostd (re)starts hostd as the screen user's service when anything
// of it changed, logs the screen user's hostctl in, and gives the menu its
// token.
func (in *installer) startHostd(ctx context.Context) error {
	// Lingering starts the user's systemd: wait for it before talking to it.
	bus := in.user.runtime() + "/bus"
	for i := 0; i < 60 && !in.exists(bus); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	if !in.exists(bus) {
		return fmt.Errorf("the %s user's systemd did not start", in.user.name)
	}
	if _, err := in.asScreen(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	// A running VNC server picks up a changed unit only when restarted.
	if in.vncChanged && in.screenOK(ctx, "systemctl", "--user", "is-active", "--quiet", "wayvnc.service") {
		if _, err := in.asScreen(ctx, "systemctl", "--user", "restart", "wayvnc.service"); err == nil {
			in.Out.Changed("restarted the VNC server")
		}
	}
	if in.hostdChange || !in.screenOK(ctx, "systemctl", "--user", "is-active", "--quiet", "hostd") {
		// Type=notify: this returns once hostd is serving, or fails.
		if _, err := in.asScreen(ctx, "systemctl", "--user", "restart", "hostd"); err != nil {
			if out, jerr := in.asScreen(ctx, "journalctl", "--user", "-u", "hostd", "-n", "20", "--no-pager"); jerr == nil {
				_, _ = in.Out.Writer().Write(out)
			}
			return errors.New("hostd did not start")
		}
		in.Out.Changed("hostd is running")
	} else {
		in.Out.Info("hostd is running")
	}

	// On its first start hostd writes an admin token for the first login.
	// It goes to the screen user's own hostctl, so hostctl works on this
	// machine and can create tokens for other devices.
	tokenFile := in.user.runtime() + "/hostd-admin-token"
	config := in.user.home + "/.config/hostctl/config.toml"
	if in.exists(tokenFile) && !in.exists(config) {
		if _, err := in.asScreen(ctx, "hostctl", "login", "unix://"+in.user.runtime()+"/hostd.sock", "--token-file", tokenFile); err != nil {
			return err
		}
		in.Out.Changed("logged the %s user's hostctl in", in.user.name)
	}
	// The menu's token: kind "local", so what is chosen on the screen
	// counts as someone at the screen (it comes to the front, and outranks
	// phones and scripts).
	localToken := in.user.home + "/.config/hostd/local.token"
	if b, _ := os.ReadFile(in.path(localToken)); len(bytes.TrimSpace(b)) == 0 && in.exists(config) {
		out, err := in.asScreen(ctx, "hostctl", "--json", "token", "create", "menu", "--kind", "local", "--scopes", "read,apps")
		var t struct{ Secret string }
		if err == nil && json.Unmarshal(out, &t) == nil && strings.HasPrefix(t.Secret, "hostd_") {
			if err := in.mkdirs(filepath.Dir(localToken), in.user.name); err != nil {
				return err
			}
			if err := os.WriteFile(in.path(localToken), []byte(t.Secret+"\n"), 0o600); err != nil {
				return err
			}
			if err := in.chown(in.path(localToken), in.user.name); err != nil {
				return err
			}
			in.Out.Changed("created the menu's token")
		} else {
			in.Out.Warn("warning: could not create the menu's token; the menu will not work")
		}
	}
	return in.restartMenu(ctx, localToken)
}

// restartMenu: a running menu keeps its old code. If it was on the screen
// it comes back there (as if opened from the screen); otherwise it waits
// for the next Super or Guide press.
func (in *installer) restartMenu(ctx context.Context, localToken string) error {
	token, _ := os.ReadFile(in.path(localToken))
	if !in.menuChange || len(bytes.TrimSpace(token)) == 0 {
		return nil
	}
	out, err := in.asScreen(ctx, "hostctl", "--json", "ps")
	if err != nil {
		return nil //nolint:nilerr // hostd does not answer: no menu runs that could be restarted
	}
	var running []struct{ ID string }
	_ = json.Unmarshal(out, &running) // an answer it cannot read: as if no menu ran
	if !slices.ContainsFunc(running, func(r struct{ ID string }) bool { return r.ID == "hostd-menu" }) {
		return nil
	}
	inFront := false
	if out, err := in.asScreen(ctx, "hostctl", "--json", "get", "display", "windows"); err == nil {
		var wins []struct {
			Instance string
			Focused  bool
		}
		if json.Unmarshal(out, &wins) == nil {
			inFront = slices.ContainsFunc(wins, func(w struct {
				Instance string
				Focused  bool
			}) bool {
				return w.Instance == "hostd-menu" && w.Focused
			})
		}
	}
	_, _ = in.asScreen(ctx, "hostctl", "stop", "hostd-menu")
	if inFront {
		_, _ = in.asScreen(ctx, "env", "HOSTD_TOKEN="+strings.TrimSpace(string(token)), "hostctl", "start", "hostd-menu")
	}
	in.Out.Changed("restarted the menu on its new version")
	return nil
}

// installSelf puts this hostd-setup on the PATH, so later updates and an
// uninstall work without anything else.
func (in *installer) installSelf(context.Context) error {
	in.Out.Step("Installing hostd-setup (update and uninstall)")
	self, err := os.ReadFile(in.o.Self)
	if err != nil {
		return fmt.Errorf("reading this hostd-setup: %w", err)
	}
	// The installer of earlier versions was a copy of deploy/ with a link.
	if fi, err := os.Lstat(in.path("/usr/local/sbin/hostd-setup")); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		_ = os.Remove(in.path("/usr/local/sbin/hostd-setup"))
	}
	if in.exists("/usr/local/share/hostd-setup") {
		_ = os.RemoveAll(in.path("/usr/local/share/hostd-setup"))
		in.Out.Changed("removed the shell installer of earlier versions")
	}
	ch, err := in.installFile(self, "/usr/local/sbin/hostd-setup", 0o755, "root")
	if err == nil && !ch {
		in.Out.Info("hostd-setup up to date")
	}
	return err
}

// updates sets up the update timer and records the build now in use.
func (in *installer) updates(ctx context.Context) error {
	in.Out.Step("Updates")
	conf := "/etc/hostd/update.conf"
	if !in.exists(conf) {
		text := strings.Join([]string{
			"# hostd updates: hostd-update.timer looks for a new signed build every few",
			"# hours (sudo hostd-setup auto-update).",
			"#   CHANNEL: edge (every build of main that passed CI) or stable (releases)",
			"#   APPLY:   auto (install it once no deploy or scene runs and nobody uses",
			"#            the screen), notify (a notice on the screen), manual (nothing;",
			"#            sudo hostd-setup update)",
			"CHANNEL=edge", "APPLY=auto", "",
		}, "\n")
		if err := in.mkdirs("/etc/hostd", "root"); err != nil {
			return err
		}
		if err := os.WriteFile(in.path(conf), []byte(text), 0o644); err != nil {
			return err
		}
		in.Out.Changed("wrote %s (channel edge, applied automatically)", conf)
	}
	unitsChanged := false
	for _, u := range []string{"hostd-update.service", "hostd-update.timer"} {
		ch, err := in.installFile(file("systemd/"+u), "/etc/systemd/system/"+u, 0o644, "root")
		if err != nil {
			return err
		}
		unitsChanged = unitsChanged || ch
	}
	if unitsChanged {
		_, _ = in.run(ctx, "systemctl", "daemon-reload")
	}
	if !in.ok(ctx, "systemctl", "is-enabled", "--quiet", "hostd-update.timer") {
		if _, err := in.run(ctx, "systemctl", "enable", "--now", "hostd-update.timer"); err == nil {
			in.Out.Changed("enabled hostd-update.timer")
		} else {
			in.Out.Warn("could not enable hostd-update.timer (no systemd?)")
		}
	}
	c := ReadUpdateConf(in.path(conf))
	in.Out.Info("channel %s, updates %s (%s)", c.Channel, c.Apply, conf)
	// This build is the one in use now: auto-update compares against it.
	if sums, err := os.ReadFile(filepath.Join(in.work, "SHA256SUMS")); err == nil && in.o.From == "" {
		if err := os.WriteFile(in.path("/etc/hostd/applied.sha256"), sums, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (in *installer) summary(ctx context.Context) {
	in.Out.Done()
	if !in.Out.AnyChanged() {
		in.Out.Info("No changes needed; the system was already set up.")
	}
	if in.addonsSkipped > 0 {
		in.Out.Info("%d add-on(s) not run again: nothing in them changed (hostd-setup update --all runs them, or update NAME)", in.addonsSkipped)
	}
	if in.needReboot {
		in.Out.Info("Reboot to finish: sudo reboot")
		in.Out.Info("After the reboot the screen shows a plain dark background, with no login prompt.")
	}
	if in.o.Update != nil {
		// An update: what is running is all there is to say (install tells more).
		in.Out.Info("hostd %s is running.", in.version)
		return
	}
	ip := "<this-machine>"
	if out, err := in.run(ctx, "hostname", "-I"); err == nil {
		if f := strings.Fields(string(out)); len(f) > 0 {
			ip = f[0]
		}
	}
	in.Out.Info("hostd %s is running. hostctl works on this machine as %s:", in.version, in.user.name)
	in.Out.Info("  ssh %s@%s hostctl apps --all", in.user.name, ip)
	in.Out.Info("Other devices (a phone, a script) use the API at http://%s:7300", ip)
	in.Out.Info("with their own token, made with: hostctl token create <name> --scopes read,apps")
	in.Out.Info("Later: sudo hostd-setup update (latest version), sudo hostd-setup uninstall")
}

// --- small helpers -----------------------------------------------------------

func copyFile(src, dst string, mode fs.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, mode)
}

// untarGz unpacks a .tar.gz into dir, refusing paths that leave it.
func untarGz(b []byte, dir string) error {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(dir, h.Name)
		if !strings.HasPrefix(target, filepath.Clean(dir)+string(os.PathSeparator)) && target != filepath.Clean(dir) {
			return fmt.Errorf("%s leaves the directory", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}
