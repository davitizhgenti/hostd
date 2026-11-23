package kit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/davitizhgenti/hostd/sdk"
)

type lampArgs struct {
	Lamp string `json:"lamp"`
	On   bool   `json:"on,omitempty"`
}

func TestActionsTable(t *testing.T) {
	ctx := context.Background()
	tbl := NewActions("lights")
	On(tbl, sdk.ActionSpec{Type: "lights.set", Scope: "lights"}, func(_ context.Context, a sdk.Action, args lampArgs) (map[string]any, error) {
		return map[string]any{"lamp": args.Lamp, "on": args.On, "action": a.ID}, nil
	})
	On(tbl, sdk.ActionSpec{Type: "lights.off", Scope: "lights"}, func(context.Context, sdk.Action, struct{}) (struct{}, error) {
		return struct{}{}, nil
	})
	OnResult(tbl, sdk.ActionSpec{Type: "lights.blink", Scope: "lights"}, func(_ context.Context, _ sdk.Action, args lampArgs) (sdk.Result, error) {
		return sdk.Result{Status: sdk.StatusSkipped, Reason: "busy " + args.Lamp}, nil
	})
	Check(tbl, "lights.set", func(_ context.Context, _ sdk.Action, args lampArgs) error {
		if args.Lamp == "garage" {
			return sdk.Errorf(sdk.CodeNotFound, "no lamp %q", args.Lamp)
		}
		return nil
	})

	specs := tbl.Specs()
	if len(specs) != 3 || specs[0].Type != "lights.set" || specs[0].Schema == nil || specs[1].Schema != nil || specs[2].Schema == nil {
		t.Fatalf("specs %+v", specs)
	}
	if !tbl.Has("lights.off") || tbl.Has("lights.dim") {
		t.Fatal("Has")
	}

	res, err := tbl.Handle(ctx, sdk.Action{ID: "act_1", Type: "lights.set", Args: json.RawMessage(`{"lamp":"desk","on":true}`)})
	if err != nil || string(res.Data) != `{"action":"act_1","lamp":"desk","on":true}` {
		t.Fatalf("set: %s %v", res.Data, err)
	}
	if res, err := tbl.Handle(ctx, sdk.Action{Type: "lights.off"}); err != nil || res.Data != nil {
		t.Fatalf("off: %s %v", res.Data, err)
	}
	if res, err := tbl.Handle(ctx, sdk.Action{Type: "lights.blink", Args: json.RawMessage(`{"lamp":"desk"}`)}); err != nil || res.Reason != "busy desk" {
		t.Fatalf("blink: %+v %v", res, err)
	}
	if _, err := tbl.Handle(ctx, sdk.Action{Type: "lights.dim"}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := tbl.Handle(ctx, sdk.Action{Type: "lights.set", Args: json.RawMessage(`{"lamp":4}`)}); err == nil {
		t.Fatal("bad arguments were handled")
	}
	if _, err := tbl.Handle(ctx, sdk.Action{Type: "lights.blink", Args: json.RawMessage(`[]`)}); err == nil {
		t.Fatal("bad arguments were handled (result)")
	}
	if err := tbl.Validate(ctx, sdk.Action{Type: "lights.set", Args: json.RawMessage(`{"lamp":"garage"}`)}); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("check: %v", err)
	}
	if err := tbl.Validate(ctx, sdk.Action{Type: "lights.set", Args: json.RawMessage(`{"lamp":7}`)}); err == nil {
		t.Fatal("check with bad arguments")
	}
	if err := tbl.Validate(ctx, sdk.Action{Type: "lights.off"}); err != nil {
		t.Fatalf("no check: %v", err)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("an action declared twice")
		}
	}()
	On(tbl, sdk.ActionSpec{Type: "lights.off"}, func(context.Context, sdk.Action, struct{}) (struct{}, error) { return struct{}{}, nil })
}

func TestReadsTable(t *testing.T) {
	ctx := context.Background()
	tbl := NewReads("lights")
	Serve(tbl, sdk.ReadSpec{Name: "lamp", Path: "/v1/lamps/{lamp}"}, func(_ context.Context, p struct {
		Lamp  string `json:"lamp"`
		Limit int    `json:"limit"`
	}) ([]string, error) {
		if p.Lamp == "boom" {
			return nil, errors.New("boom")
		}
		return []string{p.Lamp}, nil
	})
	if s := tbl.Specs(); len(s) != 1 || string(s[0].Schema) != `{"type":"array","items":{"type":"string"}}` {
		t.Fatalf("specs %+v", s)
	}
	got, err := tbl.Read(ctx, "lamp", map[string]string{"lamp": "desk", "limit": "3"})
	if err != nil || got.([]string)[0] != "desk" {
		t.Fatalf("read %v %v", got, err)
	}
	if _, err := tbl.Read(ctx, "lamp", map[string]string{"limit": "many"}); sdk.CodeOf(err) != sdk.CodeInvalidArgs {
		t.Fatalf("bad params: %v", err)
	}
	if _, err := tbl.Read(ctx, "nope", nil); sdk.CodeOf(err) != sdk.CodeNotFound {
		t.Fatalf("unknown: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("a read declared twice")
		}
	}()
	Serve(tbl, sdk.ReadSpec{Name: "lamp"}, func(context.Context, struct{}) (int, error) { return 0, nil })
}
