package sdk

import (
	"context"
	"encoding/json"
	"testing"
)

// readCore answers reads from a map of module/name to JSON.
type readCore struct {
	Core
	answers map[string]string
}

func (c readCore) Read(_ context.Context, module, read string, params map[string]string) (json.RawMessage, error) {
	a, ok := c.answers[module+"/"+read+params["id"]]
	if !ok {
		return nil, Errorf(CodeNotFound, "no read %s", read)
	}
	return json.RawMessage(a), nil
}

func TestReadRef(t *testing.T) {
	type lamp struct {
		Name string `json:"name"`
		On   bool   `json:"on"`
	}
	ref := ReadRef[lamp]{Module: "lights", Name: "lamp"}
	core := readCore{answers: map[string]string{"lights/lampdesk": `{"name":"desk","on":true}`, "lights/lampbad": `[1]`}}
	ctx := context.Background()
	if got, err := ref.Get(ctx, core, map[string]string{"id": "desk"}); err != nil || got != (lamp{"desk", true}) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := ref.Get(ctx, core, map[string]string{"id": "porch"}); CodeOf(err) != CodeNotFound {
		t.Fatalf("missing: %v", err)
	}
	if _, err := ref.Get(ctx, core, map[string]string{"id": "bad"}); err == nil {
		t.Fatal("a list read as an object")
	}
	if _, err := ref.Get(ctx, nil, nil); CodeOf(err) != CodeModuleUnavailable {
		t.Fatalf("no core: %v", err)
	}
}
