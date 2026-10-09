package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/config"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newTokenCmd(a))
	})
}

type tokenView struct {
	Token             string `json:"token"`
	Masked            bool   `json:"masked"`
	Source            string `json:"source"`
	SourceDescription string `json:"source_description"`
}

// tokenSourceSentence says where the token in use came from, for people.
func tokenSourceSentence(src config.TokenSource) string {
	switch src {
	case config.SourceFlag:
		return "From the --token flag."
	case config.SourceEnv:
		return "From the AUDD_API_TOKEN environment variable."
	case config.SourceConfig:
		return "Set with audd config set token."
	case config.SourceLogin:
		return "Fetched by audd login."
	}
	return "Source: " + src.Describe() + "."
}

func newTokenView(token string, reveal bool, src config.TokenSource) tokenView {
	v := tokenView{Token: token, Source: string(src), SourceDescription: src.Describe()}
	if !reveal {
		v.Token, v.Masked = config.MaskToken(token), true
	}
	return v
}

func newTokenCmd(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Show, rotate, or re-fetch your API token",
		Long: `Show which API token audd uses and where it comes from, rotate it, or fetch
it again from your account.

audd picks the token from, in order: --token, AUDD_API_TOKEN,
audd config set token, and the token saved by audd login.`,
		Example: `  audd token show
  audd token show --reveal
  audd token rotate
  audd token refresh-local`,
		GroupID: GroupAccount,
	}

	var reveal bool
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the API token in use (masked unless --reveal)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, src, err := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
			if err != nil {
				return err
			}
			if t == "" {
				return &output.Error{Code: "no_token", Message: "no API token is set",
					Hint: api.NoTokenHint, Exit: output.ExitAuth}
			}
			v := newTokenView(t, reveal, src)
			return a.Out.Result(v, func(w io.Writer) {
				fmt.Fprintln(w, v.Token)
				a.Out.Info("%s", tokenSourceSentence(src))
				if v.Masked {
					a.Out.Info("Show the full token with audd token show --reveal.")
				}
			})
		},
	}
	show.Flags().BoolVar(&reveal, "reveal", false, "print the full token")

	var rotateReveal bool
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Replace your API token with a new one",
		Long: `Replace your account's API token with a new one. The old token stops working
immediately, everywhere it is used. The new token is saved for this profile.

If you unticked the token:write permission when signing in, this asks you
to approve it.`,
		Example: "  audd token rotate\n  audd token rotate --yes --reveal",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			oc := newOAuth(a)
			if !oc.LoggedIn() {
				return notSignedIn(a)
			}
			if err := a.Out.Confirm("Rotate your AudD API token? The current token stops working immediately, everywhere it is used.", a.Flags.Yes); err != nil {
				return err
			}
			if _, err := oc.EnsureScopes(cmd.Context(), "token:write"); err != nil {
				return err
			}
			b, err := a.Account()
			if err != nil {
				return err
			}
			_, before, err := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
			if err != nil {
				return err
			}
			t, err := b.RotateAPIToken(cmd.Context())
			if err != nil {
				return rotateFailed(err)
			}
			if err := a.Secrets.Set(a.Profile.Name, "login_api_token", t); err != nil {
				return fmt.Errorf("the token was rotated but could not be saved (%w); fetch it with audd token refresh-local", err)
			}
			restartRecorder(a)
			v := newTokenView(t, rotateReveal, config.SourceLogin)
			err = a.Out.Result(v, func(w io.Writer) {
				fmt.Fprintf(w, "Rotated. New token: %s\n", v.Token)
				a.Out.Info("The old token no longer works. The new one is saved for profile %s.", a.Profile.Name)
			})
			switch before {
			case config.SourceFlag, config.SourceEnv, config.SourceConfig:
				a.Out.Warn("Your %s still holds the old token, which no longer works. Update it, or remove it to use the new token saved by audd login.", before.Describe())
			}
			return err
		},
	}
	rotate.Flags().BoolVar(&rotateReveal, "reveal", false, "print the full new token")

	refresh := &cobra.Command{
		Use:   "refresh-local",
		Short: "Fetch your API token from your account again and save it",
		Long: `Fetch your account's current API token and save it for this profile, for
example after rotating it in the dashboard.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := a.Account()
			if err != nil {
				return err
			}
			t, err := b.APIToken(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.Secrets.Set(a.Profile.Name, "login_api_token", t); err != nil {
				return err
			}
			restartRecorder(a)
			_, src, _ := config.ResolveToken(a.Flags.Token, a.Profile.Name, a.Secrets)
			v := newTokenView(t, false, config.SourceLogin)
			return a.Out.Result(v, func(w io.Writer) {
				fmt.Fprintf(w, "Saved your API token %s for profile %s.\n", v.Token, a.Profile.Name)
				if src != config.SourceLogin {
					a.Out.Info("The token from the %s takes precedence over it.", src.Describe())
				}
			})
		},
	}
	cmd.AddCommand(show, rotate, refresh)
	return cmd
}

// restartRecorder restarts the background stream recorder after the stored
// token changed, so it records with the new token.
func restartRecorder(a *app.App) {
	restarted, err := app.RestartRecorder(a)
	switch {
	case err != nil:
		a.Out.Warn("Could not restart the stream recorder with the new token: %s. Run: audd streams recorder stop, then audd streams recorder start", output.AsError(err).Message)
	case restarted:
		a.Out.Info("Restarted the stream recorder with the new token.")
	}
}

// rotateFailed explains a rotation whose outcome is unknown: the server may
// have replaced the token before the connection failed or the reply was lost.
func rotateFailed(err error) error {
	e := output.AsError(err)
	if e.Exit != output.ExitNetwork && e.Code != "unexpected_response" && e.Code != "unexpected" {
		return err
	}
	ne := *e
	ne.Message = e.Message + "; the token may already have been rotated, so the old one may no longer work"
	ne.Hint = "audd token refresh-local"
	ne.Retryable = false
	return &ne
}
