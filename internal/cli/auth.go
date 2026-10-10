package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/account"
	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/mcpclient"
	"github.com/AudDMusic/audd-cli/internal/oauth"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/secrets"
	"github.com/AudDMusic/audd-cli/internal/streams"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		a.Account = func() (account.Backend, error) { return newAccountBackend(a) }
		login := newLoginCmd(a, "login")
		login.GroupID = GroupAccount
		login.Short = "Sign in to your AudD account (same as audd auth login)"
		login.Annotations = aliasOf(login.Annotations, "auth login")
		logout := newLogoutCmd(a, "logout")
		logout.Hidden = true
		logout.Short += " (same as audd auth logout)"
		logout.Annotations = aliasOf(logout.Annotations, "auth logout")
		whoami := newStatusCmd(a, "whoami")
		whoami.Hidden = true
		whoami.Short += " (same as audd auth status)"
		whoami.Annotations = aliasOf(whoami.Annotations, "auth status")
		root.AddCommand(newAuthCmd(a), login, logout, whoami)
	})
}

// accountServiceURL is the AudD account service (MCP server).
// AUDD_MCP_URL points the CLI at another server. It is intentionally
// supported but left out of the user docs: tests (internal/e2e) and staging
// or self-hosted proxies use it.
func accountServiceURL() string {
	if u := strings.TrimSpace(os.Getenv("AUDD_MCP_URL")); u != "" {
		return u
	}
	return oauth.DefaultResourceURL
}

// accountHTTPClient is the HTTP client for the sign-in and account
// services; with --debug it logs each request's method, URL, status, and
// time (never headers or bodies, which carry tokens).
func accountHTTPClient(a *app.App, timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if a.Flags.Debug {
		c.Transport = debugTransport{a: a, next: http.DefaultTransport}
	}
	return c
}

type debugTransport struct {
	a    *app.App
	next http.RoundTripper
}

func (d debugTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	u := *r.URL
	if q := u.Query(); q.Has("code") || q.Has("state") || q.Has("code_challenge") {
		u.RawQuery = "…"
	}
	resp, err := d.next.RoundTrip(r)
	if err != nil {
		d.a.Out.Warn("debug: %s %s: %v", r.Method, u.String(), err)
		return nil, err
	}
	d.a.Out.Warn("debug: %s %s -> %d (%s)", r.Method, u.String(), resp.StatusCode, time.Since(start).Round(time.Millisecond))
	return resp, nil
}

// aliasOf marks a top-level shortcut with the command it runs, for
// audd commands --json.
func aliasOf(ann map[string]string, target string) map[string]string {
	if ann == nil {
		ann = map[string]string{}
	}
	ann[agent.AnnotationAliasOf] = target
	return ann
}

func newOAuthFor(a *app.App, p *config.Profile, opts ...oauth.Option) *oauth.Client {
	return oauth.New(p, a.Secrets, a.Out, append([]oauth.Option{
		oauth.WithHTTPClient(accountHTTPClient(a, 30*time.Second)),
		oauth.WithResourceURL(accountServiceURL()),
		oauth.WithSaveConfig(func() error { return a.Cfg.Save() }),
		oauth.WithNow(a.Now),
	}, opts...)...)
}

func newOAuth(a *app.App) *oauth.Client { return newOAuthFor(a, a.Profile) }

// newAccountBackend is the App.Account factory: the AudD MCP server,
// authenticated with the profile's sign-in.
func newAccountBackend(a *app.App) (account.Backend, error) {
	if a.Profile == nil || a.Secrets == nil {
		return nil, app.NotImplemented("account access before setup")
	}
	oc := newOAuth(a)
	if !oc.LoggedIn() {
		return nil, notSignedIn(a)
	}
	mc := mcpclient.New(oc.ResourceURL(),
		func(ctx context.Context) (string, error) {
			t, err := oc.Token(ctx)
			if err != nil {
				return "", err
			}
			return t.AccessToken, nil
		},
		mcpclient.WithRefresh(func(ctx context.Context) (string, error) {
			t, err := oc.Refresh(ctx)
			if err != nil {
				return "", err
			}
			return t.AccessToken, nil
		}),
		mcpclient.WithSessionCache(filepath.Join(config.CacheDir(), "mcp-session-"+config.FileSafe(a.Profile.Name))),
		mcpclient.WithUserAgent("audd-cli/"+app.Version),
		mcpclient.WithHTTPClient(accountHTTPClient(a, 60*time.Second)),
	)
	return account.NewMCP(mc), nil
}

func notSignedIn(a *app.App) *output.Error {
	hint := "audd login"
	if a.Profile != nil && a.Profile.Name != config.DefaultProfile {
		hint = "audd login --profile " + a.Profile.Name
	}
	return &output.Error{Code: "login_required", Message: "this needs you to sign in to your AudD account",
		Hint: hint, Exit: output.ExitAuth}
}

func newAuthCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Sign in, sign out, and switch profiles",
		Long: `Sign in to your AudD account to see usage, plans, and billing, and to fetch
your API token without copying it by hand.

On a desktop, signing in opens your browser. Over SSH, in containers, and
for scripts and agents, audd shows a page and a short code instead:
open the page on any device, check the code, and approve.

Recognition does not need a sign-in: set AUDD_API_TOKEN, pass --token, or run
audd config set token.`,
		GroupID: GroupAccount,
		Example: `  audd auth login
  audd auth status
  audd auth login --profile work
  audd auth switch work
  audd auth logout --all`,
	}
	cmd.AddCommand(newLoginCmd(a, "login"), newLogoutCmd(a, "logout"), newStatusCmd(a, "status"), newRefreshCmd(a), newSwitchCmd(a))
	return cmd
}

type loginView struct {
	Profile       string   `json:"profile"`
	Account       string   `json:"account"`
	Scopes        []string `json:"scopes"`
	APITokenSaved bool     `json:"api_token_saved"`
	TokenSource   string   `json:"token_source"`
}

func newLoginCmd(a *app.App, use string) *cobra.Command {
	var scopes []string
	var noBrowser, device, viaBrowser bool
	var complete string
	cmd := &cobra.Command{
		Use:   use,
		Short: "Sign in to your AudD account",
		Long: `Sign in to your AudD account. There are two ways to approve the sign-in,
and audd picks one for you:

  Browser: on a desktop (macOS, Windows, or Linux with a display), audd
  opens your browser, prints the sign-in URL, and waits. If the browser is
  on another machine, approve there, copy the address it was sent to (it
  starts with http://127.0.0.1), and paste it into the terminal.

  Code: over SSH, in a container, or without a display, audd prints a
  page and a code. Open the page on any device (your phone works), check
  that it shows the same code, and approve. The code expires in 15 minutes.

To choose the method yourself, pass --browser or --device.

Without a terminal (scripts and agents), audd uses a code and prints a
JSON login_pending record with verification_uri, verification_uri_complete,
user_code, expires_in_seconds, and interval, then keeps waiting until the
sign-in is approved, denied, or expires. Agents show the user the URL and
the code. With --format json or csv the record goes to stderr, so stdout
keeps one document.

A browser sign-in that is waiting in another process can be finished with:
  audd auth login --complete '<redirect URL>'

The sign-in asks for everything audd uses: to read your profile,
account, usage, billing, and API token, to create payment links, and to
rotate the API token. On the approval page you can untick any of them;
audd auth status shows what was approved. Commands that need one you
unticked (payment links, rotating the API token) ask for it again when you
use them.

After signing in, audd saves your API token for this profile.
Tokens from --token, AUDD_API_TOKEN, and audd config set token take
precedence over it.`,
		Example: `  audd login
  audd login --device
  audd login --browser
  audd login --profile work
  audd auth login --complete 'http://127.0.0.1:53682/callback?code=…&state=…'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			oc := newOAuth(a)
			ctx := cmd.Context()
			var tok *oauth.Tokens
			var err error
			if complete != "" {
				if device {
					return output.Errf(output.ExitUsage, "invalid_argument", "",
						"--complete finishes a browser sign-in; it cannot be used with --device")
				}
				tok, err = oc.CompletePending(ctx, complete)
			} else {
				if !a.Out.Options().StdinTTY {
					a.Out.SetStreaming()
				}
				method := oauth.MethodAuto
				switch {
				case device:
					method = oauth.MethodDevice
				case viaBrowser:
					method = oauth.MethodBrowser
				}
				tok, err = oc.Login(ctx, oauth.LoginOptions{
					Scopes:      oauth.Union(oauth.DefaultScopes, scopes),
					KeepGranted: true, // keep permissions approved earlier
					Method:      method,
					NoBrowser:   noBrowser,
					In:          a.In,
				})
			}
			if err != nil {
				return err
			}
			return afterLogin(ctx, a, oc, tok)
		},
	}
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "more permissions to ask for, comma-separated (the default already includes every permission audd uses)")
	cmd.Flags().BoolVar(&device, "device", false, "sign in with a code on any device")
	cmd.Flags().BoolVar(&viaBrowser, "browser", false, "sign in with a browser (on this machine, or paste the address back)")
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser on this machine (the URL is printed either way)")
	cmd.Flags().StringVar(&complete, "complete", "", "finish a waiting browser sign-in with the address the browser was sent to")
	cmd.MarkFlagsMutuallyExclusive("device", "browser")
	return cmd
}

// afterLogin stores the account email and the API token, then reports. On
// a terminal the sign-in's waiting block is replaced by a one-line success
// message; warnings print after it.
func afterLogin(ctx context.Context, a *app.App, oc *oauth.Client, tok *oauth.Tokens) error {
	var notes []string
	defer func() {
		if b := a.Out.Transient(); b != nil {
			b.Keep() // something failed: leave the instructions on screen
		}
		oc.FlushNotes()
		for _, n := range notes {
			a.Out.Warn("%s", n)
		}
	}()
	view := loginView{Profile: a.Profile.Name, Scopes: tok.Scopes}
	b, err := a.Account()
	if err != nil {
		return err
	}
	if tok.HasScopes("profile:read") {
		if p, err := b.Profile(ctx); err == nil {
			a.Profile.Account = p.Email
			view.Account = p.Email
		} else {
			notes = append(notes, "Signed in, but could not read the account email: "+output.AsError(err).Message)
		}
	}
	if tok.HasScopes("token:read") {
		if t, err := b.APIToken(ctx); err == nil {
			if err := a.Secrets.Set(a.Profile.Name, "login_api_token", t); err != nil {
				return err
			}
			view.APITokenSaved = true
			restartRecorder(a)
		} else {
			notes = append(notes, "Signed in, but could not fetch the API token: "+output.AsError(err).Message)
		}
	}
	if err := a.Cfg.Save(); err != nil {
		return err
	}
	_, src, _ := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
	view.TokenSource = string(src)
	erased := false
	if b := a.Out.Transient(); b != nil {
		erased = b.Clear()
	}
	return a.Out.Result(view, func(w io.Writer) {
		if erased {
			fmt.Fprintln(w, oauth.SignedInLine(a.Out.Styles(), view.Account, view.Profile))
		} else {
			who := view.Account
			if who == "" {
				who = "your AudD account"
			}
			fmt.Fprintf(w, "Signed in as %s (profile %s).\n", who, view.Profile)
		}
		if view.APITokenSaved {
			if src == config.SourceLogin {
				fmt.Fprintln(w, "Saved your API token; audd uses it for recognition.")
			} else {
				fmt.Fprintf(w, "Saved your API token. The token from the %s takes precedence over it.\n", src.Describe())
			}
		}
	})
}

type statusView struct {
	Profile           string                 `json:"profile"`
	SignedIn          bool                   `json:"signed_in"`
	Account           string                 `json:"account,omitempty"`
	SignInMethods     []account.SignInMethod `json:"sign_in_methods,omitempty"`
	Scopes            []string               `json:"scopes"`
	ScopesNotGranted  []string               `json:"scopes_not_granted"`
	AccessExpiresAt   *string                `json:"access_token_expires_at"`
	TokenSource       string                 `json:"token_source"`
	TokenSourceDetail string                 `json:"token_source_description"`
	APIToken          *string                `json:"api_token"`
}

func newStatusCmd(a *app.App, use string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: "Show the signed-in account and which API token is in use",
		Long: `Show the signed-in account (email and sign-in methods), the permissions
approved and unticked at sign-in, and the API token audd uses, masked, with
where it comes from: --token, AUDD_API_TOKEN, audd config set token, or
audd login.

Exits with code 3 when there is neither a sign-in nor an API token.`,
		Example: "  audd auth status\n  audd whoami --format json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			oc := newOAuth(a)
			tok, err := oc.Stored()
			if err != nil {
				return err
			}
			v := statusView{Profile: a.Profile.Name, Scopes: []string{}, ScopesNotGranted: []string{}}
			if tok != nil && tok.HasScopes("profile:read") {
				// Ask the account for the current email and sign-in methods;
				// without a connection, show what was saved at sign-in.
				if p, err := statusProfile(cmd.Context(), a); err == nil {
					v.SignInMethods = p.SignInMethods
					if p.Email != "" && p.Email != a.Profile.Account {
						a.Profile.Account = p.Email
						_ = a.Cfg.Save()
					}
				} else {
					a.Out.Warn("Could not read the account profile: %s", output.AsError(err).Message)
				}
				// Reading the profile may have renewed the sign-in.
				if t, err := oc.Stored(); err == nil && t != nil {
					tok = t
				}
			}
			if tok != nil {
				v.SignedIn = true
				v.Account = a.Profile.Account
				v.Scopes = tok.Scopes
				v.ScopesNotGranted = notGranted(tok)
				if !tok.Expiry.IsZero() {
					s := tok.Expiry.UTC().Format(time.RFC3339)
					v.AccessExpiresAt = &s
				}
			}
			t, src, err := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
			if err != nil {
				return err
			}
			v.TokenSource, v.TokenSourceDetail = string(src), src.Describe()
			if t != "" {
				m := config.MaskToken(t)
				v.APIToken = &m
			}
			err = a.Out.Result(v, func(w io.Writer) {
				st := a.Out.Styles()
				var rows [][2]string
				row := func(k, val string) {
					if k != "" {
						k = st.Key.Render(k)
					}
					rows = append(rows, [2]string{k, val})
				}
				row("Profile", v.Profile)
				if v.SignedIn {
					who := v.Account
					if who == "" {
						who = "signed in"
					}
					row("Account", who)
					signInRows(row, v.SignInMethods)
					row("Permissions", strings.Join(v.Scopes, " "))
					if len(v.ScopesNotGranted) > 0 {
						row("Not approved", fmt.Sprintf("%s (unticked; audd auth refresh --scopes %s)",
							strings.Join(v.ScopesNotGranted, " "), strings.Join(v.ScopesNotGranted, ",")))
					}
					if !tok.Expiry.IsZero() {
						row("Access expires", accessExpiry(tok.Expiry, a.Now()))
					}
				} else {
					row("Account", "not signed in (audd login)")
				}
				if v.APIToken != nil {
					row("API token", fmt.Sprintf("%s (%s)", *v.APIToken, v.TokenSourceDetail))
				} else {
					row("API token", "none (audd login, or audd config set token)")
				}
				// Long values (the permissions) wrap at spaces to fit the
				// terminal, aligned with the other values.
				output.WriteKeyValues(w, rows, a.Out.StdoutWidth())
			})
			if err != nil {
				return err
			}
			if !v.SignedIn && v.APIToken == nil {
				e := notSignedIn(a)
				e.Code, e.Message = "no_token", "not signed in, and no API token is set"
				e.Hint = api.NoTokenHint
				return e
			}
			return nil
		},
	}
}

// notGranted lists the scopes the sign-in asked for that the user unticked.
func notGranted(t *oauth.Tokens) []string {
	out := []string{}
	for _, s := range t.Requested {
		if !t.HasScopes(s) {
			out = append(out, s)
		}
	}
	return out
}

// accessExpiry describes when the short-lived access token expires, in
// local time. The CLI renews it on its own, so expiry needs no action.
func accessExpiry(exp, now time.Time) string {
	when := exp.Local().Format("2006-01-02 15:04 MST")
	d := exp.Sub(now)
	if d <= 0 {
		return when + " (expired; renewed on next use)"
	}
	m := int(d.Round(time.Minute) / time.Minute)
	rel := fmt.Sprintf("%d min", m)
	switch {
	case m == 0:
		rel = "under a minute"
	case m >= 60:
		rel = fmt.Sprintf("%d h %d min", m/60, m%60)
	}
	return when + " (in " + rel + ", renewed automatically)"
}

func newRefreshCmd(a *app.App) *cobra.Command {
	var scopes []string
	var device, viaBrowser bool
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Renew the sign-in, or approve extra permissions",
		Long: `Renew the sign-in now. With --scopes, sign in again to approve permissions
you unticked before, such as billing:pay for payment links or token:write
for audd token rotate. The new sign-in asks for everything approved now
plus the new permissions, in the browser or with a code, like audd login.
Commands that need an unticked permission ask for it on their own.`,
		Example: "  audd auth refresh\n  audd auth refresh --scopes billing:pay\n  audd auth refresh --scopes token:write --device",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			method := oauth.MethodAuto
			switch {
			case device:
				method = oauth.MethodDevice
			case viaBrowser:
				method = oauth.MethodBrowser
			}
			oc := newOAuthFor(a, a.Profile, oauth.WithMethod(method))
			var tok *oauth.Tokens
			var err error
			if len(scopes) > 0 {
				tok, err = oc.EnsureScopes(cmd.Context(), oauth.Union(scopes, nil)...)
			} else {
				tok, err = oc.Refresh(cmd.Context())
			}
			if err != nil {
				return err
			}
			v := statusView{Profile: a.Profile.Name, SignedIn: true, Account: a.Profile.Account, Scopes: tok.Scopes, ScopesNotGranted: notGranted(tok)}
			if !tok.Expiry.IsZero() {
				s := tok.Expiry.UTC().Format(time.RFC3339)
				v.AccessExpiresAt = &s
			}
			_, src, _ := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
			v.TokenSource, v.TokenSourceDetail = string(src), src.Describe()
			return a.Out.Result(v, func(w io.Writer) {
				fmt.Fprintf(w, "Sign-in renewed (profile %s).\nPermissions: %s\n", v.Profile, strings.Join(v.Scopes, " "))
			})
		},
	}
	cmd.Flags().StringSliceVar(&scopes, "scopes", nil, "permissions to add, such as billing:pay or token:write (comma-separated)")
	cmd.Flags().BoolVar(&device, "device", false, "approve with a code on any device")
	cmd.Flags().BoolVar(&viaBrowser, "browser", false, "approve in a browser")
	cmd.MarkFlagsMutuallyExclusive("device", "browser")
	return cmd
}

func newSwitchCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:     "switch <profile>",
		Short:   "Make another profile the default",
		Example: "  audd auth switch work",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" || config.FileSafe(name) != name {
				return output.Errf(output.ExitUsage, "invalid_argument", "", "profile names use letters, digits, - and _")
			}
			existed := profileExists(a, name)
			a.Cfg.ActiveProfile = name
			a.Cfg.Profile(name)
			if err := a.Cfg.Save(); err != nil {
				return err
			}
			if os.Getenv("AUDD_PROFILE") != "" {
				a.Out.Warn("Note: AUDD_PROFILE is set and takes precedence.")
			}
			return a.Out.Result(map[string]any{"active_profile": name, "existing": existed}, func(w io.Writer) {
				fmt.Fprintf(w, "Now using profile %s.\n", name)
				if !existed {
					a.Out.Info("It is a new profile; sign in with audd login.")
				}
			})
		},
	}
}

// profileExists reports whether a profile was used before: it has
// settings, a sign-in, or a saved token, or it is the default or active one.
// Profiles with no settings are not written to config.toml.
func profileExists(a *app.App, name string) bool {
	if _, ok := a.Cfg.Profiles[name]; ok || name == config.DefaultProfile || name == a.Cfg.ActiveProfile {
		return true
	}
	if a.Secrets == nil {
		return false
	}
	for _, key := range []string{"oauth", "api_token", "login_api_token"} {
		if v, err := a.Secrets.Get(name, key); err == nil && v != "" {
			return true
		}
	}
	return false
}

type logoutItem struct {
	Profile      string `json:"profile"`
	WasSignedIn  bool   `json:"was_signed_in"`
	KeptAPIToken bool   `json:"kept_api_token"`
	// StoppedRecorder reports that the profile's background stream
	// recorder, which used the token fetched by audd login, was stopped.
	StoppedRecorder bool `json:"stopped_recorder"`
}

func newLogoutCmd(a *app.App, use string) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   use,
		Short: "Sign out and remove the saved sign-in",
		Long: `Sign out: revoke the sign-in on the server and remove it from this
machine, along with the API token that audd login saved. A token you set with
audd config set token stays. The API token is not rotated. A
background stream recorder that used the token from the sign-in is stopped.`,
		Example: "  audd logout\n  audd auth logout --profile work\n  audd auth logout --all",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			names := []string{a.Profile.Name}
			if all {
				names = a.Cfg.ProfileNames()
			}
			var items []logoutItem
			for _, n := range names {
				p := a.Cfg.Profile(n)
				oc := newOAuthFor(a, p)
				it := logoutItem{Profile: n, WasSignedIn: oc.LoggedIn()}
				_, src, _ := config.ResolveToken("", n, a.Secrets)
				if err := oc.Logout(cmd.Context()); err != nil {
					return err
				}
				if src == config.SourceLogin {
					// The recorder would keep using a token this
					// machine no longer holds.
					stopped, err := streams.StopBackground(n)
					if err != nil {
						a.Out.Warn("Could not stop the background stream recorder for profile %s: %v", n, err)
					}
					it.StoppedRecorder = stopped
				}
				if t, err := a.Secrets.Get(n, "api_token"); err == nil && t != "" {
					it.KeptAPIToken = true
				} else if err != nil && !errors.Is(err, secrets.ErrNotFound) {
					return err
				}
				items = append(items, it)
			}
			var v any = items
			if !all {
				v = items[0]
			}
			return a.Out.Result(v, func(w io.Writer) {
				for _, it := range items {
					if it.WasSignedIn {
						fmt.Fprintf(w, "Signed out of profile %s.\n", it.Profile)
					} else {
						fmt.Fprintf(w, "Profile %s was not signed in.\n", it.Profile)
					}
					if it.StoppedRecorder {
						a.Out.Info("Stopped the background stream recorder for profile %s, which used the token from your sign-in.", it.Profile)
					}
					if it.KeptAPIToken {
						a.Out.Info("Kept the API token set with audd config set token for profile %s (remove it with audd config unset token).", it.Profile)
					}
				}
				if os.Getenv("AUDD_API_TOKEN") != "" {
					a.Out.Info("AUDD_API_TOKEN is still set in your environment.")
				}
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "sign out of every profile")
	return cmd
}

// statusProfile reads the account profile for audd auth status, giving up
// after a short wait so status stays quick without a connection.
func statusProfile(ctx context.Context, a *app.App) (*account.Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := a.Account()
	if err != nil {
		return nil, err
	}
	return b.Profile(ctx)
}
