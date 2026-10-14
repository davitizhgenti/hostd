package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"db-password": true, "a": true, "x.y_z-1": true,
		"": false, "Upper": false, "-lead": false, "../etc": false, "a/b": false, "has space": false,
	} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v", name, got)
		}
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if ValidName(string(long)) || !ValidName(string(long[:64])) {
		t.Error("the length limit is 64")
	}
}

func TestStore(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "secrets")} // not made yet
	if names, err := s.Names(); err != nil || len(names) != 0 {
		t.Fatalf("empty: %v %v", names, err)
	}
	if _, err := s.Read("db"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("a missing secret: %v", err)
	}
	if err := s.Write("db", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("db", []byte("two")); err != nil { // replaces
		t.Fatal(err)
	}
	_ = s.Write("api", []byte("k"))
	if b, err := s.Read("db"); err != nil || string(b) != "two" {
		t.Fatalf("read %q %v", b, err)
	}
	if fi, err := os.Stat(filepath.Join(s.Dir, "db")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	if fi, _ := os.Stat(s.Dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", fi.Mode().Perm())
	}
	// Strays (a temp file, a directory, a bad name) are not secrets.
	_ = os.WriteFile(filepath.Join(s.Dir, ".tmp-1"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(s.Dir, "Bad Name"), nil, 0o600)
	_ = os.Mkdir(filepath.Join(s.Dir, "dir"), 0o700)
	if names, _ := s.Names(); !reflect.DeepEqual(names, []string{"api", "db"}) {
		t.Fatalf("names %v", names)
	}
	if err := s.Remove("db"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("db"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("removing twice: %v", err)
	}
	// A name that is not one never reaches the file system.
	if _, err := s.Read("../x"); !errors.Is(err, ErrNotExist) || s.Write("../x", nil) == nil || !errors.Is(s.Remove("../x"), ErrNotExist) {
		t.Fatal("a path got through")
	}
	if (Store{}).Write("a", nil) == nil {
		t.Fatal("wrote with no directory")
	}
}
