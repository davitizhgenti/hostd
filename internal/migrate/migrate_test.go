package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const oldRules = `
[[rule]]
name = "gaming audio"
on   = "instance.started"
if   = { app = "dota*", between = "18:00-02:00" }
do   = { scene = "gaming" }
cooldown = "10m"

[[rule]]
name = "mute"
on   = "display.idle"
do   = { action = "audio.mute.set", args = { muted = true } }

[[rule]]
name = "Gaming Audio!"
on   = "instance.exited"
do   = { script = "off.sh" }
`

func TestRulesAreSplit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rules.toml"), []byte(oldRules), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Needed(dir); err == nil || !strings.Contains(err.Error(), "hostd -migrate") {
		t.Fatalf("Needed = %v", err)
	}
	did, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(did) != 4 || !strings.Contains(did[3], BackupDir) {
		t.Fatalf("did = %q", did)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, "rules", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	g := read("gaming-audio.toml")
	for _, want := range []string{`# Was the rule "gaming audio"`, `on = "instance.started"`, `cooldown = "10m"`, `app = "dota*"`, `scene = "gaming"`} {
		if !strings.Contains(g, want) {
			t.Errorf("gaming-audio.toml lacks %q:\n%s", want, g)
		}
	}
	if m := read("mute.toml"); strings.Contains(m, "Was the rule") || !strings.Contains(m, `action = "audio.mute.set"`) || !strings.Contains(m, "muted = true") {
		t.Errorf("mute.toml:\n%s", m)
	}
	// Two names that make the same file name do not overwrite each other.
	if g2 := read("gaming-audio-2.toml"); !strings.Contains(g2, `script = "off.sh"`) {
		t.Errorf("gaming-audio-2.toml:\n%s", g2)
	}
	if _, err := os.Stat(filepath.Join(dir, "rules.toml")); err == nil {
		t.Error("rules.toml is still there")
	}
	if b, err := os.ReadFile(filepath.Join(dir, BackupDir, "rules.toml")); err != nil || string(b) != oldRules {
		t.Errorf("the backup is not the original: %v", err)
	}
	if err := Needed(dir); err != nil {
		t.Fatalf("still needed after migrating: %v", err)
	}
	// Again: nothing to do.
	if did, err := Run(dir); err != nil || len(did) != 0 {
		t.Fatalf("second run: %q %v", did, err)
	}
}

func TestMigrateRefusesWhatItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"broken":  "[[rule]\nname = ",
		"no name": "[[rule]]\non = \"x\"\ndo = { scene = \"s\" }\n",
	} {
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "rules.toml"), []byte(body), 0o644)
		if _, err := Run(dir); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, err := os.Stat(filepath.Join(dir, "rules.toml")); err != nil {
			t.Errorf("%s: the file was moved though migrating failed", name)
		}
		if entries, _ := os.ReadDir(filepath.Join(dir, "rules")); len(entries) > 0 {
			t.Errorf("%s: left rule files", name)
		}
	}
	if err := Needed(t.TempDir()); err != nil {
		t.Fatalf("an empty config: %v", err)
	}
	if did, err := Run(t.TempDir()); err != nil || did != nil {
		t.Fatalf("an empty config: %v %v", did, err)
	}
}

func TestScriptsGetScriptFiles(t *testing.T) {
	dir := t.TempDir()
	scripts := filepath.Join(dir, "scripts")
	_ = os.MkdirAll(scripts, 0o755)
	write := func(name, body string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(scripts, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("evening.sh", "#!/bin/sh\n# hostd-scopes: audio display\necho hi\n", 0o755) // scopes in the header
	write("slow.sh", "#!/bin/sh\nsleep 1\n", 0o755)                                   // scopes in a sidecar
	write("slow.sh.toml", "scopes = [\"audio\"]\ntimeout = \"2m\"\n", 0o644)
	write("done.py", "print(1)\n", 0o755) // already described
	write("done.py.toml", "command = [\"python3\", \"done.py\"]\n", 0o644)
	write("helper.txt", "not a script\n", 0o644) // not executable: left alone
	write("Loud.sh", "#!/bin/sh\n", 0o755)       // a name no script can have: left alone

	if err := Needed(dir); err == nil || !strings.Contains(err.Error(), "scripts/evening.sh") && !strings.Contains(err.Error(), "scripts/slow.sh") {
		t.Fatalf("Needed = %v", err)
	}
	did, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(did) != 2 {
		t.Fatalf("did = %q", did)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(scripts, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("evening.sh.toml"); !strings.Contains(got, `command = ["./evening.sh"]`) || !strings.Contains(got, `scopes = ["audio", "display"]`) {
		t.Errorf("evening.sh.toml:\n%s", got)
	}
	if got := read("slow.sh.toml"); !strings.Contains(got, `scopes = ["audio"]`) || !strings.Contains(got, `timeout = "2m"`) || !strings.Contains(got, `command = ["./slow.sh"]`) {
		t.Errorf("slow.sh.toml:\n%s", got)
	}
	if got := read("done.py.toml"); got != "command = [\"python3\", \"done.py\"]\n" {
		t.Errorf("an already described script was rewritten:\n%s", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, BackupDir, "slow.sh.toml")); err != nil || !strings.Contains(string(b), `timeout = "2m"`) {
		t.Errorf("the old sidecar is not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scripts, "helper.txt.toml")); err == nil {
		t.Error("a file that is not executable got a script file")
	}
	if err := Needed(dir); err != nil {
		t.Fatalf("still needed: %v", err)
	}
	if did, err := Run(dir); err != nil || len(did) != 0 {
		t.Fatalf("second run: %q %v", did, err)
	}
}
