package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/mcpserver"
	"github.com/AudDMusic/audd-cli/internal/output"
	"github.com/AudDMusic/audd-cli/internal/safety"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newMCPCmd(a))
	})
}

func newMCPCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run a local MCP server for AI agents (stdio)",
		Long: `Run audd as a local MCP server over stdin and stdout, so an AI agent can call
it as tools: recognize (a file or URL), recognize_enterprise (a whole
recording; limit is required), streams_list, streams_add, streams_remove,
streams_history, now_playing, catalog_add, usage, account_status,
billing_plans, docs, payment_link, and token_rotate.

The tools use this computer's audd setup: the same API token, profile,
results cache, and stream store. --max-requests passed here is a ceiling
for the whole session: each billed call counts against it (an enterprise
scan at its limit), and calls that would go over it are refused.
Payment links are only returned for the user to open; nothing is charged.
The API token is never shown or rotated by a tool.

Add it to Claude Code:
  claude mcp add audd -- audd mcp

Other clients: run the command "audd mcp" with no arguments. Add
--profile NAME to the command to use that profile, or --max-requests N to
cap what the session can spend.`,
		Example: `  claude mcp add audd -- audd mcp
  claude mcp add audd -- audd mcp --max-requests 200`,
		GroupID: GroupAgents,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.Out.Options().StdinTTY {
				a.Out.Info("audd mcp is an MCP server that talks over stdin and stdout; start it from an MCP client, for example: claude mcp add audd -- audd mcp")
			}
			prefix := mcpForwardedFlags(a)
			ctx := cmd.Context()
			// One --max-requests ceiling for the whole session, not per call.
			budget := safety.BudgetFor(a)
			run := func(ctx context.Context, args []string) mcpserver.Result {
				full := append([]string{}, prefix...)
				cost, capped := mcpCost(args)
				if cost > 0 && budget.Remaining() >= 0 {
					if rem := budget.Remaining(); capped && cost > rem && rem > 0 {
						cost = rem // the scan's limit is lowered to what is left
					}
					if err := budget.Take(cost); err != nil {
						var errb bytes.Buffer
						p := output.NewPrinter(io.Discard, &errb, output.PrinterOptions{Format: output.FormatJSON})
						code := p.Error(err)
						return mcpserver.Result{Stderr: errb.Bytes(), Exit: code}
					}
					full = append(full, "--max-requests", strconv.Itoa(cost))
				}
				var out, errb bytes.Buffer
				code := Run(ctx, append(full, args...), IO{
					In: strings.NewReader(""), Out: &out, Err: &errb,
				})
				if cost > 0 && budget.Remaining() >= 0 && !mcpSpent(code, out.Bytes()) {
					budget.Refund(cost)
				}
				return mcpserver.Result{Stdout: out.Bytes(), Stderr: errb.Bytes(), Exit: code}
			}
			return mcpserver.Serve(ctx, mcpserver.Options{Run: run, Version: app.Version},
				io.NopCloser(a.In), nopWriteCloser{a.Out.Stdout()})
		},
	}
}

// mcpForwardedFlags passes the global flags given to `audd mcp` on to every
// tool call.
func mcpForwardedFlags(a *app.App) []string {
	var out []string
	if a.Flags.Token != "" {
		out = append(out, "--token", a.Flags.Token)
	}
	if a.Profile != nil && a.Profile.Name != "" {
		out = append(out, "--profile", a.Profile.Name)
	}
	return out
}

// mcpCost is the most requests a tool call can spend, counted against the
// session's --max-requests: one for a recognition or a catalog upload, the
// limit for an enterprise scan (capped is set: the scan's limit can be
// lowered to fit), nothing for a dry run or a call that is not billed.
func mcpCost(args []string) (cost int, capped bool) {
	if len(args) == 0 || slices.Contains(args, "--dry-run") {
		return 0, false
	}
	switch {
	case args[0] == "recognize" && slices.Contains(args, "--enterprise"):
		if i := slices.Index(args, "--limit"); i >= 0 && i+1 < len(args) {
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				return n, true
			}
		}
		return 1, true
	case args[0] == "recognize", len(args) > 1 && args[0] == "catalog" && args[1] == "add":
		return 1, false
	}
	return 0, false
}

// mcpSpent reports whether a tool call may have spent the requests taken
// for it: not when it failed before sending (usage, sign-in, account, and
// safety errors) or the result came from the cache.
func mcpSpent(code int, stdout []byte) bool {
	switch code {
	case output.ExitUsage, output.ExitAuth, output.ExitQuota, output.ExitSafety:
		return false
	case 0:
		var doc struct {
			Cached bool `json:"cached"`
		}
		if json.Unmarshal(bytes.TrimSpace(stdout), &doc) == nil && doc.Cached {
			return false
		}
	}
	return true
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
