package streams

import (
	"context"
	"errors"
	"strings"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// EmptyCallbackURL is a placeholder callback URL that accepts and discards
// callbacks. Longpoll only delivers events when some callback URL is set.
const EmptyCallbackURL = "https://audd.tech/empty/"

const docsStreams = "https://docs.audd.io/streams"

func userAgent() string { return "audd-cli/" + app.Version }

// Do runs a stream API call through api.Do, so a rejected token fetched
// by audd login is healed and retried once, and errors get the same codes,
// exit codes, and token hints as every other command (see MapError).
func Do[T any](ctx context.Context, a *app.App, call func(c *audd.Client) (T, error)) (T, error) {
	v, err := api.Do(ctx, a, call)
	if err != nil {
		return v, MapError(a, err)
	}
	return v, nil
}

// MapError turns an audd-go error into an *output.Error the way api.MapError
// does (with a hint that fits where the token came from), plus the streams
// docs link and a stream-monitoring hint when it is not enabled. Other
// errors pass through with any token in their text redacted (Go's
// connection errors include the request URL, which carries the token on
// some API calls).
func MapError(a *app.App, err error) error {
	if err == nil {
		return nil
	}
	var oe *output.Error
	if errors.As(err, &oe) {
		return forStreams(oe)
	}
	var apiErr *audd.AudDAPIError
	if errors.As(err, &apiErr) || errors.Is(err, audd.ErrConnection) || errors.Is(err, audd.ErrSerialization) {
		var src config.TokenSource
		if a != nil {
			src = api.TokenSource(a)
		}
		mapped := api.MapError(err, src)
		if errors.As(mapped, &oe) {
			return forStreams(oe)
		}
		return mapped
	}
	if msg := err.Error(); output.Redact(msg) != msg {
		return redactedError{err}
	}
	return err
}

// forStreams returns a copy of e with the streams docs link (for AudD
// errors), a hint about
// enabling stream monitoring for AudD errors 904 and 905, and any token in
// the message redacted.
func forStreams(e *output.Error) *output.Error {
	c := *e
	if c.APICode == 904 || c.APICode == 905 {
		c.Code, c.Exit = "not_enabled", output.ExitQuota
		c.Hint = "stream monitoring isn't enabled for this token; turn it on at " + api.DashboardURL + " or write to api@audd.io"
	}
	if c.DocsURL == "" && c.APICode != 0 {
		c.DocsURL = docsStreams
	}
	c.Message = output.Redact(c.Message)
	return &c
}

// redactedError is an error whose text has its token values hidden.
type redactedError struct{ err error }

func (e redactedError) Error() string { return output.Redact(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

// permanent reports whether retrying will not help (bad token, plan limits,
// invalid request).
func permanent(err error) bool {
	var oe *output.Error
	if errors.As(err, &oe) && (oe.Exit == output.ExitAuth || oe.Exit == output.ExitQuota || oe.Exit == output.ExitUsage) {
		return true
	}
	return errors.Is(err, audd.ErrAuthentication) || errors.Is(err, audd.ErrQuota) ||
		errors.Is(err, audd.ErrSubscription) || errors.Is(err, audd.ErrInvalidRequest)
}

// CallbackMissing reports whether the account has no callback URL (AudD
// error 19 from getCallbackUrl), which stops longpoll from delivering events.
// Other errors come back as the SDK returned them.
func CallbackMissing(ctx context.Context, c *audd.Client) (bool, error) {
	_, err := c.Streams().GetCallbackUrlContext(ctx)
	if err == nil {
		return false, nil
	}
	if IsNoCallback(err) {
		return true, nil
	}
	return false, err
}

// IsNoCallback reports whether err is AudD error 19 meaning the account has
// no callback URL.
func IsNoCallback(err error) bool {
	var apiErr *audd.AudDAPIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != 19 {
		return false
	}
	m := strings.ToLower(apiErr.Message)
	return strings.Contains(m, "internal") || strings.Contains(m, "callback")
}

// EnsureCallbackURL makes sure longpoll can deliver events: when the account
// has no callback URL it offers to set EmptyCallbackURL, and sets it only
// after confirmation (or --yes). It never changes an existing callback URL.
func EnsureCallbackURL(ctx context.Context, a *app.App) error {
	missing, err := Do(ctx, a, func(c *audd.Client) (bool, error) { return CallbackMissing(ctx, c) })
	if err != nil || !missing {
		return err
	}
	a.Out.Info("Live stream results need a callback URL on your account, and none is set.")
	q := "Set the callback URL to " + EmptyCallbackURL + " (a placeholder that discards callbacks)?"
	if err := a.Out.Confirm(q, a.Flags.Yes); err != nil {
		e := output.AsError(err)
		return &output.Error{
			Code:    "callback_url_required",
			Message: "live stream results need a callback URL on your account",
			Hint:    "audd streams callback set " + EmptyCallbackURL + "   (or pass --yes)",
			Exit:    e.Exit,
			DocsURL: docsStreams,
		}
	}
	if _, err := Do(ctx, a, func(c *audd.Client) (struct{}, error) {
		return struct{}{}, c.Streams().SetCallbackUrlContext(ctx, EmptyCallbackURL, nil)
	}); err != nil {
		return err
	}
	a.Out.Info("Callback URL set to %s.", EmptyCallbackURL)
	return nil
}
