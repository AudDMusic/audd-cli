package output

import (
	"errors"
	"fmt"
)

// Error is the only error type commands return for expected failures.
// Printer.Error renders it for humans or machines and returns Exit.
type Error struct {
	Code      string // stable snake_case: "no_token", "token_rejected", "login_required", "quota_exceeded", "limit_required", "confirmation_required", "max_requests_reached", "invalid_argument", "network", "server", "no_match", "missing_tool", ...
	APICode   int    // AudD API error code when applicable, else 0
	Message   string
	Hint      string // the exact next command when possible
	Retryable bool
	Exit      int
	DocsURL   string
}

func (e *Error) Error() string { return e.Message }

// Errf builds an *Error with the given exit code, stable code, and hint.
func Errf(exit int, code, hint, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Hint: hint, Exit: exit}
}

// AsError returns err as an *Error, wrapping unknown errors as "unexpected".
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{
		Code:    "unexpected",
		Message: err.Error(),
		Hint:    "run again with --debug; report persistent problems at https://github.com/AudDMusic/audd-cli/issues",
		Exit:    ExitUnexpected,
	}
}

// ExitCode returns the process exit code for err (0 for nil).
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	e := AsError(err)
	if e.Exit == 0 {
		return ExitUnexpected
	}
	return e.Exit
}
