// Package update installs a new hostd binary next to the running one and
// switches to it. The layout (made by deploy/install.sh):
//
//	~/.local/lib/hostd/
//	  versions/<version>/hostd   every installed version
//	  current -> versions/<v>    what hostd.service runs
//	  previous                   "versions/<v>" to roll back to
//	  rollback.sh                run by hostd-rollback.service if the new
//	                             version fails to start; it never depends
//	                             on the hostd binary
//
// Switching never touches a running app: apps live in their own units and
// containers, and the new hostd adopts them.
package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Keep is how many versions stay installed (current and previous always
// among them).
const Keep = 3

// MaxSize is the largest binary accepted.
const MaxSize = 200 << 20

var reVersion = regexp.MustCompile(`^hostd ([A-Za-z0-9._+-]{1,64})$`)

// Lib is an installation directory.
type Lib struct{ Dir string }

// FromExecutable finds the installation the running binary belongs to:
// it lives in <lib>/versions/<v>/hostd. Outside such a layout (a
// development build) updating is not possible.
func FromExecutable() (Lib, error) {
	exe, err := os.Executable()
	if err != nil {
		return Lib{}, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return Lib{}, err
	}
	versions := filepath.Dir(filepath.Dir(exe))
	if filepath.Base(versions) != "versions" {
		return Lib{}, fmt.Errorf("%s was not installed by deploy/install.sh (not in a versions/ directory)", exe)
	}
	return Lib{Dir: filepath.Dir(versions)}, nil
}

// Current returns the current version's directory name ("versions/x").
func (l Lib) Current() (string, error) { return os.Readlink(filepath.Join(l.Dir, "current")) }

// Previous returns what a rollback would switch to, or "".
func (l Lib) Previous() string {
	b, _ := os.ReadFile(filepath.Join(l.Dir, "previous"))
	return strings.TrimSpace(string(b))
}

// RolledBackFrom returns the version a rollback replaced, if the last
// update was rolled back.
func (l Lib) RolledBackFrom() string {
	b, _ := os.ReadFile(filepath.Join(l.Dir, "rolled-back-from"))
	return strings.TrimSpace(string(b))
}

// Stage writes a binary into versions/<version>/hostd after checking it
// runs on this machine and reports its version. It returns the version.
func (l Lib) Stage(ctx context.Context, binary io.Reader) (string, error) {
	versions := filepath.Join(l.Dir, "versions")
	if err := os.MkdirAll(versions, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(versions, ".incoming-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(binary, MaxSize+1))
	if err == nil {
		err = tmp.Chmod(0o755)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > MaxSize {
		return "", fmt.Errorf("binary is larger than %d MB", MaxSize>>20)
	}
	if n == 0 {
		return "", errors.New("empty upload")
	}
	ver, err := versionOf(ctx, tmp.Name())
	if err != nil {
		return "", err
	}
	dir := filepath.Join(versions, ver)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, "hostd")); err != nil {
		return "", err
	}
	return ver, nil
}

// versionOf runs `binary -version`. A binary for another architecture, or
// not a hostd at all, fails here, before anything is switched.
func versionOf(ctx context.Context, binary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, "-version")
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("the new binary does not run on this machine: %w", err)
	}
	m := reVersion.FindStringSubmatch(strings.TrimSpace(out.String()))
	if m == nil {
		return "", fmt.Errorf("not a hostd binary (it printed %q)", strings.TrimSpace(out.String()))
	}
	return m[1], nil
}

// Switch makes versions/<version> current, records the old one as
// previous, and removes old versions beyond Keep. The symlink changes
// atomically (rename), so a crash never leaves no current version.
func (l Lib) Switch(version string) (previous string, err error) {
	target := filepath.Join("versions", version)
	if _, err := os.Stat(filepath.Join(l.Dir, target, "hostd")); err != nil {
		return "", fmt.Errorf("version %s is not installed", version)
	}
	cur, err := l.Current()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if cur == target {
		return l.Previous(), nil
	}
	if cur != "" {
		if err := writeAtomic(filepath.Join(l.Dir, "previous"), cur+"\n"); err != nil {
			return "", err
		}
	}
	tmp := filepath.Join(l.Dir, ".current.tmp")
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, filepath.Join(l.Dir, "current")); err != nil {
		return "", err
	}
	_ = os.Remove(filepath.Join(l.Dir, "rolled-back-from"))
	l.prune()
	return cur, nil
}

// prune keeps current, previous and the newest others, up to Keep.
func (l Lib) prune() {
	entries, err := os.ReadDir(filepath.Join(l.Dir, "versions"))
	if err != nil {
		return
	}
	cur, _ := l.Current()
	prev := l.Previous()
	type v struct {
		name string
		mod  time.Time
	}
	var others []v
	kept := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel := filepath.Join("versions", e.Name())
		if rel == cur || rel == prev {
			kept++
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		others = append(others, v{e.Name(), fi.ModTime()})
	}
	sort.Slice(others, func(i, j int) bool { return others[i].mod.After(others[j].mod) })
	for i, o := range others {
		if kept+i >= Keep {
			_ = os.RemoveAll(filepath.Join(l.Dir, "versions", o.name))
		}
	}
}

// Versions lists installed versions, newest first.
func (l Lib) Versions() []string {
	entries, _ := os.ReadDir(filepath.Join(l.Dir, "versions"))
	type v struct {
		name string
		mod  time.Time
	}
	var vs []v
	for _, e := range entries {
		if e.IsDir() {
			if fi, err := e.Info(); err == nil {
				vs = append(vs, v{e.Name(), fi.ModTime()})
			}
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].mod.After(vs[j].mod) })
	out := make([]string, len(vs))
	for i, x := range vs {
		out[i] = x.name
	}
	return out
}

func writeAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
