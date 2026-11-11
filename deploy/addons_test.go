package deploy

import (
	"bytes"
	"context"
	"os"
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
	want := []string{"chromium", "compose", "controllers", "dolphin", "flatpak", "nvidia", "seats", "steam", "tailscale", "wireguard"}
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
	want := map[string]bool{"steam": false, "dolphin": false, "chromium": false,
		"compose": true, "controllers": true, "flatpak": true, "nvidia": true, "seats": true, "tailscale": true, "wireguard": true}
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
	os.WriteFile(filepath.Join(m.root, "etc/hostd/addons"), []byte("flatpak\nsunshine\nandroid\nshare-remote\nshare-keys\nmoonlight\nshare-pc\nshare-phone\n"), 0o644)
	// The gone ones (the sharing add-ons) are left out of the list.
	if got := s.installedAddons(); !slices.Equal(got, []string{"flatpak"}) {
		t.Errorf("installed %v", got)
	}
	if got := s.adminInstalled(); !slices.Equal(got, []string{"flatpak"}) {
		t.Errorf("admin list %v", got)
	}
	for _, gone := range retired {
		if _, err := LoadAddon(gone); err == nil {
			t.Errorf("%s is retired but still exists", gone)
		}
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
