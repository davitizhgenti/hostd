package deploy

import (
	"slices"
	"testing"
)

// Every add-on's manifest loads, names files it has, and what it requires
// exists.
func TestAddonsLoad(t *testing.T) {
	names := AddonNames()
	want := []string{"chromium", "compose", "controllers", "dolphin", "flatpak", "nvidia", "pterodactyl", "steam"}
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
		for _, r := range a.Requires {
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
