package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/AudDMusic/audd-go"
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/api"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/safety"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		a.APIClient = api.NewClientFactory(a)
		a.APIClientOnce = api.NewClientFactory(a, audd.WithMaxAttempts(1))
		root.AddCommand(newAPICmd(a))
	})
}

var methodName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func newAPICmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:     "api <method> [key=value...]",
		Short:   "Call any AudD API method directly",
		GroupID: GroupAgents,
		Long: `Call an AudD API method by name with form parameters, and print the JSON
response. Your API token is added for you. The response is printed with the
API's fields and values; piped output also starts with "schema_version": 1,
like every JSON document audd prints, and --fields keeps only the fields
you name.

Use it for methods and parameters the other commands don't cover. The
methods are described at https://docs.audd.io. Calls that recognize audio
are billed as usual. Each call is sent once and never retried
automatically.

The exit code follows the response: 0 for "status": "success", and the
usual error codes (3 auth, 4 quota or plan, 2 bad request, 5 server) for
"status": "error". The response is printed either way.`,
		Example: `  audd api getStreams
  audd api getCallbackUrl
  audd api recognize url=https://audd.tech/example.mp3 return=spotify`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			method := strings.Trim(args[0], "/")
			if !methodName.MatchString(method) {
				return usageErr("audd api getStreams", "invalid method name %q", args[0])
			}
			params := map[string]string{}
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return usageErr("pass parameters as key=value", "invalid parameter %q", kv)
				}
				if k == "api_token" {
					return usageErr("use --token, AUDD_API_TOKEN, or audd config set token", "the API token is added for you")
				}
				params[k] = v
			}
			if err := safety.BudgetFor(a).Take(1); err != nil {
				return err
			}
			var body map[string]any
			// Sent once: a method can be billed (recognition) or change
			// the account (a custom catalog upload), so a failure is
			// never retried automatically.
			_, err := api.DoOnce(ctx, a, func(c *audd.Client) (struct{}, error) {
				b, err := c.Advanced().RawRequestContext(ctx, method, params)
				if err != nil {
					return struct{}{}, err
				}
				body = b
				return struct{}{}, errorFromBody(b)
			})
			if body == nil {
				return err
			}
			if perr := a.Out.Result(body, func(w io.Writer) {
				b, _ := json.MarshalIndent(body, "", "  ")
				fmt.Fprintln(w, string(b))
			}); perr != nil {
				return perr
			}
			return err
		},
	}
}

// errorFromBody turns a "status": "error" response into the SDK's error so
// it gets the usual exit code (and token healing).
func errorFromBody(b map[string]any) error {
	if b["status"] != "error" {
		return nil
	}
	e := &audd.AudDAPIError{RawResponse: b}
	if eb, ok := b["error"].(map[string]any); ok {
		switch c := eb["error_code"].(type) {
		case float64:
			e.ErrorCode = int(c)
		case string:
			e.ErrorCode, _ = strconv.Atoi(strings.TrimSpace(c))
		}
		e.Message, _ = eb["error_message"].(string)
	}
	return e
}
