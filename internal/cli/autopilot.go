package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/autopilot"
	"github.com/qabas1/qabasone-cli/internal/backup"
	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/config"
	"github.com/qabas1/qabasone-cli/internal/deploy"
	"github.com/qabas1/qabasone-cli/internal/doctor"
	"github.com/qabas1/qabasone-cli/internal/lock"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/state"
	"github.com/qabas1/qabasone-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

const (
	autopilotDockerTimeout = 30 * time.Minute
	autopilotLockLimit     = 30 * time.Minute
)

// Injected for tests.
var newAutopilotScheduler = autopilot.NewScheduler

func newAutopilotCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "autopilot",
		Aliases: []string{"self-maintenance"},
		Short:   "Unattended self-maintenance (disk, Docker, WAL, logs, self-heal)",
		Long: `Autopilot keeps a QabasOne host healthy without operator care. Every hour it:

  - prunes dangling Docker images and build cache older than 7 days
    (all unused build cache when disk space is low)
  - removes PITR WAL segments no kept base backup can replay
  - applies backup retention early when disk space is low, and trims scheduled
    backups to a minimum when disk is critical (manual, pre-deploy and
    pre-restore backups are never touched)
  - turns on container log rotation (json-file, ` + compose.LogMaxSize + ` x ` + compose.LogMaxFiles + ` per service)
  - turns maintenance mode off once its window has passed by more than 1h
  - restarts services reported unhealthy (at most 3 times per service per day)

It never deletes volumes, release images in use, or non-scheduled backups.
Runs skip quietly while another qabas operation holds the lock.`,
		Example: `  sudo qabas autopilot enable
  qabas autopilot status
  sudo qabas autopilot run
  sudo qabas autopilot disable`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newAutopilotRunCmd(f), newAutopilotEnableCmd(f), newAutopilotDisableCmd(f), newAutopilotStatusCmd(f))
	return cmd
}

func newAutopilotRunCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run one self-maintenance pass now",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if f.DryRun {
				info := sysinfo.Collect(sysinfo.Targets(e.Paths)...)
				e.Out.Printf("would run autopilot (resources: %s)\n", sysinfo.Worst(info.Findings))
				return nil
			}
			l, err := e.acquireLock(cmdContext(cmd), lock.Options{Info: lock.Info{Limit: autopilotLockLimit}})
			if errors.Is(err, lock.ErrBusy) {
				e.Out.Printf("Skipped: %v.\n", err)
				return nil
			}
			if err != nil {
				return err
			}
			defer l.Release()
			prev, _ := autopilot.Load(e.Paths)
			rec := autopilot.Run(autopilotDeps(cmdContext(cmd), e), prev)
			if err := autopilot.Save(e.Paths, rec); err != nil {
				return err
			}
			audit(e, "autopilot", "", "", nil, rec.Summary())
			if f.JSON {
				return e.Out.Encode(rec)
			}
			return renderAutopilotRecord(cmd, rec)
		},
	}
}

func autopilotDeps(ctx context.Context, e *env) autopilot.Deps {
	rt := newRuntime()
	st, _ := state.Load(e.Paths)
	cfg, _ := config.Load(e.Paths)
	_, writer, _ := e.scheduleCluster()
	d := autopilot.Deps{
		Now:     time.Now,
		Machine: func() sysinfo.Info { return sysinfo.Collect(sysinfo.Targets(e.Paths)...) },
		Tasks: []autopilot.Task{
			{Name: "logs-retention", Run: func() (string, uint64, error) { return enforceLogRetention(e.Paths) }},
			{Name: "permissions", Run: func() (string, uint64, error) { return "", 0, paths.Harden(e.Paths) }},
		},
	}
	if !rt.Available() {
		return d
	}
	d.Docker = func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, autopilotDockerTimeout)
		defer cancel()
		out, err := exec.CommandContext(c, "docker", args...).CombinedOutput()
		return string(out), err
	}
	d.Tasks = append(d.Tasks, autopilot.Task{Name: "loki-volume", Run: func() (string, uint64, error) { return lokiVolumeTask(d.Docker) }})
	if !st.Installed() {
		return d
	}
	env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
	d.Healable = !st.Maintenance && !deploy.IncompletePhase(st.Phase)
	d.Services = func() ([]compose.ServiceStatus, error) { return rt.ServiceStatus(e.Paths, env, st.CurrentRelease) }
	d.Restart = func(svc string) error { return rt.Restart(e.Paths, env, svc) }
	d.EnsureLogRotation = func() (bool, error) {
		if compose.DaemonLoggingDriver(ctx) != "json-file" {
			return false, nil
		}
		return compose.SyncLoggingOverlay(e.Paths, e.Paths.ComposeFile())
	}
	d.MaintenanceExpired = func(now time.Time) bool {
		return st.Maintenance && !st.MaintenanceUntil.IsZero() &&
			now.After(st.MaintenanceUntil.Add(autopilot.MaintenanceGrace)) && !deploy.IncompletePhase(st.Phase)
	}
	d.EndMaintenance = func() error { return setMaintenanceMode(e.Paths, &st, false, time.Time{}, "") }
	if !writer {
		return d
	}
	if m, _ := e.pitrManager(); runtime.GOOS == "linux" && m.Enabled() {
		d.PruneWAL = func() (int, int64, error) {
			r, err := m.PruneWAL(ctx)
			return r.Removed, r.Bytes, err
		}
	}
	if backup.Supported() {
		d.PruneBackups = func(minimal bool) ([]string, error) {
			return pruneScheduled(e, autopilotRetention(cfg.Backup.Schedule, minimal), "")
		}
	}
	return d
}

// autopilotRetention is the configured schedule retention, or the emergency
// minimum (2 daily + 1 weekly) when disk is critical.
func autopilotRetention(s config.Schedule, minimal bool) config.Schedule {
	if s.KeepDaily+s.KeepWeekly+s.KeepMonthly <= 0 {
		d := config.DefaultSchedule()
		s.KeepDaily, s.KeepWeekly, s.KeepMonthly = d.KeepDaily, d.KeepWeekly, d.KeepMonthly
	}
	if minimal {
		s.KeepDaily, s.KeepWeekly, s.KeepMonthly = min(s.KeepDaily, 2), min(s.KeepWeekly, 1), 0
		if s.KeepDaily+s.KeepWeekly == 0 {
			s.KeepDaily = 1
		}
	}
	return s
}

func renderAutopilotRecord(cmd *cobra.Command, rec autopilot.Record) error {
	w := cmd.OutOrStdout()
	rows := make([][]string, 0, len(rec.Actions))
	for _, a := range rec.Actions {
		status, detail := "ok", a.Detail
		if a.Error != "" {
			status = "FAIL"
			detail = strings.TrimSpace(detail + " " + a.Error)
		}
		rows = append(rows, []string{status, a.Task, detail})
	}
	if len(rows) > 0 {
		if err := output.Table(w, []string{"STATUS", "TASK", "DETAIL"}, rows); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Freed %s; resources %s.\n", sysinfo.HumanBytes(rec.FreedBytes), rec.Health)
	for _, fd := range rec.Findings {
		if fd.Level != sysinfo.LevelOK {
			fmt.Fprintf(w, "WARNING: %s: %s\n", fd.Check, fd.Detail)
		}
	}
	return nil
}

// autopilotBinary is the command the scheduler runs.
func autopilotBinary() string {
	if runtime.GOOS == "windows" {
		if p, err := os.Executable(); err == nil {
			return p
		}
	}
	return qabasCmd
}

// enableAutopilot installs the scheduler and clears an opt-out.
func enableAutopilot(ctx context.Context, e *env) error {
	s := newAutopilotScheduler()
	if err := s.Install(ctx, autopilotBinary()); err != nil {
		return err
	}
	if err := autopilot.SetOptOut(e.Paths, false); err != nil {
		return err
	}
	audit(e, "autopilot-enable", "", "", nil, s.Describe())
	return nil
}

func newAutopilotEnableCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Install the hourly autopilot schedule",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			s := newAutopilotScheduler()
			if f.DryRun {
				e.Out.Printf("would install %s running: %s autopilot run\n", s.Describe(), autopilotBinary())
				return nil
			}
			if err := enableAutopilot(cmdContext(cmd), e); err != nil {
				return wrapErr(err)
			}
			e.Out.Printf("Autopilot enabled: %s.\nRun a pass now with: qabas autopilot run\n", s.Describe())
			return nil
		},
	}
}

func newAutopilotDisableCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove the autopilot schedule",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			s := newAutopilotScheduler()
			if f.DryRun {
				e.Out.Printf("would remove %s\n", s.Describe())
				return nil
			}
			if err := s.Remove(cmdContext(cmd)); err != nil {
				return wrapErr(err)
			}
			if err := autopilot.SetOptOut(e.Paths, true); err != nil {
				return err
			}
			audit(e, "autopilot-disable", "", "", nil, "")
			e.Out.Println("Autopilot disabled. Disk, logs and WAL are no longer maintained automatically.")
			return nil
		},
	}
}

type autopilotStatus struct {
	Enabled   bool              `json:"enabled"`
	Scheduler string            `json:"scheduler"`
	OptedOut  bool              `json:"optedOut"`
	LogRotate bool              `json:"logRotation"`
	Last      *autopilot.Record `json:"last,omitempty"`
}

func newAutopilotStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the autopilot schedule and last run",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			s := newAutopilotScheduler()
			st := autopilotStatus{
				Enabled:   s.Installed(cmdContext(cmd)),
				Scheduler: s.Describe(),
				OptedOut:  autopilot.OptedOut(e.Paths),
				LogRotate: compose.LoggingOverlayEnabled(e.Paths),
			}
			if r, ok := autopilot.Load(e.Paths); ok {
				st.Last = &r
			}
			if f.JSON {
				return e.Out.Encode(st)
			}
			w := cmd.OutOrStdout()
			state := "disabled (enable: sudo qabas autopilot enable)"
			if st.Enabled {
				state = "enabled — " + st.Scheduler
			}
			rows := [][]string{{"Schedule", state}, {"Log rotation", onOff(st.LogRotate)}}
			if st.Last != nil {
				rows = append(rows,
					[]string{"Last run", st.Last.Time.Local().Format("2006-01-02 15:04") + " (" + time.Since(st.Last.Time).Round(time.Minute).String() + " ago)"},
					[]string{"Resources", st.Last.Health},
					[]string{"Freed", sysinfo.HumanBytes(st.Last.FreedBytes)},
				)
				for _, a := range st.Last.Errors() {
					rows = append(rows, []string{"Failed", a.Task + ": " + a.Error})
				}
			} else {
				rows = append(rows, []string{"Last run", "never"})
			}
			return output.Table(w, []string{"AUTOPILOT", "VALUE"}, rows)
		},
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// securityChecks are doctor's security rows (warn level: never fail doctor).
func securityChecks(ctx context.Context, e *env, installed bool) []doctor.Check {
	var docker func(args ...string) (string, error)
	driver := ""
	if newRuntime().Available() {
		docker = func(args ...string) (string, error) {
			c, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			out, err := exec.CommandContext(c, "docker", args...).Output()
			return string(out), err
		}
		driver = compose.DaemonLoggingDriver(ctx)
	}
	return doctor.SecurityChecks(e.Paths, installed, doctor.DefaultSecurityProbes(docker, driver))
}

// autopilotChecks are doctor rows (warn level: never fails doctor).
func autopilotChecks(ctx context.Context, p paths.Paths, installed bool) []doctor.Check {
	if !installed {
		return nil
	}
	if autopilot.OptedOut(p) {
		return []doctor.Check{{Name: "autopilot", OK: true, Detail: "disabled by operator", Level: "info"}}
	}
	s := newAutopilotScheduler()
	if !s.Installed(ctx) {
		return []doctor.Check{{Name: "autopilot", OK: false, Detail: "not scheduled — enable: sudo qabas autopilot enable", Level: "warn"}}
	}
	r, ok := autopilot.Load(p)
	switch {
	case !ok:
		return []doctor.Check{{Name: "autopilot", OK: false, Detail: "scheduled but has not run yet (run now: sudo qabas autopilot run)", Level: "warn"}}
	case time.Since(r.Time) > autopilot.StaleAfter:
		return []doctor.Check{{Name: "autopilot", OK: false, Detail: fmt.Sprintf("last run %s ago (check: qabas autopilot status)", time.Since(r.Time).Round(time.Minute)), Level: "warn"}}
	case len(r.Errors()) > 0:
		a := r.Errors()[0]
		return []doctor.Check{{Name: "autopilot", OK: false, Detail: fmt.Sprintf("last run had %d failed task(s); %s: %s", len(r.Errors()), a.Task, a.Error), Level: "warn"}}
	}
	return []doctor.Check{{Name: "autopilot", OK: true, Detail: "last run " + r.Time.Local().Format("2006-01-02 15:04") + ", freed " + sysinfo.HumanBytes(r.FreedBytes), Level: "info"}}
}
