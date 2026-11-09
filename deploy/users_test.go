package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installed brings a fake machine to the state after the first install.
func installed(t *testing.T) (*fakeMachine, *Setup, Options, *bytes.Buffer) {
	t.Helper()
	m := newFakeMachine(t)
	self := filepath.Join(t.TempDir(), "hostd-setup")
	os.WriteFile(self, []byte("the installer"), 0o755)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	o := Options{GPU: "other", From: buildDir(t), SudoUser: "admin", Self: self}
	if err := s.Install(context.Background(), o); err != nil {
		t.Fatalf("install: %v\n%s", err, out.String())
	}
	out.Reset()
	return m, s, o, &out
}

func TestAddAUser(t *testing.T) {
	m, s, o, out := installed(t)
	ctx := context.Background()
	if err := s.AddUser(ctx, o, "alice", 0); err != nil {
		t.Fatalf("add: %v\n%s", err, out.String())
	}
	if got := s.users(); len(got) != 1 || got[0] != (hostdUser{"alice", 7301}) {
		t.Fatalf("users = %+v", got)
	}
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(m.root, p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		return string(b)
	}
	if conf := read("home/alice/.config/hostd/hostd.toml"); !strings.Contains(conf, `listen = ":7301"`) {
		t.Errorf("alice's config:\n%s", conf)
	}
	// Her own hostd, in her own home; the first user's is untouched.
	if _, err := os.Readlink(filepath.Join(m.root, "home/alice/.local/lib/hostd/current")); err != nil {
		t.Errorf("hostd is not installed for alice: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "home/alice/.config/systemd/user/hostd.service")); err != nil {
		t.Errorf("no hostd service for alice: %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "home/screen/.config/hostd/hostd.toml")); err == nil {
		t.Error("the first user got a port setting")
	}
	if !strings.Contains(out.String(), "hostctl login <this machine>:7301") {
		t.Errorf("output:\n%s", out.String())
	}

	// Again: the port stays, nothing is overwritten (a person's edit stays).
	os.WriteFile(filepath.Join(m.root, "home/alice/.config/hostd/hostd.toml"), []byte("listen = \":9000\"\n"), 0o644)
	if err := s.AddUser(ctx, o, "alice", 0); err != nil {
		t.Fatal(err)
	}
	if got := s.users(); len(got) != 1 || got[0].Port != 7301 {
		t.Fatalf("users after adding again = %+v", got)
	}
	if conf := read("home/alice/.config/hostd/hostd.toml"); !strings.Contains(conf, "9000") {
		t.Errorf("a changed config was overwritten:\n%s", conf)
	}

	// A list the people can read, and the command that prints it.
	var list bytes.Buffer
	if err := s.ListUsers(&list); err != nil || list.String() != "alice\t:7301\n" {
		t.Fatalf("list = %q %v", list.String(), err)
	}
}

func TestAddAUserRefusals(t *testing.T) {
	_, s, o, _ := installed(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		user string
		port int
		want string
	}{
		"bad name":     {"Alice!", 0, "cannot be a user name"},
		"the first":    {"screen", 0, "first user"},
		"low port":     {"alice", 80, "1024-65535"},
		"first's port": {"alice", 7300, "first user's"},
	} {
		if err := s.AddUser(ctx, o, c.user, c.port); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if err := s.AddUser(ctx, o, "alice", 7400); err != nil {
		t.Fatal(err)
	}
	// Another user cannot take her port; the next free one skips it.
	if err := s.AddUser(ctx, o, "bob", 7400); err == nil || !strings.Contains(err.Error(), "alice") {
		t.Errorf("a port already taken: %v", err)
	}
	if got := freePort([]hostdUser{{"a", 7301}, {"b", 7302}, {"c", 7304}}); got != 7303 {
		t.Errorf("freePort = %d", got)
	}
}

func TestAnUpdateKeepsEveryUsersHostdCurrent(t *testing.T) {
	m, s, o, out := installed(t)
	ctx := context.Background()
	if err := s.AddUser(ctx, o, "alice", 0); err != nil {
		t.Fatal(err)
	}
	// A new hostd: remove alice's installed version; the update brings it back.
	if err := os.RemoveAll(filepath.Join(m.root, "home/alice/.local/lib/hostd")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	o.Update = &UpdateOptions{}
	if err := s.Install(ctx, o); err != nil {
		t.Fatalf("update: %v\n%s", err, out.String())
	}
	if _, err := os.Readlink(filepath.Join(m.root, "home/alice/.local/lib/hostd/current")); err != nil {
		t.Errorf("alice's hostd was not brought back: %v\n%s", err, out.String())
	}
}

func TestRemoveAUser(t *testing.T) {
	m, s, o, _ := installed(t)
	ctx := context.Background()
	if err := s.AddUser(ctx, o, "alice", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveUser(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if len(s.users()) != 0 {
		t.Fatalf("still listed: %+v", s.users())
	}
	if _, err := os.Stat(filepath.Join(m.root, "home/alice/.config/hostd/hostd.toml")); err != nil {
		t.Error("her data went without --purge")
	}
	ran := strings.Join(m.ran, "\n")
	if !strings.Contains(ran, "systemctl --user --machine=alice@ disable --now hostd.service") {
		t.Errorf("her hostd was not stopped:\n%s", ran)
	}
	if err := s.RemoveUser(ctx, "alice", false); err == nil {
		t.Error("removing twice is not an error")
	}
	// With purge her hostd data goes, not her.
	if err := s.AddUser(ctx, o, "alice", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveUser(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "home/alice/.config/hostd")); err == nil {
		t.Error("her hostd config is still there after --purge")
	}
	if _, err := os.Stat(filepath.Join(m.root, "home/alice")); err != nil {
		t.Error("her home went")
	}
}
