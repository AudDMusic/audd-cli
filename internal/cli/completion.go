package cli

import (
	"github.com/spf13/cobra"

	"github.com/AudDMusic/audd-cli/internal/app"
)

func newCompletionCmd(a *app.App) *cobra.Command {
	return &cobra.Command{
		Use:   "completion bash|zsh|fish|powershell",
		Short: "Print a shell completion script",
		Long: `Print a shell completion script for audd.

Bash:        audd completion bash > ~/.local/share/bash-completion/completions/audd
Zsh:         audd completion zsh > "${fpath[1]}/_audd"
Fish:        audd completion fish > ~/.config/fish/completions/audd.fish
PowerShell:  audd completion powershell | Out-String | Invoke-Expression`,
		GroupID:     GroupOther,
		Annotations: map[string]string{annotationNoConfig: "true"},
		Args:        cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs:   []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			w := a.Out.Stdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(w, true)
			case "zsh":
				return root.GenZshCompletion(w)
			case "fish":
				return root.GenFishCompletion(w, true)
			default:
				return root.GenPowerShellCompletionWithDesc(w)
			}
		},
	}
}
