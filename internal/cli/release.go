package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/qabas1/qabasone-cli/internal/config"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/manifest"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/qabas1/qabasone-cli/internal/release"
	"github.com/qabas1/qabasone-cli/internal/state"
	"github.com/qabas1/qabasone-cli/internal/update"
	"github.com/spf13/cobra"
)

func newReleaseCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Manage QabasOne product releases",
		Long:  "List and inspect product releases published in qabasone-deploy.",
		Example: `  qabas release list
  qabas release ls
  qabas release show 2.8.0
  qabas release current
  qabas release latest`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newReleaseListCmd(f), newReleaseShowCmd(f), newReleaseCurrentCmd(f), newReleaseLatestCmd(f))
	return cmd
}

func newReleaseListCmd(f *flags) *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List published product releases",
		Example: `  qabas release list
  qabas release ls --json
  qabas release ls --format '{{.Version}}'
  qabas release ls --format 'table {{.Version}}\t{{.Channel}}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			cfg, err := config.Load(e.Paths)
			if err != nil {
				return err
			}
			client := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref)
			rels, err := client.List()
			if err != nil {
				return err
			}
			rows := make([]releaseListRow, 0, len(rels))
			for _, r := range rels {
				rows = append(rows, releaseListRow{
					Version: r.Release.Version,
					Channel: r.Release.Channel,
					API:     r.Components.API.Version,
					App:     r.Components.App.Version,
					Dash:    r.Components.Dash.Version,
					Siraj:   r.Components.Siraj.Version,
				})
			}
			if f.JSON {
				return e.Out.Encode(rels)
			}
			return output.FormatList(cmd.OutOrStdout(), format,
				"table {{.Version}}\t{{.Channel}}\t{{.API}}\t{{.App}}\t{{.Dash}}\t{{.Siraj}}", rows)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", `Format output using a Go template ("table", "json", or a template)`)
	return cmd
}

type releaseListRow struct {
	Version string
	Channel string
	API     string
	App     string
	Dash    string
	Siraj   string
}

func newReleaseShowCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:               "show <version>",
		Short:             "Show one product release manifest",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReleaseVersions,
		Example:           `  qabas release show 2.8.0`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			cfg, err := config.Load(e.Paths)
			if err != nil {
				return err
			}
			client := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref)
			rel, err := client.Get(args[0])
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(rel)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "QabasOne %s (%s)\n\nAPI     %s\nApp     %s\nDash    %s\nSiraj   %s\n",
				rel.Release.Version, rel.Release.Channel,
				rel.Components.API.Version, rel.Components.App.Version, rel.Components.Dash.Version, rel.Components.Siraj.Version)
			return nil
		},
	}
}

func newReleaseCurrentCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "current",
		Short:   "Show the installed product release",
		Example: `  qabas release current`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := state.Load(e.Paths)
			if err != nil {
				return err
			}
			if !st.Installed() {
				return exitcode.NewHint(exitcode.InvalidInput,
					"sudo qabas install",
					"QabasOne is not installed")
			}
			if f.JSON {
				return e.Out.Encode(st)
			}
			fmt.Fprintln(cmd.OutOrStdout(), st.CurrentRelease)
			return nil
		},
	}
}

func newReleaseLatestCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "latest",
		Short: "Show the latest release on the selected channel",
		Example: `  qabas release latest
  qabas release latest --channel beta
  qabas release latest --channel dev`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			cfg, err := config.Load(e.Paths)
			if err != nil {
				return err
			}
			ch := e.channel()
			if ch == "" {
				ch = cfg.Channel
			}
			if ch == "" {
				ch = "stable"
			}

			client := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref)
			var rel *manifest.Release
			if ch == "dev" {
				rel, err = release.LatestDevFromGHCR(client.HTTP, "qabasone", release.GHCRUsername(), release.GHCRToken())
			} else {
				rel, err = client.Latest(ch)
			}
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(rel)
			}
			fmt.Fprintln(cmd.OutOrStdout(), rel.Release.Version)
			return nil
		},
	}
}

func newUninstallCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "uninstall",
		Short:   "Uninstall the qabas CLI package",
		Long:    "Uninstall the qabas CLI package from the host. This does not remove QabasOne services, data, backups, or configuration.",
		Example: `  sudo qabas uninstall`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS == "windows" {
				how, err := update.UninstallInstructions()
				if err != nil {
					return exitcode.Wrap(exitcode.Generic, err)
				}
				e := load(f)
				e.Out.Println("qabas does not remove itself on Windows. To uninstall the CLI, run (elevated PowerShell for MSI installs):")
				e.Out.Println("  " + how)
				e.Out.Println("QabasOne services, data, backups, and configuration under " + e.Paths.ConfigDir + " are left in place.")
				return nil
			}
			if runtime.GOOS != "linux" {
				return exitcode.NewHint(exitcode.InvalidInput,
					"remove the qabas package using your operating system package manager",
					"automatic package uninstall is only supported on Linux")
			}
			if _, err := exec.LookPath("apt"); err != nil {
				return exitcode.NewHint(exitcode.InvalidInput,
					"run your distro package manager, for example: sudo apt remove qabas",
					"apt is not installed")
			}
			if f.DryRun {
				load(f).Out.Println("would run: apt remove qabas")
				return nil
			}
			apt := exec.Command("apt", "remove", "qabas")
			apt.Stdin = os.Stdin
			apt.Stdout = os.Stdout
			apt.Stderr = os.Stderr
			if err := apt.Run(); err != nil {
				return exitcode.New(exitcode.Generic, "apt remove qabas: %w", err)
			}
			return nil
		},
	}
	return cmd
}
