package catalog

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/modules/apps/spec"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fakeLookPath finds every binary except ones named "missing...".
func fakeLookPath(name string) (string, error) {
	if strings.HasPrefix(filepath.Base(name), "missing") {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + filepath.Base(name), nil
}

var fixtureDirs = []string{"testdata/system/applications", "testdata/user/applications"}

func TestParseDesktopEntry(t *testing.T) {
	e, err := ParseDesktopEntry([]byte(`# comment
[Desktop Entry]
Name=Firefox
Name[de]=Feuerfuchs
Exec=firefox %u
Categories=Network;WebBrowser;
NoDisplay=true
OnlyShowIn=GNOME;sway;

[Desktop Action private]
Name=Private window
Exec=firefox --private
`))
	if err != nil {
		t.Fatal(err)
	}
	want := DesktopEntry{Name: "Firefox", Exec: "firefox %u", NoDisplay: true,
		Categories: []string{"Network", "WebBrowser"}, OnlyShowIn: []string{"GNOME", "sway"}}
	if !reflect.DeepEqual(e, want) {
		t.Fatalf("got  %+v\nwant %+v", e, want)
	}
	if _, err := ParseDesktopEntry([]byte("Name=x\n")); err == nil {
		t.Fatal("file without [Desktop Entry] accepted")
	}
}

func TestExecArgs(t *testing.T) {
	for _, tc := range []struct {
		exec string
		want []string
		err  bool
	}{
		{"firefox %u", []string{"firefox"}, false},
		{"/usr/bin/vlc --started-from-file %U", []string{"/usr/bin/vlc", "--started-from-file"}, false},
		{`"/opt/My App/run" --flag`, []string{"/opt/My App/run", "--flag"}, false},
		{`run "a \"b\" c" \$x`, []string{"run", `a "b" c`, `\$x`}, false},
		{`run --rate=100%% --file=%f %i %c %k`, []string{"run", "--rate=100%"}, false},
		{"  spaced   out  ", []string{"spaced", "out"}, false},
		{`run ""`, []string{"run"}, false},
		{`run "unterminated`, nil, true},
		{"%U", nil, true},
		{"", nil, true},
	} {
		got, err := ExecArgs(tc.exec)
		if tc.err {
			if err == nil {
				t.Errorf("ExecArgs(%q) = %q, want error", tc.exec, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ExecArgs(%q) = %q, %v; want %q", tc.exec, got, err, tc.want)
		}
	}
}

func buildFixture(t *testing.T) *Catalog {
	t.Helper()
	src := DesktopSource{Dirs: fixtureDirs, LookPath: fakeLookPath}
	discovered, problems := src.Scan()
	files, fileProblems := LoadAppFiles("testdata/apps")
	return Build(discovered, files, append(problems, fileProblems...))
}

// The whole merged catalog from the fixtures, pinned in a golden file.
// Regenerate after an intended change: go test ./modules/apps/catalog -update
func TestCatalogGolden(t *testing.T) {
	cat := buildFixture(t)
	got, _ := json.MarshalIndent(map[string]any{"apps": cat.List(true), "problems": cat.Problems}, "", "  ")
	got = append(got, '\n')
	path := "testdata/catalog.golden.json"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("catalog changed; diff against %s:\n%s", path, got)
	}
}

func TestCatalogDetails(t *testing.T) {
	cat := buildFixture(t)
	get := func(id string) *spec.App {
		t.Helper()
		a, ok := cat.Get(id)
		if !ok {
			t.Fatalf("no app %q", id)
		}
		return a
	}

	// The user directory overrides the system one, and a file renames it;
	// the old ID still finds it.
	b := get("browser")
	if b.Name != "Browser" || b.Instance.Policy != "multiple" ||
		!reflect.DeepEqual(b.Runner.Command, []string{"/usr/lib/firefox-esr/firefox-esr", "--kiosk"}) {
		t.Fatalf("browser = %+v", b)
	}
	if get("firefox-esr") != b {
		t.Fatal("old ID does not find the renamed app")
	}
	if _, ok := cat.apps["firefox-esr"]; ok {
		t.Fatal("renamed app still listed under its old ID")
	}

	// Defaults by surface.
	if b.Surface != spec.SurfaceWindow || b.Restart != "never" || !b.Window.Fullscreen || b.Instance.IfRunning != "focus" {
		t.Fatalf("window defaults: %+v", b)
	}
	j := get("jellyfin")
	if j.Surface != spec.SurfaceBackground || j.Restart != "always" || j.Instance.Policy != "single" || j.Source != "file" {
		t.Fatalf("background app: %+v", j)
	}
	if tv := get("jellyfin-tv"); tv.Audio.Volume == nil || *tv.Audio.Volume != 70 || tv.Runner.URL == "" {
		t.Fatalf("jellyfin-tv: %+v", tv)
	}

	// Hidden by default, and shown again by a file.
	for _, id := range []string{"settings", "htop", "dolphin"} {
		if a, ok := cat.Get(id); ok && !a.Hidden {
			t.Errorf("%s should be hidden", id)
		}
	}
	if get("foot").Hidden || get("foot").Window.Fullscreen {
		t.Fatalf("foot.toml should show foot, windowed: %+v", get("foot"))
	}
	if get("kde-dolphin") == nil || get("kcalc").Aliases[0] != "org.kde.kcalc" || get("org.kde.kcalc").ID != "kcalc" {
		t.Fatal("IDs from subdirectories or reverse-DNS names wrong")
	}
	if got := get("quoted"); got.Name != "My Tool" ||
		!reflect.DeepEqual(got.Runner.Command, []string{"/opt/My App/run", "--title", `Big "Screen"`, "--rate=100%"}) {
		t.Fatalf("quoted = %+v", got)
	}

	// Left out quietly: links, deleted by the user, TryExec missing.
	for _, id := range []string{"website", "vlc", "missing"} {
		if _, ok := cat.Get(id); ok {
			t.Errorf("%s should not be in the catalog", id)
		}
	}
	// The hidden list excludes hidden apps; the full one has them.
	if len(cat.List(false)) >= len(cat.List(true)) {
		t.Fatal("List(false) includes hidden apps")
	}
}

func TestCatalogProblems(t *testing.T) {
	cat := buildFixture(t)
	want := map[string]string{
		"broken.desktop":     "no [Desktop Entry]",
		"noexec.desktop":     "has no command",
		"Bad Name.toml":      `id "Bad Name"`,
		"bad-runner.toml":    `runner type "teleport"`,
		"broken.toml":        "",
		"later-clash.toml":   "already defined by testdata/apps/jellyfin.toml",
		"discovered-id.toml": `use extends = "steam"`,
		"needs-ghost.toml":   `requires "ghost"`,
		"orphan.toml":        `extends "dota3"`,
		"typo.toml":          "unknown key(s): window.fulscreen",
	}
	got := map[string]string{}
	for _, p := range cat.Problems {
		got[filepath.Base(p.File)] = p.Error
	}
	for file, sub := range want {
		msg, ok := got[file]
		if !ok {
			t.Errorf("%s: not reported", file)
			continue
		}
		if !strings.Contains(msg, sub) {
			t.Errorf("%s: %q does not mention %q", file, msg, sub)
		}
	}
	if len(got) != len(want) {
		t.Errorf("problems = %v", got)
	}
	// A rejected clash leaves the first definition intact.
	if j, _ := cat.Get("jellyfin"); j.Runner.Type != spec.RunnerDocker {
		t.Fatal("clashing file replaced the first one")
	}
}

func TestAppFileDefaultsIDFromName(t *testing.T) {
	f, err := ParseAppFile("/x/apps/blog.toml", []byte(`runner = { type = "docker", build = "." }`))
	if err != nil || f.ID != "blog" {
		t.Fatalf("%+v %v", f, err)
	}
	cat := Build(nil, []AppFile{f}, nil)
	if a, ok := cat.Get("blog"); !ok || a.Surface != spec.SurfaceBackground {
		t.Fatalf("blog = %+v", a)
	}
}

func TestValidation(t *testing.T) {
	for body, want := range map[string]string{
		`runner = { type = "exec" }`:                                                         "needs a command",
		`runner = { type = "url", url = "localhost:8096" }`:                                  "http://",
		`runner = { type = "docker" }`:                                                       "image or a build",
		`runner = { type = "compose" }`:                                                      "needs a file",
		`runner = { type = "flatpak" }`:                                                      "needs an app_id",
		`runner = { type = "exec", command = ["steam"], handoff = "SteamAppId" }`:            "use KEY=value",
		`runner = { type = "docker", image = "x", handoff = "A=1" }`:                         "applies to exec",
		"runner = { type = \"exec\", command = [\"x\"] }\nsurface = \"tv\"":                  `surface "tv"`,
		"runner = { type = \"exec\", command = [\"x\"] }\nrestart = \"maybe\"":               `restart "maybe"`,
		"runner = { type = \"exec\", command = [\"x\"] }\n[instance]\npolicy = \"many\"":     "instance.policy",
		"runner = { type = \"exec\", command = [\"x\"] }\n[instance]\nif_running = \"kill\"": "instance.if_running",
		"runner = { type = \"exec\", command = [\"x\"] }\n[window]\nwrap = \"xvfb\"":         "window.wrap",
		"runner = { type = \"exec\", command = [\"x\"] }\n[audio]\nvolume = 200":             "audio.volume",
	} {
		f, err := ParseAppFile("/x/a.toml", []byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		cat := Build(nil, []AppFile{f}, nil)
		if len(cat.Problems) != 1 || !strings.Contains(cat.Problems[0].Error, want) {
			t.Errorf("%q: problems %v, want %q", body, cat.Problems, want)
		}
	}
}

// --- the module -----------------------------------------------------------

func FuzzParseDesktopEntry(f *testing.F) {
	for _, file := range []string{"firefox-esr.desktop", "quoted.desktop", "broken.desktop"} {
		if b, err := os.ReadFile(filepath.Join("testdata/system/applications", file)); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		e, err := ParseDesktopEntry(data)
		if err == nil {
			_, _ = ExecArgs(e.Exec)
		}
	})
}

func FuzzExecArgs(f *testing.F) {
	for _, s := range []string{`firefox %u`, `"a b" c\ d`, `run "x \"y\""`, `%%%`, `"`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		args, err := ExecArgs(s)
		if err == nil && len(args) == 0 {
			t.Fatal("no error but no args")
		}
	})
}

func FuzzParseAppFile(f *testing.F) {
	entries, _ := os.ReadDir("testdata/apps")
	for _, e := range entries {
		if b, err := os.ReadFile(filepath.Join("testdata/apps", e.Name())); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if file, err := ParseAppFile("/x/fuzz.toml", data); err == nil {
			Build(nil, []AppFile{file}, nil) // must not panic
		}
	})
}

func TestShippedAppFiles(t *testing.T) {
	// The app files the installer and its add-ons write: each must load
	// without a problem (over the desktop entries they extend).
	all, _ := filepath.Glob("../../../deploy/addons/*/*.toml")
	var paths []string
	for _, p := range all {
		if filepath.Base(p) != "addon.toml" { // an add-on's own manifest, not an app file
			paths = append(paths, p)
		}
	}
	paths = append(paths, "../../../deploy/files/menu/hostd-menu.toml")
	desktop := []spec.App{{ID: "chromium", Name: "Chromium Web Browser", Runner: spec.Runner{Type: spec.RunnerExec, Command: []string{"chromium"}}},
		{ID: "steam", Name: "Steam", Runner: spec.Runner{Type: spec.RunnerFlatpak, AppID: "com.valvesoftware.Steam"},
			Actions: []spec.AppAction{{ID: "bigpicture", Name: "Big Picture", Command: []string{"flatpak", "run", "com.valvesoftware.Steam", "steam://open/bigpicture"}}}}}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := ParseAppFile(p, data)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		c := Build(desktop, []AppFile{f}, nil)
		if len(c.Problems) != 0 {
			t.Errorf("%s: %v", p, c.Problems)
		}
	}
	steam := mustParse(t, "steam.toml", string(must(os.ReadFile("../../../deploy/addons/steam/steam.toml"))))
	app, _ := Build(desktop, []AppFile{steam}, nil).Get("steam")
	for _, a := range app.Actions {
		if a.Command[len(a.Command)-1][:8] != "steam://" {
			t.Errorf("Steam action %s does not hand Steam a link: %q", a.ID, a.Command)
		}
	}
	if len(app.Actions) == 0 || app.Runner.Handoff != "FLATPAK_ID=com.valvesoftware.Steam" {
		t.Errorf("steam app %+v", app)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestUnder(t *testing.T) {
	c := Build(nil, []AppFile{
		mustParse(t, "/x/steam-620.toml", "name = \"Portal 2\"\nunder = \"steam\"\nrunner = { type = \"exec\", command = [\"x\"] }"),
		mustParse(t, "/x/self.toml", "under = \"self\"\nrunner = { type = \"exec\", command = [\"x\"] }"),
	}, nil)
	game, _ := c.Get("steam-620")
	if game.Under != "steam" {
		t.Fatalf("under %q", game.Under)
	}
	if len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Error, "under") {
		t.Fatalf("problems %v", c.Problems)
	}
}

func TestFlatpakEntries(t *testing.T) {
	dir := t.TempDir()
	apps := filepath.Join(dir, "applications")
	_ = os.MkdirAll(apps, 0o755)
	_ = os.WriteFile(filepath.Join(apps, "tv.kodi.Kodi.desktop"), []byte(`[Desktop Entry]
Type=Application
Name=Kodi
Exec=/usr/bin/flatpak run --branch=stable --arch=x86_64 --command=kodi --file-forwarding tv.kodi.Kodi @@ %F @@
X-Flatpak=tv.kodi.Kodi
`), 0o644)
	found, problems := (&DesktopSource{Dirs: []string{apps}}).Scan()
	if len(problems) != 0 || len(found) != 1 {
		t.Fatalf("found %+v, problems %v", found, problems)
	}
	cat := Build(found, nil, nil)
	kodi, ok := cat.Get("kodi")
	if !ok {
		t.Fatalf("no kodi: %+v", cat.List(true))
	}
	if kodi.Runner.Type != spec.RunnerFlatpak || kodi.Runner.AppID != "tv.kodi.Kodi" {
		t.Fatalf("runner %+v", kodi.Runner)
	}
	// Flatpak apps run in a scope of their own: windows match by app ID
	// or by the sandbox's FLATPAK_ID.
	if kodi.Match != (spec.Match{AppID: "tv.kodi.Kodi", Env: "FLATPAK_ID=tv.kodi.Kodi"}) {
		t.Fatalf("match %+v", kodi.Match)
	}
}

func TestDefaultDesktopDirsIncludeFlatpak(t *testing.T) {
	// A service has no XDG_DATA_DIRS: the default plus Flatpak's exports.
	t.Setenv("XDG_DATA_DIRS", "")
	t.Setenv("XDG_DATA_HOME", "/home/u/.local/share")
	want := []string{
		"/usr/share/applications", "/usr/local/share/applications",
		"/var/lib/flatpak/exports/share/applications",
		"/home/u/.local/share/flatpak/exports/share/applications",
		"/home/u/.local/share/applications",
	}
	if got := DefaultDesktopDirs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("dirs\n%q\nwant\n%q", got, want)
	}
	// A session's own list is used as it is.
	t.Setenv("XDG_DATA_DIRS", "/opt/share:/usr/share")
	if got := DefaultDesktopDirs(); !reflect.DeepEqual(got, []string{"/usr/share/applications", "/opt/share/applications", "/home/u/.local/share/applications"}) {
		t.Fatalf("dirs %q", got)
	}
}

// --- commands: flatpak, url, gamescope ---------------------------------------------

func TestDesktopActions(t *testing.T) {
	e, err := ParseDesktopEntry([]byte(`[Desktop Entry]
Type=Application
Name=Chromium
Exec=chromium %U
Actions=new-window;new-private-window;missing;

[Desktop Action new-window]
Name=New Window
Name[de]=Neues Fenster
Exec=chromium

[Desktop Action new-private-window]
Name=New Incognito Window
Exec=chromium --incognito

[Desktop Action unlisted]
Name=Not listed, so not offered
Exec=chromium --secret
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []DesktopAction{{"new-window", "New Window", "chromium"}, {"new-private-window", "New Incognito Window", "chromium --incognito"}}
	if !reflect.DeepEqual(e.Actions, want) || e.Exec != "chromium %U" {
		t.Fatalf("entry %+v", e)
	}

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "chromium.desktop"), []byte(`[Desktop Entry]
Type=Application
Name=Chromium
Exec=chromium %U
Actions=new-private-window;
[Desktop Action new-private-window]
Name=New Incognito Window
Exec=chromium --incognito %U
`), 0o644)
	found, _ := (&DesktopSource{Dirs: []string{dir}}).Scan()
	// An app file adds one and renames another.
	files := []AppFile{{Path: "/x/chromium.toml", Extends: "chromium", Actions: []spec.AppAction{
		{ID: "kiosk", Name: "Kiosk", Command: []string{"chromium", "--kiosk"}},
		{ID: "new-private-window", Name: "Private", Command: []string{"chromium", "--incognito"}},
	}}}
	cat := Build(found, files, nil)
	app, _ := cat.Get("chromium")
	wantActions := []spec.AppAction{
		{ID: "new-private-window", Name: "Private", Command: []string{"chromium", "--incognito"}},
		{ID: "kiosk", Name: "Kiosk", Command: []string{"chromium", "--kiosk"}},
	}
	if len(cat.Problems) != 0 || !reflect.DeepEqual(app.Actions, wantActions) {
		t.Fatalf("actions %+v, problems %v", app.Actions, cat.Problems)
	}
	// Or replaces them all (Steam's desktop actions run outside its session).
	files[0].ReplaceActions = true
	files[0].Actions = files[0].Actions[:1]
	app, _ = Build(found, files, nil).Get("chromium")
	if !reflect.DeepEqual(app.Actions, []spec.AppAction{{ID: "kiosk", Name: "Kiosk", Command: []string{"chromium", "--kiosk"}}}) {
		t.Fatalf("replaced actions %+v", app.Actions)
	}

	for body, want := range map[string]string{
		`[[actions]]` + "\nid = \"x\"\nname = \"X\"":                                                                         "needs a name and a command",
		`[[actions]]` + "\nid = \"Bad ID\"\nname = \"X\"\ncommand = [\"x\"]":                                                 "action id",
		"[[actions]]\nid = \"a\"\nname = \"A\"\ncommand = [\"x\"]\n[[actions]]\nid = \"a\"\nname = \"B\"\ncommand = [\"y\"]": "defined twice",
	} {
		f, err := ParseAppFile("/x/a.toml", []byte("runner = { type = \"exec\", command = [\"a\"] }\n"+body))
		if err != nil {
			t.Fatal(err)
		}
		c := Build(nil, []AppFile{f}, nil)
		if len(c.Problems) != 1 || !strings.Contains(c.Problems[0].Error, want) {
			t.Errorf("%q: problems %v, want %q", body, c.Problems, want)
		}
	}
}

func TestFlatpakAppsFollowTheirSandbox(t *testing.T) {
	c := Build(nil, []AppFile{
		mustParse(t, "/x/kodi.toml", `runner = { type = "flatpak", app_id = "tv.kodi.Kodi" }`),
		mustParse(t, "/x/many.toml", "runner = { type = \"flatpak\", app_id = \"org.x.Many\" }\n[instance]\npolicy = \"multiple\""),
	}, nil)
	if len(c.Problems) != 0 {
		t.Fatal(c.Problems)
	}
	kodi, _ := c.Get("kodi")
	many, _ := c.Get("many")
	if kodi.Runner.Handoff != "FLATPAK_ID=tv.kodi.Kodi" || many.Runner.Handoff != "" {
		t.Fatalf("handoffs %q %q", kodi.Runner.Handoff, many.Runner.Handoff)
	}
}

func mustParse(t *testing.T, path, body string) AppFile {
	t.Helper()
	f, err := ParseAppFile(path, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return f
}
