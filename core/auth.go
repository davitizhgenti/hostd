package core

import (
	"encoding/json"

	"github.com/davitizhgenti/hostd/sdk"
)

// Auth is what the caller of an action is allowed to do, from its token.
type Auth struct {
	TokenID string
	Name    string
	Scopes  []string

	// trusted marks actions a module starts on its own (not on behalf of
	// a caller); they skip scope checks.
	trusted bool
}

// Has reports whether the token grants scope. admin grants everything.
func (a Auth) Has(scope string) bool {
	if a.trusted {
		return true
	}
	for _, s := range a.Scopes {
		if s == scope || s == sdk.ScopeAdmin {
			return true
		}
	}
	return false
}

// authorize checks the action's scope and its argument-dependent scopes.
func (a Auth) authorize(spec sdk.ActionSpec, args json.RawMessage) error {
	if !a.Has(spec.Scope) {
		return sdk.Errorf(sdk.CodeForbidden, "%s needs scope %q", spec.Type, spec.Scope)
	}
	if len(spec.ArgScopes) == 0 {
		return nil
	}
	// Arguments that are not an object have already failed schema
	// validation; values then stays empty and no extra scope applies.
	var values map[string]json.RawMessage
	_ = json.Unmarshal(args, &values)
	for arg, scope := range spec.ArgScopes {
		if string(values[arg]) == "true" && !a.Has(scope) {
			return sdk.Errorf(sdk.CodeForbidden, "%s with %s=true needs scope %q", spec.Type, arg, scope)
		}
	}
	return nil
}
