package apps

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// DesktopEntry is the part of a .desktop file the catalog uses: keys of the
// [Desktop Entry] group, unlocalized.
type DesktopEntry struct {
	Type       string
	Name       string
	Exec       string
	TryExec    string
	Icon       string
	NoDisplay  bool
	Hidden     bool
	Terminal   bool
	Categories []string
	OnlyShowIn []string
	NotShowIn  []string
	Flatpak    string // X-Flatpak: the Flatpak app ID of an exported entry
}

// ParseDesktopEntry reads a .desktop file. It reads only the [Desktop
// Entry] group, ignores localized keys (Name[de]) and comments, and
// unescapes values as the spec says.
func ParseDesktopEntry(data []byte) (DesktopEntry, error) {
	var e DesktopEntry
	inEntry, sawEntry := false, false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			sawEntry = sawEntry || inEntry
			continue
		}
		if !inEntry {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if strings.Contains(key, "[") {
			continue // localized
		}
		switch key {
		case "Type":
			e.Type = value
		case "Name":
			e.Name = unescapeValue(value)
		case "Exec":
			e.Exec = unescapeValue(value)
		case "TryExec":
			e.TryExec = unescapeValue(value)
		case "Icon":
			e.Icon = unescapeValue(value)
		case "NoDisplay":
			e.NoDisplay = value == "true"
		case "Hidden":
			e.Hidden = value == "true"
		case "Terminal":
			e.Terminal = value == "true"
		case "Categories":
			e.Categories = splitList(value)
		case "OnlyShowIn":
			e.OnlyShowIn = splitList(value)
		case "NotShowIn":
			e.NotShowIn = splitList(value)
		case "X-Flatpak":
			e.Flatpak = value
		}
	}
	if err := sc.Err(); err != nil {
		return e, err
	}
	if !sawEntry {
		return e, errors.New("no [Desktop Entry] group")
	}
	return e, nil
}

// unescapeValue applies the spec's escapes for string values: \s \n \t \r \\.
func unescapeValue(v string) string {
	if !strings.Contains(v, `\`) {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+1 == len(v) {
			b.WriteByte(v[i])
			continue
		}
		i++
		switch v[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default: // not a value escape: keep it for Exec's own quoting rules
			b.WriteByte('\\')
			b.WriteByte(v[i])
		}
	}
	return b.String()
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ExecArgs splits an Exec value into argv: arguments are separated by
// spaces, double quotes group them (with \" \` \$ \\ escaped inside), and
// field codes (%f %U %i ...) are dropped, since hostd launches apps with no
// files or URLs. %% is a literal percent sign.
func ExecArgs(exec string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inArg, quoted := false, false
	for i := 0; i < len(exec); i++ {
		c := exec[i]
		switch {
		case quoted && c == '\\' && i+1 < len(exec) && strings.IndexByte("\"`$\\", exec[i+1]) >= 0:
			i++
			cur.WriteByte(exec[i])
		case c == '"':
			quoted = !quoted
			inArg = true
		case !quoted && (c == ' ' || c == '\t'):
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteByte(c)
			inArg = true
		}
	}
	if quoted {
		return nil, errors.New("unterminated quote in Exec")
	}
	if inArg {
		args = append(args, cur.String())
	}
	var out []string
	for _, a := range args {
		// An argument with a field code stands for a file or URL hostd
		// does not pass ("--file=%f"), so it goes entirely.
		a, hadCode := stripFieldCodes(a)
		if a != "" && !hadCode {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("the Exec key has no command")
	}
	return out, nil
}

// stripFieldCodes turns %% into % and reports whether arg holds any other
// field code (%f %F %u %U %i %c %k and the deprecated ones).
func stripFieldCodes(arg string) (string, bool) {
	if !strings.Contains(arg, "%") {
		return arg, false
	}
	var b strings.Builder
	had := false
	for i := 0; i < len(arg); i++ {
		if arg[i] != '%' || i+1 == len(arg) {
			b.WriteByte(arg[i])
			continue
		}
		i++
		if arg[i] == '%' {
			b.WriteByte('%')
		} else {
			had = true
		}
	}
	return b.String(), had
}

// hiddenCategories are hidden by default: settings panels, terminals and
// system tools nobody needs on a TV. A file can show one again with
// hidden = false.
var hiddenCategories = map[string]bool{
	"Settings": true, "System": true, "TerminalEmulator": true, "ConsoleOnly": true,
	"PackageManager": true, "Monitor": true, "X-GNOME-Settings-Panel": true, "DesktopSettings": true,
}

// DesktopSource discovers apps from .desktop files in the XDG data
// directories.
type DesktopSource struct {
	// Dirs to scan, lowest priority first: an entry in a later directory
	// replaces one with the same ID from an earlier one.
	Dirs []string
	// Desktop is the current desktop name for OnlyShowIn/NotShowIn
	// (default "sway").
	Desktop string
	// LookPath finds TryExec binaries; defaults to exec.LookPath.
	LookPath func(string) (string, error)
}

// DefaultDesktopDirs returns <dir>/applications for XDG_DATA_DIRS (lowest
// priority first) and XDG_DATA_HOME last.
func DefaultDesktopDirs() []string {
	home := os.Getenv("XDG_DATA_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".local", "share")
		}
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		// The default, plus Flatpak's exports: a session's XDG_DATA_DIRS
		// has them (Flatpak's profile script adds them), but a service's
		// environment has no XDG_DATA_DIRS at all. User installs rank
		// above system ones, both above /usr/share.
		dataDirs = "/usr/local/share:/usr/share"
		if home != "" {
			dataDirs = filepath.Join(home, "flatpak", "exports", "share") + ":/var/lib/flatpak/exports/share:" + dataDirs
		}
	}
	parts := strings.Split(dataDirs, ":")
	var dirs []string
	for i := len(parts) - 1; i >= 0; i-- { // the spec lists most important first
		if parts[i] != "" {
			dirs = append(dirs, filepath.Join(parts[i], "applications"))
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "applications"))
	}
	return dirs
}

// Scan reads every .desktop file. Files that are not applications, are
// deleted (Hidden), or whose TryExec is missing are left out quietly;
// broken files are reported as problems.
func (s *DesktopSource) Scan() ([]App, []Problem) {
	desktop := s.Desktop
	if desktop == "" {
		desktop = "sway"
	}
	lookPath := s.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	byFileID := map[string]App{} // desktop file ID -> app; later dirs win
	var problems []Problem
	for _, dir := range s.Dirs {
		entries := map[string]string{} // desktop file ID -> path
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil //nolint:nilerr // an unreadable or missing entry adds nothing; keep walking
			}
			if d.IsDir() || !strings.HasSuffix(path, ".desktop") {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			// subdir/foo.desktop has the desktop file ID subdir-foo.desktop
			entries[strings.ReplaceAll(strings.TrimSuffix(rel, ".desktop"), string(filepath.Separator), "-")] = path
			return nil
		})
		for fileID, path := range entries {
			data, err := os.ReadFile(path)
			if err != nil {
				problems = append(problems, Problem{File: path, Error: err.Error()})
				continue
			}
			e, err := ParseDesktopEntry(data)
			if err != nil {
				problems = append(problems, Problem{File: path, Error: err.Error()})
				continue
			}
			if e.Hidden { // "deleted": also removes the same ID from lower dirs
				delete(byFileID, fileID)
				continue
			}
			if e.Type != "Application" {
				continue
			}
			if e.TryExec != "" {
				if _, err := lookPath(e.TryExec); err != nil {
					delete(byFileID, fileID)
					continue
				}
			}
			argv, err := ExecArgs(e.Exec)
			if err != nil {
				problems = append(problems, Problem{File: path, Error: err.Error()})
				continue
			}
			app := App{
				Name: e.Name, Icon: e.Icon, Source: "desktop", Files: []string{path},
				Runner: Runner{Type: RunnerExec, Command: argv},
				Window: Window{Fullscreen: true},
				Hidden: e.NoDisplay || e.Terminal || !shownIn(e, desktop) || hiddenByCategory(e.Categories),
			}
			if e.Flatpak != "" {
				app.Runner = Runner{Type: RunnerFlatpak, AppID: e.Flatpak}
			}

			byFileID[fileID] = app
		}
	}
	return assignIDs(byFileID), problems
}

func shownIn(e DesktopEntry, desktop string) bool {
	has := func(list []string) bool {
		for _, d := range list {
			if strings.EqualFold(d, desktop) {
				return true
			}
		}
		return false
	}
	if len(e.OnlyShowIn) > 0 && !has(e.OnlyShowIn) {
		return false
	}
	return !has(e.NotShowIn)
}

func hiddenByCategory(cats []string) bool {
	for _, c := range cats {
		if hiddenCategories[c] {
			return true
		}
	}
	return false
}

// assignIDs gives each app a readable ID: the desktop file ID lowercased
// ("firefox-esr"), or for reverse-DNS names its last part
// ("org.kde.kcalc" -> "kcalc") when that is unique.
func assignIDs(byFileID map[string]App) []App {
	fileIDs := make([]string, 0, len(byFileID))
	for id := range byFileID {
		fileIDs = append(fileIDs, id)
	}
	sort.Strings(fileIDs)

	short := map[string]int{}
	for _, fid := range fileIDs {
		short[shortID(fid)]++
	}
	var apps []App
	for _, fid := range fileIDs {
		app := byFileID[fid]
		id := shortID(fid)
		if short[id] > 1 {
			id = sanitizeID(fid)
		}
		app.ID = id
		if id != sanitizeID(fid) {
			app.Aliases = []string{sanitizeID(fid)}
		}
		apps = append(apps, app)
	}
	return apps
}

func shortID(fileID string) string {
	parts := strings.Split(fileID, ".")
	if len(parts) >= 3 { // reverse DNS: org.example.App
		return sanitizeID(parts[len(parts)-1])
	}
	return sanitizeID(fileID)
}

func sanitizeID(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), ".-_")
	if out == "" {
		out = "app"
	}
	return out
}
