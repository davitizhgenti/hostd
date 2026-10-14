package deploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestQuietOutput(t *testing.T) {
	var b bytes.Buffer
	o := NewOutput(&b, false)
	o.Info("up to date (edge)") // outside any step: always shown
	o.Step("Checking the system")
	o.Info("Debian 13 (amd64)")
	o.Step("Installing packages")
	o.Info("installing: foo")
	o.Writer().Write([]byte("Setting up foo ...\n"))
	o.Changed("installed foo")
	o.Step("Setting up the screen user")
	o.Info("user exists")
	o.Warn("no SSH keys to copy")
	o.Done()
	o.Info("hostd v1 is running.")
	o.Close()
	want := "    up to date (edge)\n" + "\n\033[1m==> Installing packages\033[0m\n    installing: foo\nSetting up foo ...\n    installed foo\n" +
		"\n\033[1m==> Setting up the screen user\033[0m\n    user exists\n    no SSH keys to copy\n" +
		"\n\033[1m==> Done\033[0m\n    hostd v1 is running.\n" +
		"\n(1 steps already up to date; hostd-setup update --verbose lists them)\n"
	if b.String() != want {
		t.Fatalf("quiet:\n%q\nwant\n%q", b.String(), want)
	}
	if !o.AnyChanged() {
		t.Fatal("a change was not noted")
	}

	b.Reset()
	o = NewOutput(&b, true)
	o.Step("Checking the system")
	o.Info("Debian 13 (amd64)")
	o.Done()
	o.Close()
	if !strings.Contains(b.String(), "==> Checking the system\033[0m\n    Debian 13 (amd64)\n") || strings.Contains(b.String(), "already up to date") || o.AnyChanged() {
		t.Fatalf("verbose:\n%s", b.String())
	}
}

func testSetup(t *testing.T) (*Setup, *bytes.Buffer, *[]string) {
	t.Helper()
	var b bytes.Buffer
	var owners []string
	return &Setup{Root: t.TempDir(), Out: NewOutput(&b, true), Chown: func(path, owner string) error {
		owners = append(owners, owner)
		return nil
	}}, &b, &owners
}

func TestInstallFile(t *testing.T) {
	s, out, owners := testSetup(t)
	// New, with missing parents: written, the parents given to the owner.
	if ch, err := s.installFile([]byte("a\n"), "/home/screen/.config/sway/config", 0o644, "screen"); err != nil || !ch {
		t.Fatalf("new file: %v %v", ch, err)
	}
	if len(*owners) != 5 { // home, screen, .config, sway, the file
		t.Fatalf("owners %v", *owners)
	}
	// The same again: nothing.
	if ch, err := s.installFile([]byte("a\n"), "/home/screen/.config/sway/config", 0o644, "screen"); err != nil || ch {
		t.Fatalf("same file: %v %v", ch, err)
	}
	// Another mode: rewritten.
	if ch, _ := s.installFile([]byte("a\n"), "/home/screen/.config/sway/config", 0o600, "screen"); !ch {
		t.Fatal("a new mode did not count")
	}
	if fi, _ := os.Stat(s.path("/home/screen/.config/sway/config")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// A file hostd did not write is kept as .orig, once.
	os.MkdirAll(s.path("/etc/greetd"), 0o755)
	os.WriteFile(s.path("/etc/greetd/config.toml"), []byte("theirs\n"), 0o644)
	s.installFile([]byte("# "+managedMark+"\nv1\n"), "/etc/greetd/config.toml", 0o644, "root")
	s.installFile([]byte("# "+managedMark+"\nv2\n"), "/etc/greetd/config.toml", 0o644, "root")
	if b, _ := os.ReadFile(s.path("/etc/greetd/config.toml.orig")); string(b) != "theirs\n" {
		t.Fatalf(".orig = %q", b)
	}
	// In /usr/local the file is hostd's: no .orig.
	os.MkdirAll(s.path("/usr/local/bin"), 0o755)
	os.WriteFile(s.path("/usr/local/bin/hostctl"), []byte("old"), 0o755)
	s.installFile([]byte("new"), "/usr/local/bin/hostctl", 0o755, "root")
	if _, err := os.Stat(s.path("/usr/local/bin/hostctl.orig")); err == nil {
		t.Fatal("a backup of hostd's own file")
	}
	if !strings.Contains(out.String(), "wrote /etc/greetd/config.toml") {
		t.Fatalf("output %s", out.String())
	}
}

// signedRelease makes a build in a directory: files, SHA256SUMS listing
// them, signed with a new key it returns (PEM).
func signedRelease(t *testing.T, files map[string]string) (string, []byte, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(pub)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	dir := t.TempDir()
	var sums strings.Builder
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		h := sha256.Sum256([]byte(body))
		sums.WriteString(hex.EncodeToString(h[:]) + "  " + name + "\n")
	}
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0o644)
	os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), ed25519.Sign(priv, []byte(sums.String())), 0o644)
	return dir, pemKey, priv
}

func TestVerify(t *testing.T) {
	dir, key, priv := signedRelease(t, map[string]string{"hostd-linux-amd64": "binary", "hostd-setup-linux-amd64": "setup"})
	if err := Verify(dir, key, "hostd-linux-amd64", "hostd-setup-linux-amd64"); err != nil {
		t.Fatal(err)
	}
	if !Lists(dir, "hostd-linux-amd64") || Lists(dir, "hostd-docs.tar.gz") {
		t.Fatal("Lists")
	}
	// A file that does not match, one not listed, another key, a changed
	// SHA256SUMS: each refused.
	os.WriteFile(filepath.Join(dir, "hostd-linux-amd64"), []byte("evil"), 0o644)
	if err := Verify(dir, key, "hostd-linux-amd64"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered file: %v", err)
	}
	if err := Verify(dir, key, "other"); err == nil || !strings.Contains(err.Error(), "does not list") {
		t.Fatalf("unlisted: %v", err)
	}
	_, other, _ := signedRelease(t, nil)
	if err := Verify(dir, other); err == nil || !strings.Contains(err.Error(), "not signed with hostd's key") {
		t.Fatalf("other key: %v", err)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), append(sums, []byte("00  evil\n")...), 0o644)
	if err := Verify(dir, key); err == nil {
		t.Fatal("a changed SHA256SUMS was accepted")
	}
	_ = priv
	// hostd's own key parses.
	if _, err := parseKey(releaseKey); err != nil {
		t.Fatal(err)
	}
}

func TestFetch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/flaky":
			if calls.Add(1) < 3 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.Write([]byte("at last"))
		case "/gone":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.Write([]byte("file " + r.URL.Path))
		}
	}))
	defer srv.Close()
	r := Release{URL: srv.URL, Retries: 3, Wait: time.Millisecond}
	dir := t.TempDir()
	if err := r.Fetch(context.Background(), dir, "a", "flaky"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "flaky")); string(b) != "at last" {
		t.Fatalf("flaky = %q", b)
	}
	if err := r.Fetch(context.Background(), dir, "gone"); err == nil || !strings.Contains(err.Error(), "no gone in the build") {
		t.Fatalf("404: %v", err)
	}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "x"), []byte("local"), 0o644)
	if err := (Release{URL: "file://" + src}).Fetch(context.Background(), dir, "x"); err != nil {
		t.Fatal(err)
	}
}
