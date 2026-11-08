// Package fsx has the file helpers hostd's modules share.
package fsx

import (
	"os"
	"path/filepath"
)

// WriteFile writes a file in one step: a reader (a folder's watcher, a
// person with an editor open) sees the old file or the new one, never half
// of one. The data goes to a temporary file in the same folder, named with
// a leading dot (which the app and resource folders skip), is synced, and is
// renamed over path. The folder is created (0755) if it is missing.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // after the rename there is nothing to remove
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
