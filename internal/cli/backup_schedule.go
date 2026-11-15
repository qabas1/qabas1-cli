package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/backup"
	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/config"
	"github.com/qabas1/qabasone-cli/internal/doctor"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/lock"
	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/state"
	"github.com/qabas1/qabasone-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

const (
	scheduleLockWait   = 2 * time.Hour
	scheduleLockPoll   = 30 * time.Second
	scheduleMinFree    = 2 << 30
	scheduleStaleAge   = 36 * time.Hour
	offsiteDialTimeout = 30 * time.Second
)

var scheduleTimerPath = backup.ScheduleTimerPath

// scheduleRecord is the outcome of the most recent scheduled run.
type scheduleRecord struct {
	Time          time.Time `json:"time"`
	ID            string    `json:"id,omitempty"`
	Bytes         int64     `json:"bytes,omitempty"`
	DurationSec   float64   `json:"durationSeconds"`
	Pruned        []string  `json:"pruned,omitempty"`
	Offsite       string    `json:"offsite,omitempty"`
	OffsiteStatus string    `json:"offsiteStatus,omitempty"`
	OffsiteError  string    `json:"offsiteError,omitempty"`
	Error         string    `json:"error,omitempty"`
	LastSuccess   time.Time `json:"lastSuccess,omitempty"`
	LastSuccessID string    `json:"lastSuccessId,omitempty"`
}

func scheduleRecordFile(p paths.Paths) string {
	return filepath.Join(p.StateDir, "backup-schedule.json")
}

func loadScheduleRecord(p paths.Paths) (scheduleRecord, bool) {
	var r scheduleRecord
	b, err := os.ReadFile(scheduleRecordFile(p))
	if err != nil {
		return r, false
	}
	if json.Unmarshal(b, &r) != nil {
		return scheduleRecord{}, false
	}
	return r, true
}

func saveScheduleRecord(p paths.Paths, r scheduleRecord) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return err
	}
	tmp := scheduleRecordFile(p) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, scheduleRecordFile(p))
}

// scheduleWarnings reports why scheduled backups need attention (empty = healthy).
func scheduleWarnings(p paths.Paths, now time.Time) []string {
	cfg, err := config.Load(p)
	if err != nil || !cfg.Backup.Schedule.Enabled {
		return nil
	}
	if c, err := cluster.Load(p); err == nil && selfNodeName(p, c) != c.Writer {
		return nil
	}
	r, ok := loadScheduleRecord(p)
	var out []string
	if b, err := os.ReadFile(scheduleTimerPath); err == nil && !backup.TimerHasZone(string(b), cfg.Backup.Schedule.Zone()) {
		out = append(out, fmt.Sprintf("installed backup timer is not pinned to %s (re-run: sudo qabas backup schedule enable)",
			cfg.Backup.Schedule.Zone()))
	}
	switch {
	case !ok:
		out = append(out, "no scheduled backup has run yet (check: qabas backup schedule status)")
	case r.LastSuccess.IsZero():
		out = append(out, "no successful scheduled backup yet (check: qabas backup schedule status)")
	case now.Sub(r.LastSuccess) > scheduleStaleAge:
		out = append(out, fmt.Sprintf("last successful scheduled backup is %s old (%s)",
			now.Sub(r.LastSuccess).Round(time.Hour), r.LastSuccessID))
	}
	if ok && r.Error != "" && r.OffsiteStatus != "failed" {
		out = append(out, "last scheduled backup failed: "+r.Error)
	}
	if ok && r.OffsiteStatus == "failed" {
		out = append(out, fmt.Sprintf("last off-site push to %s failed: %s", r.Offsite, r.OffsiteError))
	}
	return out
}

// scheduleChecks are doctor rows for scheduled backups (warn level: never fails doctor).
func scheduleChecks(p paths.Paths, cfg config.Config, now time.Time) []doctor.Check {
	if !cfg.Backup.Schedule.Enabled {
		return nil
	}
	warns := scheduleWarnings(p, now)
	if len(warns) == 0 {
		detail := "daily at " + cfg.Backup.Schedule.At + " (" + cfg.Backup.Schedule.Zone() + ")"
		if r, ok := loadScheduleRecord(p); ok && !r.LastSuccess.IsZero() {
			detail += ", last " + r.LastSuccessID
		}
		return []doctor.Check{{Name: "backup-schedule", OK: true, Detail: detail, Level: "info"}}
	}
	out := make([]doctor.Check, 0, len(warns))
	for _, w := range warns {
		out = append(out, doctor.Check{Name: "backup-schedule", OK: false, Detail: w, Level: "warn"})
	}
	return out
}

// scheduleLocation is the schedule's zone; config.Load already rejects unknown zones.
func scheduleLocation(s config.Schedule) *time.Location {
	if loc, err := s.Location(); err == nil {
		return loc
	}
	return time.Local
}

// scheduleNextRun asks systemd for the next run, falling back to computing it from the
// schedule (approx=true) when systemd cannot report it.
func scheduleNextRun(ctx context.Context, x nodeexec.Executor, s config.Schedule, now time.Time) (next time.Time, approx bool) {
	if next = backup.NextScheduledRun(ctx, x); !next.IsZero() || !s.Enabled {
		return next, false
	}
	next = backup.NextDailyRun(now, s.At, scheduleLocation(s))
	return next, !next.IsZero()
}

func formatNextRun(next time.Time, approx bool, loc *time.Location) string {
	out := next.In(loc).Format("2006-01-02 15:04 MST")
	if approx {
		out += " (approx., up to 10m later)"
	}
	return out
}

func selfNodeName(p paths.Paths, c *cluster.Cluster) string {
	return (&env{Paths: p}).selfName(c)
}

// scheduleCluster returns the registry (nil on single-node installs) and whether this node is the writer.
func (e *env) scheduleCluster() (*cluster.Cluster, bool, error) {
	c, err := cluster.Load(e.Paths)
	if errors.Is(err, cluster.ErrNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return c, e.selfName(c) == c.Writer, nil
}

func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func newBackupScheduleCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Automatic scheduled full backups",
		Long: `Run a full backup (PostgreSQL + object storage) every day from a systemd timer.

--at is a time of day in --timezone (an IANA zone, default ` + config.DefaultTimezone + `),
independent of the host's clock setting; daylight saving is handled by systemd.
Retention days, weeks and months are counted in the same zone.

Retention is grandfather-father-son: keep the newest backup of each of the last
--keep-daily days, --keep-weekly ISO weeks, and --keep-monthly months. Only
backups labelled "scheduled" are ever pruned; manual, pre-deploy, and
pre-restore backups are left alone.

With --offsite, every verified backup is also streamed over the pinned SSH
connection to a registered replica and kept under ` + backup.OffsiteRoot + `/<writer>
with the same retention.

Scheduled runs only act on the current writer; on replicas they exit quietly.`,
		Example: `  sudo qabas backup schedule enable
  sudo qabas backup schedule enable --at 02:15 --keep-daily 14 --offsite dr1
  qabas backup schedule status
  sudo qabas backup schedule run
  sudo qabas backup schedule disable`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(
		newBackupScheduleEnableCmd(f),
		newBackupScheduleDisableCmd(f),
		newBackupScheduleStatusCmd(f),
		newBackupScheduleRunCmd(f),
	)
	return cmd
}

func newBackupScheduleEnableCmd(f *flags) *cobra.Command {
	var at, offsite, tz string
	var daily, weekly, monthly int
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Install the daily backup timer",
		Example: `  sudo qabas backup schedule enable
  sudo qabas backup schedule enable --at 02:15 --offsite dr1
  sudo qabas backup schedule enable --at 02:15 --timezone UTC`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			c, writer, err := e.scheduleCluster()
			if err != nil {
				return err
			}
			if !writer {
				return exitcode.NewHint(exitcode.InvalidInput,
					"enable it on the writer ("+c.Writer+")",
					"scheduled backups run on the writer; this node is %s", e.selfName(c))
			}
			cfg, err := config.Load(e.Paths)
			if err != nil {
				return err
			}
			s := cfg.Backup.Schedule
			if s.At == "" && s.KeepDaily == 0 && s.KeepWeekly == 0 && s.KeepMonthly == 0 {
				s = config.DefaultSchedule()
			}
			fl := cmd.Flags()
			if fl.Changed("at") {
				s.At = at
			}
			if fl.Changed("keep-daily") {
				s.KeepDaily = daily
			}
			if fl.Changed("keep-weekly") {
				s.KeepWeekly = weekly
			}
			if fl.Changed("keep-monthly") {
				s.KeepMonthly = monthly
			}
			if fl.Changed("offsite") {
				s.Offsite = strings.TrimSpace(offsite)
			}
			if fl.Changed("timezone") {
				s.Timezone = strings.TrimSpace(tz)
			}
			s.Timezone = s.Zone()
			if s.Offsite != "" {
				if err := validateOffsiteNode(c, s.Offsite); err != nil {
					return err
				}
			}
			s.Enabled = true
			if err := s.Validate(); err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			if f.DryRun {
				svc, tmr := backup.ScheduleUnits(qabasCmd, s.At, s.Timezone)
				e.Out.Printf("Would install %s:\n%s\n%s:\n%s", backup.ScheduleService, svc, backup.ScheduleTimer, tmr)
				return nil
			}
			return e.withLock(func() error {
				ctx := cmdContext(cmd)
				x := &nodeexec.Local{Name: "local"}
				if err := backup.RequireZoneinfo(ctx, x, s.Timezone); err != nil {
					if errors.Is(err, backup.ErrNoZoneinfo) {
						return exitcode.NewHint(exitcode.InvalidInput,
							"install timezone data (apt-get install -y tzdata) or pick another --timezone",
							"systemd cannot use timezone %s: %v", s.Timezone, err)
					}
					return err
				}
				if err := backup.InstallScheduleTimer(ctx, x, qabasCmd, s.At, s.Timezone); err != nil {
					return err
				}
				cfg.Backup.Schedule = s
				if err := config.Save(e.Paths, cfg); err != nil {
					return err
				}
				audit(e, "backup-schedule-enable", "", s.Offsite, nil, fmt.Sprintf("at %s %s keep %d/%d/%d", s.At, s.Timezone, s.KeepDaily, s.KeepWeekly, s.KeepMonthly))
				e.Out.Printf("Scheduled backups enabled: daily at %s (%s), keep %d daily / %d weekly / %d monthly.\n",
					s.At, s.Timezone, s.KeepDaily, s.KeepWeekly, s.KeepMonthly)
				if s.Offsite != "" {
					e.Out.Printf("Off-site copies go to %s:%s/%s.\n", s.Offsite, backup.OffsiteRoot, offsiteSource(e, c))
				}
				if next, approx := scheduleNextRun(ctx, x, s, time.Now()); !next.IsZero() {
					e.Out.Printf("Next run: %s\n", formatNextRun(next, approx, scheduleLocation(s)))
				}
				return nil
			})
		},
	}
	def := config.DefaultSchedule()
	cmd.Flags().StringVar(&at, "at", def.At, "daily run time HH:MM in --timezone (up to 10m random delay)")
	cmd.Flags().StringVar(&tz, "timezone", def.Timezone, "IANA timezone for --at and retention days, e.g. Africa/Cairo, UTC")
	cmd.Flags().IntVar(&daily, "keep-daily", def.KeepDaily, "daily backups to keep")
	cmd.Flags().IntVar(&weekly, "keep-weekly", def.KeepWeekly, "weekly backups to keep")
	cmd.Flags().IntVar(&monthly, "keep-monthly", def.KeepMonthly, "monthly backups to keep")
	cmd.Flags().StringVar(&offsite, "offsite", "", `registered replica that receives a copy of every backup ("" disables)`)
	return cmd
}

func validateOffsiteNode(c *cluster.Cluster, name string) error {
	if c == nil {
		return exitcode.NewHint(exitcode.InvalidInput,
			"add a replica first: sudo qabas replica add <node> ...",
			"--offsite %s: no cluster registry on this host", name)
	}
	for _, n := range c.Standbys() {
		if n.Name == name {
			return nil
		}
	}
	var names []string
	for _, n := range c.Standbys() {
		names = append(names, n.Name)
	}
	return exitcode.NewHint(exitcode.InvalidInput,
		"registered replicas: "+strings.Join(names, ", "),
		"--offsite %s is not a registered replica", name)
}

func offsiteSource(e *env, c *cluster.Cluster) string {
	if c == nil {
		return "local"
	}
	if s := e.selfName(c); s != "" {
		return s
	}
	return c.Writer
}

func newBackupScheduleDisableCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "disable",
		Short:   "Remove the backup timer (existing backups are kept)",
		Example: `  sudo qabas backup schedule disable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if f.DryRun {
				e.Out.Printf("Would remove %s and %s.\n", backup.ScheduleTimer, backup.ScheduleService)
				return nil
			}
			return e.withLock(func() error {
				if err := backup.RemoveScheduleTimer(cmdContext(cmd), &nodeexec.Local{Name: "local"}); err != nil {
					return err
				}
				cfg, err := config.Load(e.Paths)
				if err != nil {
					return err
				}
				cfg.Backup.Schedule.Enabled = false
				if err := config.Save(e.Paths, cfg); err != nil {
					return err
				}
				audit(e, "backup-schedule-disable", "", "", nil, "")
				e.Out.Println("Scheduled backups disabled. Existing backups were kept.")
				return nil
			})
		},
	}
}

type scheduleStatus struct {
	Schedule config.Schedule `json:"schedule"`
	Writer   bool            `json:"writer"`
	NextRun  *time.Time      `json:"nextRun,omitempty"`
	Approx   bool            `json:"nextRunApprox,omitempty"`
	Timezone string          `json:"timezone"`
	LastRun  *scheduleRecord `json:"lastRun,omitempty"`
	Local    []backup.Meta   `json:"local"`
	Offsite  []backup.Meta   `json:"offsite,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	Error    string          `json:"offsiteError,omitempty"`
}

func newBackupScheduleStatusCmd(f *flags) *cobra.Command {
	var noRemote bool
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show schedule, last run, and retained backups",
		Example: `  qabas backup schedule status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			ctx := cmdContext(cmd)
			cfg, err := config.Load(e.Paths)
			if err != nil {
				return err
			}
			c, writer, err := e.scheduleCluster()
			if err != nil {
				return err
			}
			st := scheduleStatus{Schedule: cfg.Backup.Schedule, Writer: writer, Timezone: cfg.Backup.Schedule.Zone(),
				Warnings: scheduleWarnings(e.Paths, time.Now())}
			if next, approx := scheduleNextRun(ctx, &nodeexec.Local{Name: "local"}, cfg.Backup.Schedule, time.Now()); !next.IsZero() {
				st.NextRun, st.Approx = &next, approx
			}
			if r, ok := loadScheduleRecord(e.Paths); ok {
				st.LastRun = &r
			}
			items, err := backup.List(e.Paths)
			if err != nil {
				return err
			}
			st.Local = backup.OnlyScheduled(items)
			if s := cfg.Backup.Schedule.Offsite; s != "" && c != nil && !noRemote {
				st.Offsite, err = listOffsite(ctx, e, c, s)
				if err != nil {
					st.Error = err.Error()
				}
			}
			if f.JSON {
				return e.Out.Encode(st)
			}
			printScheduleStatus(e, st)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noRemote, "no-remote", false, "do not contact the off-site replica")
	return cmd
}

func printScheduleStatus(e *env, st scheduleStatus) {
	s := st.Schedule
	loc := scheduleLocation(s)
	if s.Enabled {
		e.Out.Printf("Schedule:  daily at %s (%s), keep %d daily / %d weekly / %d monthly\n", s.At, s.Zone(), s.KeepDaily, s.KeepWeekly, s.KeepMonthly)
	} else {
		e.Out.Println("Schedule:  disabled (enable: sudo qabas backup schedule enable)")
	}
	if s.Offsite != "" {
		e.Out.Printf("Off-site:  %s\n", s.Offsite)
	}
	if !st.Writer {
		e.Out.Println("Role:      replica (scheduled runs are skipped here)")
	}
	if st.NextRun != nil {
		e.Out.Printf("Next run:  %s\n", formatNextRun(*st.NextRun, st.Approx, loc))
	}
	if r := st.LastRun; r != nil {
		result := "ok " + r.ID
		if r.Error != "" {
			result = "FAILED: " + r.Error
		}
		e.Out.Printf("Last run:  %s  %s  (%s, %.0fs)\n", r.Time.In(loc).Format("2006-01-02 15:04"), result, humanBytes(r.Bytes), r.DurationSec)
		if r.OffsiteStatus != "" {
			e.Out.Printf("Off-site:  %s %s\n", r.OffsiteStatus, r.OffsiteError)
		}
	}
	e.Out.Printf("Local scheduled backups (%d):\n", len(st.Local))
	for _, m := range st.Local {
		e.Out.Printf("  %s  %s\n", m.ID, m.CreatedAt.In(loc).Format("2006-01-02 15:04"))
	}
	if s.Offsite != "" {
		if st.Error != "" {
			e.Out.Printf("Off-site backups: unavailable (%s)\n", st.Error)
		} else if st.Offsite != nil {
			e.Out.Printf("Off-site backups on %s (%d):\n", s.Offsite, len(st.Offsite))
			for _, m := range st.Offsite {
				e.Out.Printf("  %s  %s\n", m.ID, m.CreatedAt.In(loc).Format("2006-01-02 15:04"))
			}
		}
	}
	for _, w := range st.Warnings {
		e.Out.Printf("WARNING: %s\n", w)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func dialOffsite(ctx context.Context, e *env, c *cluster.Cluster, name string) (nodeexec.Executor, error) {
	n := c.Node(name)
	if n == nil {
		return nil, fmt.Errorf("off-site node %s is not registered", name)
	}
	dctx, cancel := context.WithTimeout(ctx, offsiteDialTimeout)
	defer cancel()
	return nodeexec.DialNode(dctx, *n, os.Getenv(EnvSSHIdentity), cluster.SSHKeyPath(e.Paths))
}

func listOffsite(ctx context.Context, e *env, c *cluster.Cluster, name string) ([]backup.Meta, error) {
	x, err := dialOffsite(ctx, e, c, name)
	if err != nil {
		return nil, err
	}
	defer x.Close()
	items, err := backup.Offsite{X: x, Source: offsiteSource(e, c)}.List(ctx)
	return backup.OnlyScheduled(items), err
}

func newBackupScheduleRunCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run one scheduled backup now (what the timer runs)",
		Long: `Create, verify, prune, and (optionally) push one scheduled backup.

Waits up to 2h for other qabas operations (for example a deploy) to finish.
Exits 0 without doing anything when this node is not the writer.`,
		Example: `  sudo qabas backup schedule run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			c, writer, err := e.scheduleCluster()
			if err != nil {
				return err
			}
			if !writer {
				e.Out.Printf("Scheduled backup skipped: %s is not the writer (%s).\n", e.selfName(c), c.Writer)
				return nil
			}
			if err := requireBackupSupported(); err != nil {
				return err
			}
			cfg, err := config.Load(e.Paths)
			if err != nil {
				return err
			}
			s := cfg.Backup.Schedule
			if s.KeepDaily+s.KeepWeekly+s.KeepMonthly <= 0 {
				d := config.DefaultSchedule()
				s.KeepDaily, s.KeepWeekly, s.KeepMonthly = d.KeepDaily, d.KeepWeekly, d.KeepMonthly
			}
			if f.DryRun {
				e.Out.Printf("Would create a full backup named %q, keep %d/%d/%d", backup.ScheduledName, s.KeepDaily, s.KeepWeekly, s.KeepMonthly)
				if s.Offsite != "" {
					e.Out.Printf(", and push it to %s", s.Offsite)
				}
				e.Out.Println(".")
				return nil
			}
			ctx := cmdContext(cmd)
			l, err := e.acquireLock(ctx, lock.Options{Wait: scheduleLockWait, Poll: scheduleLockPoll})
			if err != nil {
				return err
			}
			defer l.Release()
			return runScheduledBackup(ctx, e, c, s)
		},
	}
}

func runScheduledBackup(ctx context.Context, e *env, c *cluster.Cluster, s config.Schedule) error {
	start := time.Now()
	prev, _ := loadScheduleRecord(e.Paths)
	rec := scheduleRecord{Time: start.UTC(), LastSuccess: prev.LastSuccess, LastSuccessID: prev.LastSuccessID, Offsite: s.Offsite}

	err := func() error {
		st, err := state.Load(e.Paths)
		if err != nil {
			return err
		}
		if !st.Installed() {
			return exitcode.NewHint(exitcode.InvalidInput, "sudo qabas install", "QabasOne is not installed")
		}
		if err := backup.RequireVPG(); err != nil {
			return err
		}
		if err := os.MkdirAll(e.Paths.Backups(), 0o700); err != nil {
			return err
		}
		if err := ensureBackupSpace(e, s, &rec); err != nil {
			return err
		}
		meta, err := backup.Create(e.Paths, newRuntime(), st, nil, true, true, backup.ScheduledName, false)
		if err != nil {
			return err
		}
		rec.ID = meta.ID
		if err := backup.Verify(e.Paths, meta.ID); err != nil {
			return fmt.Errorf("verify %s: %w", meta.ID, err)
		}
		rec.Bytes = dirSize(backup.Dir(e.Paths, meta.ID))
		rec.LastSuccess, rec.LastSuccessID = time.Now().UTC(), meta.ID
		e.Out.Printf("Backup %s created and verified (%s).\n", meta.ID, humanBytes(rec.Bytes))

		pruned, err := pruneScheduled(e, s, meta.ID)
		rec.Pruned = append(rec.Pruned, pruned...)
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		if s.Offsite != "" {
			rec.OffsiteStatus = "ok"
			if err := pushOffsite(ctx, e, c, s, *meta); err != nil {
				rec.OffsiteStatus, rec.OffsiteError = "failed", err.Error()
				return fmt.Errorf("off-site push to %s: %w", s.Offsite, err)
			}
			e.Out.Printf("Copied %s to %s.\n", meta.ID, s.Offsite)
		}
		return nil
	}()

	rec.DurationSec = time.Since(start).Round(time.Second).Seconds()
	if err != nil {
		rec.Error = err.Error()
	}
	if serr := saveScheduleRecord(e.Paths, rec); serr != nil && err == nil {
		err = serr
	}
	audit(e, "backup-schedule", "", rec.ID, err, fmt.Sprintf("%s in %.0fs, pruned %d", humanBytes(rec.Bytes), rec.DurationSec, len(rec.Pruned)))
	return err
}

// ensureBackupSpace refuses to start when the backups filesystem cannot hold
// another copy, pruning old scheduled backups first if that would help.
func ensureBackupSpace(e *env, s config.Schedule, rec *scheduleRecord) error {
	free := diskFreeBytes(e.Paths.Backups())
	if free < 0 {
		return nil
	}
	need := int64(scheduleMinFree)
	if items, err := backup.List(e.Paths); err == nil {
		if sched := backup.OnlyScheduled(items); len(sched) > 0 {
			if n := dirSize(backup.Dir(e.Paths, sched[0].ID)) * 3 / 2; n > need {
				need = n
			}
		}
	}
	if free >= need {
		return nil
	}
	pruned, err := pruneScheduled(e, s, "")
	rec.Pruned = append(rec.Pruned, pruned...)
	if err != nil {
		return err
	}
	if free = diskFreeBytes(e.Paths.Backups()); free >= 0 && free < need {
		return exitcode.NewHint(exitcode.InvalidInput,
			"free space under "+e.Paths.Backups()+" or lower --keep-* (qabas backup schedule enable)",
			"not enough disk for a backup: %s free, need %s", humanBytes(free), humanBytes(need))
	}
	return nil
}

// pruneScheduled applies GFS retention to local scheduled backups, never removing keepID.
func pruneScheduled(e *env, s config.Schedule, keepID string) ([]string, error) {
	items, err := backup.List(e.Paths)
	if err != nil {
		return nil, err
	}
	_, drop := backup.SelectGFS(backup.OnlyScheduled(items), s.KeepDaily, s.KeepWeekly, s.KeepMonthly, scheduleLocation(s))
	var removed []string
	for _, m := range drop {
		if m.ID == keepID {
			continue
		}
		if err := backup.Delete(e.Paths, m.ID, false); err != nil {
			return removed, err
		}
		removed = append(removed, m.ID)
		e.Out.Printf("Pruned %s.\n", m.ID)
	}
	return removed, nil
}

func pushOffsite(ctx context.Context, e *env, c *cluster.Cluster, s config.Schedule, m backup.Meta) error {
	if c == nil {
		return errors.New("no cluster registry")
	}
	x, err := dialOffsite(ctx, e, c, s.Offsite)
	if err != nil {
		return err
	}
	defer x.Close()
	o := backup.Offsite{X: x, Source: offsiteSource(e, c)}
	if err := o.Push(ctx, backup.Dir(e.Paths, m.ID), m); err != nil {
		return err
	}
	removed, err := o.Prune(ctx, s.KeepDaily, s.KeepWeekly, s.KeepMonthly, scheduleLocation(s))
	for _, id := range removed {
		e.Out.Printf("Pruned %s on %s.\n", id, s.Offsite)
	}
	return err
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// diskFreeBytes returns available bytes on dir's filesystem, or -1 when unknown.
func diskFreeBytes(dir string) int64 {
	d, err := sysinfo.DiskUsage(dir)
	if err != nil || d.TotalBytes == 0 {
		return -1
	}
	return int64(d.FreeBytes)
}
