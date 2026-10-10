package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
)

func TestPushKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")
	m := New(Options{Root: t.TempDir(), AuthorizedKeys: path, GitShell: "/usr/local/bin/hostctl git-shell"})
	const mine = "ssh-ed25519 a2V5LW1pbmUtMDAwMQ== me@laptop"
	const laptop = "ssh-ed25519 a2V5LWxhcHRvcC0wMQ== push@laptop"
	const ci = "ssh-ed25519 a2V5LWNpLTAwMDAwMQ== ci"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(path, []byte(mine+"\n"), 0o600)

	if _, err := m.authorize("laptop", laptop); err != nil {
		t.Fatal(err)
	}
	if _, err := m.authorize("ci", ci); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	want := mine + "\n" +
		`command="/usr/local/bin/hostctl git-shell",restrict ssh-ed25519 a2V5LWxhcHRvcC0wMQ== hostd-push:laptop` + "\n" +
		`command="/usr/local/bin/hostctl git-shell",restrict ssh-ed25519 a2V5LWNpLTAwMDAwMQ== hostd-push:ci` + "\n"
	if string(b) != want {
		t.Fatalf("authorized_keys:\n%s\nwant:\n%s", b, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}

	// A new key for a name replaces the old one.
	if _, err := m.authorize("ci", "ssh-ed25519 a2V5LW5ld2NpLTAwMQ=="); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "a2V5LWNpLTAwMDAwMQ==") || strings.Count(string(b), "hostd-push:ci") != 1 {
		t.Fatalf("replace:\n%s", b)
	}
	// A key that already logs in without restrictions is refused.
	if _, err := m.authorize("me", mine); sdk.CodeOf(err) != sdk.CodePreconditionFailed {
		t.Fatalf("unrestricted key: %v", err)
	}
	for _, bad := range []string{"", "hello", "ssh-ed25519 not-base64!", "ssh-ed25519 AAAA\nssh-rsa AAAA", `ssh-ed25519 AAAA" x`} {
		if _, err := m.authorize("x", bad); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := m.authorize("Bad Name", laptop); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("bad name: %v", err)
	}

	keys, _ := m.pushKeys()
	if len(keys) != 2 || keys[0].Name != "laptop" || keys[1].Name != "ci" || keys[0].Type != "ssh-ed25519" {
		t.Fatalf("keys %+v", keys)
	}
	if err := m.revoke("laptop"); err != nil {
		t.Fatal(err)
	}
	if err := m.revoke("laptop"); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("revoke twice: %v", err)
	}
	if b, _ := os.ReadFile(path); !strings.HasPrefix(string(b), mine+"\n") || strings.Contains(string(b), "hostd-push:laptop") {
		t.Fatalf("after revoke:\n%s", b)
	}
}
