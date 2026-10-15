package deploy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
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
	"apt-get", "chown", "curl", "df", "dpkg", "dpkg-query", "flatpak", "free", "gpg", "hostname", "ip",
	"mariadb", "modprobe", "nginx", "nvidia-smi", "openssl", "pgrep", "php8.3", "runuser", "sh", "sha256sum",
	"sleep", "systemctl", "tar", "timedatectl", "update-initramfs",
}

func newHookRig(t *testing.T) *hookRig {
	t.Helper()
	r := &hookRig{t: t, root: t.TempDir(), stubs: t.TempDir()}
	r.log = filepath.Join(r.stubs, "calls.log")
	// A stub logs its call; NAME.out is what it prints, NAME.rc its exit
	// status, and NAME.sh, if there, runs first with its arguments. A
	// -o FILE (curl) is created.
	stub := `#!/bin/bash
name=${0##*/}
printf '%s\n' "$name $*" >>"$STUB_LOG"
prev=""
for a in "$@"; do [ "$prev" = -o ] && [ "$a" != /dev/null ] && : >"$a"; prev=$a; done
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

// --- pterodactyl -------------------------------------------------------------

const ptero = "/var/www/pterodactyl"

// pteroMachine is a Debian 13 machine whose commands answer as a fresh
// one does: no panel database, the PHP repository's real key.
func pteroMachine(t *testing.T) *hookRig {
	r := newHookRig(t)
	r.file("/etc/os-release", "VERSION_CODENAME=trixie\n")
	r.file("/etc/apt/sources.list.d/debian.sources", "Types: deb\n")
	r.file("/etc/nginx/sites-available/default", "server {}\n")
	if err := os.MkdirAll(filepath.Join(r.root, "etc/nginx/sites-enabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/nginx/sites-available/default", filepath.Join(r.root, "etc/nginx/sites-enabled/default")); err != nil {
		t.Fatal(err)
	}
	r.file("/etc/hostd/addons", "chromium\npterodactyl\n")
	r.stub("gpg", "fpr:::::::::15058500A0235D97F5D10063B188E2B695BD4743:\n", 0)
	r.stub("mariadb", "0\n", 0)
	r.stub("openssl", "secret", 0)
	r.stub("hostname", "core\n", 0)
	r.stub("ip", "1.1.1.1 via 192.168.18.1 dev eno1 src 192.168.18.33 uid 0\n", 0)
	r.stub("free", "Mem:  16000  2000  14000\n", 0)
	r.stub("df", "  Avail\n100000M\n", 0)
	r.stub("dpkg", "amd64\n", 0)
	r.stub("timedatectl", "America/New_York\n", 0)
	// The panel's node hands Wings its config; the panel's release unpacks
	// into a directory with the files the hook uses.
	r.stub("runuser", "debug: false\nremote: 'http://core.local'\n", 0)
	r.script("tar", `[ "$1" = xzf ] && mkdir -p "$4/storage/logs" "$4/bootstrap/cache" && : >"$4/artisan" && : >"$4/.env.example"`)
	return r
}

func TestPterodactylHookFirstInstall(t *testing.T) {
	r := pteroMachine(t)
	in, err := r.run("pterodactyl", "addon_install", Options{Adding: []string{"pterodactyl"}})
	if err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.mustCall(
		"curl -fsSL --retry 6 --retry-delay 5 --retry-all-errors -o", // the PHP repository's key
		"apt-get update",
		"mariadb",
		"runuser -u www-data -- php8.3 artisan migrate --seed --force",
		"runuser -u www-data -- php8.3 artisan p:user:make --email=admin@core.local",
		"runuser -u www-data -- php8.3 artisan p:node:make --name=core --description=This machine --locationId=1 --fqdn=192.168.18.33",
		"nginx -t",
		"systemctl daemon-reload",
		"systemctl enable --now pteroq.service pterodactyl-schedule.timer",
		"systemctl restart wings",
	)
	src := r.read("/etc/apt/sources.list.d/hostd-sury-php.sources")
	if !strings.Contains(src, "Signed-By: /usr/share/keyrings/deb.sury.org-php.gpg") || !strings.Contains(src, "Suites: trixie") {
		t.Errorf("PHP repository:\n%s", src)
	}
	if l, _ := os.Readlink(filepath.Join(r.root, "etc/nginx/sites-enabled/pterodactyl.conf")); l != "/etc/nginx/sites-available/pterodactyl.conf" {
		t.Errorf("site link -> %q", l)
	}
	if r.exists("/etc/nginx/sites-enabled/default") {
		t.Error("Debian's default site still claims port 80")
	}
	for _, f := range []string{"/etc/systemd/system/wings.service", "/etc/systemd/system/pteroq.service", "/usr/local/bin/wings",
		ptero + "/.env", ptero + "/.hostd-owned", ptero + "/.hostd-bootstrapped", "/home/screen/.config/hostd/apps/pterodactyl.toml"} {
		if !r.exists(f) {
			t.Errorf("missing %s", f)
		}
	}
	if v := strings.TrimSpace(r.read(ptero + "/.hostd-version")); v == "" {
		t.Error("no version recorded")
	}
	conf := filepath.Join(r.root, "etc/pterodactyl/config.yml")
	if st, err := os.Stat(conf); err != nil || st.Mode().Perm() != 0o600 || !strings.Contains(r.read("/etc/pterodactyl/config.yml"), "remote: 'http://127.0.0.1'") {
		t.Errorf("Wings' config (private, the panel on 127.0.0.1): %v\n%s", err, r.read("/etc/pterodactyl/config.yml"))
	}
	r.says("admin password: secret")
	if !in.Out.AnyChanged() {
		t.Error("nothing reported as changed")
	}

	// Again, as an update: everything is in place, nothing is downloaded.
	want := pteroWingsSHA256(t)
	r.stub("sha256sum", want+"  wings\n", 0)
	in, err = r.run("pterodactyl", "addon_install", Options{})
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, r.out.String())
	}
	r.mustNotCall("curl -fsSL", "mariadb\n", "runuser -u www-data -- php8.3 artisan migrate", "systemctl restart wings", "nginx -t")
	if in.Out.AnyChanged() {
		t.Errorf("second run changed something:\n%s", r.out.String())
	}
	r.says("the panel is at")
	r.says("nginx site up to date")
}

// pteroWingsSHA256 is the Wings checksum the hook pins, for amd64.
func pteroWingsSHA256(t *testing.T) string {
	b, err := os.ReadFile("addons/pterodactyl/addon.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "PTERO_WINGS_SHA256_amd64="); ok {
			return v
		}
	}
	t.Fatal("no PTERO_WINGS_SHA256_amd64 in the hook")
	return ""
}

// The update timer never upgrades the panel: its migrations change data.
func TestPterodactylHookNoUnattendedUpgrade(t *testing.T) {
	r := pteroMachine(t)
	r.file(ptero+"/.hostd-bootstrapped", "")
	r.file(ptero+"/.hostd-owned", "")
	r.file(ptero+"/.hostd-version", "1.0.0\n")
	r.file("/usr/local/bin/wings", "old")
	r.stub("sha256sum", "0000  wings\n", 0)
	if _, err := r.run("pterodactyl", "addon_install", Options{Auto: true}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	r.says("is available: sudo hostd-setup update upgrades it (not done unattended)")
	if c := r.calls(); strings.Contains(c, "panel.tar.gz") || strings.Contains(c, "wings_linux") {
		t.Errorf("downloaded the panel or Wings:\n%s", c)
	}
	r.mustNotCall("runuser -u www-data -- php8.3 artisan migrate")
	if r.read(ptero+"/.hostd-version") != "1.0.0\n" || r.read("/usr/local/bin/wings") != "old" {
		t.Error("upgraded unattended")
	}
}

// A panel this add-on did not install is left alone.
func TestPterodactylHookForeignPanel(t *testing.T) {
	r := pteroMachine(t)
	r.file(ptero+"/index.php", "<?php")
	_, err := r.run("pterodactyl", "addon_install", Options{Adding: []string{"pterodactyl"}})
	if err == nil || !strings.Contains(err.Error(), "was not made by this add-on") {
		t.Fatalf("err %v", err)
	}
	r.mustNotCall("apt-get", "mariadb")
}

func TestPterodactylHookRemove(t *testing.T) {
	r := pteroMachine(t)
	if _, err := r.run("pterodactyl", "addon_install", Options{Adding: []string{"pterodactyl"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.run("pterodactyl", "addon_remove", Options{}); err != nil {
		t.Fatal(err)
	}
	r.mustCall("systemctl disable --now wings.service pteroq.service pterodactyl-schedule.timer", "systemctl reload nginx")
	for _, f := range []string{"/etc/systemd/system/wings.service", "/usr/local/bin/wings", "/etc/nginx/sites-enabled/pterodactyl.conf",
		"/home/screen/.config/hostd/apps/pterodactyl.toml"} {
		if r.exists(f) {
			t.Errorf("%s kept", f)
		}
	}
	if l, _ := os.Readlink(filepath.Join(r.root, "etc/nginx/sites-enabled/default")); l != "/etc/nginx/sites-available/default" {
		t.Errorf("Debian's default site not back: %q", l)
	}
	if !r.exists(ptero+"/.env") || !r.exists("/etc/pterodactyl/config.yml") {
		t.Error("the panel's data must stay")
	}
	r.says("Kept, so nothing is lost")
}

// --- steam -------------------------------------------------------------------

func TestSteamHook(t *testing.T) {
	r := newHookRig(t)
	r.file("/usr/local/bin/hostd-steam-session", "#!/bin/sh\n") // from a version that ran Steam in gamescope
	r.stub("nvidia-smi", "4096\n", 0)
	r.stub("flatpak", "[Context]\nfilesystems=xdg-run/gamescope-0:ro;\n", 0)
	if _, err := r.run("steam", "addon_install", Options{}); err != nil {
		t.Fatalf("%v\n%s", err, r.out.String())
	}
	if r.exists("/usr/local/bin/hostd-steam-session") {
		t.Error("the gamescope session stays")
	}
	r.mustCall("flatpak override --system --reset com.valvesoftware.Steam",
		"flatpak override --system --env=DXVK_CONFIG=dxgi.maxDeviceMemory = 3328 com.valvesoftware.Steam")
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
	r.mustNotCall("flatpak override --system --env")
}
