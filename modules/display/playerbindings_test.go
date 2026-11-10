package display

import (
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/internal/evdev"
)

func TestPlayerBindings(t *testing.T) {
	_, buttons, err := Bindings(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PlayerBindings(buttons, map[string]PlayerConfig{
		"2": {Buttons: map[string]string{"guide": ""}},             // off for player 2
		"3": {Buttons: map[string]string{"select": "window.next"}}, // another action
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := got[2]["guide"]; has {
		t.Error("guide is still bound for player 2")
	}
	if got[2]["start"] != buttons["start"] && buttons["start"] != "" {
		t.Errorf("player 2 lost a binding it did not change: %v", got[2])
	}
	if got[3]["select"] != "window.next" || got[3]["guide"] != buttons["guide"] {
		t.Errorf("player 3 = %v", got[3])
	}
	if _, has := got[1]; has {
		t.Error("player 1 has a table of its own though nothing changed")
	}
	for name, players := range map[string]map[string]PlayerConfig{
		"slot not a number": {"two": {}}, "slot 0": {"0": {}}, "slot 17": {"17": {}},
		"unknown button": {"2": {Buttons: map[string]string{"banana": "display.menu"}}},
		"unbindable":     {"2": {Buttons: map[string]string{"guide": "audio.volume.set"}}},
	} {
		if _, err := PlayerBindings(buttons, players); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// hostd.toml's own check says so too.
	c := &Config{Players: map[string]PlayerConfig{"2": {Buttons: map[string]string{"nope": "display.menu"}}}}
	if err := c.Check(); err == nil || !strings.Contains(err.Error(), "players.2") {
		t.Errorf("Config.Check = %v", err)
	}
}

// Player 2's Guide is off; player 1's still opens the menu.
func TestAPlayersButtonCanDoSomethingElse(t *testing.T) {
	r := newDisplayRig(t, true)
	r.m.playerButtons = map[int]map[string]string{2: {}} // nothing bound for player 2
	hold := func(dev string, player int) {
		r.input.send(t, evdev.InputEvent{Device: dev, Player: player, Buttons: []string{"guide"}})
		waitFor(t, "held", func() bool {
			r.clock.Advance(100 * time.Millisecond)
			r.m.mu.Lock()
			defer r.m.mu.Unlock()
			_, held := r.m.holds[dev+"/guide"]
			return !held
		})
	}
	hold("pad-two", 2)
	select {
	case a := <-r.started:
		t.Fatalf("player 2's Guide did %s", a.Args)
	case <-time.After(300 * time.Millisecond):
	}
	hold("pad-one", 1)
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("player 1's Guide did nothing")
	}
}
