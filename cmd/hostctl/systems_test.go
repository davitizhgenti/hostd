package main

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestMachineSystems(t *testing.T) {
	dir := t.TempDir()
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	os.MkdirAll(filepath.Join(dir, me.Uid), 0o755)
	os.WriteFile(filepath.Join(dir, me.Uid, "hostd.sock"), nil, 0o644)
	users := filepath.Join(dir, "users")
	os.WriteFile(users, []byte("# comment\nalice 7301\nbob\n"), 0o644)
	holds := seatHolder() == me.Username
	got := machineSystems(dir, users)
	want := map[string]hostSystem{me.Username: {Name: me.Username, Port: 7300, Running: true, Holds: holds}, "alice": {Name: "alice", Port: 7301}, "bob": {Name: "bob"}}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	for _, g := range got {
		if g != want[g.Name] {
			t.Errorf("%+v, want %+v", g, want[g.Name])
		}
	}
	if got[0].Port != 0 || got[2].Name != "alice" {
		t.Errorf("order %+v", got)
	}
	var out bytes.Buffer
	a := &app{stdout: &out}
	if err := a.systemsCommand().RunE(nil, nil); err != nil {
		t.Fatal(err)
	}
}
