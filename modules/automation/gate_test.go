package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
)

// TestM5Gate reproduces the design's audit example: a rule and a manual
// change on the same volume.
//
//  1. manual volume set 30 → applied
//  2. a rule sets 70 two minutes later → skipped, held by manual
//  3. something outside hostd changes it to 55 → observed
//  4. the rule fires after the hold has expired → applied
func TestM5Gate(t *testing.T) {
	w := &world{volume: 50}
	r := newRig(t, w)
	r.scene(t, "loud", "[audio]\nvolume = 70\n")
	r.rules(t, "[[rule]]\nname = \"evening volume\"\non = \"display.idle\"\ndo = { scene = \"loud\" }\n")
	start := r.clock.Now()

	// 1.
	if _, err := r.e.Submit(context.Background(), sdk.Action{Type: "audio.volume.set", Args: json.RawMessage(`{"percent":30}`),
		Source: sdk.Source{Kind: sdk.SourceManual, Name: "phone"}}, adminAuth); err != nil {
		t.Fatal(err)
	}
	// 2.
	r.clock.Advance(2 * time.Minute)
	r.emit(t, "display.idle", map[string]any{})
	waitFor(t, "the rule's first firing", func() bool { return r.state(t, "evening volume").LastResult != "" })
	if res := r.state(t, "evening volume").LastResult; res != "applied: volume skipped" {
		t.Fatalf("first firing: %q", res)
	}
	// 3.
	w.mu.Lock()
	w.volume = 55
	w.mu.Unlock()
	r.w.mods["audio"].Core().Emit(sdk.Event{Type: "audio.volume.changed", Resource: "audio.master", Data: json.RawMessage(`{"percent":55}`)})
	// 4.
	r.clock.Advance(2 * time.Minute) // 4 minutes after the manual change: its 3-minute hold is over
	r.emit(t, "display.idle", map[string]any{})
	waitFor(t, "the rule's second firing", func() bool {
		return r.state(t, "evening volume").Fired == 2 && r.state(t, "evening volume").LastResult != "applied: volume skipped"
	})
	if res := r.state(t, "evening volume").LastResult; res != "applied: volume applied" {
		t.Fatalf("second firing: %q", res)
	}

	var got []string
	for _, e := range r.audit.Entries() {
		if e.Resource != "audio.master" {
			continue
		}
		line := fmt.Sprintf("+%s %s v%d %s %s", e.Time.Sub(start), e.Status, e.Version, e.Source, e.Args)
		if e.Status == string(sdk.StatusSkipped) {
			line = fmt.Sprintf("+%s %s held_by=%s %s %s", e.Time.Sub(start), e.Status, e.HeldBy, e.Source, e.Args)
		}
		got = append(got, line)
	}
	want := []string{
		`+0s applied v1 manual (phone) {"percent":30}`,
		`+2m0s skipped held_by=manual automation (evening volume) {"percent":70}`,
		`+2m0s observed v2 external (audio) {"percent":55}`,
		`+4m0s applied v3 automation (evening volume) {"percent":70}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("audit:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if w.volume != 70 {
		t.Fatalf("volume %d", w.volume)
	}
}
