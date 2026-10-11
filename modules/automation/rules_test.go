package automation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/contract"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestParseRules(t *testing.T) {
	rules, err := ParseRules([]byte(`
[[rule]]
name = "gaming audio"
on   = "instance.started"
if   = { app = "dota*", between = "18:00-02:00" }
do   = { scene = "gaming" }
cooldown = "10m"
`))
	if err != nil || len(rules) != 1 || rules[0].cooldown != 10*time.Minute || rules[0].from != 18*60 || rules[0].to != 2*60 {
		t.Fatalf("%+v %v", rules, err)
	}
	for body, want := range map[string]string{
		"[[rule]]\non = \"x\"\ndo = { scene = \"s\" }":                                                                           "needs a name",
		"[[rule]]\nname = \"a\"\ndo = { scene = \"s\" }":                                                                         "needs on",
		"[[rule]]\nname = \"a\"\non = \"x\"":                                                                                     "a scene or a script",
		"[[rule]]\nname = \"a\"\non = \"x\"\ndo = { scene = \"s\", script = \"x.sh\" }":                                          "a scene or a script",
		"[[rule]]\nname = \"a\"\non = \"x\"\ndo = { scene = \"s\" }\nif = { between = \"9-5\" }":                                 "HH:MM-HH:MM",
		"[[rule]]\nname = \"a\"\non = \"x\"\ndo = { scene = \"s\" }\ncooldown = \"soon\"":                                        "cooldown",
		"[[rule]]\nname = \"a\"\non = \"x\"\ndo = { scene = \"s\" }\nwhen = 1":                                                   "unknown key",
		"[[rule]]\nname = \"a\"\non = \"x\"\ndo = { scene = \"s\" }\n[[rule]]\nname = \"a\"\non = \"y\"\ndo = { scene = \"s\" }": "twice",
	} {
		if _, err := ParseRules([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
}

func TestInTime(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 11, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		from, to string
		t        time.Time
		want     bool
	}{
		{"09:00", "17:00", at(12, 0), true},
		{"09:00", "17:00", at(17, 0), false},
		{"22:00", "06:00", at(23, 30), true},
		{"22:00", "06:00", at(3, 0), true},
		{"22:00", "06:00", at(6, 0), false},
		{"22:00", "06:00", at(12, 0), false},
	} {
		from, to, err := parseBetween(c.from + "-" + c.to)
		if err != nil || inTime(c.t, from, to) != c.want {
			t.Errorf("%s-%s at %s: %v", c.from, c.to, c.t.Format("15:04"), !c.want)
		}
	}
}

func (r *rig) rules(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(r.dir, "rules.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// A new modification time, so the rules are read again.
	r.clock.Advance(time.Second)
	_ = os.Chtimes(p, r.clock.Now(), r.clock.Now())
}

// emit sends an event as the module that owns its type.
func (r *rig) emit(t *testing.T, typ string, data any) {
	t.Helper()
	mod := "apps"
	if strings.HasPrefix(typ, "display.") {
		mod = "display"
	}
	r.w.mods[mod].Core().Emit(sdk.Event{Type: typ, Data: sdk.MustJSON(data)})
}

func (r *rig) state(t *testing.T, name string) RuleState {
	t.Helper()
	for _, st := range r.m.readRules().Rules {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no rule %q", name)
	return RuleState{}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRuleFiresScene(t *testing.T) {
	w := &world{volume: 70}
	r := newRig(t, w)
	r.scene(t, "gaming", "[audio]\nvolume = 90\n")
	r.rules(t, `
[[rule]]
name = "gaming audio"
on   = "instance.started"
if   = { app = "dota*" }
do   = { scene = "gaming" }
cooldown = "10m"
`)
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "firefox", App: "firefox"})
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "dota2", App: "dota2"})
	waitFor(t, "the rule", func() bool { return r.state(t, "gaming audio").Fired == 1 })
	waitFor(t, "its result", func() bool { return r.state(t, "gaming audio").LastResult == "applied: volume applied" })
	if sent := w.takeSent(); len(sent) != 1 || sent[0] != `audio.volume.set {"percent":90}` {
		t.Fatalf("sent %v", sent)
	}
	// The work ran as automation, from the rule, caused by the event.
	found := 0
	for _, e := range r.audit.Entries() {
		auto := e.Source.Kind == sdk.SourceAutomation && e.Source.Name == "gaming audio"
		if (e.Type == "audio.volume.set" && auto && e.Parent != "") || (e.Type == "scene.apply" && auto && e.Cause != "") {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("audit %+v", r.audit.Entries())
	}
	// Cooldown: a second start within 10 minutes does nothing.
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "dota2#2", App: "dota2"})
	time.Sleep(100 * time.Millisecond)
	if st := r.state(t, "gaming audio"); st.Fired != 1 {
		t.Fatalf("fired %d times within the cooldown", st.Fired)
	}
}

func TestRuleLoopIsPaused(t *testing.T) {
	// A rule set off by its own scene's event: the core's chain depth
	// stops it, and the rule is paused.
	w := &world{}
	r := newRig(t, w)
	r.scene(t, "echo", "[audio]\nmuted = false\n")
	r.rules(t, `
[[rule]]
name = "kick"
on   = "instance.started"
do   = { scene = "echo" }
[[rule]]
name = "echo"
on   = "scene.applied"
do   = { scene = "echo" }
`)
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "a", App: "a"})
	waitFor(t, "the pause", func() bool { return r.state(t, "echo").PausedUntil != nil })
	st := r.state(t, "echo")
	if !strings.Contains(st.PausedWhy, "loop") || st.Fired > 10 {
		t.Fatalf("state %+v", st)
	}
}

func TestRulePauseResume(t *testing.T) {
	w := &world{}
	r := newRig(t, w)
	r.scene(t, "s", "[audio]\nvolume = 10\n")
	r.rules(t, "[[rule]]\nname = \"r\"\non = \"instance.started\"\ndo = { scene = \"s\" }\n")
	do := func(typ string) error {
		_, err := r.e.Submit(context.Background(), sdk.Action{Type: typ, Args: sdk.MustJSON(map[string]string{"name": "r"}),
			Source: sdk.Source{Kind: sdk.SourceManual}}, adminAuth)
		return err
	}
	if err := do("rule.pause"); err != nil {
		t.Fatal(err)
	}
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "a", App: "a"})
	time.Sleep(100 * time.Millisecond)
	if st := r.state(t, "r"); st.Fired != 0 || st.PausedWhy != "paused by hand" {
		t.Fatalf("paused rule: %+v", st)
	}
	if err := do("rule.resume"); err != nil {
		t.Fatal(err)
	}
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "a", App: "a"})
	waitFor(t, "the resumed rule", func() bool { return r.state(t, "r").Fired == 1 })
	if _, err := r.e.Submit(context.Background(), sdk.Action{Type: "rule.pause", Args: sdk.MustJSON(map[string]string{"name": "nope"}),
		Source: sdk.Source{Kind: sdk.SourceManual}}, adminAuth); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown rule: %v", err)
	}
}

func TestRuleWaitsForIdle(t *testing.T) {
	w := &world{power: true}
	r := newRig(t, w)
	w.lastInput = r.clock.Now().Add(-5 * time.Minute)
	r.scene(t, "screen-off", "[display]\npower = \"off\"\n")
	r.rules(t, "[[rule]]\nname = \"idle\"\non = \"display.idle\"\nif = { minutes = 30 }\ndo = { scene = \"screen-off\" }\n")
	r.emit(t, "display.idle", map[string]any{})
	time.Sleep(100 * time.Millisecond)
	if r.state(t, "idle").Fired != 0 {
		t.Fatal("fired before 30 minutes")
	}
	r.clock.Advance(30 * time.Minute)
	waitFor(t, "the screen off", func() bool { w.mu.Lock(); defer w.mu.Unlock(); return !w.power })

	// Someone used the screen meanwhile: no firing.
	w.mu.Lock()
	w.power = true
	w.mu.Unlock()
	r.clock.Advance(time.Hour) // past the rule's last firing
	w.mu.Lock()
	w.lastInput = r.clock.Now().Add(-5 * time.Minute)
	w.mu.Unlock()
	r.emit(t, "display.idle", map[string]any{})
	time.Sleep(50 * time.Millisecond)
	w.mu.Lock()
	w.lastInput = r.clock.Now().Add(20 * time.Minute) // input 20 minutes from now
	w.mu.Unlock()
	r.clock.Advance(25 * time.Minute)
	time.Sleep(50 * time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.power {
		t.Fatal("the screen went off although someone used it")
	}
}

func TestRuleScript(t *testing.T) {
	w := &world{}
	r := newRig(t, w)
	scripts := filepath.Join(r.dir, "scripts")
	_ = os.MkdirAll(scripts, 0o755)
	out := filepath.Join(r.dir, "out")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(scripts, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("evening.sh", "#!/bin/sh\n# hostd-scopes: audio display\necho \"$HOSTD_URL $HOSTD_TOKEN $HOSTD_RULE\" > "+out+"\necho \"$HOSTD_EVENT\" >> "+out+"\n")
	write("slow.sh", "#!/bin/sh\nsleep 30 &\necho $! > "+out+".child\nsleep 30\n")
	_ = os.WriteFile(filepath.Join(scripts, "slow.sh.toml"), []byte("scopes = [\"audio\"]\ntimeout = \"300ms\"\n"), 0o644)
	write("boss.sh", "#!/bin/sh\n# hostd-scopes: admin\n")
	write("fails.sh", "#!/bin/sh\necho oh no\nexit 3\n")
	r.m.opts.URL = "unix:///run/hostd.sock"
	r.rules(t, `
[[rule]]
name = "evening"
on   = "instance.started"
do   = { script = "evening.sh" }
[[rule]]
name = "slow"
on   = "instance.exited"
do   = { script = "slow.sh" }
[[rule]]
name = "boss"
on   = "instance.failed"
do   = { script = "boss.sh" }
[[rule]]
name = "fails"
on   = "instance.starting"
do   = { script = "fails.sh" }
[[rule]]
name = "escape"
on   = "instance.closing"
do   = { script = "../rules.toml" }
`)
	r.emit(t, contract.EventInstanceStarted, contract.Instance{ID: "tv", App: "tv"})
	waitFor(t, "the script", func() bool { return r.state(t, "evening").LastResult != "" })
	if res := r.state(t, "evening").LastResult; res != "ran: exit 0, 0 action(s) sent" {
		t.Fatalf("result %q", res)
	}
	b, _ := os.ReadFile(out)
	if lines := strings.Split(string(b), "\n"); lines[0] != "unix:///run/hostd.sock hostd_secret evening" || !strings.Contains(lines[1], `"type":"instance.started"`) {
		t.Fatalf("script saw %q", b)
	}
	w.mu.Lock()
	if len(w.minted) != 1 || w.minted[0] != "script evening #1 audio,display" || w.revoked != 1 {
		t.Fatalf("tokens %q, revoked %d", w.minted, w.revoked)
	}
	w.mu.Unlock()

	// Out of time: stopped, with what it started.
	r.emit(t, contract.EventInstanceExited, contract.Instance{ID: "tv", App: "tv"})
	waitFor(t, "the timeout", func() bool { return r.state(t, "slow").LastResult != "" })
	if res := r.state(t, "slow").LastResult; !strings.Contains(res, "timed out after 300ms") {
		t.Fatalf("result %q", res)
	}
	pid, _ := os.ReadFile(out + ".child")
	if _, err := os.Stat("/proc/" + strings.TrimSpace(string(pid))); err == nil {
		t.Fatalf("the script's child %s still runs", pid)
	}

	for name, want := range map[string]string{"boss": "cannot have scope admin", "fails": "exit status 3", "escape": "a file in"} {
		typ := map[string]string{"boss": "instance.failed", "fails": "instance.starting", "escape": "instance.closing"}[name]
		r.emit(t, typ, contract.Instance{ID: "x", App: "x"})
		waitFor(t, name, func() bool { return r.state(t, name).LastResult != "" })
		if res := r.state(t, name).LastResult; !strings.Contains(res, want) {
			t.Errorf("%s: %q, want %q", name, res, want)
		}
	}
}
