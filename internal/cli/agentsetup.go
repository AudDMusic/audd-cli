package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/agent"
	"github.com/AudDMusic/audd-cli/internal/app"
	"github.com/AudDMusic/audd-cli/internal/output"
)

func init() {
	Register(func(root *cobra.Command, a *app.App) {
		root.AddCommand(newAgentSetupCmd(a))
	})
}

type agentFile struct {
	Target  agent.Target `json:"target"`
	Path    string       `json:"path"`
	Content string       `json:"content"`
}

func newAgentSetupCmd(a *app.App) *cobra.Command {
	var claude, cursor, codex, agentsMD, print bool
	var dir string
	cmd := &cobra.Command{
		Use:   "agent-setup",
		Short: "Teach your coding agent to use audd",
		Long: `Write short instructions that teach a coding agent when and how to use audd:
which command fits which task, to plan billable work with --dry-run and
explicit limits, how to read the JSON output and exit codes, and never to
print the API token.

  --claude      Claude Code skill: .claude/skills/audd/SKILL.md
  --cursor      Cursor rule: .cursor/rules/audd.mdc
  --codex       a section in AGENTS.md (Codex and other agents read it)
  --agents-md   the same AGENTS.md section

Without a flag, audd writes the file for each agent it finds set up in the
project (a .claude or .cursor folder, or an AGENTS.md), or AGENTS.md when it
finds none. Running it again updates the files in place; in AGENTS.md only
the audd section is replaced.

To use audd as an MCP server instead: claude mcp add audd -- audd mcp`,
		Example: `  audd agent-setup
  audd agent-setup --claude
  audd agent-setup --cursor --agents-md
  audd agent-setup --claude --print`,
		GroupID: GroupAgents,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var targets []agent.Target
			if claude {
				targets = append(targets, agent.TargetClaude)
			}
			if cursor {
				targets = append(targets, agent.TargetCursor)
			}
			if codex || agentsMD {
				targets = append(targets, agent.TargetAgentsMD)
			}
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = wd
			}
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return output.Errf(output.ExitUsage, "invalid_argument", "create the folder, or pass --dir .", "%s is not a folder", dir)
			}
			if len(targets) == 0 {
				targets = agent.Detect(dir)
				if len(targets) == 0 {
					targets = []agent.Target{agent.TargetAgentsMD}
				}
			}
			if print {
				files := make([]agentFile, 0, len(targets))
				for _, t := range targets {
					content, err := agent.Render(t)
					if err != nil {
						return err
					}
					files = append(files, agentFile{Target: t, Path: t.RelPath(), Content: content})
				}
				return a.Out.Result(map[string]any{"files": files}, func(w io.Writer) {
					for i, f := range files {
						if len(files) > 1 {
							if i > 0 {
								fmt.Fprintln(w)
							}
							fmt.Fprintf(w, "==> %s <==\n", filepath.ToSlash(f.Path))
						}
						io.WriteString(w, f.Content)
					}
				})
			}
			results := make([]agent.WriteResult, 0, len(targets))
			for _, t := range targets {
				r, err := agent.Install(dir, t)
				if errors.Is(err, agent.ErrUnclosedSection) {
					return output.Errf(output.ExitUnexpected, "write_failed",
						"add "+agent.EndMarker+" where the audd section ends in "+r.Path+", or delete the section, then run audd agent-setup again",
						"left %s unchanged: it has an audd section start marker but no end marker", r.Path)
				}
				if err != nil {
					return output.Errf(output.ExitUnexpected, "write_failed", "audd agent-setup --print", "could not write %s: %v", r.Path, err)
				}
				results = append(results, r)
			}
			return a.Out.Result(map[string]any{"files": results}, func(w io.Writer) {
				for _, r := range results {
					rel, err := filepath.Rel(dir, r.Path)
					if err != nil {
						rel = r.Path
					}
					switch {
					case r.Created:
						fmt.Fprintf(w, "Wrote %s\n", rel)
					case r.Changed:
						fmt.Fprintf(w, "Updated %s\n", rel)
					default:
						fmt.Fprintf(w, "%s is up to date\n", rel)
					}
				}
			})
		},
	}
	f := cmd.Flags()
	f.BoolVar(&claude, "claude", false, "write a Claude Code skill (.claude/skills/audd/SKILL.md)")
	f.BoolVar(&cursor, "cursor", false, "write a Cursor rule (.cursor/rules/audd.mdc)")
	f.BoolVar(&codex, "codex", false, "write an audd section in AGENTS.md")
	f.BoolVar(&agentsMD, "agents-md", false, "write an audd section in AGENTS.md")
	f.BoolVar(&print, "print", false, "print the instructions instead of writing files")
	f.StringVar(&dir, "dir", "", "project folder to write into (default: the current folder)")
	return cmd
}
