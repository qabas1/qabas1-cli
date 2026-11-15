package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/replica"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/qabas1/qabas1-cli/internal/update"
	"github.com/qabas1/qabas1-cli/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print CLI and installed product versions",
		Example: `  qabas version
  qabas version --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, _ := state.Load(e.Paths)
			type ver struct {
				CLI      string `json:"cli"`
				Commit   string `json:"commit,omitempty"`
				QabasOne string `json:"qabasone,omitempty"`
			}
			v := ver{CLI: version.Version, QabasOne: st.CurrentRelease}
			if version.Commit != "none" {
				v.Commit = version.Commit
			}
			if f.JSON {
				return e.Out.Encode(v)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "qabas CLI          %s\n", version.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "QabasOne           %s\n", dash(st.CurrentRelease))
			if st.CurrentRelease != "" {
				if b, err := os.ReadFile(filepath.Join(e.Paths.Releases(), st.CurrentRelease+".yaml")); err == nil {
					if rel, err := manifest.Parse(b); err == nil {
						fmt.Fprintf(cmd.OutOrStdout(), "API                %s\n", rel.Components.API.Version)
						fmt.Fprintf(cmd.OutOrStdout(), "App                %s\n", rel.Components.App.Version)
						fmt.Fprintf(cmd.OutOrStdout(), "Dash               %s\n", rel.Components.Dash.Version)
						fmt.Fprintf(cmd.OutOrStdout(), "Siraj              %s\n", rel.Components.Siraj.Version)
					}
				}
			}
			return nil
		},
	}
}

func newUpdateCmd(f *flags) *cobra.Command {
	var dev bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the qabas CLI from GitHub Releases",
		Example: `  qabas update
  qabas update --dev
  qabas update --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			track := "stable"
			if dev {
				track = "dev"
			}
			info, err := update.CheckTrack(os.Getenv("QABAS_GITHUB_API"), track)
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(info)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Current qabas CLI: %s\nTarget qabas CLI:  %s\n", info.Current, info.Latest)
			if track != "dev" && !update.NeedsUpdate(info.Current, info.Latest) {
				e.Out.Println("Already up to date.")
				return nil
			}
			if f.DryRun {
				e.Out.Printf("would download %s\n", info.Asset)
				return nil
			}
			if info.URL == "" {
				return exitcode.NewHint(exitcode.Network, update.MissingAssetHint(),
					"no CLI package published for %s", info.Asset)
			}
			e.Out.Printf("Downloading %s...\n", info.Asset)
			started, err := update.Apply(info)
			if err != nil {
				return err
			}
			if started {
				e.Out.Printf("Installing qabas CLI %s in the background (msiexec); open a new terminal in a minute and run `qabas version`.\n", info.Latest)
				return nil
			}
			e.Out.Printf("qabas CLI updated to %s.\n", info.Latest)
			restartRelayUnits()
			return nil
		},
	}
	cmd.Flags().BoolVar(&dev, "dev", false, "update to the rolling dev CLI release")
	return cmd
}

// restartRelayUnits makes running relay services pick up the new binary.
func restartRelayUnits() {
	if runtime.GOOS != "linux" {
		return
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return
	}
	_ = exec.Command("systemctl", "try-restart", replica.RelayServerUnit, replica.RelayClientUnit).Run()
}

func dash(s string) string {
	if s == "" {
		return "(not installed)"
	}
	return s
}
