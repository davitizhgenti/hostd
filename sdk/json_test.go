package sdk

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The JSON form of these types is the wire format of the HTTP API and the
// external-module socket. Golden files pin it; regenerate after an
// intended change with: go test ./sdk -run Golden -update
var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".golden.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed:\n--- got\n%s\n--- want\n%s", path, got, want)
	}

	// Round trip: decoding the golden file and encoding again is lossless.
	fresh := reflect.New(reflect.TypeOf(v)).Interface()
	if err := json.Unmarshal(want, fresh); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	again, _ := json.MarshalIndent(fresh, "", "  ")
	if !bytes.Equal(append(again, '\n'), want) {
		t.Fatalf("%s does not round-trip:\n%s", path, again)
	}
}

var at = time.Date(2026, 10, 4, 19, 4, 5, 0, time.UTC)

func TestGoldenAction(t *testing.T) {
	v := uint64(17)
	golden(t, "action", Action{
		ID:            "act_01J9ZQ3XK2V5N8M7P6R4T3W2Y1",
		Type:          "audio.volume.set",
		Args:          json.RawMessage(`{"percent":40}`),
		ExpectVersion: &v,
		Source:        Source{Kind: SourceAutomation, Name: "evening.sh", Token: "tok_01J9ZQ3XK2V5N8M7P6R4T3W2Y2"},
		Cause:         "evt_01J9ZQ3XK2V5N8M7P6R4T3W2Y3",
	})
}

func TestGoldenChildAction(t *testing.T) {
	golden(t, "action_child", Action{
		ID:     "act_01J9ZQ3XK2V5N8M7P6R4T3W2Y5",
		Type:   "instance.stop",
		Args:   json.RawMessage(`{"id":"firefox"}`),
		Source: Source{Kind: SourceManual, Name: "phone", Module: "apps"},
		Parent: "act_01J9ZQ3XK2V5N8M7P6R4T3W2Y4",
	})
}

func TestGoldenResultSkipped(t *testing.T) {
	until := at.Add(3 * time.Minute)
	golden(t, "result_skipped", Result{
		Action: "act_01J9ZQ3XK2V5N8M7P6R4T3W2Y1",
		Status: StatusSkipped,
		Reason: "held",
		HeldBy: SourceManual,
		Until:  &until,
	})
}

func TestGoldenResultApplied(t *testing.T) {
	golden(t, "result_applied", Result{
		Action:  "act_01J9ZQ3XK2V5N8M7P6R4T3W2Y1",
		Status:  StatusApplied,
		Version: 18,
		Data:    json.RawMessage(`{"instance":"firefox"}`),
	})
}

func TestGoldenEvent(t *testing.T) {
	golden(t, "event", Event{
		ID:       "evt_01J9ZQ3XK2V5N8M7P6R4T3W2Y6",
		Type:     "audio.volume.changed",
		Time:     at,
		Data:     json.RawMessage(`{"percent":55}`),
		Source:   &Source{Kind: SourceExternal, Name: "pipewire"},
		Resource: "audio.master",
		Version:  18,
	})
}

func TestGoldenError(t *testing.T) {
	golden(t, "error", Error{
		Code:    CodeInstanceNotRunning,
		Message: "instance dota2#1 is not running",
		Action:  "act_01J9ZQ3XK2V5N8M7P6R4T3W2Y1",
	})
}

func TestGoldenManifest(t *testing.T) {
	m := validManifest()
	m.Actions[0].ArgScopes = map[string]string{"relative": "audio"}
	m.Actions[0].Slow = false
	golden(t, "manifest", m)
}

func TestSourcePriority(t *testing.T) {
	for kind, want := range map[SourceKind]int{
		SourceLocal: 3, SourceManual: 2, SourceAutomation: 1, SourceExternal: 0, "script": 0,
	} {
		if got := kind.Priority(); got != want {
			t.Errorf("%s.Priority() = %d, want %d", kind, got, want)
		}
	}
	if !SourceManual.Valid() || SourceKind("script").Valid() {
		t.Fatal("Valid() wrong")
	}
	if got := (Source{Kind: SourceManual, Name: "phone"}).String(); got != "manual (phone)" {
		t.Fatalf("String() = %q", got)
	}
}

func TestDecodeArgs(t *testing.T) {
	var args struct {
		Percent int `json:"percent"`
	}
	if err := (Action{Args: json.RawMessage(`{"percent":40}`)}).DecodeArgs(&args); err != nil || args.Percent != 40 {
		t.Fatalf("DecodeArgs = %+v, %v", args, err)
	}
	if err := (Action{}).DecodeArgs(&args); err != nil {
		t.Fatalf("empty args should decode as {}: %v", err)
	}
	if err := (Action{Type: "x.y", Args: json.RawMessage(`{"percent":"high"}`)}).DecodeArgs(&args); CodeOf(err) != CodeInvalidArgs {
		t.Fatalf("type mismatch: err = %v", err)
	}
}
