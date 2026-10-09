package mcpclient

import (
	"errors"
	"fmt"
	"strings"

	"github.com/AudDMusic/audd-cli/internal/output"
)

const docsURL = "https://docs.audd.io/mcp"

var (
	errSessionGone  = errors.New("mcp session expired")
	errUnauthorized = errors.New("mcp access token rejected")
)

func loginRequired() *output.Error {
	return &output.Error{Code: "login_required", Message: "the AudD account service did not accept your sign-in",
		Hint: "audd login", Exit: output.ExitAuth, DocsURL: docsURL}
}

func scopeHint(scope string) string {
	if scope == "" {
		return "audd login"
	}
	return "audd auth refresh --scopes " + scope
}

func scopeMissing(tool, scope string) *output.Error {
	what := "this"
	if tool != "" {
		what = tool
	}
	msg := fmt.Sprintf("your sign-in does not allow %s", what)
	if scope != "" {
		msg += " (needs the " + scope + " permission)"
	}
	return &output.Error{Code: "scope_missing", Message: msg, Hint: scopeHint(scope), Exit: output.ExitAuth, DocsURL: docsURL}
}

func unexpected(format string, args ...any) *output.Error {
	return &output.Error{Code: "unexpected_response", Message: fmt.Sprintf(format, args...),
		Hint: "update audd (audd update); report persistent problems at https://github.com/AudDMusic/audd-cli/issues", Exit: output.ExitUnexpected}
}

// ToolError maps a tool result with isError set to a CLI error.
func ToolError(tool, text string) *output.Error {
	msg := strings.TrimSpace(text)
	if msg == "" {
		msg = "the request failed"
	}
	l := strings.ToLower(msg)
	e := &output.Error{Code: "account_error", Message: "AudD account service: " + msg, Exit: output.ExitUnexpected}
	switch {
	case strings.Contains(l, "scope") || strings.Contains(l, "permission") || strings.Contains(l, "forbidden"):
		s := ToolScopes[tool]
		return &output.Error{Code: "scope_missing", Message: e.Message, Hint: scopeHint(s), Exit: output.ExitAuth, DocsURL: docsURL}
	// Sign-in problems before the generic "invalid ..." argument match:
	// "invalid token" is an auth error, not a bad argument.
	case containsAny(l, "unauthorized", "unauthenticated", "not authenticated", "sign in again", "invalid token", "invalid_token",
		"invalid access token", "expired token", "token expired", "token has expired", "token is invalid", "token was revoked", "invalid credentials"):
		e.Code, e.Exit, e.Hint, e.DocsURL = "login_required", output.ExitAuth, "audd login", docsURL
	case strings.Contains(l, "rate limit") || strings.Contains(l, "too many requests") || strings.Contains(l, "temporarily"):
		e.Code, e.Exit, e.Retryable = "server", output.ExitNetwork, true
	case strings.Contains(l, "invalid") || strings.Contains(l, "must be") || strings.Contains(l, "unknown") || strings.Contains(l, "not found") || strings.Contains(l, "required"):
		e.Code, e.Exit = "invalid_argument", output.ExitUsage
	}
	return e
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
