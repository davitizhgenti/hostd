package deploy

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Setup is one run of the installer.
type Setup struct {
	Root string // "/" on a machine
	Run  Runner
	Out  *Output
	// Chown gives a path to a user (and its group). nil: the real one,
	// by name; tests record it instead.
	Chown func(path, owner string) error
}

// path is p under Root.
func (s *Setup) path(p string) string { return filepath.Join(s.Root, p) }

func (s *Setup) chown(path, owner string) error {
	if s.Chown != nil {
		return s.Chown(path, owner)
	}
	u, err := user.Lookup(owner)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return os.Lchown(path, uid, gid)
}

// managedMark is in every file hostd writes where a comment fits.
const managedMark = "Managed by hostd"

// hostdOwned reports whether hostd wrote the file at p (on the machine):
// it says so, or it is in /usr/local, where the system's packages never
// put files.
func (s *Setup) hostdOwned(p string) bool {
	if strings.HasPrefix(p, "/usr/local/") {
		return true
	}
	b, err := os.ReadFile(s.path(p))
	return err == nil && bytes.Contains(b, []byte(managedMark))
}

// installFile puts content at dest (a path on the machine) with mode and
// owner, unless it is there already, and says whether it changed. A file
// hostd did not write is kept as dest.orig the first time. Missing parent
// directories are made and given to owner too.
func (s *Setup) installFile(content []byte, dest string, mode fs.FileMode, owner string) (bool, error) {
	full := s.path(dest)
	if old, err := os.ReadFile(full); err == nil && bytes.Equal(old, content) {
		if fi, err := os.Stat(full); err == nil && fi.Mode().Perm() == mode {
			return false, nil
		}
	} else if err == nil {
		if _, err := os.Stat(full + ".orig"); errors.Is(err, fs.ErrNotExist) && !s.hostdOwned(dest) {
			if err := os.WriteFile(full+".orig", old, 0o644); err != nil {
				return false, err
			}
		}
	}
	if err := s.mkdirs(filepath.Dir(dest), owner); err != nil {
		return false, err
	}
	tmp := full + ".hostd-new"
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil { // WriteFile honours the umask
		return false, err
	}
	if err := s.chown(tmp, owner); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, full); err != nil {
		return false, err
	}
	s.Out.Changed("wrote %s", dest)
	return true, nil
}

// mkdirs makes dir (on the machine) and any missing parents, giving the
// ones it makes to owner.
func (s *Setup) mkdirs(dir, owner string) error {
	var missing []string
	for d := dir; d != "/" && d != "."; d = filepath.Dir(d) {
		if _, err := os.Stat(s.path(d)); err == nil {
			break
		}
		missing = append(missing, d)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(s.path(missing[i]), 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := s.chown(s.path(missing[i]), owner); err != nil {
			return err
		}
	}
	return nil
}
