// Package migrate moves a config folder from the layout of earlier hostd
// versions to the current one, once: hostd refuses to start on the old
// layout (Needed) and says to run `hostd -migrate`, which does it (Run). The
// old files are kept in .pre-0.2/ beside the config, and running it again
// changes nothing.
package migrate

import (
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
	return nil
}

// Run migrates the config folder and returns what it did, a line each.
func Run(configDir string) ([]string, error) {
	var did []string
	lines, err := rules(configDir)
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
