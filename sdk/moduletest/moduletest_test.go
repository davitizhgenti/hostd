package moduletest_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/davitizhgenti/hostd/sdk"
	"github.com/davitizhgenti/hostd/sdk/kit"
	"github.com/davitizhgenti/hostd/sdk/moduletest"
)

type focusArgs struct {
	App string `json:"app"`
}

// A module that reads another's data, sends an action when it can, and
// reacts to events: tested with no engine.
func newModule() *kit.Module {
	m := kit.New("front", "1").Owns("front.*")
	opened := kit.Event[map[string]string](m, "front.opened", "An app was brought forward")
	kit.Action(m, kit.Spec{Type: "front.open", Scope: "read"},
		func(ctx context.Context, c kit.Ctx, a focusArgs) (struct{}, error) {
			raw, err := c.Core.Read(ctx, "apps", "instances", nil)
			if err != nil {
				return struct{}{}, err
			}
			var list []struct{ ID string }
			_ = json.Unmarshal(raw, &list)
			if c.Core.Handles("window.focus") && len(list) > 0 {
				if _, err := c.Core.Do(ctx, sdk.Action{Type: "window.focus"}); err != nil {
					return struct{}{}, err
				}
			}
			opened.Emit(c, map[string]string{"app": a.App}, "")
			return struct{}{}, nil
		})
	return m.OnStart(func(ctx context.Context, c kit.Ctx) error {
		events := c.Core.Subscribe(ctx, "instance.*")
		go func() {
			for ev := range events {
				c.Core.Emit(sdk.Event{Type: "front.opened", Data: ev.Data})
			}
		}()
		return nil
	})
}

func TestFakeCoreRecordsAndScripts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	core := moduletest.NewCore()
	core.OnRead("apps", "instances", `[{"ID":"firefox"}]`)
	m := newModule()
	if err := m.Start(ctx, core); err != nil {
		t.Fatal(err)
	}

	// No window module yet: no action is sent.
	if _, err := m.Handle(ctx, sdk.Action{ID: "act_1", Type: "front.open", Args: json.RawMessage(`{"app":"firefox"}`)}); err != nil {
		t.Fatal(err)
	}
	if got := core.Did(); len(got) != 0 {
		t.Fatalf("sent %v without a handler", got)
	}
	// The event carries the action it belongs to.
	if ev := core.Emitted("front.opened"); len(ev) != 1 || ev[0].Action != "act_1" {
		t.Fatalf("emitted = %+v", ev)
	}

	core.CanHandle("window.focus")
	if _, err := m.Handle(ctx, sdk.Action{ID: "act_2", Type: "front.open", Args: json.RawMessage(`{"app":"firefox"}`)}); err != nil {
		t.Fatal(err)
	}
	if got := core.Did("window.focus"); len(got) != 1 {
		t.Fatalf("did = %v, want one window.focus", got)
	}

	// An unscripted read is not_found; Do can be scripted to fail.
	if _, err := core.Read(ctx, "audio", "audio", nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unscripted read: %v", err)
	}
	core.OnDo(func(context.Context, sdk.Action) (sdk.Result, error) {
		return sdk.Result{}, sdk.Errorf(sdk.CodeNotFound, "gone")
	})
	if _, err := m.Handle(ctx, sdk.Action{ID: "act_3", Type: "front.open", Args: json.RawMessage(`{"app":"x"}`)}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("a failing Do should fail the action: %v", err)
	}
}

func TestFakeCorePublishesToSubscribers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	core := moduletest.NewCore()
	if err := newModule().Start(ctx, core); err != nil {
		t.Fatal(err)
	}
	core.Publish(sdk.Event{Type: "window.opened"}) // not matched by instance.*
	core.Publish(sdk.Event{Type: "instance.started", Data: json.RawMessage(`{"id":"a"}`)})
	deadline := time.After(5 * time.Second)
	for len(core.Emitted("front.opened")) == 0 {
		select {
		case <-deadline:
			t.Fatal("the module never saw the event")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := core.Emitted(); len(got) != 1 {
		t.Fatalf("emitted %d events, want 1", len(got))
	}
	cancel()
}

func TestOptionalCapabilitiesAreNilByDefault(t *testing.T) {
	core := moduletest.NewCore()
	if core.Tokens() != nil || core.Limiter() != nil || core.Introspect() != nil {
		t.Fatal("capabilities should be nil unless a test sets them")
	}
}
