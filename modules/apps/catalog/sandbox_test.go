package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/modules/apps/spec"
)

// sandboxTree makes ~/.var/app with Steam's sandbox as Steam leaves it: a
// shortcut per game, its icon in several sizes; and the exported entry of
// Steam itself, whose name names the games' folder.
func sandboxTree(t *testing.T) (exported, sandboxes string) {
	t.Helper()
	root := t.TempDir()
	write := func(p, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exported = filepath.Join(root, "exports", "applications")
	write(filepath.Join(exported, "com.valvesoftware.Steam.desktop"),
		"[Desktop Entry]\nType=Application\nName=Steam\nExec=/usr/bin/flatpak run com.valvesoftware.Steam\nX-Flatpak=com.valvesoftware.Steam\nCategories=Network;Game;\n")
	write(filepath.Join(exported, "portal-2.desktop"), // a name taken already
		"[Desktop Entry]\nType=Application\nName=Not a game\nExec=other\n")
	sandboxes = filepath.Join(root, "var", "app")
	steam := filepath.Join(sandboxes, "com.valvesoftware.Steam", ".local", "share")
	write(filepath.Join(steam, "applications", "POSTAL 2.desktop"),
		"[Desktop Entry]\nType=Application\nName=POSTAL 2\nExec=steam steam://rungameid/223470\nIcon=steam_icon_223470\nCategories=Game;\n")
	write(filepath.Join(steam, "applications", "Portal 2.desktop"),
		"[Desktop Entry]\nType=Application\nName=Portal 2\nExec=steam steam://rungameid/620\nIcon=steam_icon_620\nCategories=Game;\n")
	write(filepath.Join(steam, "applications", "Tool.desktop"),
		"[Desktop Entry]\nType=Application\nName=Tool\nExec=tool --x\nIcon=/abs/tool.png\n")
	for _, size := range []string{"32x32", "256x256", "48x48"} {
		write(filepath.Join(steam, "icons", "hicolor", size, "apps", "steam_icon_223470.png"), "png")
	}
	write(filepath.Join(steam, "icons", "hicolor", "scalable", "apps", "steam_icon_620.svg"), "<svg/>")
	write(filepath.Join(sandboxes, "org.empty.App", "cache", "x"), "") // no shortcuts: nothing
	return exported, sandboxes
}

func TestSandboxShortcutsAreApps(t *testing.T) {
	exported, sandboxes := sandboxTree(t)
	src := DesktopSource{Dirs: []string{exported}, Sandboxes: sandboxes, LookPath: fakeLookPath}
	apps, problems := src.Scan()
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	by := map[string]spec.App{}
	for _, a := range apps {
		by[a.ID] = a
	}
	postal, ok := by["postal-2"]
	if !ok {
		t.Fatalf("no postal-2 in %v", reflect.ValueOf(by).MapKeys())
	}
	icons := filepath.Join(sandboxes, "com.valvesoftware.Steam", ".local", "share", "icons", "hicolor")
	if postal.Name != "POSTAL 2" || postal.Folder != "Games/Steam" ||
		postal.Icon != filepath.Join(icons, "256x256", "apps", "steam_icon_223470.png") {
		t.Errorf("postal: %+v", postal)
	}
	if want := []string{"flatpak", "run", "--command=steam", "com.valvesoftware.Steam", "steam://rungameid/223470"}; !reflect.DeepEqual(postal.Runner.Command, want) {
		t.Errorf("runs %q, want %q", postal.Runner.Command, want)
	}
	if !reflect.DeepEqual(postal.Aliases, []string{"steam-postal-2"}) {
		t.Errorf("aliases %v", postal.Aliases)
	}
	// portal-2 is taken by an exported entry: this one is steam-portal-2.
	portal, ok := by["steam-portal-2"]
	if !ok || portal.Icon != filepath.Join(icons, "scalable", "apps", "steam_icon_620.svg") || len(portal.Aliases) != 0 {
		t.Errorf("portal: %+v", portal)
	}
	if by["portal-2"].Name != "Not a game" {
		t.Errorf("the exported entry lost its id: %+v", by["portal-2"])
	}
	// Not a game: no folder; an icon given as a path is kept.
	if tool := by["tool"]; tool.Folder != "" || tool.Icon != "/abs/tool.png" {
		t.Errorf("tool: %+v", tool)
	}
	// A game exported as usual goes in Games.
	if steam := by["steam"]; steam.Folder != "Games" {
		t.Errorf("steam: %+v", steam)
	}
	if got := SandboxDesktopDirs(sandboxes); len(got) != 1 || !strings.HasSuffix(got[0], "com.valvesoftware.Steam/.local/share/applications") {
		t.Errorf("dirs to watch: %v", got)
	}
	if SandboxDesktopDirs("") != nil {
		t.Error("no sandboxes, no dirs")
	}
}

// An add-on refines a shortcut by extending it under its own id: one app,
// the shortcut's name and icon, the add-on's runner and pictures.
func TestAnAppFileExtendsASandboxShortcut(t *testing.T) {
	exported, sandboxes := sandboxTree(t)
	apps, _ := (&DesktopSource{Dirs: []string{exported}, Sandboxes: sandboxes, LookPath: fakeLookPath}).Scan()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "steam-223470.toml"), []byte(`extends = "steam-postal-2"
id = "steam-223470"
tile = "/covers/postal.jpg"
banner = "/covers/postal-hero.jpg"
details = "Last played today"
runner = { type = "exec", command = ["flatpak", "run", "com.valvesoftware.Steam", "steam://rungameid/223470"], handoff = "SteamAppId=223470" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	files, problems := LoadAppFiles(dir)
	cat := Build(apps, files, problems)
	if p := cat.Problems; len(p) != 0 {
		t.Fatalf("problems: %v", p)
	}
	a, ok := cat.Get("steam-223470")
	if !ok || a.Name != "POSTAL 2" || !strings.HasSuffix(a.Icon, "steam_icon_223470.png") || a.Folder != "Games/Steam" ||
		a.Tile != "/covers/postal.jpg" || a.Banner != "/covers/postal-hero.jpg" || a.Details != "Last played today" || a.Runner.Handoff != "SteamAppId=223470" {
		t.Fatalf("extended: %+v", a)
	}
	if _, dup := cat.Get("postal-2"); !dup { // still found by its old ids, as one app
		t.Error("postal-2 no longer finds it")
	}
	n := 0
	for _, x := range cat.List(true) {
		if x.Name == "POSTAL 2" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d POSTAL 2s", n)
	}
}

func TestPicturesMustBeAbsolute(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.toml"), []byte(`id = "x"
tile = "covers/x.jpg"
details = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
runner = { type = "exec", command = ["x"] }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	files, problems := LoadAppFiles(dir)
	cat := Build(nil, files, problems)
	if p := cat.Problems; len(p) != 1 || !strings.Contains(p[0].Error, "tile") || !strings.Contains(p[0].Error, "details") {
		t.Fatalf("problems: %v", p)
	}
}
