// Package autopilot is the unattended self-maintenance pass run on a schedule
// (`qabas autopilot run`): it keeps disk, Docker, WAL, logs, and services
// healthy so a QabasOne host runs for long periods without operator care.
package autopilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
)

const (
	// MaxRestartsPerDay caps self-heal restarts per service in a rolling 24h.
	MaxRestartsPerDay = 3
	// MaintenanceGrace is how long past MaintenanceUntil autopilot waits before
	// turning maintenance off by itself.
	MaintenanceGrace = time.Hour
	// StaleAfter is when doctor reports autopilot as not running.
	StaleAfter = 3 * time.Hour
	// BuildCacheMaxAge is the age of unused build cache pruned on every run.
	BuildCacheMaxAge = "168h"
)

// Deps are the side effects Run relies on. Nil funcs skip their task.
type Deps struct {
	Now     func() time.Time
	Machine func() sysinfo.Info
	// Docker runs a raw docker CLI command and returns combined output.
	Docker func(args ...string) (string, error)
	// Services lists compose services; Restart restarts one of them.
	Services func() ([]compose.ServiceStatus, error)
	Restart  func(service string) error
	// PruneWAL removes PITR WAL no base backup can replay (nil when PITR is off).
	PruneWAL func() (removed int, bytes int64, err error)
	// PruneBackups trims scheduled backups; minimal=true applies emergency retention.
	PruneBackups func(minimal bool) ([]string, error)
	// MaintenanceExpired reports whether maintenance mode should be turned off;
	// EndMaintenance does it.
	MaintenanceExpired func(now time.Time) bool
	EndMaintenance     func() error
	// EnsureLogRotation writes the compose logging overlay; changed=true when written.
	EnsureLogRotation func() (changed bool, err error)
	// Healable reports whether services may be restarted now (installed, not in
	// maintenance, no deploy in progress).
	Healable bool
	// Tasks are extra housekeeping steps (log retention, permission repair, …).
	Tasks []Task
}

// Task is a housekeeping step. It reports an Action only when it did
// something (non-empty detail) or failed.
type Task struct {
	Name string
	Run  func() (detail string, freed uint64, err error)
}

type Action struct {
	Task       string `json:"task"`
	Detail     string `json:"detail,omitempty"`
	FreedBytes uint64 `json:"freedBytes,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Record is the outcome of a run, persisted to <state>/autopilot.json.
type Record struct {
	Time        time.Time              `json:"time"`
	DurationSec float64                `json:"durationSeconds"`
	Health      string                 `json:"health"`
	Actions     []Action               `json:"actions,omitempty"`
	FreedBytes  uint64                 `json:"freedBytes"`
	Findings    []sysinfo.Finding      `json:"findings,omitempty"`
	Restarts    map[string][]time.Time `json:"restarts,omitempty"`
	Skipped     string                 `json:"skipped,omitempty"`
}

// Errors returns the failed actions.
func (r Record) Errors() []Action {
	var out []Action
	for _, a := range r.Actions {
		if a.Error != "" {
			out = append(out, a)
		}
	}
	return out
}

// Run executes one maintenance pass. prev carries the restart history.
func Run(d Deps, prev Record) Record {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	start := now()
	rec := Record{Time: start.UTC(), Restarts: recentRestarts(prev.Restarts, start)}
	add := func(a Action) {
		rec.Actions = append(rec.Actions, a)
		rec.FreedBytes += a.FreedBytes
	}

	var info sysinfo.Info
	if d.Machine != nil {
		info = d.Machine()
	}
	pressure := diskPressure(info)

	if d.Docker != nil {
		add(dockerTask(d.Docker, "docker-images", "image", "prune", "-f"))
		add(dockerTask(d.Docker, "docker-build-cache", "builder", "prune", "-f", "--filter", "until="+BuildCacheMaxAge))
		if pressure != sysinfo.LevelOK {
			add(dockerTask(d.Docker, "docker-build-cache-all", "builder", "prune", "-af"))
		}
	}

	if d.PruneWAL != nil {
		n, b, err := d.PruneWAL()
		a := Action{Task: "pitr-wal", FreedBytes: uint64(max(b, 0))}
		if err != nil {
			a.Error = err.Error()
		} else {
			a.Detail = fmt.Sprintf("removed %d segment(s)", n)
		}
		add(a)
	}

	if d.PruneBackups != nil && pressure != sysinfo.LevelOK {
		ids, err := d.PruneBackups(false)
		a := Action{Task: "backup-retention", Detail: fmt.Sprintf("pruned %d scheduled backup(s)", len(ids))}
		if err != nil {
			a.Error = err.Error()
		}
		add(a)
		if err == nil && pressure == sysinfo.LevelCrit && d.Machine != nil {
			if diskPressure(d.Machine()) == sysinfo.LevelCrit {
				ids, err := d.PruneBackups(true)
				a := Action{Task: "backup-emergency", Detail: fmt.Sprintf("disk critical: pruned %d scheduled backup(s) to minimum retention", len(ids))}
				if err != nil {
					a.Error = err.Error()
				}
				add(a)
			}
		}
	}

	if d.EnsureLogRotation != nil {
		changed, err := d.EnsureLogRotation()
		if err != nil {
			add(Action{Task: "log-rotation", Error: err.Error()})
		} else if changed {
			add(Action{Task: "log-rotation", Detail: "container log rotation configured; applies on next qabas up/deploy"})
		}
	}

	for _, t := range d.Tasks {
		if t.Run == nil {
			continue
		}
		detail, freed, err := t.Run()
		if err != nil {
			add(Action{Task: t.Name, Detail: detail, FreedBytes: freed, Error: err.Error()})
		} else if detail != "" {
			add(Action{Task: t.Name, Detail: detail, FreedBytes: freed})
		}
	}

	if d.MaintenanceExpired != nil && d.EndMaintenance != nil && d.MaintenanceExpired(start) {
		a := Action{Task: "maintenance-expiry", Detail: "maintenance window ended; maintenance mode turned off"}
		if err := d.EndMaintenance(); err != nil {
			a.Error = err.Error()
		}
		add(a)
	}

	if d.Healable && d.Services != nil && d.Restart != nil {
		for _, a := range heal(d, &rec, start) {
			add(a)
		}
	}

	if d.Machine != nil {
		rec.Findings = d.Machine().Findings
	} else {
		rec.Findings = info.Findings
	}
	rec.Health = sysinfo.Worst(rec.Findings)
	if len(rec.Errors()) > 0 && rec.Health == sysinfo.LevelOK {
		rec.Health = sysinfo.LevelWarn
	}
	rec.DurationSec = now().Sub(start).Round(time.Second).Seconds()
	return rec
}

func heal(d Deps, rec *Record, now time.Time) []Action {
	svcs, err := d.Services()
	if err != nil {
		return []Action{{Task: "self-heal", Error: err.Error()}}
	}
	var out []Action
	for _, s := range svcs {
		if !strings.EqualFold(s.Health, "unhealthy") || s.Service == "" {
			continue
		}
		if len(rec.Restarts[s.Service]) >= MaxRestartsPerDay {
			out = append(out, Action{Task: "self-heal", Detail: s.Service + " unhealthy; restart cap reached (check: qabas logs " + s.Service + ")",
				Error: fmt.Sprintf("%s restarted %d times in 24h", s.Service, MaxRestartsPerDay)})
			continue
		}
		a := Action{Task: "self-heal", Detail: "restarted unhealthy " + s.Service}
		if err := d.Restart(s.Service); err != nil {
			a.Error = err.Error()
		}
		if rec.Restarts == nil {
			rec.Restarts = map[string][]time.Time{}
		}
		rec.Restarts[s.Service] = append(rec.Restarts[s.Service], now.UTC())
		out = append(out, a)
	}
	return out
}

func recentRestarts(in map[string][]time.Time, now time.Time) map[string][]time.Time {
	out := map[string][]time.Time{}
	for svc, ts := range in {
		for _, t := range ts {
			if now.Sub(t) < 24*time.Hour {
				out[svc] = append(out[svc], t)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func dockerTask(run func(...string) (string, error), task string, args ...string) Action {
	out, err := run(args...)
	a := Action{Task: task, FreedBytes: reclaimed(out)}
	if err != nil {
		a.Error = strings.TrimSpace(firstLine(out) + " " + err.Error())
		return a
	}
	a.Detail = "reclaimed " + sysinfo.HumanBytes(a.FreedBytes)
	return a
}

// reclaimed parses "Total reclaimed space: 1.2GB" (or "Total: 1.2GB" from builder prune).
func reclaimed(out string) uint64 {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"Total reclaimed space:", "Total:"} {
			if v, ok := strings.CutPrefix(line, prefix); ok {
				return sysinfo.ParseSize(v)
			}
		}
	}
	return 0
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// diskPressure is the worst level among disk findings on QabasOne volumes.
func diskPressure(info sysinfo.Info) string {
	var fs []sysinfo.Finding
	for _, f := range info.Findings {
		if strings.HasPrefix(f.Check, "disk:") {
			fs = append(fs, f)
		}
	}
	return sysinfo.Worst(fs)
}

func RecordFile(p paths.Paths) string { return filepath.Join(p.StateDir, "autopilot.json") }

func Load(p paths.Paths) (Record, bool) {
	var r Record
	b, err := os.ReadFile(RecordFile(p))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return Record{}, false
	}
	return r, true
}

func Save(p paths.Paths, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return err
	}
	tmp := RecordFile(p) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, RecordFile(p))
}

// Summary is a one-line description of a run for audit history.
func (r Record) Summary() string {
	tasks := map[string]bool{}
	for _, a := range r.Actions {
		if a.Error == "" && (a.FreedBytes > 0 || !strings.HasPrefix(a.Task, "docker-")) {
			tasks[a.Task] = true
		}
	}
	names := make([]string, 0, len(tasks))
	for t := range tasks {
		names = append(names, t)
	}
	sort.Strings(names)
	s := fmt.Sprintf("health=%s freed=%s", r.Health, sysinfo.HumanBytes(r.FreedBytes))
	if len(names) > 0 {
		s += " tasks=" + strings.Join(names, ",")
	}
	if n := len(r.Errors()); n > 0 {
		s += fmt.Sprintf(" errors=%d", n)
	}
	return s
}
