package sdk

import (
	"context"
	"encoding/json"
)

// A ReadRef names another module's read and the type it answers with, so
// a module that uses it reads typed data instead of decoding JSON by hand,
// and a change of either side shows up at compile time. Modules publish
// them in a shared package (hostd's own modules: contract).
type ReadRef[T any] struct {
	Module, Name string
}

// Get asks the module for the read through core.
func (r ReadRef[T]) Get(ctx context.Context, core Core, params map[string]string) (T, error) {
	var out T
	if core == nil {
		return out, Errorf(CodeModuleUnavailable, "no core to read %s from %s", r.Name, r.Module)
	}
	raw, err := core.Read(ctx, r.Module, r.Name, params)
	if err != nil {
		return out, err
	}
	return out, json.Unmarshal(raw, &out)
}
