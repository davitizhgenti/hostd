package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeats(t *testing.T) {
	m := newFakeMachine(t)
	var out bytes.Buffer
	s := &Setup{Root: m.root, Run: m, Out: NewOutput(&out, true), Chown: func(string, string) error { return nil }}
	ctx := context.Background()
	m.alice = true
	os.MkdirAll(filepath.Join(m.root, "etc/greetd"), 0o755)
	os.WriteFile(filepath.Join(m.root, "etc/greetd/config.toml"), []byte("[terminal]\nvt = 7\n\n[initial_session]\ncommand = \"sway\"\nuser = \"screen\"\n"), 0o644)
	if err := s.AddSeat(ctx, "screen", 7); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "etc/systemd/system/hostd-session-screen.service")); err == nil {
		t.Error("a unit for greetd's own user")
	}
	if err := s.AddSeat(ctx, "alice", 7); err == nil || !strings.Contains(err.Error(), "VT 7 is screen's") {
		t.Fatalf("two users on a VT: %v", err)
	}
	if err := s.AddSeat(ctx, "alice", 99); err == nil {
		t.Fatal("VT 99")
	}
	if err := s.AddSeat(ctx, "alice", 8); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	b, _ := os.ReadFile(filepath.Join(m.root, "etc/systemd/system/hostd-session-alice.service"))
	for _, want := range []string{"User=alice", "TTYPath=/dev/tty8", "XDG_VTNR=8", "ExecStart=/usr/bin/sway"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("unit lacks %q:\n%s", want, b)
		}
	}
	if m.didRun("systemctl enable") || m.didRun("systemctl start hostd-session") {
		t.Errorf("a session unit must not start at boot, nor now: %q", m.ran)
	}
	if strings.Contains(string(b), "[Install]") || !strings.Contains(string(b), "greetd.service") {
		t.Errorf("the unit must start after greetd and only on demand:\n%s", b)
	}
	if got := s.seats(); len(got) != 2 || got[0] != (seatUser{"screen", 7}) || got[1] != (seatUser{"alice", 8}) {
		t.Fatalf("seats %+v", got)
	}
	var list bytes.Buffer
	if err := s.ListSeats(&list); err != nil || !strings.Contains(list.String(), "alice") {
		t.Errorf("%v %s", err, list.String())
	}
	if err := s.RemoveSeat(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "etc/systemd/system/hostd-session-alice.service")); err == nil {
		t.Error("the unit stays")
	}
	if err := s.RemoveSeat(ctx, "alice"); err == nil {
		t.Error("removed twice")
	}
	if err := s.RemoveSeat(ctx, "screen"); err != nil {
		t.Fatal(err)
	}
	list.Reset()
	if err := s.ListSeats(&list); err != nil || !strings.Contains(list.String(), "no seat users") {
		t.Errorf("%v %s", err, list.String())
	}
}
