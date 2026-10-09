package cli

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newDocsCmd(a))
	})
}

// docsResult is the JSON form of `audd docs <topic>`.
type docsResult struct {
	Topic    string `json:"topic"`
	Source   string `json:"source"` // the page URL, "embedded", or "account service"
	Markdown string `json:"markdown"`
}

func newDocsCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "docs [topic]",
		Short: "Print AudD documentation as markdown",
		Long: `Print AudD documentation as markdown, for reading in the terminal or giving
to a coding agent. Without a topic, list the topics.

Topics:
` + topicList() + `
The pages come from docs.audd.io. The cli topic is built in and works
offline. When docs.audd.io cannot be reached and you are signed in, the
docs come from your AudD account instead.

The markdown goes to stdout as is, also when piped or redirected. With
--format json, the result is a JSON document whose markdown field holds
the page.`,
		Example: `  audd docs
  audd docs api
  audd docs enterprise > enterprise.md
  audd docs cli
  audd docs api --format json`,
		GroupID:   GroupAgents,
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: agent.TopicNames(),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return a.Out.Result(agent.Topics, func(w io.Writer) {
					tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
					for _, t := range agent.Topics {
						fmt.Fprintf(tw, "%s\t%s\n", t.Name, t.Title)
					}
					tw.Flush()
					if !a.Out.Quiet() {
						fmt.Fprintln(w, "\nRead one with: audd docs <topic>")
					}
				})
			}
			t, ok := agent.FindTopic(args[0])
			if !ok {
				return output.Errf(output.ExitUsage, "invalid_argument", "audd docs",
					"unknown docs topic %q; topics: %s", args[0], strings.Join(agent.TopicNames(), ", "))
			}
			res := docsResult{Topic: t.Name, Source: "embedded"}
			if t.Path == "" {
				res.Markdown = agent.CLIDoc
			} else {
				md, err := agent.FetchDocs(cmd.Context(), docsHTTPClient(a), agent.DocsBase(), t)
				if err == nil {
					res.Markdown, res.Source = md, t.PageURL(agent.DocsBase())
				} else {
					md, ferr := docsFromAccount(a, cmd, t)
					if ferr != nil {
						return &output.Error{
							Code: "network", Exit: output.ExitNetwork, Retryable: true,
							Message: fmt.Sprintf("could not download the %s docs: %v", t.Name, err),
							Hint:    "audd docs cli (built in, works offline)",
							DocsURL: t.PageURL(agent.DefaultDocsBase),
						}
					}
					res.Markdown, res.Source = md, "account service"
				}
			}
			writeMarkdown := func(w io.Writer) {
				io.WriteString(w, res.Markdown)
				if !strings.HasSuffix(res.Markdown, "\n") {
					io.WriteString(w, "\n")
				}
			}
			// Markdown is the default output, piped or not; JSON only when
			// asked for with --format (or AUDD_FORMAT) or --fields.
			if !a.Out.Explicit() && len(a.Out.Fields()) == 0 {
				writeMarkdown(a.Out.Stdout())
				return nil
			}
			return a.Out.Result(res, writeMarkdown)
		},
	}
}

func topicList() string {
	var b strings.Builder
	for _, t := range agent.Topics {
		fmt.Fprintf(&b, "  %-11s %s\n", t.Name, t.Title)
	}
	return b.String()
}

func docsHTTPClient(a *app.App) *http.Client {
	return accountHTTPClient(a, 20*time.Second)
}

// docsFromAccount asks the account service for a topic when the docs site
// is unreachable; it needs a sign-in.
func docsFromAccount(a *app.App, cmd *cobra.Command, t agent.Topic) (string, error) {
	if t.Account == "" || !newOAuth(a).LoggedIn() {
		return "", fmt.Errorf("not available")
	}
	b, err := a.Account()
	if err != nil {
		return "", err
	}
	return b.Docs(cmd.Context(), t.Account)
}
