package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileReplacesInOneStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "a.toml") // the folder is made
	if err := WriteFile(path, []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("content = %q", b)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
}

func TestWriteFileFailsWithoutLeavingFiles(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(blocker, "x"), []byte("a"), 0o644); err == nil {
		t.Fatal("writing under a file should fail")
	}
}
