// Package api builds AudD API clients for the CLI: it resolves the token,
// configures the SDK's HTTP transport, heals a login-fetched token that the
// API rejects, and turns SDK errors into the CLI's error contract.
package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/AudDMusic/audd-go"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

// DashboardURL is where people get and manage API tokens.
const DashboardURL = "https://dashboard.audd.io"

// NoTokenHint is the fix shown with every no_token error.
const NoTokenHint = "audd login, or audd config set token <your-api-token> (get one at " + DashboardURL + ")"

// NewClientFactory returns the App.APIClient factory. Each call resolves the
// token (--token > AUDD_API_TOKEN > audd config > audd login) and returns a
// client with the given extra options; with no token it returns a no_token
// error (exit 3).
func NewClientFactory(a *app.App, opts ...audd.Option) func() (*audd.Client, error) {
	var (
		once sync.Once
		hc   *http.Client
	)
	return func() (*audd.Client, error) {
		tok, _, err := resolve(a)
		if err != nil {
			return nil, err
		}
		if tok == "" {
			return nil, &output.Error{
				Code:    "no_token",
				Message: "no AudD API token is set",
				Hint:    NoTokenHint,
				Exit:    output.ExitAuth,
			}
		}
		once.Do(func() { hc = HTTPClient(a) })
		return newClient(a, tok, hc, opts...), nil
	}
}

func newClient(a *app.App, tok string, hc *http.Client, opts ...audd.Option) *audd.Client {
	return audd.NewClient(tok, append([]audd.Option{
		audd.WithHTTPClient(hc),
		audd.WithOnDeprecation(func(msg string) {
			if a.Out != nil {
				a.Out.Info("Note from AudD: %s", msg)
			}
		}),
	}, opts...)...)
}

func resolve(a *app.App) (string, config.TokenSource, error) {
	profile := config.DefaultProfile
	if a.Profile != nil && a.Profile.Name != "" {
		profile = a.Profile.Name
	}
	tok, src, err := config.ResolveToken(a.Flags.Token, profile, a.Secrets)
	if err != nil {
		return "", config.SourceNone, output.AsError(err)
	}
	return tok, src, nil
}

func profileName(a *app.App) string {
	if a.Profile != nil && a.Profile.Name != "" {
		return a.Profile.Name
	}
	return config.DefaultProfile
}

// healMu serializes token healing so concurrent batch workers fetch the new
// token once.
var healMu sync.Mutex

// Do runs call with a client from a.APIClient. When the API rejects the
// token as invalid, and that token was fetched by `audd login`, Do fetches
// the current token from the account, stores it, and retries once (the API
// rejects a bad token before doing any metered work). Tokens given with
// --token, AUDD_API_TOKEN, or `audd config` are never replaced. Errors come
// back as *output.Error (see MapError).
func Do[T any](ctx context.Context, a *app.App, call func(c *audd.Client) (T, error)) (T, error) {
	return do(ctx, a, a.APIClient, call)
}

// DoOnce is Do with a client from a.APIClientOnce, which never retries a
// request on its own: for calls that may be billed or change the account
// even when the response is lost.
func DoOnce[T any](ctx context.Context, a *app.App, call func(c *audd.Client) (T, error)) (T, error) {
	factory := a.APIClientOnce
	if factory == nil {
		factory = a.APIClient
	}
	return do(ctx, a, factory, call)
}

func do[T any](ctx context.Context, a *app.App, factory func() (*audd.Client, error), call func(c *audd.Client) (T, error)) (T, error) {
	var zero T
	c, err := factory()
	if err != nil {
		return zero, err
	}
	v, err := call(c)
	if err == nil {
		return v, nil
	}
	_, src, _ := resolve(a)
	if !IsAuthRejected(err) || src != config.SourceLogin {
		return zero, MapError(err, src)
	}
	healed, herr := heal(ctx, a, c, true)
	if herr != nil {
		return zero, herr
	}
	if !healed {
		return zero, MapError(err, src)
	}
	v, err = call(c)
	if err != nil {
		return zero, MapError(err, src)
	}
	return v, nil
}

// TokenSource reports where the API token in use comes from.
func TokenSource(a *app.App) config.TokenSource {
	_, src, _ := resolve(a)
	return src
}

// HealLogin is the token healing Do performs, for long-running loops that
// hold their own client (the stream recorder): when err is the API
// rejecting a token fetched by audd login, it fetches the account's current
// token, stores it, and sets it on c. It reports whether c now has a
// different token to retry with. Tokens from --token, AUDD_API_TOKEN, or
// audd config are never replaced. It does not restart the background
// recorder.
func HealLogin(ctx context.Context, a *app.App, c *audd.Client, err error) (bool, error) {
	if !IsAuthRejected(err) || TokenSource(a) != config.SourceLogin {
		return false, nil
	}
	return heal(ctx, a, c, false)
}

// heal swaps c's token for the account's current one. It reports false when
// there is nothing better to try. With restartRecorder, a running
// background stream recorder is restarted to use the new token.
func heal(ctx context.Context, a *app.App, c *audd.Client, restartRecorder bool) (bool, error) {
	healMu.Lock()
	defer healMu.Unlock()
	old := c.APIToken()
	// Another worker may have healed it already.
	if tok, src, _ := resolve(a); src == config.SourceLogin && tok != "" && tok != old {
		_ = c.SetAPIToken(tok)
		return true, nil
	}
	acct, err := a.Account()
	if err != nil {
		return false, nil
	}
	tok, err := acct.APIToken(ctx)
	if err != nil {
		// A revoked or expired login is reported as such (exit 3).
		var oe *output.Error
		if errors.As(err, &oe) && oe.Exit == output.ExitAuth {
			return false, oe
		}
		return false, nil
	}
	if tok == "" || tok == old {
		return false, nil
	}
	if err := a.Secrets.Set(profileName(a), "login_api_token", tok); err != nil {
		return false, output.AsError(err)
	}
	if err := c.SetAPIToken(tok); err != nil {
		return false, nil
	}
	if a.Out != nil {
		a.Out.Info("The API token from your login was rejected; fetched the current one from your AudD account and retried.")
	}
	if !restartRecorder {
		return true, nil
	}
	// The background stream recorder still uses the old token.
	if restarted, err := app.RestartRecorder(a); err == nil && restarted && a.Out != nil {
		a.Out.Info("Restarted the stream recorder with the new token.")
	}
	return true, nil
}
