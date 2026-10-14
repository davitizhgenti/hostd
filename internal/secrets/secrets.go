// Package secrets is hostd's secret store: values that app environments
// name as {secret:<name>} and webhooks are signed with, kept out of app
// files (and any config repository). Each is a 0600 file in one directory,
// unencrypted: this protects against other local users and against
// committing them, not against anything running as the screen user or
// someone holding the disk.
package secrets

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

var name = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)

// ValidName reports whether s can be a secret's name.
func ValidName(s string) bool { return name.MatchString(s) }

// ErrNotExist is what Read and Remove return for a secret that is not there.
var ErrNotExist = fs.ErrNotExist

// DefaultDir is $XDG_CONFIG_HOME/hostd/secrets.
func DefaultDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "hostd", "secrets")
}

// Store is a directory of secrets. The zero Store (no directory) holds none.
type Store struct{ Dir string }

// Read returns a secret's value; a missing one is ErrNotExist.
func (s Store) Read(n string) ([]byte, error) {
	if !ValidName(n) || s.Dir == "" {
		return nil, ErrNotExist
	}
	return os.ReadFile(filepath.Join(s.Dir, n))
}

// Write stores a secret, replacing any, atomically.
func (s Store) Write(n string, value []byte) error {
	if !ValidName(n) || s.Dir == "" {
		return errors.New("no such secret store or name")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".tmp-*") // 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.Dir, n))
}

// Remove deletes a secret; a missing one is ErrNotExist.
func (s Store) Remove(n string) error {
	if !ValidName(n) || s.Dir == "" {
		return ErrNotExist
	}
	return os.Remove(filepath.Join(s.Dir, n))
}

// Names lists the secrets' names, never their values.
func (s Store) Names() ([]string, error) {
	out := []string{}
	if s.Dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && ValidName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
