// Package migrate moves a config folder from the layout of earlier hostd
// versions to the current one, once: hostd refuses to start on the old
// layout (Needed) and says to run `hostd -migrate`, which does it (Run). The
// old files are kept in .pre-0.2/ beside the config, and running it again
// changes nothing.
package migrate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/davitizhgenti/hostd/internal/fsx"
)

// BackupDir is where the old files go, inside the config folder.
const BackupDir = ".pre-0.2"

// Needed returns an error naming what is still in an earlier layout, or nil.
func Needed(configDir string) error {
	if _, err := os.Stat(filepath.Join(configDir, "rules.toml")); err == nil {
		return fmt.Errorf("%s is the format before 0.2 (one file for all rules; now one file each in rules/): run `hostd -migrate`",
			filepath.Join(configDir, "rules.toml"))
	}
	if old := legacyScripts(configDir); len(old) > 0 {
		return fmt.Errorf("the script %s has no script file (scripts are now described by scripts/<name>.toml): run `hostd -migrate`",
			filepath.Join(configDir, "scripts", old[0]))
	}
	return nil
}

// Run migrates the config folder and returns what it did, a line each.
func Run(configDir string) ([]string, error) {
	var did []string
	lines, err := rules(configDir)
	did = append(did, lines...)
	if err != nil {
		return did, err
	}
	lines, err = scripts(configDir)
	did = append(did, lines...)
	return did, err
}

// legacyRule is a rule of the old rules.toml.
type legacyRule struct {
	Name     string         `toml:"name"`
	On       string         `toml:"on"`
	If       map[string]any `toml:"if"`
	Do       map[string]any `toml:"do"`
	Cooldown string         `toml:"cooldown"`
}

// ruleFile is a rule as its own file says it.
type ruleFile struct {
	On       string         `toml:"on"`
	If       map[string]any `toml:"if,omitempty"`
	Do       map[string]any `toml:"do"`
	Cooldown string         `toml:"cooldown,omitempty"`
}

var notName = regexp.MustCompile(`[^a-z0-9._-]+`)

// slug is a rule's name as a file name: lowercase, with letters, digits,
// '.', '_' and '-' only.
func slug(name string) string {
	s := notName.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-._")
	if len(s) > 64 {
		s = strings.Trim(s[:64], "-._")
	}
	if s == "" {
		s = "rule"
	}
	return s
}

// rules splits rules.toml into rules/<name>.toml, one file per rule.
func rules(configDir string) ([]string, error) {
	old := filepath.Join(configDir, "rules.toml")
	data, err := os.ReadFile(old)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Rule []legacyRule `toml:"rule"`
	}
	if _, err := toml.Decode(string(data), &doc); err != nil {
		return nil, fmt.Errorf("%s: %w (fix it, or move it away, then run again)", old, err)
	}
	dir := filepath.Join(configDir, "rules")
	var did []string
	taken := map[string]bool{}
	for _, r := range doc.Rule {
		if r.Name == "" {
			return nil, fmt.Errorf("%s: a rule has no name (fix it, or move the file away, then run again)", old)
		}
		name := slug(r.Name)
		for n := 2; taken[name] || exists(filepath.Join(dir, name+".toml")); n++ {
			name = fmt.Sprintf("%s-%d", slug(r.Name), n)
		}
		taken[name] = true
		var buf bytes.Buffer
		if name != r.Name {
			fmt.Fprintf(&buf, "# Was the rule %q in rules.toml.\n", r.Name)
		}
		if err := toml.NewEncoder(&buf).Encode(ruleFile{On: r.On, If: r.If, Do: r.Do, Cooldown: r.Cooldown}); err != nil {
			return nil, err
		}
		if err := fsx.WriteFile(filepath.Join(dir, name+".toml"), buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
		did = append(did, fmt.Sprintf("rules.toml: rule %q is now rules/%s.toml", r.Name, name))
	}
	backup, err := keep(configDir, old)
	if err != nil {
		return did, err
	}
	did = append(did, fmt.Sprintf("rules.toml kept as %s", backup))
	return did, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// keep moves a file into the backup folder, under a name that is free.
func keep(configDir, path string) (string, error) {
	dir := filepath.Join(configDir, BackupDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, filepath.Base(path))
	for n := 2; exists(dest); n++ {
		dest = filepath.Join(dir, fmt.Sprintf("%s.%d", filepath.Base(path), n))
	}
	return dest, os.Rename(path, dest)
}

// scriptName is what a script may be called (the file name, as for every
// resource): the same rule the core applies.
var scriptName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// scriptFile is a script as its own file says it.
type scriptFile struct {
	Command []string `toml:"command"`
	Scopes  []string `toml:"scopes,omitempty"`
	Timeout string   `toml:"timeout,omitempty"`
}

// oldSettings is what an earlier version's sidecar file held (and what a
// new script file has too, besides command and run).
type oldSettings struct {
	Command []string `toml:"command"`
	Run     string   `toml:"run"`
	Scopes  []string `toml:"scopes"`
	Timeout string   `toml:"timeout"`
}

// legacyScripts lists the executables in scripts/ that no script file
// describes yet, by file name. A name a script cannot have is not listed:
// migrating cannot fix it, and refusing to start for it would be a trap.
func legacyScripts(configDir string) []string {
	dir := filepath.Join(configDir, "scripts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".toml") || !scriptName.MatchString(name) {
			continue
		}
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		var s oldSettings
		if b, err := os.ReadFile(filepath.Join(dir, name+".toml")); err == nil {
			if _, err := toml.Decode(string(b), &s); err == nil && (len(s.Command) > 0 || s.Run != "") {
				continue // already described
			}
		}
		out = append(out, name)
	}
	return out
}

// scripts writes a script file for every executable in scripts/: command
// "./<name>", the scopes and timeout from its sidecar <name>.toml or its
// "# hostd-scopes:" header. The script keeps its name, so rules that name it
// go on working; an old sidecar is kept in the backup folder.
func scripts(configDir string) ([]string, error) {
	var did []string
	dir := filepath.Join(configDir, "scripts")
	for _, name := range legacyScripts(configDir) {
		var s oldSettings
		sidecar := filepath.Join(dir, name+".toml")
		if b, err := os.ReadFile(sidecar); err == nil {
			if _, err := toml.Decode(string(b), &s); err != nil {
				return did, fmt.Errorf("%s: %w (fix it, or move it away, then run again)", sidecar, err)
			}
			if _, err := keep(configDir, sidecar); err != nil {
				return did, err
			}
		} else {
			s.Scopes = headerScopes(filepath.Join(dir, name))
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(scriptFile{Command: []string{"./" + name}, Scopes: s.Scopes, Timeout: s.Timeout}); err != nil {
			return did, err
		}
		if err := fsx.WriteFile(sidecar, buf.Bytes(), 0o644); err != nil {
			return did, err
		}
		did = append(did, fmt.Sprintf("scripts/%s: now described by scripts/%s.toml", name, name))
	}
	return did, nil
}

// headerScopes reads the "# hostd-scopes: audio display" line near the top
// of a script, the way earlier versions did.
func headerScopes(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 0; n < 20 && sc.Scan(); n++ {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "# hostd-scopes:"); ok {
			return strings.Fields(rest)
		}
	}
	return nil
}
