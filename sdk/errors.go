package sdk

import (
	"context"
	"errors"
	"fmt"
)

// Code is a stable error code that scripts can branch on.
type Code string

const (
	// CodeInvalidArgs: arguments fail the action's schema.
	CodeInvalidArgs Code = "invalid_args"
	// CodeNotFound: unknown app, instance, output, window or action type.
	CodeNotFound Code = "not_found"
	// CodeInstanceNotRunning: the target instance has exited.
	CodeInstanceNotRunning Code = "instance_not_running"
	// CodePreconditionFailed: expect_version did not match.
	CodePreconditionFailed Code = "precondition_failed"
	// CodeForbidden: the token lacks the scope.
	CodeForbidden Code = "forbidden"
	// CodeTimeout: the module did not finish in time; the outcome is
	// reported later by event.
	CodeTimeout Code = "timeout"
	// CodeModuleUnavailable: the owning module is not running, e.g.
	// display before the session starts.
	CodeModuleUnavailable Code = "module_unavailable"
	// CodeLoopDetected: the action's cause chain is too deep, or the rule
	// that sent it is paused for firing too often.
	CodeLoopDetected Code = "loop_detected"
	// CodeUnauthorized: the request has no token, or an invalid one.
	CodeUnauthorized Code = "unauthorized"
	// CodeInternal: anything else; a bug or an unexpected system error.
	CodeInternal Code = "internal"
)

// Error is the error type of every action failure. Its JSON form is the
// "error" object of the API's error responses.
type Error struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	// Action is the ID of the failed action, when known.
	Action string `json:"action,omitempty"`
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Errorf returns an *Error with the given code and formatted message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf returns the code of err: its own if it wraps an *Error, timeout
// for a context deadline, internal otherwise, and "" for nil.
func CodeOf(err error) Code {
	var e *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &e):
		return e.Code
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	default:
		return CodeInternal
	}
}

// AsError converts any error to an *Error, keeping an existing one (and its
// message) and wrapping anything else with the code from CodeOf.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: CodeOf(err), Message: err.Error()}
}
