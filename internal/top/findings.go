package top

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/sysinfo"
)

// Thresholds that grade the live stack.
const (
	ServiceCPUWarnPercent   = 90.0
	ServiceMemWarnPercent   = 90.0
	RestartWarnCount        = 3
	ConnWarnPercent         = 80.0
	ConnCritPercent         = 95.0
	CacheHitWarnPercent     = 90.0
	CacheHitMinBlocks       = 10_000
	LongQueryWarn           = 5 * time.Minute
	ReplicaLagWarn          = 60 * time.Second
	ReplicaLagCrit          = 10 * time.Minute
	SlotRetainedWarnBytes   = 10 << 30
	ArchiveFailureFreshness = time.Hour
	ArchiveLagWarn          = 5 * time.Minute
	ArchiveLagWarnBytes     = 64 << 20
)

// Evaluate grades a snapshot: host findings from sysinfo (non-ok only) plus
// service, database, replication, and state rules. Most severe first.
func Evaluate(s Snapshot) []sysinfo.Finding {
	var out []sysinfo.Finding
	add := func(level, check, format string, a ...any) {
		out = append(out, sysinfo.Finding{Check: check, Level: level, Detail: fmt.Sprintf(format, a...)})
	}
	for _, f := range s.Host.Info.Findings {
		if f.Level != sysinfo.LevelOK {
			out = append(out, f)
		}
	}

	for _, r := range s.Services.Rows {
		check := "service:" + r.Service
		switch {
		case strings.EqualFold(r.State, "restarting") || strings.EqualFold(r.State, "dead"):
			add(sysinfo.LevelCrit, check, "%s is %s", r.Name, r.State)
			continue
		case !strings.EqualFold(r.State, "running"):
			add(sysinfo.LevelWarn, check, "%s is %s", r.Name, orUnknown(r.State))
			continue
		case strings.EqualFold(r.Health, "unhealthy"):
			add(sysinfo.LevelCrit, check, "%s is unhealthy", r.Name)
		}
		if r.Restarts >= RestartWarnCount {
			add(sysinfo.LevelWarn, check, "%s restarted %d times (possible restart loop)", r.Name, r.Restarts)
		}
		if r.MemLimitBytes > 0 && r.MemPercent >= ServiceMemWarnPercent {
			add(sysinfo.LevelWarn, check, "%s uses %.0f%% of its memory limit", r.Name, r.MemPercent)
		}
		if r.CPUPercent >= ServiceCPUWarnPercent {
			add(sysinfo.LevelWarn, check, "%s at %.0f%% CPU", r.Name, r.CPUPercent)
		}
	}

	if pg := s.Postgres; pg != nil {
		switch {
		case pg.Err != "":
			add(sysinfo.LevelWarn, "postgres", "unreachable: %s", pg.Err)
		default:
			if pg.MaxConnections > 0 {
				pct := float64(pg.Connections) / float64(pg.MaxConnections) * 100
				switch {
				case pct >= ConnCritPercent:
					add(sysinfo.LevelCrit, "postgres:connections", "%d of %d connections used (%.0f%%)", pg.Connections, pg.MaxConnections, pct)
				case pct >= ConnWarnPercent:
					add(sysinfo.LevelWarn, "postgres:connections", "%d of %d connections used (%.0f%%)", pg.Connections, pg.MaxConnections, pct)
				}
			}
			if pg.LockWaits > 0 {
				add(sysinfo.LevelWarn, "postgres:locks", "%d backend(s) waiting on locks", pg.LockWaits)
			}
			if pg.BlksHit+pg.BlksRead >= CacheHitMinBlocks && pg.CacheHitPercent < CacheHitWarnPercent {
				add(sysinfo.LevelWarn, "postgres:cache", "cache hit ratio %.1f%% (shared_buffers may be too small)", pg.CacheHitPercent)
			}
			for _, q := range pg.Queries {
				if q.State == "active" && time.Duration(q.DurationSec*float64(time.Second)) >= LongQueryWarn {
					add(sysinfo.LevelWarn, "postgres:query", "pid %d running for %s", q.PID, ShortDuration(time.Duration(q.DurationSec)*time.Second))
				}
			}
			if !strings.EqualFold(pg.ArchiveMode, "off") &&
				!pg.LastFailedAt.IsZero() && pg.LastFailedAt.After(pg.LastArchivedAt) &&
				s.CollectedAt.Sub(pg.LastFailedAt) < ArchiveFailureFreshness {
				add(sysinfo.LevelCrit, "postgres:wal-archive", "WAL archiving failing since %s", pg.LastFailedAt.Local().Format("15:04"))
			}
		}
	}

	if rs := s.Replicas; rs != nil && rs.Err == "" {
		if rs.Standby {
			if lvl := lagLevel(rs.LagSeconds); lvl != sysinfo.LevelOK {
				add(lvl, "replica:self", "this standby is %s behind", ShortDuration(time.Duration(rs.LagSeconds)*time.Second))
			}
			if rs.Upstream != "" && rs.Upstream != "streaming" {
				add(sysinfo.LevelWarn, "replica:self", "WAL receiver is %s", rs.Upstream)
			}
		}
		for _, r := range rs.Rows {
			check := "replica:" + r.Name
			if r.State != "streaming" {
				add(sysinfo.LevelCrit, check, "%s is %s", r.Name, r.State)
			} else if lvl := lagLevel(r.LagSeconds); lvl != sysinfo.LevelOK {
				add(lvl, check, "%s is %s behind", r.Name, ShortDuration(time.Duration(r.LagSeconds)*time.Second))
			}
			if r.RetainedBytes >= SlotRetainedWarnBytes {
				add(sysinfo.LevelWarn, check, "slot %s retains %s of WAL", r.Slot, sysinfo.HumanBytes(uint64(r.RetainedBytes)))
			}
			if r.WALStatus == "lost" {
				add(sysinfo.LevelCrit, check, "slot %s lost required WAL (re-sync needed)", r.Slot)
			}
		}
		if a := rs.Archive; a != nil {
			evaluateArchive(a, add)
		}
	}

	if s.State.NotInstalled() {
		add(sysinfo.LevelWarn, "qabasone", "QabasOne is not installed on this machine (sudo qabas install)")
	}
	if s.State.LockStuck {
		add(sysinfo.LevelWarn, "lock", "operations lock looks stuck: %s", s.State.Lock)
	}
	for _, w := range s.State.Warnings {
		check := "backup-schedule"
		if strings.HasPrefix(w, "autopilot") {
			check = "autopilot"
		}
		add(sysinfo.LevelWarn, check, "%s", w)
	}

	sort.SliceStable(out, func(i, j int) bool { return levelRank(out[i].Level) > levelRank(out[j].Level) })
	return out
}

// evaluateArchive grades the PITR archive stream, matching pitr.Status problems.
func evaluateArchive(a *WALArchive, add func(level, check, format string, args ...any)) {
	const check = "postgres:wal-archive"
	switch {
	case a.Slot != "" && !a.SlotActive:
		add(sysinfo.LevelCrit, check, "slot %s is inactive (WAL accumulates on the writer)", a.Slot)
	case a.Connected && (time.Duration(a.LagSeconds*float64(time.Second)) >= ArchiveLagWarn || a.LagBytes >= ArchiveLagWarnBytes):
		add(sysinfo.LevelWarn, check, "WAL archive is %s (%s) behind",
			ShortDuration(time.Duration(a.LagSeconds)*time.Second), sysinfo.HumanBytes(uint64(a.LagBytes)))
	}
	if a.RetainedBytes >= SlotRetainedWarnBytes {
		add(sysinfo.LevelWarn, check, "slot %s retains %s of WAL", a.Slot, sysinfo.HumanBytes(uint64(a.RetainedBytes)))
	}
	if a.WALStatus == "lost" {
		add(sysinfo.LevelCrit, check, "slot %s lost required WAL (take a new base backup)", a.Slot)
	}
}

func lagLevel(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	switch {
	case d >= ReplicaLagCrit:
		return sysinfo.LevelCrit
	case d >= ReplicaLagWarn:
		return sysinfo.LevelWarn
	}
	return sysinfo.LevelOK
}

func levelRank(l string) int {
	switch l {
	case sysinfo.LevelCrit:
		return 2
	case sysinfo.LevelWarn:
		return 1
	}
	return 0
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
