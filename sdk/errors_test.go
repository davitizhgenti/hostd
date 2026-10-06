package sdk

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestCodeOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want Code
	}{
		{"nil", nil, ""},
		{"sdk error", Errorf(CodeNotFound, "no app %q", "x"), CodeNotFound},
		{"wrapped sdk error", fmt.Errorf("start: %w", Errorf(CodeInstanceNotRunning, "gone")), CodeInstanceNotRunning},
		{"deadline", context.DeadlineExceeded, CodeTimeout},
		{"wrapped deadline", fmt.Errorf("sway: %w", context.DeadlineExceeded), CodeTimeout},
		{"plain error", errors.New("boom"), CodeInternal},
	} {
		if got := CodeOf(tc.err); got != tc.want {
			t.Errorf("%s: CodeOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAsError(t *testing.T) {
	if AsError(nil) != nil {
		t.Fatal("AsError(nil) != nil")
	}
	orig := Errorf(CodeForbidden, "needs scope %s", "audio")
	if got := AsError(fmt.Errorf("x: %w", orig)); got != orig {
		t.Fatalf("AsError should return the wrapped *Error, got %#v", got)
	}
	got := AsError(errors.New("disk full"))
	if got.Code != CodeInternal || got.Message != "disk full" {
		t.Fatalf("AsError(plain) = %#v", got)
	}
	if got.Error() != "internal: disk full" {
		t.Fatalf("Error() = %q", got.Error())
	}
}
