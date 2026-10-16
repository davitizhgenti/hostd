package demo

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/core"
	"github.com/davitizhgenti/hostd/sdk"
)

func TestDemoLamps(t *testing.T) {
	m := New()
	man := m.Manifest()
	if err := man.Validate(); err != nil {
		t.Fatal(err)
	}
	reg := core.NewRegistry()
	if err := reg.Add(m); err != nil {
		t.Fatal(err)
	}
	e := core.New(reg, core.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(context.Background())
	events := e.Subscribe(ctx, "demo.*")
	admin := core.Auth{Scopes: []string{sdk.ScopeAdmin}}
	do := func(typ string, args any) {
		t.Helper()
		if _, err := e.Submit(ctx, sdk.Action{Type: typ, Args: sdk.MustJSON(args), Source: sdk.Source{Kind: sdk.SourceManual, Name: "test"}}, admin); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	do("demo.lamp.set", map[string]any{"lamp": "porch", "brightness": 40})
	do("demo.lamp.set", map[string]any{"lamp": "hall", "brightness": 70})
	st, _ := m.State(ctx)
	if b, _ := json.Marshal(st); string(b) != `{"lamps":{"hall":70,"porch":40}}` {
		t.Fatalf("state %s", b)
	}
	do("demo.lamp.off", map[string]any{})
	st, _ = m.State(ctx)
	if b, _ := json.Marshal(st); string(b) != `{"lamps":{"hall":0,"porch":0}}` {
		t.Fatalf("after off %s", b)
	}
	for i := 0; i < 4; i++ {
		select {
		case ev := <-events:
			if ev.Type != "demo.lamp.changed" {
				t.Fatalf("event %s", ev.Type)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%d events, want 4", i)
		}
	}
}
