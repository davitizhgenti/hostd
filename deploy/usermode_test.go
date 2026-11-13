package deploy

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// files lists every file under root (relative), but run/ (the fake's sockets).
func filesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		if err == nil && !d.IsDir() && !strings.HasPrefix(rel, "run/") {
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// The point of user mode: run as a user, an install, an update and an
// uninstall issue no root command and write nothing outside the home.
func TestUserModeNeedsNoRoot(t *testing.T) {
	m := newFakeMachine(t)
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the installer"), 0o755)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), User: &screenUser{name: "screen", home: "/home/screen", uid: 1001}}
	before := append(filesUnder(t, m.root), "etc/hostd/addons") // the admin's file, made below
	o := Options{GPU: "other", From: buildDir(t), Self: self}
	ctx := context.Background()
	if err := s.Install(ctx, o); err != nil {
		t.Fatalf("install: %v\n%s", err, out.String())
	}
	if err := s.AddAddons([]string{"chromium"}); err == nil || !strings.Contains(err.Error(), "needs root") {
		t.Fatalf("a user add-on that needs the Flatpak add-on: %v", err) // flatpak is root, not installed by an admin
	}
	os.MkdirAll(filepath.Join(m.root, "etc/hostd"), 0o755)
	os.WriteFile(filepath.Join(m.root, "etc/hostd/addons"), []byte("flatpak\n"), 0o644) // an admin installed it
	if err := s.AddAddons([]string{"chromium"}); err != nil {
		t.Fatal(err)
	}
	o.Update, o.Adding = &UpdateOptions{}, []string{"chromium"}
	if err := s.Install(ctx, o); err != nil {
		t.Fatalf("update: %v\n%s", err, out.String())
	}
	if err := s.AddAddons([]string{"compose"}); err == nil || !strings.Contains(err.Error(), "sudo hostd-admin addons add compose") {
		t.Fatalf("a root add-on: %v", err)
	}
	if err := s.Uninstall(ctx, UninstallArgs{Purge: true, All: true}); err != nil {
		t.Fatal(err)
	}

	for _, line := range m.ran {
		fields := strings.Fields(line)
		bad := slices.ContainsFunc(fields, func(f string) bool {
			return slices.Contains([]string{"apt-get", "useradd", "usermod", "groupadd", "runuser", "sudo", "update-initramfs", "modprobe", "chown", "enable-linger", "--system"}, f)
		})
		if bad || (slices.Contains(fields, "systemctl") && !slices.Contains(fields, "--user")) {
			t.Errorf("user mode ran a root command: %s", line)
		}
	}
	for _, f := range filesUnder(t, m.root) {
		if !strings.HasPrefix(f, "home/screen/") && !slices.Contains(before, f) {
			t.Errorf("wrote outside the home: %s", f)
		}
	}
}

func TestUserModeInstallsInTheHome(t *testing.T) {
	m := newFakeMachine(t)
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the installer"), 0o755)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), User: &screenUser{name: "screen", home: "/home/screen", uid: 1001}}
	if err := s.Install(context.Background(), Options{GPU: "other", From: buildDir(t), Self: self}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, p := range []string{".local/bin/hostctl", ".local/bin/hostd-setup", ".local/lib/hostd/shell/hostd-shell", ".config/systemd/user/hostd-shell.service", ".local/share/hostd/docs/index.html",
		".config/hostd/update.conf", ".config/systemd/user/hostd-update.timer", ".config/systemd/user/hostd.service", ".config/hostd/apps/hostd-menu.toml"} {
		if _, err := os.Stat(filepath.Join(m.root, "home/screen", p)); err != nil {
			t.Errorf("not installed: %s", p)
		}
	}
	toml, _ := os.ReadFile(filepath.Join(m.root, "home/screen/.config/hostd/apps/hostd-menu.toml"))
	if !strings.Contains(string(toml), `"/home/screen/.local/lib/hostd/shell/hostd-shell"`) {
		t.Errorf("the menu app points elsewhere:\n%s", toml)
	}
	if !slices.ContainsFunc(m.ran, func(l string) bool {
		return strings.HasSuffix(l, "systemctl --user is-enabled --quiet hostd-update.timer")
	}) {
		t.Errorf("no user timer: %q", m.ran)
	}
}

func TestUserModeSetupAndAddons(t *testing.T) {
	var out bytes.Buffer
	s := &Setup{Root: t.TempDir(), Run: newFakeMachine(t), Out: NewOutput(&out, true)}
	t.Setenv("HOME", "/home/someone")
	if err := s.CurrentUserMode(); err != nil || s.User.home != "/home/someone" {
		t.Fatalf("%v %+v", err, s.User)
	}
	if got := s.UpdateConfPath(); got != "/home/someone/.config/hostd/update.conf" {
		t.Errorf("update.conf at %s", got)
	}
	var list bytes.Buffer
	if err := s.ListAddons(&list); err != nil || !strings.Contains(list.String(), "root add-ons: an admin") {
		t.Errorf("%v\n%s", err, list.String())
	}
	if err := s.UserMode(&user.User{Uid: "x"}); err == nil {
		t.Error("a user id that is no number")
	}
	// Removing what is not installed; and the update timer's "manual" setting.
	if err := s.RemoveAddons(context.Background(), "", []string{"steam"}); err == nil {
		t.Error("removed an add-on that is not installed")
	}
}

func TestUserModeAutoUpdateManual(t *testing.T) {
	m := newFakeMachine(t)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), User: &screenUser{name: "screen", home: "/home/screen", uid: 1001}}
	os.MkdirAll(filepath.Join(m.root, "home/screen/.config/hostd"), 0o755)
	os.WriteFile(filepath.Join(m.root, "home/screen/.config/hostd/update.conf"), []byte("APPLY=manual\n"), 0o644)
	if err := s.AutoUpdate(context.Background(), UpdateArgs{}, nil); err != nil || !strings.Contains(out.String(), "hostd-setup update") || strings.Contains(out.String(), "sudo") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}
