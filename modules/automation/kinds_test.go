package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/migrate"
	"github.com/davitizhgenti/hostd/sdk"
)

// Rules the migrator wrote are rules the module accepts, as written.
func TestMigratedRulesAreAccepted(t *testing.T) {
	dir := t.TempDir()
	old := `
[[rule]]
name = "gaming audio"
on   = "instance.started"
if   = { app = "dota*", between = "18:00-02:00", minutes = 5 }
do   = { scene = "gaming" }
cooldown = "10m"

[[rule]]
name = "mute"
on   = "display.idle"
do   = { action = "audio.mute.set", args = { muted = true } }
`
	if err := os.WriteFile(filepath.Join(dir, "rules.toml"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Run(dir); err != nil {
		t.Fatal(err)
	}
	m := New(Options{})
	for file, check := range map[string]func(Rule){
		"gaming-audio": func(r Rule) {
			if r.On != "instance.started" || r.If.App != "dota*" || r.If.Minutes != 5 || r.Do.Scene != "gaming" || r.cooldown != 10*time.Minute || r.from != 18*60 {
				t.Errorf("gaming-audio = %+v", r)
			}
		},
		"mute": func(r Rule) {
			if r.Do.Action != "audio.mute.set" || r.Do.Args["muted"] != true {
				t.Errorf("mute = %+v", r)
			}
		},
	} {
		raw, err := os.ReadFile(filepath.Join(dir, "rules", file+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Apply(context.Background(), sdk.Resource{Kind: "rule", Name: file, Raw: raw}); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		m.mu.Lock()
		for _, r := range m.rules {
			if r.Name == file {
				check(r)
			}
		}
		m.mu.Unlock()
	}
}

// Script files the migrator wrote are accepted, and run.
func TestMigratedScriptsAreAccepted(t *testing.T) {
	dir := t.TempDir()
	scripts := filepath.Join(dir, "scripts")
	_ = os.MkdirAll(scripts, 0o755)
	if err := os.WriteFile(filepath.Join(scripts, "evening.sh"), []byte("#!/bin/sh\n# hostd-scopes: audio display\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Run(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(scripts, "evening.sh.toml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseScript(raw)
	if err != nil || len(spec.Command) != 1 || spec.Command[0] != "./evening.sh" || len(spec.Scopes) != 2 {
		t.Fatalf("%+v %v", spec, err)
	}
}
