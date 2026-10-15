package deploy

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// An add-on installs something optional on top of hostd: an app (Steam,
// Dolphin), a tool (podman-compose), device access (controllers). Each is
// addons/NAME/addon.toml: what it puts on the machine, as data. One whose
// steps are a chain of commands (NVIDIA's driver, Pterodactyl) names a hook
// script for them; the parts the toml describes run first.
type Addon struct {
	Name        string
	Description string   `toml:"description"`
	Requires    []string `toml:"requires"` // add-ons it needs, installed first

	Packages []string `toml:"packages"` // Debian packages
	Purge    bool     `toml:"purge"`    // remove them again when the add-on goes

	Flathub       bool     `toml:"flathub"`        // set up the Flathub repository
	FlatpakUpdate bool     `toml:"flatpak_update"` // update every Flatpak app and runtime
	Flatpaks      []string `toml:"flatpaks"`       // apps from Flathub, removed with the add-on

	KernelModules []string    `toml:"kernel_modules"` // loaded now and at boot
	Files         []AddonFile `toml:"files"`
	// OnChange runs when one of the files changed (applying udev rules).
	OnChange [][]string `toml:"on_change"`
	// UserUnits are the screen user's units it enables and starts (their
	// files are among Files); UserStart are started once on every run.
	UserUnits []string `toml:"user_units"`
	UserStart []string `toml:"user_start"`
	Rescan    bool     `toml:"rescan"` // hostd reads the app catalog again after
	Keeps     string   `toml:"keeps"`  // what stays when it is removed (said then)
	Hook      string   `toml:"hook"`   // a script with addon_install and addon_remove
}

// An AddonFile is one file an add-on puts on the machine. In Dest, ~ is the
// screen user's home; Owner "screen" is the screen user.
type AddonFile struct {
	Src   string `toml:"src"`
	Dest  string `toml:"dest"`
	Mode  string `toml:"mode"`  // octal, default 644
	Owner string `toml:"owner"` // root (default) or screen
}

const addonsState = "/etc/hostd/addons" // installed add-ons, one name per line
const addonsStamp = "/etc/hostd/addons.applied"

// LoadAddon reads addons/NAME/addon.toml.
func LoadAddon(name string) (Addon, error) {
	b, err := fs.ReadFile(embedded, path.Join("addons", name, "addon.toml"))
	if err != nil {
		return Addon{}, fmt.Errorf("no add-on %q; see: hostd-setup addons", name)
	}
	var a Addon
	md, err := toml.Decode(string(b), &a)
	if err != nil {
		return Addon{}, fmt.Errorf("add-on %s: %w", name, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return Addon{}, fmt.Errorf("add-on %s: unknown key %s", name, und[0])
	}
	a.Name = name
	return a, nil
}

// AddonNames lists every add-on there is.
func AddonNames() []string {
	entries, _ := fs.ReadDir(embedded, "addons")
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := fs.Stat(embedded, path.Join("addons", e.Name(), "addon.toml")); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

func (s *Setup) installedAddons() []string {
	b, _ := os.ReadFile(s.path(addonsState))
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func (s *Setup) setInstalled(names []string) error {
	if err := s.mkdirs("/etc/hostd", "root"); err != nil {
		return err
	}
	text := ""
	for _, n := range names {
		text += n + "\n"
	}
	return os.WriteFile(s.path(addonsState), []byte(text), 0o644)
}

// addonOrder returns names with what they require, requirements first.
func addonOrder(names []string) ([]Addon, error) {
	var out []Addon
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(n string) error {
		if seen[n] {
			return nil
		}
		seen[n] = true
		a, err := LoadAddon(n)
		if err != nil {
			return err
		}
		for _, r := range a.Requires {
			if err := visit(r); err != nil {
				return err
			}
		}
		out = append(out, a)
		return nil
	}
	for _, n := range names {
		if err := visit(n); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addonsBundle is a stamp of every add-on as this installer has them: when
// an update brings the same, they need not run again.
func addonsBundle() string {
	h := sha256.New()
	_ = fs.WalkDir(embedded, "addons", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".md") { // documentation changes nothing on the machine
			return err
		}
		b, _ := fs.ReadFile(embedded, p)
		fmt.Fprintf(h, "%s %d\n", p, len(b))
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

// addons runs the installed add-ons again, which also updates them. An
// update does that only when the add-ons changed since the last time they
// ran, or when asked: --all for every one, or by name. Their own upgrades
// (Flatpak apps, the panel) are not worth a check on every build of hostd.
func (in *installer) addons(ctx context.Context) error {
	installed := in.installedAddons()
	if len(installed) == 0 {
		return nil
	}
	bundle := addonsBundle()
	stamp, _ := os.ReadFile(in.path(addonsStamp))
	forced := append(slices.Clone(in.o.Adding), forcedOf(in.o.Update)...)
	quick := in.o.Update != nil && !in.o.Update.All
	if quick && strings.TrimSpace(string(stamp)) == bundle {
		if len(forced) == 0 {
			in.addonsSkipped = len(installed)
			return nil
		}
		order, err := addonOrder(forced)
		if err != nil {
			return err
		}
		return in.runAddons(ctx, order)
	}
	order, err := addonOrder(installed)
	if err != nil {
		return err
	}
	if err := in.runAddons(ctx, order); err != nil {
		return err
	}
	return os.WriteFile(in.path(addonsStamp), []byte(bundle+"\n"), 0o644)
}

func forcedOf(u *UpdateOptions) []string {
	if u == nil {
		return nil
	}
	return u.Addons
}

func (in *installer) runAddons(ctx context.Context, order []Addon) error {
	for _, a := range order {
		in.Out.Step("Add-on: " + a.Name)
		if err := in.installAddon(ctx, a); err != nil {
			return fmt.Errorf("add-on %s: %w", a.Name, err)
		}
	}
	return nil
}

// dest is an add-on file's place on the machine.
func (in *installer) dest(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return in.user.home + "/" + rest
	}
	return p
}

func (in *installer) owner(o string) string {
	if o == "screen" {
		return in.user.name
	}
	return "root"
}

func (in *installer) installAddon(ctx context.Context, a Addon) error {
	if len(a.Packages) > 0 {
		if err := in.ensurePackages(ctx, a.Packages...); err != nil {
			return err
		}
	}
	if a.Flathub {
		out, _ := in.run(ctx, "flatpak", "remotes", "--system", "--columns=name")
		if slices.Contains(strings.Fields(string(out)), "flathub") {
			in.Out.Info("Flathub is set up")
		} else {
			if err := in.stream(ctx, nil, "flatpak", "remote-add", "--system", "--if-not-exists", "flathub",
				"https://dl.flathub.org/repo/flathub.flatpakrepo"); err != nil {
				return err
			}
			in.Out.Changed("added the Flathub repository")
		}
	}
	if a.FlatpakUpdate {
		if out, _ := in.run(ctx, "flatpak", "list", "--system", "--columns=application"); strings.TrimSpace(string(out)) != "" {
			in.Out.Info("updating Flatpak apps and runtimes")
			if _, err := in.run(ctx, "flatpak", "update", "--system", "-y", "--noninteractive"); err != nil {
				return err
			}
			in.Out.Info("Flatpak apps up to date")
		}
	}
	for _, id := range a.Flatpaks {
		if in.ok(ctx, "flatpak", "info", "--system", id) {
			in.Out.Info("%s is installed", id)
			continue
		}
		in.Out.Info("installing %s from Flathub", id)
		if _, err := in.run(ctx, "flatpak", "install", "--system", "-y", "--noninteractive", "flathub", id); err != nil {
			return err
		}
		in.Out.Changed("installed %s", id)
	}
	for _, m := range a.KernelModules {
		if _, err := in.installFile([]byte(m+"\n"), "/etc/modules-load.d/hostd-"+m+".conf", 0o644, "root"); err != nil {
			return err
		}
		_, _ = in.run(ctx, "modprobe", m) // a container may not have it: then at the next boot
	}
	filesChanged, unitsChanged := false, false
	for _, f := range a.Files {
		b, err := fs.ReadFile(embedded, path.Join("addons", a.Name, f.Src))
		if err != nil {
			return fmt.Errorf("no file %s", f.Src)
		}
		mode := fs.FileMode(0o644)
		if f.Mode != "" {
			m, err := strconv.ParseUint(f.Mode, 8, 32)
			if err != nil {
				return fmt.Errorf("file %s: mode %q", f.Src, f.Mode)
			}
			mode = fs.FileMode(m)
		}
		ch, err := in.installFile(b, in.dest(f.Dest), mode, in.owner(f.Owner))
		if err != nil {
			return err
		}
		if ch {
			filesChanged = true
			if strings.Contains(f.Dest, "/systemd/user/") {
				unitsChanged = true
			}
		} else {
			in.Out.Info("%s up to date", path.Base(f.Dest))
		}
	}
	if filesChanged {
		for _, argv := range a.OnChange {
			_, _ = in.run(ctx, argv...) // a container may have no udev: then at the next boot
		}
	}
	if unitsChanged {
		_, _ = in.asScreen(ctx, "systemctl", "--user", "daemon-reload")
	}
	if len(a.UserUnits) > 0 {
		// A unit that failed (a path unit whose service hit its start
		// limit) stays failed until told otherwise: this run starts it again.
		_, _ = in.asScreen(ctx, append([]string{"systemctl", "--user", "reset-failed"}, a.UserUnits...)...)
		_, _ = in.asScreen(ctx, append([]string{"systemctl", "--user", "enable", "--now"}, a.UserUnits...)...)
	}
	for _, u := range a.UserStart {
		if _, err := in.asScreen(ctx, "systemctl", "--user", "start", u); err != nil {
			in.Out.Warn("warning: %s failed (see journalctl --user -u %s)", u, u)
		}
	}
	if a.Hook != "" {
		if err := in.runHook(ctx, a, "addon_install"); err != nil {
			return err
		}
	}
	if a.Rescan {
		in.rescan(ctx)
	}
	if !in.Out.StepChanged() {
		in.Out.Info("%s: up to date", a.Name)
	}
	return nil
}

// rescan has hostd read the app catalog again: an app's files land in a
// directory hostd may not have been watching (it did not exist yet).
func (in *installer) rescan(ctx context.Context) {
	if in.exists(in.user.runtime() + "/hostd.sock") {
		_, _ = in.asScreen(ctx, "hostctl", "app", "rescan")
	}
}

// removeAddon undoes an add-on: its files, units, apps and (if it says so)
// packages; its hook's addon_remove.
func (in *installer) removeAddon(ctx context.Context, a Addon) error {
	if len(a.UserUnits) > 0 {
		_, _ = in.asScreen(ctx, append([]string{"systemctl", "--user", "disable", "--now"}, a.UserUnits...)...)
	}
	if a.Hook != "" {
		if err := in.runHook(ctx, a, "addon_remove"); err != nil {
			return err
		}
	}
	units := false
	for _, f := range a.Files {
		d := in.dest(f.Dest)
		if err := os.Remove(in.path(d)); err == nil {
			in.Out.Changed("removed %s", d)
			units = units || strings.Contains(f.Dest, "/systemd/user/")
		}
	}
	for _, m := range a.KernelModules {
		_ = os.Remove(in.path("/etc/modules-load.d/hostd-" + m + ".conf"))
	}
	if len(a.Files) > 0 {
		for _, argv := range a.OnChange {
			_, _ = in.run(ctx, argv...)
		}
	}
	if units {
		_, _ = in.asScreen(ctx, "systemctl", "--user", "daemon-reload")
	}
	for _, id := range a.Flatpaks {
		if _, err := in.run(ctx, "flatpak", "uninstall", "--system", "-y", "--noninteractive", id); err == nil {
			in.Out.Changed("removed %s", id)
		}
	}
	if a.Purge && len(a.Packages) > 0 {
		cmd := append([]string{"apt-get", "purge", "-y", "-q"}, a.Packages...)
		if _, err := in.Run.Run(ctx, Cmd{Argv: cmd, Env: []string{"DEBIAN_FRONTEND=noninteractive"}}); err == nil {
			in.Out.Changed("removed %s", strings.Join(a.Packages, " "))
		}
	}
	if a.Rescan {
		in.rescan(ctx)
	}
	if a.Keeps != "" {
		in.Out.Info("%s", strings.ReplaceAll(a.Keeps, "~", in.user.home))
	}
	return nil
}

// runHook runs an add-on's hook script (fn: addon_install or addon_remove)
// with the helpers of lib.sh, and passes what it reports to the output.
func (in *installer) runHook(ctx context.Context, a Addon, fn string) error {
	dir, err := os.MkdirTemp("", "hostd-addon-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// The add-on's files, where the hook expects them (ADDON_HERE).
	sub, _ := fs.Sub(embedded, path.Join("addons", a.Name))
	if err := os.CopyFS(dir, sub); err != nil {
		return err
	}
	lib := filepath.Join(dir, ".lib.sh")
	if err := os.WriteFile(lib, mustRead("addons/lib.sh"), 0o644); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o755)
	pr, pw := io.Pipe()
	env := []string{"SCREEN_USER=" + in.user.name, "SCREEN_HOME=" + in.user.home, "SCREEN_UID=" + strconv.Itoa(in.user.uid),
		"ADDON_HERE=" + dir, "ADDON_HOOK=" + filepath.Join(dir, a.Hook), "ADDONS_STATE=" + in.path(addonsState),
		"HOSTD_ADDING=" + strings.Join(in.o.Adding, " "), "HOSTD_FORCE_ADDONS=" + strings.Join(forcedOf(in.o.Update), " ")}
	if in.o.Auto {
		env = append(env, "HOSTD_AUTO=1")
	}
	if r := filepath.Clean(in.Root); r != "/" && r != "." {
		env = append(env, "HOSTD_ROOT="+r) // a test's machine, in a directory
	}
	var hookErr string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			if len(l) >= 2 && l[0] == '\001' {
				msg := l[2:]
				switch l[1] {
				case 'I':
					in.Out.Info("%s", msg)
				case 'C':
					in.Out.Changed("%s", msg)
				case 'W':
					in.Out.Warn("%s", msg)
				case 'E':
					hookErr = msg
				case 'R':
					in.needReboot = true
				}
				continue
			}
			fmt.Fprintln(in.Out.Writer(), l)
		}
	}()
	_, err = in.Run.Run(ctx, Cmd{Argv: []string{"bash", lib, fn}, Env: env, Stdout: pw})
	pw.Close()
	<-done
	if hookErr != "" {
		return errors.New(hookErr)
	}
	return err
}

func mustRead(name string) []byte {
	b, err := fs.ReadFile(embedded, name)
	if err != nil {
		panic("deploy: no embedded " + name)
	}
	return b
}

// --- the add-on commands --------------------------------------------------------

// ListAddons prints every add-on, marking the installed ones.
func (s *Setup) ListAddons(w io.Writer) error {
	installed := s.installedAddons()
	for _, n := range AddonNames() {
		a, err := LoadAddon(n)
		if err != nil {
			return err
		}
		mark := " "
		if slices.Contains(installed, n) {
			mark = "*"
		}
		fmt.Fprintf(w, "%s %-12s %s\n", mark, n, a.Description)
		if len(a.Requires) > 0 {
			fmt.Fprintf(w, "  %-12s (with: %s)\n", "", strings.Join(a.Requires, " "))
		}
	}
	fmt.Fprintln(w, "(* installed)  sudo hostd-setup add NAME | remove NAME")
	return nil
}

// AddAddons records names (and what they require) as installed; the update
// that follows installs them.
func (s *Setup) AddAddons(names []string) error {
	if len(names) == 0 {
		return errors.New("add what? see: hostd-setup addons")
	}
	order, err := addonOrder(names)
	if err != nil {
		return err
	}
	installed := s.installedAddons()
	for _, a := range order {
		if !slices.Contains(installed, a.Name) {
			installed = append(installed, a.Name)
		}
	}
	return s.setInstalled(installed)
}

// RemoveAddons undoes add-ons, refusing one another installed add-on needs.
func (s *Setup) RemoveAddons(ctx context.Context, user string, names []string) error {
	if len(names) == 0 {
		return errors.New("remove what? see: hostd-setup addons")
	}
	installed := s.installedAddons()
	for _, n := range names {
		if !slices.Contains(installed, n) {
			return fmt.Errorf("add-on %q is not installed", n)
		}
		for _, other := range installed {
			if other == n {
				continue
			}
			o, err := LoadAddon(other)
			if err == nil && slices.Contains(o.Requires, n) {
				return fmt.Errorf("%s needs %s: remove %s first", other, n, other)
			}
		}
	}
	if user == "" {
		user = "screen"
	}
	home, _ := s.homeOf(ctx, user)
	out, _ := s.run(ctx, "id", "-u", user)
	uid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	in := &installer{Setup: s, user: screenUser{name: user, home: home, uid: uid}}
	for _, n := range names {
		a, err := LoadAddon(n)
		if err != nil {
			return err
		}
		s.Out.Step("Removing add-on " + n)
		if err := in.removeAddon(ctx, a); err != nil {
			return err
		}
		installed = slices.DeleteFunc(installed, func(x string) bool { return x == n })
		if err := s.setInstalled(installed); err != nil {
			return err
		}
	}
	s.Out.Done()
	return nil
}
