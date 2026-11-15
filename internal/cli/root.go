package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/clilog"
	"github.com/qabas1/qabasone-cli/internal/config"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/hint"
	"github.com/qabas1/qabasone-cli/internal/interrupt"
	"github.com/qabas1/qabasone-cli/internal/lock"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/redact"
	"github.com/qabas1/qabasone-cli/internal/secrets"
	"github.com/qabas1/qabasone-cli/internal/state"
	"github.com/qabas1/qabasone-cli/internal/update"
	"github.com/qabas1/qabasone-cli/internal/version"
	"github.com/spf13/cobra"
)

const (
	groupCommon     = "common"
	groupManagement = "management"
	groupLifecycle  = "lifecycle"
	groupSystem     = "system"
)

type flags struct {
	DryRun         bool
	Yes            bool
	NonInteractive bool
	JSON           bool
	Quiet          bool
	Channel        string
}

func NewRoot() *cobra.Command {
	// Preserve AddCommand order within groups (install before auth, etc.).
	cobra.EnableCommandSorting = false

	f := &flags{}
	root := &cobra.Command{
		Use:                   "qabas [OPTIONS] COMMAND",
		Short:                 "QabasOne system administration CLI",
		SilenceUsage:          true,
		SilenceErrors:         true,
		Version:               version.Version,
		DisableFlagsInUseLine: true,
		Args:                  argsWithSuggestions,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.AddGroup(
		&cobra.Group{ID: groupCommon, Title: "Common Commands:"},
		&cobra.Group{ID: groupManagement, Title: "Management Commands:"},
		&cobra.Group{ID: groupLifecycle, Title: "Lifecycle Commands:"},
		&cobra.Group{ID: groupSystem, Title: "System Commands:"},
	)
	root.PersistentFlags().BoolVar(&f.DryRun, "dry-run", false, "print the plan without changing the system")
	root.PersistentFlags().BoolVarP(&f.Yes, "yes", "y", false, "assume yes for confirmation prompts")
	root.PersistentFlags().BoolVar(&f.NonInteractive, "non-interactive", false, "fail instead of prompting")
	root.PersistentFlags().BoolVar(&f.JSON, "json", false, "machine-readable JSON output")
	root.PersistentFlags().BoolVarP(&f.Quiet, "quiet", "q", false, "suppress human output")
	root.PersistentFlags().StringVar(&f.Channel, "channel", "", "release channel (default: stable)")
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetHelpCommandGroupID(groupSystem)
	root.SuggestionsMinimumDistance = 2
	_ = root.RegisterFlagCompletionFunc("channel", completeChannel)

	install := newInstallCmd(f)
	install.GroupID = groupCommon
	install.SuggestFor = []string{"setup", "init"}
	upgrade := newUpgradeCmd(f)
	upgrade.GroupID = groupCommon
	upgrade.SuggestFor = []string{"update-product"}
	status := newStatusCmd(f)
	status.GroupID = groupCommon
	status.SuggestFor = []string{"info", "health"}
	ps := newPsCmd(f)
	ps.GroupID = groupCommon
	topCmd := newTopCmd(f)
	topCmd.GroupID = groupCommon
	topCmd.SuggestFor = []string{"dashboard", "monitor", "stats", "watch"}
	doctor := newDoctorCmd(f)
	doctor.GroupID = groupCommon
	doctor.SuggestFor = []string{"diag", "diagnose", "check"}
	guideCmd := newGuideCmd(f)
	guideCmd.GroupID = groupCommon
	guideCmd.SuggestFor = []string{"howto", "tutorial", "walkthrough"}
	logs := newLogsCmd(f)
	logs.GroupID = groupCommon
	auth := newAuthCmd(f)
	auth.GroupID = groupCommon
	login := newAuthLoginCmd(f)
	login.GroupID = groupCommon
	login.SuggestFor = []string{"signin"}

	release := newReleaseCmd(f)
	release.GroupID = groupManagement
	backup := newBackupCmd(f)
	backup.GroupID = groupManagement
	restore := newRestoreCmd(f)
	restore.GroupID = groupManagement
	cfg := newConfigCmd(f)
	cfg.GroupID = groupManagement
	db := newDBCmd(f)
	db.GroupID = groupManagement
	maintenance := newMaintenanceCmd(f)
	maintenance.GroupID = groupManagement
	replicaCmd := newReplicaCmd(f)
	replicaCmd.GroupID = groupManagement
	replicaCmd.SuggestFor = []string{"replicas", "standby", "mirror"}
	relayCmd := newRelayCmd(f)
	relayCmd.GroupID = groupManagement
	drCmd := newDRCmd(f)
	drCmd.GroupID = groupManagement
	drCmd.SuggestFor = []string{"failover", "promote", "switchover"}
	autopilotCmd := newAutopilotCmd(f)
	autopilotCmd.GroupID = groupManagement
	autopilotCmd.SuggestFor = []string{"housekeeping", "cleanup", "prune", "selfheal"}
	secretsCmd := newSecretsCmd(f)
	secretsCmd.GroupID = groupManagement
	secretsCmd.SuggestFor = []string{"secret", "credentials", "passwords", "vault"}
	limitsCmd := newLimitsCmd(f)
	limitsCmd.GroupID = groupManagement
	limitsCmd.SuggestFor = []string{"quota", "quotas", "resources-limits"}
	scaleCmd := newScaleCmd(f)
	scaleCmd.GroupID = groupManagement
	autoscaleCmd := newAutoscaleCmd(f)
	autoscaleCmd.GroupID = groupManagement
	gatewayCmd := newGatewayCmd(f)
	gatewayCmd.GroupID = groupManagement

	deploy := newDeployCmd(f)
	deploy.GroupID = groupLifecycle
	up := newUpCmd(f)
	up.GroupID = groupLifecycle
	start := newStartCmd(f)
	start.GroupID = groupLifecycle
	down := newDownCmd(f)
	down.GroupID = groupLifecycle
	stop := newStopCmd(f)
	stop.GroupID = groupLifecycle
	restart := newRestartCmd(f)
	restart.GroupID = groupLifecycle
	downgrade := newDowngradeCmd(f)
	downgrade.GroupID = groupLifecycle
	rollback := newRollbackCmd(f)
	rollback.GroupID = groupLifecycle
	rollback.SuggestFor = []string{"undo", "revert"}
	uninstall := newUninstallCmd(f)
	uninstall.GroupID = groupLifecycle
	uninstall.SuggestFor = []string{"remove", "delete"}

	history := newHistoryCmd(f)
	history.GroupID = groupSystem
	sysinfoCmd := newSysinfoCmd(f)
	sysinfoCmd.GroupID = groupSystem
	sysinfoCmd.SuggestFor = []string{"resources", "hardware", "specs"}
	ver := newVersionCmd(f)
	ver.GroupID = groupSystem
	update := newUpdateCmd(f)
	update.GroupID = groupSystem
	completion := newCompletionCmd()
	completion.GroupID = groupSystem
	lockCmd := newLockCmd(f)
	lockCmd.GroupID = groupSystem
	lockCmd.SuggestFor = []string{"unlock", "locks"}

	root.AddCommand(
		install, upgrade, status, ps, topCmd, doctor, guideCmd, logs, auth, login,
		release, backup, restore, cfg, db, maintenance, replicaCmd, relayCmd, drCmd, autopilotCmd, secretsCmd, limitsCmd, scaleCmd, autoscaleCmd, gatewayCmd,
		deploy, up, start, down, stop, restart, downgrade, rollback, uninstall,
		history, sysinfoCmd, lockCmd, ver, update, completion,
	)
	localizeHelp(root)
	return root
}

// localizeHelp rewrites sudo-style help text for the current OS.
func localizeHelp(cmd *cobra.Command) {
	cmd.Short = hint.Localize(cmd.Short)
	cmd.Long = hint.Localize(cmd.Long)
	cmd.Example = hint.Localize(cmd.Example)
	for _, c := range cmd.Commands() {
		localizeHelp(c)
	}
}

func Execute(args []string) int {
	interrupt.Install()
	update.CleanupStale()
	if msg := paths.MigrateLegacy(); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	p := paths.Resolve()
	secrets.RegisterKnown(p)
	start := time.Now()
	root := NewRoot()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		code := exitcode.CodeOf(err)
		msg := redact.String(hint.Localize(err.Error()))
		h := redact.String(hint.Localize(exitcode.HintOf(err)))
		fmt.Fprintf(os.Stderr, "error: %s\n", msg)
		if h != "" {
			fmt.Fprintf(os.Stderr, "hint: %s\n", h)
		}
		logCommand(p, args, start, code, msg, h)
		return code
	}
	logCommand(p, args, start, exitcode.Success, "", "")
	return exitcode.Success
}

// unloggedCommands run too often (shell completion) or say nothing about the
// system to be worth an audit line.
var unloggedCommands = map[string]bool{
	"__complete": true, "__completeNoDesc": true, "completion": true,
	"help": true, "version": true, "--version": true, "-v": true, "--help": true, "-h": true,
}

func logCommand(p paths.Paths, args []string, start time.Time, code int, msg, h string) {
	if len(args) == 0 || unloggedCommands[args[0]] {
		return
	}
	_ = clilog.Record(p.LogFile(), clilog.Entry{
		Time: start, Args: args, Exit: code, Duration: time.Since(start), Error: msg, Hint: h,
	})
}

type env struct {
	flags *flags
	Paths paths.Paths
	Out   *output.Printer
}

func load(f *flags) *env {
	return &env{
		flags: f,
		Paths: paths.Resolve(),
		Out:   output.New(f.JSON, f.Quiet),
	}
}

func (e *env) channel() string {
	if e.flags.Channel != "" {
		return e.flags.Channel
	}
	cfg, err := config.Load(e.Paths)
	if err == nil && cfg.Channel != "" {
		return cfg.Channel
	}
	return "stable"
}

func (e *env) cfgState() (config.Config, state.State, error) {
	cfg, err := config.Load(e.Paths)
	if err != nil {
		return cfg, state.State{}, err
	}
	if e.flags.Channel != "" {
		cfg.Channel = e.flags.Channel
	}
	st, err := state.Load(e.Paths)
	return cfg, st, err
}

func (e *env) withLock(fn func() error) error {
	if e.flags.DryRun {
		return fn()
	}
	if err := requireRoot(); err != nil {
		return err
	}
	wait := lockWaitNonInteractive
	if stdinIsTTY() && !e.flags.NonInteractive {
		wait = -1
	}
	l, err := e.acquireLock(context.Background(), lock.Options{Wait: wait})
	if err != nil {
		return err
	}
	defer l.Release()
	return fn()
}

// lockWaitNonInteractive bounds how long scripts and timers wait behind a healthy holder.
const lockWaitNonInteractive = 30 * time.Minute

// acquireLock takes the operations lock with auto-resolve: stuck holders are
// stopped (and audited), healthy ones are waited on with a progress line.
func (e *env) acquireLock(ctx context.Context, o lock.Options) (*lock.Lock, error) {
	o.Takeover = true
	var lastNote time.Time
	o.OnWait = func(st lock.Status) {
		if time.Since(lastNote) < 15*time.Second {
			return
		}
		lastNote = time.Now()
		note := "Waiting for " + st.Summary()
		if st.Critical {
			note += " [" + st.Reason + ", never interrupted]"
		}
		if o.Wait < 0 {
			note += "... Ctrl+C to cancel"
		}
		e.Out.Warnf("%s", note)
	}
	o.OnTakeover = func(st lock.Status) {
		e.Out.Warnf("Stopping stuck qabas operation: %s: %s\n", st.Summary(), st.Reason)
		audit(e, "lock-takeover", "", "", nil, st.Summary()+": "+st.Reason)
	}
	return lock.AcquireOrResolve(ctx, e.Paths.LockPath, o)
}

// requireRoot refuses state-changing commands from non-root users on Linux,
// where /etc/qabasone secrets and the Docker socket are root-only. Custom
// QABAS_* path overrides (development, tests) opt out.
func requireRoot() error {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		return nil
	}
	for _, k := range []string{paths.EnvConfigDir, paths.EnvStateDir, paths.EnvDataDir, paths.EnvLockPath} {
		if os.Getenv(k) != "" {
			return nil
		}
	}
	return exitcode.NewHint(exitcode.InvalidInput,
		"re-run with sudo: sudo qabas "+strings.Join(os.Args[1:], " "),
		"this command changes the system and must run as root")
}

func (e *env) confirm(prompt string) error {
	if e.flags.Yes || e.flags.DryRun {
		return nil
	}
	if e.flags.NonInteractive {
		return exitcode.NewHint(exitcode.InvalidInput,
			"re-run with --yes (or -y)",
			"refusing to continue without --yes (non-interactive)")
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	if ans != "y" && ans != "Y" && ans != "yes" {
		return exitcode.New(exitcode.InvalidInput, "aborted")
	}
	return nil
}

func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.As(err, new(*exitcode.Error)) {
		return err
	}
	return exitcode.Wrap(exitcode.Generic, err)
}
