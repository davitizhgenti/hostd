package deploy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"slices"
	"testing"

	"github.com/davitizhgenti/hostd/modules/apps/catalog"
)

// Every add-on's manifest loads, names files it has, and what it requires
// exists.
func TestAddonsLoad(t *testing.T) {
	names := AddonNames()
	want := []string{"chromium", "compose", "controllers", "dolphin", "flatpak", "nvidia", "seats", "share-keys", "share-pc", "share-phone", "share-remote", "steam", "tailscale", "wireguard"}
	if !slices.Equal(names, want) {
		t.Fatalf("add-ons %v", names)
	}
	for _, n := range names {
		a, err := LoadAddon(n)
		if err != nil {
			t.Fatal(err)
		}
		if a.Description == "" {
			t.Errorf("%s: no description", n)
		}
		for _, r := range a.Needs {
			if !slices.Contains(names, r) {
				t.Errorf("%s requires %s, which does not exist", n, r)
			}
		}
		for _, f := range append([]string{a.Hook}, func() (s []string) {
			for _, f := range a.Files {
				s = append(s, f.Src)
			}
			return
		}()...) {
			if f != "" {
				if _, err := embedded.Open("addons/" + n + "/" + f); err != nil {
					t.Errorf("%s: no file %s", n, f)
				}
			}
		}
	}
	if _, err := LoadAddon("nope"); err == nil {
		t.Error("an unknown add-on loaded")
	}
}

func TestAddonOrder(t *testing.T) {
	order, err := addonOrder([]string{"steam", "dolphin"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range order {
		names = append(names, a.Name)
	}
	if !slices.Equal(names, []string{"flatpak", "controllers", "steam", "dolphin"}) {
		t.Fatalf("order %v", names)
	}
	if b := addonsBundle(); len(b) != 64 || b != addonsBundle() {
		t.Fatal("bundle stamp")
	}
}

// Every resource an add-on ships is a valid one of its kind, and its file is
// there: a change to a schema cannot silently break an add-on.
func TestBundledResourcesAreValid(t *testing.T) {
	for _, name := range AddonNames() {
		a, err := LoadAddon(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, r := range a.Resources {
			b, err := os.ReadFile(filepath.Join("addons", name, r))
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			plural, file, _ := strings.Cut(r, "/")
			if plural == "apps" {
				if _, err := catalog.ParseAppFile("/x/"+file, b); err != nil {
					t.Errorf("%s: %s: %v", name, r, err)
				}
			}
		}
	}
}

func TestAddonPrivilege(t *testing.T) {
	want := map[string]bool{"steam": false, "dolphin": false, "chromium": false, "share-pc": false,
		"compose": true, "controllers": true, "flatpak": true, "nvidia": true, "seats": true, "share-phone": true, "share-remote": true, "share-keys": true, "tailscale": true, "wireguard": true}
	for name, root := range want {
		a, err := LoadAddon(name)
		if err != nil {
			t.Fatal(err)
		}
		if a.Privileged() != root {
			t.Errorf("%s: root = %v, want %v", name, a.Privileged(), root)
		}
		if !root && a.needsRoot() {
			t.Errorf("%s says it needs no root but declares what does", name)
		}
	}
	for _, n := range AddonNames() {
		if _, ok := want[n]; !ok {
			t.Errorf("add-on %s is not classified in this test", n)
		}
	}
	f := false
	if (Addon{Root: &f, Packages: []string{"x"}}).needsRoot() == false {
		t.Error("packages need root")
	}
	if !(Addon{Files: []AddonFile{{Dest: "/etc/x"}}}).Privileged() || (Addon{Files: []AddonFile{{Dest: "~/x"}}}).Privileged() {
		t.Error("files outside the home need root; inside, not")
	}
}

func TestRenamedAddonsKeepWorking(t *testing.T) {
	m := newFakeMachine(t)
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&bytes.Buffer{}, true)}
	os.MkdirAll(filepath.Join(m.root, "etc/hostd"), 0o755)
	os.WriteFile(filepath.Join(m.root, "etc/hostd/addons"), []byte("flatpak\nsunshine\nandroid\nshare-remote\n"), 0o644)
	if got := s.installedAddons(); !slices.Equal(got, []string{"flatpak", "share-remote", "share-phone"}) {
		t.Errorf("installed %v", got)
	}
	if got := s.adminInstalled(); !slices.Contains(got, "share-phone") || slices.Contains(got, "android") {
		t.Errorf("admin list %v", got)
	}
	for old, now := range renamed {
		if _, err := LoadAddon(now); err != nil {
			t.Errorf("%s was renamed %s: %v", old, now, err)
		}
		if _, err := LoadAddon(old); err == nil {
			t.Errorf("%s still exists beside %s", old, now)
		}
	}
}

// The helpers that make an app only when there is something to show.
func TestShareHelpersMakeAppsOnDemand(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "adb"), []byte("#!/bin/sh\n[ \"$1\" = connect ] && echo \"connected to $2\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(script string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{script}, args...)...)
		cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + home + "/.config", "PATH=" + bin + ":/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	apps := filepath.Join(home, ".config/hostd/apps")
	pc, phone := "addons/share-pc/hostd-share-pc", "addons/share-phone/hostd-share-phone"

	// A PC: no app until it is added; the name is checked; remove takes it away.
	if _, err := os.Stat(filepath.Join(apps, "share-office.toml")); err == nil {
		t.Fatal("an app before anything was added")
	}
	if out, err := run(pc, "add", "Bad Name", "10.0.0.5"); err == nil {
		t.Errorf("a bad name was accepted: %s", out)
	}
	if out, err := run(pc, "add", "office", "10.0.0.5"); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(apps, "share-office.toml"))
	for _, want := range []string{`name = "Share: office"`, `"stream", "10.0.0.5", "Desktop"`, "fullscreen = true"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("app file lacks %q:\n%s", want, b)
		}
	}
	if out, _ := run(pc, "list"); strings.TrimSpace(out) != "office" {
		t.Errorf("list: %q", out)
	}
	if out, err := run(pc, "remove", "office"); err != nil {
		t.Fatalf("remove: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(apps, "share-office.toml")); err == nil {
		t.Error("the app stays after remove")
	}
	if _, err := run(pc, "remove", "office"); err == nil {
		t.Error("removed a PC that is not there")
	}

	// A phone: the app appears only after a successful connect.
	if _, err := os.Stat(filepath.Join(apps, "share-phone.toml")); err == nil {
		t.Fatal("a phone app before connect")
	}
	if out, err := run(phone, "connect", "192.168.18.40:41231"); err != nil {
		t.Fatalf("connect: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(apps, "share-phone.toml")); err != nil {
		t.Error("no phone app after connect")
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".config/hostd/share-phone.addr")); strings.TrimSpace(string(b)) != "192.168.18.40:41231" {
		t.Errorf("saved address %q", b)
	}
	if out, err := run(phone, "forget"); err != nil {
		t.Fatalf("forget: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(apps, "share-phone.toml")); err == nil {
		t.Error("the phone app stays after forget")
	}
	// A failed connect makes nothing.
	if err := os.WriteFile(filepath.Join(bin, "adb"), []byte("#!/bin/sh\n[ \"$1\" = connect ] && echo \"failed to connect to $2\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := run(phone, "connect", "10.9.9.9:1"); err == nil {
		t.Error("connect succeeded though adb said it failed")
	}
	if _, err := os.Stat(filepath.Join(apps, "share-phone.toml")); err == nil {
		t.Error("an app for a phone that is not reachable")
	}
}

func TestRetiredAppFilesGoAway(t *testing.T) {
	m := newFakeMachine(t)
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&bytes.Buffer{}, true)}
	in := &installer{Setup: s, user: screenUser{name: "screen", home: "/home/screen", uid: 1001}}
	old := filepath.Join(m.root, "home/screen/.config/hostd/apps/android.toml")
	os.MkdirAll(filepath.Dir(old), 0o755)
	os.WriteFile(old, []byte("# Managed by hostd's android add-on\nname = \"Android phone\"\n"), 0o644)
	mine := filepath.Join(m.root, "home/screen/.config/hostd/apps/share-pc.toml")
	os.WriteFile(mine, []byte("name = \"mine\"\n"), 0o644)
	if err := in.runAddons(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("the retired app file stays")
	}
	// A file a person wrote under the same name is left alone.
	os.WriteFile(old, []byte("name = \"my own android app\"\n"), 0o644)
	if err := in.runAddons(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("a person's own app file was removed")
	}
}
