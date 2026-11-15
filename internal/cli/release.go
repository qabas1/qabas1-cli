package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/autopilot"
	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/nodeexec"
	"github.com/qabas1/qabas1-cli/internal/observability"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/spf13/cobra"
)

func newReleaseCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Manage QabasOne product releases",
		Long:  "List and inspect product releases published in qabas1-deploy.",
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
				owner, _, _ := strings.Cut(client.Repo, "/")
				if v := os.Getenv("QABAS_GHCR_OWNER"); v != "" {
					owner = v
				}
				rel, err = release.LatestDevFromGHCR(client.HTTP, owner, release.GHCRUsername(), release.GHCRToken())
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
		Short:   "Completely remove the installed QabasOne system",
		Long:    "Stop and remove QabasOne containers, named volumes, schedules, configuration, secrets, state, backups, and logs. The qabas CLI remains installed. This permanently deletes local product data.",
		Example: `  sudo qabas uninstall`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runtime.GOOS != "linux" {
				return exitcode.New(exitcode.InvalidInput, "system uninstall is currently supported on Linux")
			}
			e := load(f)
			p := e.Paths
			st, err := state.Load(p)
			if err != nil {
				return err
			}
			for _, dir := range []string{p.ConfigDir, p.StateDir, p.LogDir} {
				if dir == "" || dir == "/" || dir == "." {
					return exitcode.New(exitcode.InvalidInput, "unsafe uninstall path %q", dir)
				}
				if filepath.Clean(dir) != dir {
					return exitcode.New(exitcode.InvalidInput, "unclean uninstall path %q", dir)
				}
			}
			if f.DryRun {
				e.Out.Printf("Would stop and remove Compose project %s with volumes, remove QabasOne schedules, then delete %s, %s, and %s. The qabas CLI remains installed.\n", compose.ProjectName, p.ConfigDir, p.StateDir, p.LogDir)
				return nil
			}
			if err := requireRoot(); err != nil {
				return err
			}
			if err := e.confirm("Permanently delete the QabasOne system and local data? The qabas CLI will remain installed. Continue?"); err != nil {
				return err
			}
			if _, err := os.Stat(p.ComposeFile()); err == nil {
				if err := compose.NewRuntime().Down(p, compose.EnvForInstalledRelease(p, st.CurrentRelease), true); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if _, err := os.Stat(p.LogsUIDir()); err == nil {
				if err := observability.NewRuntime().Down(p, true); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := autopilot.NewScheduler().Remove(cmdContext(cmd)); err != nil {
				return err
			}
			if err := backup.RemoveScheduleTimer(cmdContext(cmd), &nodeexec.Local{Name: "local"}); err != nil {
				return err
			}
			if err := removeOtherQabasUnits(); err != nil {
				return err
			}
			for _, dir := range []string{p.ConfigDir, p.StateDir, p.LogDir} {
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
			}
			e.Out.Println("QabasOne system removed. The qabas CLI remains installed.")
			return nil
		},
	}
	return cmd
}

// Remove only known product units; the qabas package and its files stay installed.
func removeOtherQabasUnits() error {
	units := []string{
		"qabas-pitr-base.timer", "qabas-pitr-base.service",
		"qabas-autoscale.timer", "qabas-autoscale.service",
		"qabas-dr-lease.timer", "qabas-dr-lease.service",
		"qabas-dr-check.timer", "qabas-dr-check.service",
		"qabas-relay.service", "qabas-relay-client.service",
	}
	changed := false
	for _, name := range units {
		path := filepath.Join("/etc/systemd/system", name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		_ = exec.Command("systemctl", "disable", "--now", name).Run()
		if err := os.Remove(path); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}
