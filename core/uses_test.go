package core

import (
	"context"
	"testing"

	"github.com/davitizhgenti/hostd/internal/testutil"
	"github.com/davitizhgenti/hostd/sdk"
)

// A module reads from and sends actions to only itself and the modules its
// manifest names (requires or uses); "*" names every module.
func TestModulesUseOnlyWhatTheyDeclare(t *testing.T) {
	ctx := context.Background()
	lights := testutil.NewModule("lights", nil, []string{"lights.set"}, nil)
	lights.ReadFunc = func(context.Context, string, map[string]string) (any, error) { return map[string]int{"on": 1}, nil }
	quiet := testutil.NewModule("quiet", nil, []string{"quiet.do"}, nil)
	user := testutil.NewModule("user", nil, nil, nil)
	user.M.Uses = []string{"lights"}
	anyMod := testutil.NewModule("any", nil, nil, nil)
	anyMod.M.Uses = []string{"*"}
	reg := NewRegistry()
	for _, m := range []sdk.Module{lights, quiet, user, anyMod} {
		if err := reg.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	e := New(reg, Options{})
	if err := e.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer e.Stop(ctx)

	if _, err := user.Core().Do(ctx, sdk.Action{Type: "lights.set"}); err != nil {
		t.Fatalf("declared action: %v", err)
	}
	if _, err := user.Core().Read(ctx, "lights", "state", nil); err != nil {
		t.Fatalf("declared read: %v", err)
	}
	if _, err := user.Core().Do(ctx, sdk.Action{Type: "quiet.do"}); sdk.CodeOf(err) != sdk.CodeForbidden {
		t.Fatalf("undeclared action: %v", err)
	}
	if _, err := user.Core().Read(ctx, "quiet", "state", nil); sdk.CodeOf(err) != sdk.CodeForbidden {
		t.Fatalf("undeclared read: %v", err)
	}
	if _, err := quiet.Core().Do(ctx, sdk.Action{Type: "quiet.do"}); err != nil {
		t.Fatalf("its own action: %v", err)
	}
	if _, err := anyMod.Core().Do(ctx, sdk.Action{Type: "quiet.do"}); err != nil {
		t.Fatalf("uses *: %v", err)
	}
	if _, err := user.Core().Do(ctx, sdk.Action{Type: "nobody.does"}); sdk.CodeOf(err) == sdk.CodeForbidden {
		t.Fatalf("an action nobody declares is not a matter of uses: %v", err)
	}
}

func TestUsesMustNameModules(t *testing.T) {
	m := testutil.NewModule("bad", nil, nil, nil)
	m.M.Uses = []string{"Not A Module"}
	if err := NewRegistry().Add(m); err == nil {
		t.Fatal("a bad name in uses was accepted")
	}
}
