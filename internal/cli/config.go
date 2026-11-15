package cli

import (
	"fmt"
	"os"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage local QabasOne configuration",
		Long:  "Show, get, set, and validate local QabasOne configuration (secrets redacted on show).",
		Example: `  qabas config show
  qabas config get channel
  qabas config set channel stable
  qabas config validate`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(
		&cobra.Command{
			Use: "show", Short: "Print configuration (secrets redacted)",
			RunE: func(cmd *cobra.Command, args []string) error {
				e := load(f)
				cfg, err := config.Load(e.Paths)
				if err != nil {
					return err
				}
				m := config.RedactMap(cfg.Map())
				if f.JSON {
					return e.Out.Encode(m)
				}
				raw, _ := os.ReadFile(e.Paths.SecretsFile())
				for k, v := range m {
					fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", k, v)
				}
				if len(raw) > 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "\n# secrets.env")
					fmt.Fprint(cmd.OutOrStdout(), config.RedactEnv(string(raw)))
				}
				return nil
			},
		},
		&cobra.Command{
			Use: "get <key>", Short: "Get one configuration value", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				e := load(f)
				cfg, err := config.Load(e.Paths)
				if err != nil {
					return err
				}
				v, err := cfg.Get(args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), v)
				return nil
			},
		},
		&cobra.Command{
			Use: "set <key> <value>", Short: "Set one configuration value", Args: cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				e := load(f)
				cfg, err := config.Load(e.Paths)
				if err != nil {
					return err
				}
				if err := cfg.Set(args[0], args[1]); err != nil {
					return err
				}
				if err := cfg.Validate(); err != nil {
					return err
				}
				if f.DryRun {
					e.Out.Printf("would set %s=%s\n", args[0], args[1])
					return nil
				}
				return config.Save(e.Paths, cfg)
			},
		},
		&cobra.Command{
			Use: "validate", Short: "Validate configuration",
			RunE: func(cmd *cobra.Command, args []string) error {
				e := load(f)
				cfg, err := config.Load(e.Paths)
				if err != nil {
					return err
				}
				if err := cfg.Validate(); err != nil {
					return err
				}
				e.Out.Println("configuration is valid")
				return nil
			},
		},
	)
	return cmd
}
