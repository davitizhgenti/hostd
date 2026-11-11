package deploy

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The add-on hooks (bash) run here for real, against a machine in a
// directory (HOSTD_ROOT) and with every command that would change the
// system replaced by a stub that records its arguments. What they call,
// the files they write and what they report are checked; that they work
// on real hardware is for core.

// hookRig is one such machine.
type hookRig struct {
	t     *testing.T
	root  string // the machine's /
	stubs string // the stub commands, first on PATH
	log   string // one line per stub call: name and arguments
	out   bytes.Buffer
}

// The commands the hooks may call that touch the system. Everything else
// (cat, sed, cp, mkdir...) is the real one.
var hookStubs = []string{
	"apt-get", "certutil", "chown", "curl", "df", "dpkg", "dpkg-query", "flatpak", "free", "git", "gpg", "hostctl", "hostname", "ip",
	"mariadb", "modprobe", "nginx", "nvidia-smi", "openssl", "pgrep", "php8.3", "python3", "runuser", "sh", "sha256sum", "ssh", "ssh-keygen",
	"qrencode", "sleep", "systemctl", "tailscale", "tar", "timedatectl", "ufw", "update-ca-certificates", "update-initramfs", "wg",
}

func newHookRig(t *testing.T) *hookRig {
	t.Helper()
	r := &hookRig{t: t, root: t.TempDir(), stubs: t.TempDir()}
	r.log = filepath.Join(r.stubs, "calls.log")
	// A stub logs its call; NAME.out is what it prints, NAME.rc its exit
	// status, and NAME.sh, if there, runs first with its arguments. curl's
	// -o FILE is created.
	stub := `#!/bin/bash
name=${0##*/}
printf '%s\n' "$name $*" >>"$STUB_LOG"
prev=""
[ "$name" = curl ] && for a in "$@"; do [ "$prev" = -o ] && [ "$a" != /dev/null ] && : >"$a"; prev=$a; done
[ -f "$STUB_DIR/$name.sh" ] && . "$STUB_DIR/$name.sh"
[ -f "$STUB_DIR/$name.out" ] && cat "$STUB_DIR/$name.out"
[ -f "$STUB_DIR/$name.rc" ] && exit "$(cat "$STUB_DIR/$name.rc")"
exit 0
`
	r.write(filepath.Join(r.stubs, "stub"), stub, 0o755)
	for _, name := range hookStubs {
		if err := os.Symlink("stub", filepath.Join(r.stubs, name)); err != nil {
			t.Fatal(err)
		}
	}
	// install is real, without the owner: the test is not root.
	real, err := exec.LookPath("install")
	if err != nil {
		t.Skip("no install command")
	}
	r.write(filepath.Join(r.stubs, "install"), `#!/bin/bash
args=()
while [ $# -gt 0 ]; do
	case $1 in -o | -g) shift 2 ;; *) args+=("$1"); shift ;; esac
done
exec `+real+` "${args[@]}"
`, 0o755)
	// The screen user's commands (as_screen: runuser -u screen -- ...) run,
	// as the test's own user; the panel's (as www-data) answer artisan.out.
	r.script("runuser", `if [ "$2" = www-data ]; then cat "$STUB_DIR/artisan.out" 2>/dev/null; exit 0; fi; shift 3; exec "$@"`)
	// apt-get install remembers what it installed, and dpkg-query says so.
	r.script("apt-get", `[ "$1" = install ] && for p in "$@"; do case $p in -*|install) ;; *) echo "$p" >>"$STUB_DIR/installed" ;; esac; done`)
	r.script("dpkg-query", `[ "$1" = -W ] && [ "$2" = '-f=${Status}' ] && grep -qsx "$3" "$STUB_DIR/installed" && { printf 'install ok installed'; exit 0; }`)
	t.Setenv("PATH", r.stubs+":"+os.Getenv("PATH"))
	t.Setenv("STUB_LOG", r.log)
	t.Setenv("STUB_DIR", r.stubs)
	r.file("/etc/hostd/addons", "")
	return r
}

func (r *hookRig) write(path, body string, mode os.FileMode) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		r.t.Fatal(err)
	}
}

// file writes a file on the machine.
func (r *hookRig) file(path, body string) { r.write(filepath.Join(r.root, path), body, 0o644) }

// read reads a file on the machine; "" if there is none.
func (r *hookRig) read(path string) string {
	b, _ := os.ReadFile(filepath.Join(r.root, path))
	return string(b)
}

func (r *hookRig) exists(path string) bool {
	_, err := os.Lstat(filepath.Join(r.root, path))
	return err == nil
}

// stub sets what a stubbed command prints and its exit status.
func (r *hookRig) stub(name, out string, rc int) {
	r.write(filepath.Join(r.stubs, name+".out"), out, 0o644)
	r.write(filepath.Join(r.stubs, name+".rc"), strconv.Itoa(rc), 0o644)
}

// script gives a stubbed command something to do (bash, its arguments in $@).
func (r *hookRig) script(name, body string) {
	r.write(filepath.Join(r.stubs, name+".sh"), body, 0o644)
}

// run runs an add-on's hook function as hostd-setup does, with a fresh
// call log and output.
func (r *hookRig) run(addon, fn string, o Options) (*installer, error) {
	r.t.Helper()
	a, err := LoadAddon(addon)
	if err != nil {
		r.t.Fatal(err)
	}
	_ = os.Remove(r.log)
	r.out.Reset()
	me, err := user.Current()
	if err != nil {
		r.t.Fatal(err)
	}
	in := &installer{Setup: &Setup{Root: r.root, Run: ExecRunner{Out: &r.out}, Out: NewOutput(&r.out, true)}, o: o,
		user: screenUser{name: me.Username, home: filepath.Join(r.root, "home/screen"), uid: 1000}}
	err = in.runHook(context.Background(), a, fn)
	in.Out.Close()
	return in, err
}

func (r *hookRig) calls() string {
	b, _ := os.ReadFile(r.log)
	return string(b)
}

// called reports whether a stub was called with a command line that starts so.
func (r *hookRig) called(prefix string) bool {
	for _, l := range strings.Split(r.calls(), "\n") {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

func (r *hookRig) mustCall(prefixes ...string) {
	r.t.Helper()
	for _, p := range prefixes {
		if !r.called(p) {
			r.t.Errorf("not called: %s\ncalls:\n%s", p, r.calls())
		}
	}
}

func (r *hookRig) mustNotCall(prefixes ...string) {
	r.t.Helper()
	for _, p := range prefixes {
		if r.called(p) {
			r.t.Errorf("called: %s\ncalls:\n%s", p, r.calls())
		}
	}
}

func (r *hookRig) says(text string) {
	r.t.Helper()
	if !strings.Contains(r.out.String(), text) {
		r.t.Errorf("output lacks %q:\n%s", text, r.out.String())
	}
}

// --- nvidia ------------------------------------------------------------------

const nvVersion = "580.178.04"

// nvidiaMachine has an NVIDIA card; own is the version of NVIDIA's own
// driver installed ("" for Debian's).
func nvidiaMachine(t *testing.T, own string) *hookRig {
	r := newHookRig(t)
	r.file("/sys/bus/pci/devices/0000:01:00.0/class", "0x030000\n")
	r.file("/sys/bus/pci/devices/0000:01:00.0/vendor", "0x10de\n")
	r.file("/etc/hostd/addons", "flatpak\nnvidia\n")
	if own != "" {
		r.write(filepath.Join(r.root, "usr/bin/nvidia-installer"), "#!/bin/sh\necho 'nvidia-installer:  version "+own+"'\n", 0o755)
		r.write(filepath.Join(r.root, "usr/bin/nvidia-uninstall"), "#!/bin/sh\necho \"nvidia-uninstall $*\" >>\"$STUB_LOG\"\n", 0o755)
	}
	return r
}

func TestNvidiaHookNoCard(t *testing.T) {
	r := newHookRig(t)
	r.file("/etc/hostd/addons", "flatpak\nnvidia\n")
	_, err := r.run("nvidia", "addon_install", Options{Adding: []string{"nvidia"}})
	if err == nil || !strings.Contains(err.Error(), "no NVIDIA graphics card") {
		t.Fatalf("err %v", err)
	}
	if got := r.read("/etc/hostd/addons"); got != "flatpak\n" {
		t.Fatalf("not taken off the installed add-ons: %q", got)
	}
}

// A plain update never swaps drivers: it stops the screen.
func TestNvidiaHookUpdateOnlyReports(t *testing.T) {
	for _, own := range []string{"", "570.86.16"} {
		r := nvidiaMachine(t, own)
		if _, err := r.run("nvidia", "addon_install", Options{}); err != nil {
			t.Fatal(err)
		}
		r.says("NVIDIA driver " + nvVersion + " is available")
		r.mustNotCall("systemctl stop", "apt-get", "sh ", "curl")
	}
}

func TestNvidiaHookInstalledKeepsItsPlace(t *testing.T) {
	r := nvidiaMachine(t, nvVersion)
	in, err := r.run("nvidia", "addon_install", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.read("/etc/apt/preferences.d/hostd-nvidia"), "Pin-Priority: -1") ||
		!strings.Contains(r.read("/etc/modprobe.d/hostd-nvidia-nouveau.conf"), "blacklist nouveau") {
		t.Fatal("pin or nouveau file missing")
	}
	r.mustCall("update-initramfs -u", "flatpak info")
	r.mustNotCall("systemctl stop")
	if !in.needReboot {
		t.Error("keeping nouveau away needs a reboot")
	}

	// Again: nothing to do.
	in, err = r.run("nvidia", "addon_install", Options{})
	if err != nil || in.Out.AnyChanged() || in.needReboot {
		t.Fatalf("second run: %v changed=%v reboot=%v\n%s", err, in.Out.AnyChanged(), in.needReboot, r.out.String())
	}
	r.mustNotCall("update-initramfs")
}

func TestNvidiaHookInstall(t *testing.T) {
	r := nvidiaMachine(t, "")
	r.stub("dpkg-query", "nvidia-driver install ok installed\nlibnvidia-glcore install ok installed\nfoot install ok installed\n", 0)
	in, err := r.run("nvidia", "addon_install", Options{Adding: []string{"nvidia"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.mustCall(
		"apt-get install -y -q linux-headers-amd64 dkms gcc make",
		"systemctl stop greetd",
		"modprobe -r nvidia_drm nvidia_modeset nvidia_uvm nvidia",
		"apt-get purge -y -q nvidia-driver libnvidia-glcore",
		"sh "+filepath.Join(r.root, "var/cache/hostd/nvidia/NVIDIA-Linux-x86_64-"+nvVersion+".run")+" --silent --dkms --kernel-module-type=proprietary",
		"systemctl start greetd",
	)
	r.mustNotCall("curl", "apt-get purge -y -q nvidia-driver libnvidia-glcore foot") // cached, checksum good; only NVIDIA's packages
	r.says("installed NVIDIA's driver " + nvVersion)
	if !in.needReboot || r.read("/etc/hostd/addons") != "flatpak\nnvidia\n" {
		t.Fatalf("reboot %v, add-ons %q", in.needReboot, r.read("/etc/hostd/addons"))
	}
}

// The driver cannot be swapped while it is in use: nothing changes.
func TestNvidiaHookDriverInUse(t *testing.T) {
	r := nvidiaMachine(t, "")
	r.file("/sys/module/nvidia/refcnt", "3\n")
	_, err := r.run("nvidia", "addon_install", Options{Adding: []string{"nvidia"}})
	if err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Fatalf("err %v", err)
	}
	r.mustCall("systemctl start greetd") // the screen comes back
	r.mustNotCall("apt-get purge", "sh ")
}

// NVIDIA's installer fails: Debian's driver comes back, and the add-on is
// not left recorded as installed.
func TestNvidiaHookInstallerFails(t *testing.T) {
	r := nvidiaMachine(t, "")
	r.stub("sh", "", 1)
	_, err := r.run("nvidia", "addon_install", Options{Adding: []string{"nvidia"}})
	if err == nil || !strings.Contains(err.Error(), "Debian's driver is back") {
		t.Fatalf("err %v", err)
	}
	r.mustCall("apt-get install -y -q linux-headers-amd64 nvidia-driver firmware-misc-nonfree", "systemctl start greetd")
	if r.exists("/etc/apt/preferences.d/hostd-nvidia") || r.read("/etc/hostd/addons") != "flatpak\n" {
		t.Fatalf("pin kept, or still recorded: %q", r.read("/etc/hostd/addons"))
	}
}

func TestNvidiaHookChecksum(t *testing.T) {
	r := nvidiaMachine(t, "")
	r.stub("sha256sum", "", 1) // neither the cache nor the download matches
	_, err := r.run("nvidia", "addon_install", Options{Adding: []string{"nvidia"}})
	if err == nil || !strings.Contains(err.Error(), "does not match its checksum") {
		t.Fatalf("err %v", err)
	}
	r.mustCall("curl -fsSL")
	r.mustNotCall("systemctl stop", "sh ")
}

func TestNvidiaHookRemove(t *testing.T) {
	r := nvidiaMachine(t, nvVersion)
	r.file("/etc/apt/preferences.d/hostd-nvidia", "# Managed by hostd\n")
	if _, err := r.run("nvidia", "addon_remove", Options{}); err != nil {
		t.Fatal(err)
	}
	r.mustCall("systemctl stop greetd", "nvidia-uninstall --silent", "apt-get install -y -q linux-headers-amd64 nvidia-driver", "systemctl start greetd")
	if r.exists("/etc/apt/preferences.d/hostd-nvidia") {
		t.Fatal("pin kept")
	}
	r.says("Debian's NVIDIA driver is back")
}

// --- steam -------------------------------------------------------------------

func TestSteamHook(t *testing.T) {
	r := newHookRig(t)
	r.stub("nvidia-smi", "4096\n", 0)
	r.stub("flatpak", "", 0)
	if _, err := r.run("steam", "addon_install", Options{}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.mustCall("flatpak override --user --env=DXVK_CONFIG=dxgi.maxDeviceMemory = 3328 com.valvesoftware.Steam")
	r.says("games see 3328 of 4096 MiB video memory")

	// The limit in place, the session gone: nothing to do.
	r.stub("flatpak", "[Environment]\nDXVK_CONFIG=dxgi.maxDeviceMemory = 3328\n", 0)
	in, err := r.run("steam", "addon_install", Options{})
	if err != nil || in.Out.AnyChanged() {
		t.Fatalf("second run: %v\n%s", err, r.out.String())
	}

	// A card too small to share, or one that cannot be asked: no limit.
	r.stub("nvidia-smi", "1024\n", 0)
	if _, err := r.run("steam", "addon_install", Options{}); err != nil {
		t.Fatal(err)
	}
	r.mustNotCall("flatpak override --user --env")
}

// --- share-remote (Sunshine) ---------------------------------------------------

func sunshineMachine(t *testing.T) *hookRig {
	r := newHookRig(t)
	r.stub("dpkg", "amd64\n", 0)
	r.stub("hostname", "core\n", 0)
	return r
}

func TestShareRemoteHook(t *testing.T) {
	r := sunshineMachine(t)
	// The screen user's session runs (its bus socket exists).
	sock := filepath.Join(r.root, "run/user/1000/bus")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix socket: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	if _, err := r.run("share-remote", "addon_install", Options{Adding: []string{"share-remote"}}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.mustCall("curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o",
		"sha256sum --check --quiet -", "apt-get install -y -q",
		// One way to run it: the package's own user unit goes; the app takes over.
		"systemctl --user disable --now app-dev.lizardbyte.app.Sunshine.service")
	// The app is started now (hostctl's full path: the user's PATH is not the hook's).
	if !strings.Contains(r.calls(), "/home/screen/.local/bin/hostctl start share-remote") {
		t.Errorf("share-remote was not started:\n%s", r.calls())
	}
	r.mustNotCall("systemctl --user enable", "systemctl --user restart")
	if !strings.Contains(r.calls(), "SunshineStream/Sunshine/releases/download/v2026.914.233613/sunshine_2026.914.233613-1+debiantrixie_amd64.deb") {
		t.Errorf("the pinned package was not fetched:\n%s", r.calls())
	}
	conf := r.read("home/screen/.config/sunshine/sunshine.conf")
	for _, want := range []string{"capture = wlr", "origin_web_ui_allowed = lan", "origin_pin_allowed = lan", "https://core.local:47990"} {
		if !strings.Contains(conf, want) {
			t.Errorf("sunshine.conf lacks %q:\n%s", want, conf)
		}
	}
	if apps := r.read("home/screen/.config/sunshine/apps.json"); !strings.Contains(apps, `"Desktop"`) || strings.Contains(apps, "Steam") {
		t.Errorf("apps.json:\n%s", apps)
	}

	// A person's settings stay; the package is current, so nothing is fetched.
	r.write(filepath.Join(r.root, "home/screen/.config/sunshine/sunshine.conf"), "capture = kms\n", 0o644)
	r.script("dpkg-query", `[ "$1" = -W ] && [ "$2" = '-f=${Version}' ] && { printf '2026.914.233613-1+debiantrixie'; exit 0; }`)
	if _, err := r.run("share-remote", "addon_install", Options{}); err != nil {
		t.Fatalf("second run: %v\n%s", err, r.out.String())
	}
	r.mustNotCall("curl", "apt-get install")
	if got := r.read("home/screen/.config/sunshine/sunshine.conf"); got != "capture = kms\n" {
		t.Errorf("a person's settings were replaced:\n%s", got)
	}
}

func TestShareRemoteHookRefusesAWrongFile(t *testing.T) {
	r := sunshineMachine(t)
	r.script("sha256sum", `exit 1`)
	if _, err := r.run("share-remote", "addon_install", Options{}); err == nil || !strings.Contains(err.Error(), "not the file this add-on pins") {
		t.Fatalf("a package with the wrong checksum: %v", err)
	}
	r.mustNotCall("apt-get install")
}

func TestShareRemoteHookRemove(t *testing.T) {
	r := sunshineMachine(t)
	if _, err := r.run("share-remote", "addon_remove", Options{}); err != nil {
		t.Fatal(err)
	}
	r.mustCall("systemctl --user disable --now app-dev.lizardbyte.app.Sunshine.service", "apt-get remove -y -q sunshine")
	if !strings.Contains(r.calls(), "/home/screen/.local/bin/hostctl stop share-remote") {
		t.Errorf("share-remote was not stopped:\n%s", r.calls())
	}
}

// --- share-keys (lan-mouse) -----------------------------------------------------

func TestShareKeysHook(t *testing.T) {
	r := sunshineMachine(t)
	if _, err := r.run("share-keys", "addon_install", Options{Adding: []string{"share-keys"}}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.mustCall("curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o", "sha256sum --check --quiet -")
	if !strings.Contains(r.calls(), "feschber/lan-mouse/releases/download/v0.11.0/lan-mouse-linux-x86_64") {
		t.Errorf("the pinned build was not fetched:\n%s", r.calls())
	}
	if !r.exists("usr/local/lib/hostd/share-keys/lan-mouse") || r.read("usr/local/lib/hostd/share-keys/lan-mouse.version") != "0.11.0\n" {
		t.Error("lan-mouse is not in place")
	}
	conf := r.read("home/screen/.config/lan-mouse/config.toml")
	if !strings.Contains(conf, "[authorized_fingerprints]") || !strings.Contains(conf, "port = 4242") {
		t.Errorf("config:\n%s", conf)
	}
	// Installed and configured: nothing is fetched, and a person's config stays.
	r.write(filepath.Join(r.root, "home/screen/.config/lan-mouse/config.toml"), "port = 5000\n", 0o644)
	if _, err := r.run("share-keys", "addon_install", Options{}); err != nil {
		t.Fatal(err)
	}
	r.mustNotCall("curl")
	if got := r.read("home/screen/.config/lan-mouse/config.toml"); got != "port = 5000\n" {
		t.Errorf("a person's config was replaced:\n%s", got)
	}
	// A wrong checksum is refused.
	r2 := sunshineMachine(t)
	r2.script("sha256sum", `exit 1`)
	if _, err := r2.run("share-keys", "addon_install", Options{}); err == nil || !strings.Contains(err.Error(), "not the file this add-on pins") {
		t.Fatalf("wrong checksum: %v", err)
	}
	if r2.exists("usr/local/lib/hostd/share-keys/lan-mouse") {
		t.Error("a file with the wrong checksum was installed")
	}
	// Remove.
	if _, err := r.run("share-keys", "addon_remove", Options{}); err != nil {
		t.Fatal(err)
	}
	if r.exists("usr/local/lib/hostd/share-keys/lan-mouse") {
		t.Error("the binary stays")
	}
}

// --- tailscale -----------------------------------------------------------

func tailscaleMachine(t *testing.T) *hookRig {
	r := newHookRig(t)
	r.file("/etc/os-release", "VERSION_CODENAME=trixie\n")
	r.file("/etc/hostd/addons", "tailscale\n")
	r.stub("gpg", "fpr:::::::::2596A99EAAB33821893C0A79458CA832957F5868:\n", 0)
	r.stub("hostname", "core\n", 0)
	r.script("tailscale", `case $1 in
status) printf '{\n  "BackendState": "%s",\n  "Self": {}\n}\n' "${TS_STATE:-NeedsLogin}"; exit 0 ;;
ip) echo 100.101.102.103; exit 0 ;;
up) case "$*" in *--authkey=*) TS_STATE=Running ;; *) printf '\nTo authenticate, visit:\n\n\thttps://login.tailscale.com/a/1b2c3d\n\n' >&2; exit 1 ;; esac ;;
esac`)
	return r
}

const tsDropIn = "/home/screen/.config/hostd/hostd.d/tailscale.toml"

func TestTailscaleHook(t *testing.T) {
	r := tailscaleMachine(t)
	t.Setenv("HOSTD_TAILSCALE_AUTHKEY", "tskey-auth-test")
	if _, err := r.run("tailscale", "addon_install", Options{Adding: []string{"tailscale"}}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	list := r.read("/etc/apt/sources.list.d/hostd-tailscale.list")
	if !strings.Contains(list, "deb [signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg] https://pkgs.tailscale.com/stable/debian trixie main") {
		t.Errorf("repository %q", list)
	}
	if !r.exists("/usr/share/keyrings/tailscale-archive-keyring.gpg") {
		t.Error("no keyring")
	}
	r.mustCall("apt-get install -y -q tailscale", "systemctl enable --now tailscaled", "tailscale up --authkey=tskey-auth-test --hostname=core",
		"systemctl --user restart hostd")
	if d := r.read(tsDropIn); !strings.Contains(d, "[api]") || !strings.Contains(d, `allow = ["100.64.0.0/10", "fd7a:115c:a1e0::/48"]`) {
		t.Errorf("drop-in %q", d)
	}

	// Again, logged in: no login, no restart, nothing changed.
	t.Setenv("TS_STATE", "Running")
	in, err := r.run("tailscale", "addon_install", Options{})
	if err != nil || in.Out.AnyChanged() {
		t.Fatalf("second run: %v\n%s", err, r.out.String())
	}
	r.mustNotCall("tailscale up", "systemctl --user restart hostd", "curl")
	r.says("logged in to the tailnet: 100.101.102.103")

	if _, err := r.run("tailscale", "addon_remove", Options{}); err != nil {
		t.Fatal(err)
	}
	if r.exists(tsDropIn) {
		t.Error("the drop-in stays")
	}
	r.mustCall("systemctl --user restart hostd", "tailscale down")
}

// Without an auth key, the URL to approve the machine at.
func TestTailscaleHookLoginURL(t *testing.T) {
	r := tailscaleMachine(t)
	if _, err := r.run("tailscale", "addon_install", Options{Adding: []string{"tailscale"}}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.says("open https://login.tailscale.com/a/1b2c3d and log in; then: sudo hostd-setup update tailscale")
}

func TestTailscaleHookPinnedKey(t *testing.T) {
	r := tailscaleMachine(t)
	r.stub("gpg", "fpr:::::::::0000000000000000000000000000000000000000:\n", 0)
	_, err := r.run("tailscale", "addon_install", Options{Adding: []string{"tailscale"}})
	if err == nil || !strings.Contains(err.Error(), "not the one this add-on pins") {
		t.Fatalf("err %v", err)
	}
	r.mustNotCall("apt-get install -y -q tailscale")
}

// --- wireguard -----------------------------------------------------------

// wireguardMachine has ufw on, and a wg whose keys are made up but look
// right: a public key is the hex of the private one's start.
func wireguardMachine(t *testing.T) *hookRig {
	r := newHookRig(t)
	r.file("/etc/hostd/addons", "wireguard\n")
	r.stub("hostname", "192.168.18.33 fd00::5\n", 0)
	r.script("ufw", `[ "$1" = status ] && { echo "Status: active"; [ -f "$STUB_DIR/ufw-open" ] && echo "51820/udp   ALLOW   Anywhere"; exit 0; }
[ "$1" = allow ] && touch "$STUB_DIR/ufw-open"`)
	r.script("systemctl", `case "$*" in
"is-active --quiet wg-quick@wg-hostd") [ -f "$STUB_DIR/wg-up" ] || exit 3 ;;
"is-enabled --quiet wg-quick@wg-hostd") [ -f "$STUB_DIR/wg-up" ] || exit 1 ;;
"enable --now wg-quick@wg-hostd") touch "$STUB_DIR/wg-up" ;;
"disable --now wg-quick@wg-hostd") rm -f "$STUB_DIR/wg-up" ;;
esac`)
	r.script("wg", `case $1 in
genkey) echo "priv-$RANDOM$RANDOM$RANDOM-key"; exit 0 ;;
pubkey) printf '%sA=\n' "$(od -An -tx1 | tr -d ' \n' | head -c 42)"; exit 0 ;;
show) [ -f "$STUB_DIR/wg.handshakes" ] && cat "$STUB_DIR/wg.handshakes"; exit 0 ;;
esac`)
	return r
}

// wgCmd runs hostd-wg on the machine.
func (r *hookRig) wgCmd(args ...string) (string, error) {
	r.t.Helper()
	cmd := exec.Command("bash", append([]string{"addons/wireguard/hostd-wg"}, args...)...)
	cmd.Env = append(os.Environ(), "HOSTD_ROOT="+r.root)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestWireguardHook(t *testing.T) {
	r := wireguardMachine(t)
	t.Setenv("HOSTD_WIREGUARD_ENDPOINT", "home.example.org")
	if _, err := r.run("wireguard", "addon_install", Options{Adding: []string{"wireguard"}}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	if env := r.read("/etc/hostd/wireguard.env"); !strings.Contains(env, "NET=10.44.0.0/24\nADDRESS=10.44.0.1\nPORT=51820\nENDPOINT=home.example.org\n") {
		t.Errorf("settings %q", env)
	}
	if fi, err := os.Stat(filepath.Join(r.root, "etc/wireguard/wg-hostd.key")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key: %v %v", fi, err)
	}
	conf := r.read("/etc/wireguard/wg-hostd.conf")
	if !strings.Contains(conf, "Address = 10.44.0.1/24\nListenPort = 51820\nPostUp = wg set %i private-key /etc/wireguard/wg-hostd.key") ||
		!strings.Contains(conf, "PostUp = nft -f /etc/wireguard/wg-hostd.nft\nPostDown = nft delete table inet hostd_wg") ||
		strings.Contains(conf, "PrivateKey") {
		t.Errorf("config %q", conf)
	}
	// Nothing is forwarded from or to the tunnel.
	if nft := r.read("/etc/wireguard/wg-hostd.nft"); !strings.Contains(nft, `iifname "wg-hostd" drop`) || !strings.Contains(nft, `oifname "wg-hostd" drop`) ||
		!strings.Contains(nft, "hook forward priority filter - 10; policy accept;") {
		t.Errorf("forwarding rules %q", nft)
	}
	r.mustCall("systemctl enable --now wg-quick@wg-hostd", "ufw allow 51820/udp")
	r.says("forward UDP 51820 on your router to this machine (192.168.18.33)")

	// Again: nothing changes.
	in, err := r.run("wireguard", "addon_install", Options{})
	if err != nil || in.Out.AnyChanged() {
		t.Fatalf("second run: %v\n%s", err, r.out.String())
	}
	r.mustNotCall("systemctl enable", "systemctl restart", "ufw allow")

	// Devices: one with a key made here, one with its own.
	out, err := r.wgCmd("add", "phone", "--no-qr")
	if err != nil || !strings.Contains(out, "Address = 10.44.0.2/32") || !strings.Contains(out, "Endpoint = home.example.org:51820") ||
		!strings.Contains(out, "PrivateKey = priv-") || !strings.Contains(out, "AllowedIPs = 10.44.0.0/24") || !strings.Contains(out, "hostctl login 10.44.0.1") {
		t.Fatalf("add phone: %v\n%s", err, out)
	}
	r.mustCall("wg set wg-hostd peer ")
	const ownKey = "xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="
	out, err = r.wgCmd("add", "laptop", "--public-key", ownKey)
	if err != nil || !strings.Contains(out, "Address = 10.44.0.3/32") || !strings.Contains(out, "<the device's own private key>") {
		t.Fatalf("add laptop: %v\n%s", err, out)
	}
	conf = r.read("/etc/wireguard/wg-hostd.conf")
	if !strings.Contains(conf, "[Peer]\n# laptop") || !strings.Contains(conf, "PublicKey = "+ownKey+"\nAllowedIPs = 10.44.0.3/32") || strings.Contains(conf, "priv-") {
		t.Errorf("config with devices %q", conf)
	}
	for args, want := range map[string]string{
		"add phone":                        "already a device called phone",
		"add Phone":                        "lowercase letters",
		"add tv --public-key nope":         "not a WireGuard public key",
		"add tv --public-key " + ownKey:    "already a device's",
		"remove tv":                        "no device called tv",
		"endpoint home.example.org;reboot": "usage: hostd-wg endpoint",
	} {
		if out, err := r.wgCmd(strings.Fields(args)...); err == nil || !strings.Contains(out, want) {
			t.Errorf("%s: %v %s", args, err, out)
		}
	}
	r.write(filepath.Join(r.stubs, "wg.handshakes"), ownKey+"\t1792231200\n", 0o644)
	out, _ = r.wgCmd("list")
	if !regexp.MustCompile(`laptop +10\.44\.0\.3 +2026-`).MatchString(out) || !regexp.MustCompile(`phone +10\.44\.0\.2 +never`).MatchString(out) {
		t.Errorf("list:\n%s", out)
	}
	if out, err := r.wgCmd("remove", "phone"); err != nil || !strings.Contains(out, "can no longer connect") {
		t.Fatalf("remove: %v %s", err, out)
	}
	r.mustCall("wg set wg-hostd peer ")
	if out, _ := r.wgCmd("add", "tablet", "--public-key", "AAAAA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg="); !strings.Contains(out, "Address = 10.44.0.2/32") {
		t.Errorf("a freed address is not used again:\n%s", out)
	}

	if _, err := r.run("wireguard", "addon_remove", Options{}); err != nil {
		t.Fatal(err)
	}
	r.mustCall("systemctl disable --now wg-quick@wg-hostd", "ufw delete allow 51820/udp")
	if !r.exists("/etc/wireguard/wg-hostd.key") || !r.exists("/etc/wireguard/hostd-peers/laptop.peer") {
		t.Error("the keys and devices were not kept")
	}
}

// Without an endpoint: the tunnel runs, and devices wait until it is set.
func TestWireguardHookNoEndpoint(t *testing.T) {
	r := wireguardMachine(t)
	if _, err := r.run("wireguard", "addon_install", Options{Adding: []string{"wireguard"}}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.says("set the address your devices reach this machine at: sudo hostd-wg endpoint home.example.org")
	if out, err := r.wgCmd("add", "phone"); err == nil || !strings.Contains(out, "sudo hostd-wg endpoint") {
		t.Fatalf("add without an endpoint: %v %s", err, out)
	}
	if out, err := r.wgCmd("endpoint", "203.0.113.7"); err != nil {
		t.Fatalf("endpoint: %v %s", err, out)
	}
	if out, err := r.wgCmd("add", "phone", "--no-qr"); err != nil || !strings.Contains(out, "Endpoint = 203.0.113.7:51820") {
		t.Fatalf("add: %v %s", err, out)
	}
}

func TestWireguardHookNetworkMustBePrivate(t *testing.T) {
	for _, net := range []string{"8.8.8.0/24", "10.44.0.0/16", "172.32.0.0/24", "10.44.0.5/24"} {
		r := wireguardMachine(t)
		t.Setenv("HOSTD_WIREGUARD_NET", net)
		if _, err := r.run("wireguard", "addon_install", Options{Adding: []string{"wireguard"}}); err == nil || !strings.Contains(err.Error(), "private /24") {
			t.Errorf("%s: %v", net, err)
		}
	}
}
