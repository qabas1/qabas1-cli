package cli

import (
	"github.com/spf13/cobra"
)

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate completion scripts for bash, zsh, fish, or powershell.

To load completions:

Bash:
  source <(qabas completion bash)
  # Linux:
  qabas completion bash > /etc/bash_completion.d/qabas
  # macOS:
  qabas completion bash > $(brew --prefix)/etc/bash_completion.d/qabas

Zsh:
  source <(qabas completion zsh)
  qabas completion zsh > "${fpath[1]}/_qabas"

Fish:
  qabas completion fish | source
  qabas completion fish > ~/.config/fish/completions/qabas.fish

PowerShell:
  qabas completion powershell | Out-String | Invoke-Expression
  # Persist:
  qabas completion powershell > qabas.ps1
`,
		DisableFlagsInUseLine: true,
		ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return cmd.Root().GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
			}
			return nil
		},
	}
	return cmd
}
