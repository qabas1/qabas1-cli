package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/config"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/output"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/pitr"
	"github.com/qabasone/qabasone-cli/internal/release"
	"github.com/spf13/cobra"
)

func (e *env) pitrManager() (*pitr.Manager, error) {
	img := nodestack.DefaultPostgresImage
	if rel, _, err := e.installedRelease(); err == nil {
		if v := compose.ImagePins(rel)["POSTGRES_IMAGE"]; v != "" {
			img = v
		}
	}
	return &pitr.Manager{X: &nodeexec.Local{Name: "local"}, ConfigDir: e.Paths.ConfigDir, StateDir: e.Paths.StateDir, PGImage: img}, nil
}

func newBackupPITRCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pitr",
		Short: "Point-in-time recovery: WAL archive, base backups, restore tests",
		Long: `Continuous WAL archiving (wal-archiver, pg_receivewal on slot qabas_wal_archive)
plus weekly base backups let you restore the database to any second, e.g. just before a
wrong posting or a DROP. This is separate from replication: replicas copy mistakes
within seconds; the archive keeps history. Keep the repository on a different disk or
copy it off-site (qabas backup cp works on it too).`,
		Example: `  sudo qabas backup pitr enable
  sudo qabas backup pitr base
  sudo qabas backup pitr status
  sudo qabas backup pitr restore-test
  sudo qabas backup pitr restore-test --target-time 2026-09-30T08:15:00Z`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	enable := &cobra.Command{
		Use:   "enable",
		Short: "Enable the WAL archive on this writer (starts wal-archiver)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				m, _ := e.pitrManager()
				if e.flags.DryRun {
					e.Out.Printf("would enable PITR into %s\n", m.Dir())
					return nil
				}
				x := &nodeexec.Local{Name: "local"}
				repl, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword)
				if err != nil {
					return err
				}
				bak, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, cluster.SecretBackupPassword)
				if err != nil {
					return err
				}
				if err := pgctl.EnsureRoles(cmd.Context(), x, repl, bak); err != nil {
					return wrapErr(err)
				}
				if err := m.Enable(cmd.Context(), bak); err != nil {
					return wrapErr(err)
				}
				cfg, err := config.Load(e.Paths)
				if err != nil {
					return err
				}
				raw, err := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref).FetchComposeFile(nodestack.OverlayPITR)
				if err != nil {
					if _, serr := os.Stat(filepath.Join(e.Paths.ComposeDir(), nodestack.OverlayPITR)); serr != nil {
						return wrapErr(fmt.Errorf("fetch %s: %w", nodestack.OverlayPITR, err))
					}
				} else if err := os.WriteFile(filepath.Join(e.Paths.ComposeDir(), nodestack.OverlayPITR), raw, 0o644); err != nil {
					return err
				}
				if err := nodestack.AddDirective(e.Paths.ComposeDir(), "file:"+nodestack.OverlayPITR); err != nil {
					return err
				}
				_, st, err := e.installedRelease()
				if err != nil {
					return err
				}
				env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
				if err := newRuntime().UpService(e.Paths, env, "wal-archiver", false, true); err != nil {
					return wrapErr(err)
				}
				if err := pitr.InstallBaseTimer(cmd.Context(), x, qabasCmd); err != nil {
					e.Out.Warnf("weekly base backup timer not installed (%v); schedule 'qabas backup pitr base' yourself\n", err)
				}
				audit(e, "pitr-enable", "", m.Dir(), nil, "")
				e.Out.Printf("WAL archive enabled into %s; weekly base backups via %s.\nTake the first base backup now: sudo qabas backup pitr base\n", m.Dir(), pitr.BaseTimer)
				return nil
			})
		},
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Archiver, slot, newest WAL and base backups",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			m, _ := e.pitrManager()
			st := m.Status(cmd.Context())
			if f.JSON {
				return e.Out.Encode(st)
			}
			e.Out.Printf("repository: %s (enabled %v)\narchiver: %s\n", st.Dir, st.Enabled, st.Archiver)
			if st.Slot != nil {
				e.Out.Printf("slot: %s active=%v wal=%s retained=%dMiB\n", st.Slot.Name, st.Slot.Active, st.Slot.WALStatus, st.Slot.RetainedBytes>>20)
			}
			if st.NewestWAL != "" {
				e.Out.Printf("newest WAL: %s (%s ago), %d segments, %d MiB\n", st.NewestWAL, time.Since(st.NewestWALAt).Round(time.Second), st.WALSegments, st.WALBytes>>20)
			}
			rows := [][]string{}
			for _, b := range st.Bases {
				rows = append(rows, []string{b.ID, b.At.Local().Format("2006-01-02 15:04"), fmt.Sprintf("%d MiB", b.Bytes>>20)})
			}
			if err := output.Table(cmd.OutOrStdout(), []string{"BASE", "TAKEN", "SIZE"}, rows); err != nil {
				return err
			}
			for _, p := range st.Problems {
				e.Out.Warnf("%s\n", p)
			}
			if len(st.Problems) > 0 {
				return exitcode.New(exitcode.Health, "PITR has %d problem(s)", len(st.Problems))
			}
			return nil
		},
	}
	var keep int
	base := &cobra.Command{
		Use:   "base",
		Short: "Take a compressed base backup into the archive repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				m, _ := e.pitrManager()
				if !m.Enabled() {
					return exitcode.NewHint(exitcode.InvalidInput, "sudo qabas backup pitr enable", "PITR is not enabled")
				}
				if e.flags.DryRun {
					return nil
				}
				bak, err := cluster.ReadSecret(e.Paths, cluster.SharedScope, cluster.SecretBackupPassword)
				if err != nil {
					return err
				}
				b, err := m.TakeBase(cmd.Context(), bak)
				audit(e, "pitr-base", "", b.ID, err, "")
				if err != nil {
					return exitcode.Wrap(exitcode.BackupRestore, err)
				}
				e.Out.Printf("base backup %s (%d MiB)\n", b.ID, b.Bytes>>20)
				if keep > 0 {
					removed, err := m.PruneBases(cmd.Context(), keep)
					if err != nil {
						return wrapErr(err)
					}
					for _, r := range removed {
						e.Out.Printf("pruned base %s\n", r)
					}
					wp, err := m.PruneWAL(cmd.Context())
					if err != nil {
						return wrapErr(err)
					}
					if wp.Removed > 0 {
						e.Out.Printf("pruned %d WAL segment(s) (%d MiB) older than the oldest base\n", wp.Removed, wp.Bytes>>20)
					}
				}
				return nil
			})
		},
	}
	base.Flags().IntVar(&keep, "keep", 4, "keep this many newest base backups (0 = keep all)")
	var baseID, target string
	restoreTest := &cobra.Command{
		Use:   "restore-test",
		Short: "Restore base + WAL into a throwaway container and check the schema",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			m, _ := e.pitrManager()
			if e.flags.DryRun {
				return nil
			}
			res, err := m.RestoreTest(cmd.Context(), baseID, target)
			audit(e, "pitr-restore-test", res.Base, target, err, "")
			if err != nil {
				return exitcode.Wrap(exitcode.BackupRestore, err)
			}
			if f.JSON {
				return e.Out.Encode(res)
			}
			e.Out.Printf("restore test OK: base %s, %d migrations, latest migration %s, took %s\n", res.Base, res.Migrations, res.LatestMigration, res.Duration.Round(time.Second))
			return nil
		},
	}
	restoreTest.Flags().StringVar(&baseID, "base", "", "base backup id (default: newest)")
	restoreTest.Flags().StringVar(&target, "target-time", "", "recover up to this RFC3339 time (default: end of archive)")
	cmd.AddCommand(enable, status, base, restoreTest)
	return cmd
}
