package deploy

import (
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
	want := []string{"android", "chromium", "compose", "controllers", "dolphin", "flatpak", "moonlight", "nvidia", "seats", "steam", "sunshine", "tailscale", "wireguard"}
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
	want := map[string]bool{"steam": false, "dolphin": false, "chromium": false, "moonlight": false,
		"compose": true, "controllers": true, "flatpak": true, "nvidia": true, "seats": true, "android": true, "sunshine": true, "tailscale": true, "wireguard": true}
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
