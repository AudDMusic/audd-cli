package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// ExitInterrupted is the exit code after Ctrl-C (128 + SIGINT).
const ExitInterrupted = 130

// IsAuthRejected reports whether the API rejected the token itself
// (invalid, missing, or disabled), as opposed to quota or plan limits.
func IsAuthRejected(err error) bool {
	return errors.Is(err, audd.ErrAuthentication)
}

// tokenHint tells people how to fix a rejected token from a given source.
func tokenHint(src config.TokenSource) string {
	switch src {
	case config.SourceFlag:
		return "check the --token value; get your token at " + DashboardURL
	case config.SourceEnv:
		return "check AUDD_API_TOKEN; get your token at " + DashboardURL
	case config.SourceConfig:
		return "audd config set token <your token> (get it at " + DashboardURL + ")"
	case config.SourceLogin:
		return "audd login"
	}
	return "audd login, or get your token at " + DashboardURL
}

// MapError converts an SDK error into an *output.Error with the right exit
// code. src is where the token came from (for the hint on auth errors).
// *output.Error values pass through unchanged.
func MapError(err error, src config.TokenSource) error {
	if err == nil {
		return nil
	}
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe
	}
	if errors.Is(err, context.Canceled) {
		return &output.Error{Code: "interrupted", Message: "interrupted", Exit: ExitInterrupted}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &output.Error{Code: "network", Message: "the request timed out", Hint: "try again; check your connection", Retryable: true, Exit: output.ExitNetwork}
	}

	var cc *audd.AudDCustomCatalogAccessError
	if errors.As(err, &cc) {
		return &output.Error{
			Code:    "not_enabled",
			APICode: cc.ErrorCode,
			Message: "adding songs to a custom catalog needs custom catalog access, which isn't enabled for this token (this command adds songs; it does not recognize them)",
			Hint:    "to recognize music, use audd recognize; to request custom catalog access, write to api@audd.io",
			Exit:    output.ExitQuota,
		}
	}

	var ae *audd.AudDAPIError
	if errors.As(err, &ae) {
		return mapAPIError(ae, src)
	}
	var ce *audd.AudDConnectionError
	if errors.As(err, &ce) {
		msg := "could not reach AudD"
		if ce.Cause != nil {
			msg += ": " + Redact(ce.Cause.Error())
		}
		return &output.Error{Code: "network", Message: msg, Hint: "check your connection (and HTTPS_PROXY, if you use a proxy) and try again", Retryable: true, Exit: output.ExitNetwork}
	}
	var se *audd.AudDSerializationError
	if errors.As(err, &se) {
		return &output.Error{Code: "server", Message: "AudD sent a response the CLI could not read: " + se.Message, Hint: "try again; if it keeps happening, report it at https://github.com/AudDMusic/audd-cli/issues", Retryable: true, Exit: output.ExitNetwork}
	}
	return output.AsError(fmt.Errorf("%s", strings.TrimPrefix(Redact(err.Error()), "audd: ")))
}

func mapAPIError(ae *audd.AudDAPIError, src config.TokenSource) *output.Error {
	msg := ae.Message
	if msg == "" {
		msg = fmt.Sprintf("AudD returned error %d", ae.ErrorCode)
	}
	e := &output.Error{APICode: ae.ErrorCode, Message: msg}
	switch code := ae.ErrorCode; {
	case code == 900 || code == 901 || code == 903:
		e.Code, e.Exit, e.Hint, e.DocsURL = "token_rejected", output.ExitAuth, tokenHint(src), DashboardURL
	case code == 902:
		e.Code, e.Exit, e.Hint = "quota_exceeded", output.ExitQuota, "audd usage shows what's left; audd billing plans lists plans"
	case code == 904 || code == 905:
		e.Code, e.Exit, e.Hint = "not_enabled", output.ExitQuota, "this feature isn't enabled for your token; see "+DashboardURL+" or write to api@audd.io"
	case code == 610:
		e.Code, e.Exit, e.Hint = "stream_limit", output.ExitQuota, "remove a stream (audd streams remove <id>) or upgrade your plan at "+DashboardURL
	case code == 611:
		e.Code, e.Exit, e.Retryable = "rate_limited", output.ExitQuota, true
	case code == 907:
		e.Code, e.Exit, e.Hint = "not_released", output.ExitQuota, "for early access, write to enterprise@audd.io"
	case code == 300 || code == 400 || code == 500:
		e.Code, e.Exit = "invalid_audio", output.ExitUsage
		if code == 400 {
			e.Hint = "send a shorter clip with --at, or use --enterprise --limit N for long files"
		}
	case code == 50 || code == 51 || (code >= 600 && code <= 602) || (code >= 700 && code <= 702) || code == 906 || code == 1000:
		e.Code, e.Exit = "invalid_request", output.ExitUsage
	case code == 19 || code == 31337:
		// 19 also covers maintenance, but mostly means the request was
		// refused (blocked, test-token scope, missing stream callback URL):
		// retrying the same request does not help.
		e.Code, e.Exit = "blocked", output.ExitQuota
	case code == 20:
		e.Code, e.Exit, e.Hint = "update_required", output.ExitQuota, "audd update"
	default: // 100, HTTP errors without a JSON body, and anything new
		e.Code, e.Exit, e.Retryable = "server", output.ExitNetwork, true
		if ae.ErrorCode == 0 && ae.HTTPStatus != 0 {
			e.Message = fmt.Sprintf("AudD returned HTTP %d", ae.HTTPStatus)
		}
	}
	if e.DocsURL == "" && e.APICode != 0 {
		e.DocsURL = APIDocsURL
	}
	return e
}

// APIDocsURL and EnterpriseDocsURL are the docs pages that list the API's
// errors, linked from errors AudD returned.
const (
	APIDocsURL        = "https://docs.audd.io/"
	EnterpriseDocsURL = "https://docs.audd.io/enterprise"
)
