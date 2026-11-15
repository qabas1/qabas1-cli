package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/qabas1/qabasone-cli/internal/autopilot"
	"github.com/qabas1/qabasone-cli/internal/backup"
	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/lock"
	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/qabas1/qabasone-cli/internal/state"
	"github.com/qabas1/qabasone-cli/internal/sysinfo"
	"github.com/qabas1/qabasone-cli/internal/top"
	"github.com/qabas1/qabasone-cli/internal/top/tui"
)

// Slow, rarely-changing sources are refreshed less often than the dashboard.
const (
	topDockerInfoTTL = time.Minute
	topScheduleTTL   = time.Minute

	topEnginePingTimeout = 2 * time.Second

	// Windows consoles do not report zoom or font changes as resize events.
	topSizePoll = 500 * time.Millisecond
)

func newTopCmd(f *flags) *cobra.Command {
	var (
		interval   time.Duration
		once       bool
		noPostgres bool
		noReplicas bool
		sortBy     string
	)
	cmd := &cobra.Command{
		Use:     "top",
		Aliases: []string{"htop"},
		Short:   "Live dashboard of QabasOne services, database, and replicas",
		Long: "Show a live, read-only dashboard of this QabasOne node: machine resources, per-service CPU, memory, " +
			"network and disk I/O, PostgreSQL connections, throughput and running queries, replica lag, " +
			"and everything doctor would flag, refreshed in place.\n\n" +
			"Keys: tab/1-4 switch pane, s sort, r reverse, / filter, +/- interval, p pause, ? help, q quit.\n" +
			"Without a terminal (pipes, scripts) it prints one snapshot, like --once.",
		Example: `  qabas top
  qabas htop --interval 5s --sort mem
  qabas top --once
  qabas top --json
  qabas top --json --interval 10s     # stream one JSON snapshot per line`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			ctx := cmdContext(cmd)
			w := cmd.OutOrStdout()
			c := top.NewCollector(topSources(e, noPostgres, noReplicas))

			if f.JSON {
				if !cmd.Flags().Changed("interval") {
					return e.Out.Encode(c.Collect(ctx))
				}
				return streamTopJSON(ctx, w, c, interval)
			}
			if once || !stdoutIsTTY() {
				return renderTopOnce(w, c.Collect(ctx))
			}
			r := lipgloss.NewRenderer(os.Stdout)
			if os.Getenv("NO_COLOR") != "" {
				r.SetColorProfile(termenv.Ascii)
			}
			opts := tui.Options{
				Collect:  c.Collect,
				Interval: interval,
				Sort:     tui.ParseSortKey(sortBy),
				Renderer: r,
				SizeFunc: terminalSize,
			}
			opts.Width, opts.Height, _ = terminalSize()
			if runtime.GOOS == "windows" {
				opts.PollSize = topSizePoll
			}
			return tui.Run(opts)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "refresh interval (1s to 1m)")
	cmd.Flags().BoolVar(&once, "once", false, "print one snapshot as plain tables and exit")
	cmd.Flags().BoolVar(&noPostgres, "no-postgres", false, "skip PostgreSQL activity")
	cmd.Flags().BoolVar(&noReplicas, "no-replicas", false, "skip replication status")
	cmd.Flags().StringVar(&sortBy, "sort", "cpu", "initial services sort: cpu, mem, net, io, restarts, name")
	_ = cmd.RegisterFlagCompletionFunc("sort", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"cpu", "mem", "net", "io", "restarts", "name"}, cobra.ShellCompDirectiveNoFileComp
	})
	return cmd
}

func streamTopJSON(ctx context.Context, w io.Writer, c *top.Collector, interval time.Duration) error {
	if interval < tui.MinInterval {
		interval = tui.MinInterval
	}
	enc := json.NewEncoder(w)
	for {
		if err := enc.Encode(c.Collect(ctx)); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

// terminalSize is the visible window of the terminal on stdout.
func terminalSize() (int, int, bool) {
	w, h, err := term.GetSize(int(os.Stdout.Fd()))
	return w, h, err == nil && w > 0 && h > 0
}

func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func topSources(e *env, noPostgres, noReplicas bool) top.Sources {
	probes := sysinfo.DefaultProbes()
	probes.Docker = top.CachedProbe(topDockerInfoTTL, probes.Docker)
	probes.Addrs = nil
	targets := sysinfo.Targets(e.Paths)
	role, roleErr := localNodeRole(e)
	replicaStack := roleErr == nil && isReplicaRole(role)
	var pg nodeexec.Executor
	if st, err := state.Load(e.Paths); err == nil && (st.Installed() || replicaStack) {
		pg = topPostgresExecutor()
	}
	var engine *top.Engine
	if sock := top.EngineSocket(); sock != "" {
		ctx, cancel := context.WithTimeout(context.Background(), topEnginePingTimeout)
		engine, _ = top.NewEngine(ctx, sock)
		cancel()
	}

	return top.Sources{
		Host:     func() sysinfo.Info { return sysinfo.CollectWith(probes, targets...) },
		CPUTimes: sysinfo.ReadCPUTimes,
		CPUStat:  sysinfo.ReadCPUStat,
		Tasks:    sysinfo.ReadTasks,
		Services: func(ctx context.Context) ([]top.BaseService, error) {
			rt := newRuntime()
			if !rt.Available() {
				return nil, errors.New("docker is not installed")
			}
			st, err := state.Load(e.Paths)
			if err != nil {
				return nil, err
			}
			role, err := localNodeRole(e)
			if err != nil {
				return nil, err
			}
			if !st.Installed() && !isReplicaRole(role) {
				return nil, errors.New("QabasOne is not installed on this machine")
			}
			rows, err := rt.ServiceList(e.Paths, compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease), st.CurrentRelease)
			if err != nil {
				return nil, err
			}
			out := make([]top.BaseService, 0, len(rows))
			for _, r := range rows {
				health := r.Health
				if health == "-" {
					health = ""
				}
				out = append(out, top.BaseService{Service: r.Service, Name: r.Name, State: r.State, Health: health})
			}
			return out, nil
		},
		Docker: func(ctx context.Context, args ...string) ([]byte, error) {
			out, err := exec.CommandContext(ctx, "docker", args...).Output()
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return out, errors.New(strings.TrimSpace(string(ee.Stderr)))
			}
			return out, err
		},
		Engine:     engine,
		PG:         pg,
		State:      topState(e),
		NoPostgres: noPostgres,
		NoReplicas: noReplicas,
	}
}

// topPostgresExecutor reaches the local postgres container; psql scripts need a POSIX sh.
func topPostgresExecutor() nodeexec.Executor {
	if _, err := exec.LookPath("sh"); err != nil {
		return unavailableExecutor{reason: "needs a POSIX sh on PATH (e.g. Git Bash) to query the postgres container"}
	}
	return &nodeexec.Local{Name: "local"}
}

type unavailableExecutor struct{ reason string }

func (u unavailableExecutor) Node() string { return "local" }
func (u unavailableExecutor) Close() error { return nil }
func (u unavailableExecutor) Run(context.Context, string, io.Reader) (nodeexec.Result, error) {
	return nodeexec.Result{}, errors.New(u.reason)
}

// topState reads qabas state files. Backup and autopilot records change rarely and
// asking systemd for the next run is slow, so those are cached.
func topState(e *env) func(context.Context) top.StateSection {
	type slow struct {
		lastBackup, nextBackup, autopilotLast time.Time
		autopilotStatus                       string
		warnings                              []string
	}
	cached := top.CachedProbe(topScheduleTTL, func() slow {
		var s slow
		now := time.Now()
		if items, err := backup.List(e.Paths); err == nil && len(items) > 0 {
			s.lastBackup = items[len(items)-1].CreatedAt
		}
		cfg, st, err := e.cfgState()
		if err != nil {
			return s
		}
		if cfg.Backup.Schedule.Enabled {
			ctx, cancel := context.WithTimeout(context.Background(), top.DefaultSourceTimeout/2)
			s.nextBackup, _ = scheduleNextRun(ctx, &nodeexec.Local{Name: "local"}, cfg.Backup.Schedule, now)
			cancel()
		}
		s.warnings = scheduleWarnings(e.Paths, now)
		if st.Installed() {
			switch r, ok := autopilot.Load(e.Paths); {
			case autopilot.OptedOut(e.Paths):
				s.autopilotStatus = "disabled"
			case !ok:
				s.autopilotStatus = "not run yet"
				s.warnings = append(s.warnings, "autopilot has not run yet (run now: sudo qabas autopilot run)")
			case len(r.Errors()) > 0:
				a := r.Errors()[0]
				s.autopilotStatus, s.autopilotLast = fmt.Sprintf("%d failed task(s)", len(r.Errors())), r.Time
				s.warnings = append(s.warnings, fmt.Sprintf("autopilot last run had %d failed task(s); %s: %s", len(r.Errors()), a.Task, a.Error))
			case now.Sub(r.Time) > autopilot.StaleAfter:
				s.autopilotStatus, s.autopilotLast = "stale", r.Time
				s.warnings = append(s.warnings, fmt.Sprintf("autopilot last ran %s ago (check: qabas autopilot status)", now.Sub(r.Time).Round(time.Minute)))
			default:
				s.autopilotStatus, s.autopilotLast = "ok", r.Time
			}
		}
		return s
	})

	return func(ctx context.Context) top.StateSection {
		var out top.StateSection
		cfg, st, err := e.cfgState()
		if err != nil {
			out.Err = err.Error()
		}
		if ls, lerr := state.Load(e.Paths); lerr == nil {
			out.Probed, out.Installed = true, ls.Installed()
		}
		role, roleErr := localNodeRole(e)
		if roleErr != nil {
			if out.Err == "" {
				out.Err = roleErr.Error()
			}
		} else if isReplicaRole(role) {
			out.Probed, out.Installed, out.Role = true, true, string(role)
		}
		out.Release, out.Maintenance = st.CurrentRelease, st.Maintenance
		out.Channel = cfg.Channel
		if out.Channel == "" {
			out.Channel = "stable"
		}
		if ls, err := lock.Inspect(e.Paths.LockPath); err == nil && ls.Held {
			out.Lock, out.LockStuck = ls.Summary(), ls.Stuck
		}
		s := cached()
		out.LastBackup, out.NextBackup = s.lastBackup, s.nextBackup
		out.AutopilotLast, out.AutopilotStatus = s.autopilotLast, s.autopilotStatus
		out.Warnings = s.warnings
		return out
	}
}

func renderTopOnce(w io.Writer, s top.Snapshot) error {
	info := s.Host.Info
	release := s.State.Release
	if release == "" && s.State.Role != "" {
		release = "replica"
	}
	releaseLabel := orDash(release)
	if s.State.Release != "" && s.State.Channel != "" {
		releaseLabel += " (" + s.State.Channel + ")"
	}
	sum := [][]string{
		{"Host", orDash(info.Hostname)},
		{"Release", releaseLabel},
		{"Collected", s.CollectedAt.Format("2006-01-02 15:04:05")},
		{"CPU", fmt.Sprintf("%.1f%% of %d cores", info.CPU.UsagePercent, info.CPU.Cores)},
	}
	if s.State.Role != "" {
		sum = append(sum, []string{"Role", s.State.Role})
	}
	if m := info.Memory; m != nil {
		sum = append(sum, []string{"Memory", fmt.Sprintf("%.1f%% used, %s free of %s", m.UsedPercent, sysinfo.HumanBytes(m.AvailableBytes), sysinfo.HumanBytes(m.TotalBytes))})
	}
	maint := "off"
	if s.State.Maintenance {
		maint = "ON"
	}
	sum = append(sum, []string{"Maintenance", maint})
	if s.State.Lock != "" {
		sum = append(sum, []string{"Lock", s.State.Lock})
	}
	if !s.State.LastBackup.IsZero() {
		sum = append(sum, []string{"Last backup", s.State.LastBackup.Local().Format("2006-01-02 15:04")})
	}
	if !s.State.NextBackup.IsZero() {
		sum = append(sum, []string{"Next backup", s.State.NextBackup.Local().Format("2006-01-02 15:04")})
	}
	if s.State.AutopilotStatus != "" {
		sum = append(sum, []string{"Autopilot", s.State.AutopilotStatus})
	}
	if err := output.Table(w, []string{"QABAS TOP", "VALUE"}, sum); err != nil {
		return err
	}

	fmt.Fprintln(w)
	if s.Services.Err != "" {
		fmt.Fprintln(w, "services: "+s.Services.Err)
	}
	rows := make([][]string, 0, len(s.Services.Rows))
	for _, r := range tui.SortServices(s.Services.Rows, tui.SortCPU, false) {
		mem := "-"
		if r.MemBytes > 0 {
			mem = sysinfo.HumanBytes(r.MemBytes)
			if r.MemLimitBytes > 0 {
				mem += " / " + sysinfo.HumanBytes(r.MemLimitBytes)
			}
		}
		up := "-"
		if !r.StartedAt.IsZero() {
			up = top.ShortDuration(s.CollectedAt.Sub(r.StartedAt))
		}
		rows = append(rows, []string{r.Service, orDash(r.State), orDash(r.Health), fmt.Sprintf("%.1f%%", r.CPUPercent),
			mem, fmt.Sprintf("%.1f%%", r.MemPercent), fmt.Sprintf("%d", r.Restarts), up})
	}
	if len(rows) > 0 {
		if err := output.Table(w, []string{"SERVICE", "STATE", "HEALTH", "CPU", "MEMORY", "MEM%", "RESTARTS", "UPTIME"}, rows); err != nil {
			return err
		}
	}

	if pg := s.Postgres; pg != nil {
		fmt.Fprintln(w)
		if pg.Err != "" {
			fmt.Fprintln(w, "postgres: "+pg.Err)
		} else {
			role := "primary"
			if pg.InRecovery {
				role = "standby"
			}
			if err := output.Table(w, []string{"POSTGRES", "VALUE"}, [][]string{
				{"Role", role},
				{"Connections", fmt.Sprintf("%d of %d (%d active)", pg.Connections, pg.MaxConnections, pg.Active)},
				{"Cache hit", fmt.Sprintf("%.1f%%", pg.CacheHitPercent)},
				{"Lock waits", fmt.Sprintf("%d", pg.LockWaits)},
				{"Deadlocks", fmt.Sprintf("%d", pg.Deadlocks)},
			}); err != nil {
				return err
			}
			if len(pg.Queries) > 0 {
				qs := make([][]string, 0, len(pg.Queries))
				for _, q := range pg.Queries {
					qs = append(qs, []string{fmt.Sprintf("%d", q.PID), orDash(q.State), orDash(q.Wait),
						top.ShortDuration(time.Duration(q.DurationSec * float64(time.Second))), q.Text})
				}
				fmt.Fprintln(w)
				if err := output.Table(w, []string{"PID", "STATE", "WAIT", "TIME", "QUERY"}, qs); err != nil {
					return err
				}
			}
		}
	}

	if rs := s.Replicas; rs != nil {
		fmt.Fprintln(w)
		switch {
		case rs.Err != "":
			fmt.Fprintln(w, "replicas: "+rs.Err)
		case rs.Standby:
			fmt.Fprintf(w, "This node is a standby: replay lag %s, WAL receiver %s\n",
				top.ShortDuration(time.Duration(rs.LagSeconds*float64(time.Second))), orDash(rs.Upstream))
		case len(rs.Rows) == 0:
			fmt.Fprintln(w, "No standbys attached.")
		default:
			reps := make([][]string, 0, len(rs.Rows))
			for _, r := range rs.Rows {
				reps = append(reps, []string{r.Name, r.State, top.ShortDuration(time.Duration(r.LagSeconds * float64(time.Second))),
					sysinfo.HumanBytes(uint64(max(r.LagBytes, 0))), sysinfo.HumanBytes(uint64(max(r.RetainedBytes, 0))), orDash(r.WALStatus)})
			}
			if err := output.Table(w, []string{"REPLICA", "STATE", "LAG", "BEHIND", "SLOT WAL", "WAL STATUS"}, reps); err != nil {
				return err
			}
		}
	}

	if len(s.Findings) > 0 {
		fs := make([][]string, 0, len(s.Findings))
		for _, f := range s.Findings {
			fs = append(fs, []string{strings.ToUpper(f.Level), f.Check, f.Detail})
		}
		fmt.Fprintln(w)
		return output.Table(w, []string{"STATUS", "CHECK", "DETAIL"}, fs)
	}
	return nil
}
