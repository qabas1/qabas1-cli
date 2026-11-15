package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/autopilot"
	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/deploy"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

const automaticDeployMaintenanceDuration = 30 * time.Minute

func newInstallCmd(f *flags) *cobra.Command {
	var force, noAutopilot bool
	var releaseSpec string
	cmd := &cobra.Command{
		Use:   "install [version]",
		Short: "Install QabasOne (latest stable by default)",
		Long: `Install QabasOne from the release database (qabas1-deploy).

With no version, installs the latest release on the selected channel (default: stable).
Use "dev" (or --channel dev) to pull compose/templates from qabas1-deploy@dev and
install the last-pushed GHCR images for each product component.
Requires GitHub + GHCR credentials (see: qabas auth login).`,
		Example: `  # Install latest stable
  sudo qabas install

  # Install latest published GHCR dev images
  sudo qabas install dev

  # Install a pinned product release
  sudo qabas install 2.8.0

  # Preview without changing the system
  sudo qabas install --dry-run`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeReleaseVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := releaseSpec
			if len(args) == 1 {
				spec = args[0]
			}
			err := runDeploy(f, deploy.Options{
				Action:         deploy.ActionInstall,
				Spec:           spec,
				Channel:        load(f).channel(),
				Force:          force,
				DryRun:         f.DryRun,
				NonInteractive: f.NonInteractive,
			})
			if err != nil || f.DryRun || noAutopilot {
				return err
			}
			e := load(f)
			if autopilot.OptedOut(e.Paths) {
				return nil
			}
			if aerr := enableAutopilot(cmdContext(cmd), e); aerr != nil {
				e.Out.Warnf("autopilot not scheduled (%v); enable later with: sudo qabas autopilot enable", aerr)
			} else {
				e.Out.Println("Autopilot enabled: hourly self-maintenance (disk, Docker, WAL, logs, self-heal). See: qabas autopilot status")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&releaseSpec, "release", "", "product version or channel")
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already present")
	cmd.Flags().BoolVar(&noAutopilot, "no-autopilot", false, "do not schedule hourly self-maintenance (qabas autopilot)")
	return cmd
}

func newDeployCmd(f *flags) *cobra.Command {
	var dev, allowDowngrade bool
	cmd := &cobra.Command{
		Use:   "deploy <version|service> [image-version|tag|digest]",
		Short: "Deploy an exact QabasOne product release",
		Long: `Deploy a specific product release version (exact match from qabas1-deploy).

Special: "dev" fetches compose/templates from qabas1-deploy@dev and pins
component images to the last-pushed GHCR package versions.

Scoped: "deploy <service> --dev" refreshes one component from last-pushed GHCR.
"deploy <service> <selector>" pins a published image tag, version, or digest.`,
		Example: `  sudo qabas deploy 2.8.0
  sudo qabas deploy dev
  sudo qabas deploy api --dev
  sudo qabas deploy app 2.8.0
  sudo qabas deploy api sha256:abc123... --allow-downgrade
  sudo qabas deploy 2.8.0 --dry-run`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 || len(args) == 2 {
				return nil
			}
			return exitcode.NewHint(exitcode.InvalidInput,
				"Use `sudo qabas deploy <version>` for an exact release, `sudo qabas deploy dev` for latest published GHCR images, `sudo qabas deploy <service> --dev` for one component, `sudo qabas install` for first install, or `sudo qabas upgrade` for the latest stable release.",
				"deploy requires a product version")
		},
		ValidArgsFunction: completeDeployArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var spec, service string
			var err error
			if len(args) == 2 {
				if dev {
					return exitcode.New(exitcode.InvalidInput, "--dev cannot be combined with an image selector")
				}
				var ok bool
				service, ok = compose.ProductService(args[0])
				if !ok {
					return exitcode.New(exitcode.InvalidInput, "%q is not a deployable product service", args[0])
				}
				spec = args[1]
				if strings.TrimSpace(spec) == "" {
					return exitcode.New(exitcode.InvalidInput, "image selector is required")
				}
			} else {
				spec, service, err = parseDeployArg(args[0], dev)
			}
			if err != nil {
				return err
			}
			return runDeploy(f, deploy.Options{
				Action:         deploy.ActionDeploy,
				Spec:           spec,
				Service:        service,
				AllowDowngrade: allowDowngrade,
				Channel:        load(f).channel(),
				DryRun:         f.DryRun,
				NonInteractive: f.NonInteractive,
			})
		},
	}
	cmd.Flags().BoolVar(&dev, "dev", false, "deploy one product service from last-pushed GHCR (api|app|dash|siraj)")
	cmd.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false, "allow an older or unverified API image in a scoped deploy")
	return cmd
}

// parseDeployArg disambiguates versioned deploy vs scoped `deploy <service> --dev`.
func parseDeployArg(arg string, dev bool) (spec, service string, err error) {
	arg = strings.TrimSpace(arg)
	if svc, ok := compose.ProductService(arg); ok {
		if !dev {
			return "", "", exitcode.NewHint(exitcode.InvalidInput,
				"Use `sudo qabas deploy "+svc+" --dev` to refresh that component, or `sudo qabas deploy <version>` / `sudo qabas deploy dev` for a full release.",
				"%q is a product service; pass --dev or use a release version", arg)
		}
		return "dev", svc, nil
	}
	if dev {
		if strings.EqualFold(arg, "dev") {
			// Redundant but harmless: full deploy dev.
			return "dev", "", nil
		}
		return "", "", exitcode.NewHint(exitcode.InvalidInput,
			"Use `sudo qabas deploy api --dev` (or app/dash/siraj), or omit --dev for `sudo qabas deploy <version>`.",
			"--dev requires a product service (api, app, dash, siraj)")
	}
	return arg, "", nil
}

func newUpgradeCmd(f *flags) *cobra.Command {
	var check, noBackup bool
	cmd := &cobra.Command{
		Use:   "upgrade [version]",
		Short: "Upgrade to the latest compatible stable release",
		Long: `Upgrade the installed QabasOne product to a newer compatible release.

With no version, selects the latest compatible release on the channel.
A pre-upgrade backup is created unless --no-backup is set.`,
		Example: `  sudo qabas upgrade
  sudo qabas upgrade --check
  sudo qabas upgrade --dry-run
  sudo qabas upgrade 2.9.0`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeReleaseVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := ""
			if len(args) == 1 {
				spec = args[0]
			}
			return runDeploy(f, deploy.Options{
				Action:         deploy.ActionUpgrade,
				Spec:           spec,
				Channel:        load(f).channel(),
				DryRun:         f.DryRun,
				CheckOnly:      check,
				NoBackup:       noBackup,
				NonInteractive: f.NonInteractive,
			})
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "preview the upgrade without applying it")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the automatic pre-upgrade backup (advanced)")
	return cmd
}

func newDowngradeCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "downgrade <version>",
		Short:             "Downgrade to an older QabasOne release (blocked if unsafe)",
		Long:              "Downgrade to an older product release. Blocked when compatibility rules forbid it.",
		Example:           "  sudo qabas downgrade 2.7.0\n  sudo qabas downgrade 2.7.0 --yes",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReleaseVersions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(f, deploy.Options{
				Action:         deploy.ActionDowngrade,
				Spec:           args[0],
				Channel:        load(f).channel(),
				DryRun:         f.DryRun,
				NonInteractive: f.NonInteractive,
			})
		},
	}
	return cmd
}

func newRollbackCmd(f *flags) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Return to the previous successfully deployed release",
		Long:  "Roll back to the previous successful deployment, or an explicit release with --to.",
		Example: `  sudo qabas rollback
  sudo qabas rollback --to 2.7.0 --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(f, deploy.Options{
				Action:         deploy.ActionRollback,
				Spec:           to,
				Channel:        load(f).channel(),
				DryRun:         f.DryRun,
				NonInteractive: f.NonInteractive,
			})
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "explicit previous release")
	return cmd
}

func runDeploy(f *flags, opt deploy.Options) error {
	e := load(f)
	if c, err := cluster.Load(e.Paths); err == nil && opt.Action != deploy.ActionInstall {
		if self := e.selfName(c); self != c.Writer {
			return exitcode.NewHint(exitcode.InvalidInput,
				"deploy on the writer ("+c.Writer+"); replicas follow automatically (or: qabas replica sync --all --restart)",
				"%s is a read-only replica; releases are deployed on the writer only", self)
		}
	}
	return e.withLock(func() error {
		cfg, st, err := e.cfgState()
		if err != nil {
			return err
		}
		eng := deploy.New(e.Paths, cfg, st, e.Out)
		eng.LogRotation = func() bool { return compose.DaemonLoggingDriver(context.Background()) == "json-file" }
		eng.ContainerRAM = sysinfo.DockerMemTotal
		plan, err := eng.BuildPlan(opt)
		if err != nil {
			return err
		}
		human := deploy.FormatPlan(plan)
		if f.JSON {
			if err := e.Out.Encode(plan); err != nil {
				return err
			}
		} else {
			e.Out.Print(human)
		}
		if plan.Blocked != "" {
			if opt.CheckOnly || opt.DryRun {
				return nil
			}
			return exitcode.New(exitcode.Compatibility, "%s", plan.Blocked)
		}
		if opt.CheckOnly || opt.DryRun {
			if plan.Blocked != "" {
				return nil
			}
			return nil
		}
		if plan.LeavingDev || opt.Action == deploy.ActionDowngrade || opt.Action == deploy.ActionRollback {
			prompt := "This will change the running QabasOne release. Continue?"
			if plan.LeavingDev {
				prompt = "Leaving the dev channel for a stable release. Schema from dev may not match. Continue?"
			}
			if err := e.confirm(prompt); err != nil {
				return err
			}
		}
		backupFn := func() (string, error) {
			rt := compose.NewRuntime()
			var rel *manifest.Release
			if plan.Target != nil {
				rel = plan.Target
			}
			meta, err := backup.Create(e.Paths, rt, st, rel, true, true, "pre-deploy", false)
			if err != nil {
				return "", err
			}
			return meta.ID, nil
		}
		if !backup.Supported() {
			if plan.BackupRequired {
				e.Out.Println("warning: pre-deploy database backup skipped (vpg backups are Linux-only)")
			}
			backupFn = nil
		}
		if err := runDeployWithMaintenance(e, eng, opt, plan, func() error {
			return eng.Run(opt, backupFn)
		}); err != nil {
			return wrapErr(err)
		}
		propagateToReplicas(e)
		return nil
	})
}

// propagateToReplicas pushes the new release to every replica after a successful deploy
// on the writer (best effort: a replica failure never fails the deploy; the operator
// sees it and can re-run `qabas replica sync --all --restart`).
func propagateToReplicas(e *env) {
	if os.Getenv("QABAS_SKIP_REPLICA_SYNC") == "1" {
		return
	}
	c, err := cluster.Load(e.Paths)
	if err != nil || len(c.Standbys()) == 0 || e.selfName(c) != c.Writer {
		return
	}
	re, err := e.newReplicaEngine(c, engineOpts{identity: os.Getenv(EnvSSHIdentity)})
	if err != nil {
		e.Out.Warnf("replicas not updated: %v\n", err)
		return
	}
	defer re.Close()
	e.Out.Println("propagating release to replicas ...")
	if err := syncReplicas(context.Background(), e, re, nil, true); err != nil {
		e.Out.Warnf("%v; retry with: sudo qabas replica sync --all --restart\n", err)
	}
}

func runDeployWithMaintenance(e *env, eng *deploy.Engine, opt deploy.Options, plan *deploy.Plan, run func() error) error {
	if opt.Action != deploy.ActionDeploy || !eng.State.Installed() || eng.State.Maintenance || deployPlanIsNoop(plan) {
		return run()
	}

	maintenanceEnabled := false
	eng.BeforeSwitch = func() error {
		until := time.Now().Add(automaticDeployMaintenanceDuration).UTC()
		if err := setMaintenanceMode(e.Paths, &eng.State, true, until, ""); err != nil {
			if errors.Is(err, errEdgeLayoutNotFound) {
				e.Out.Printf("warning: maintenance page skipped (%v); set QABAS_EDGE_NGINX_DIR to enable it\n", err)
				return nil
			}
			return err
		}
		maintenanceEnabled = true
		e.Out.Printf("maintenance mode on until %s\n", until.Local().Format("2006-01-02 15:04 MST"))
		return nil
	}

	if err := run(); err != nil {
		if maintenanceEnabled {
			e.Out.Println("deployment failed; maintenance mode remains on")
		}
		return err
	}
	if !maintenanceEnabled {
		return nil
	}

	if err := setMaintenanceMode(e.Paths, &eng.State, false, time.Time{}, ""); err != nil {
		return err
	}
	e.Out.Println("maintenance mode off")
	return nil
}

func deployPlanIsNoop(plan *deploy.Plan) bool {
	if plan == nil || len(plan.Steps) == 0 {
		return false
	}
	return plan.Steps[0] == "idempotent-skip" || plan.Steps[0] == "already-at-target"
}
