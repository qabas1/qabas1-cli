// Package top collects a live, read-only view of the whole QabasOne stack (host,
// services, PostgreSQL, replicas, operational state) for `qabas top`.
package top

import (
	"time"

	"github.com/qabasone/qabasone-cli/internal/sysinfo"
)

// Snapshot is one refresh of every source. A failing source sets its section's
// Err and leaves the other sections intact.
type Snapshot struct {
	CollectedAt time.Time         `json:"collectedAt"`
	IntervalSec float64           `json:"intervalSeconds,omitempty"`
	Host        HostSection       `json:"host"`
	Services    ServicesSection   `json:"services"`
	Postgres    *PostgresSection  `json:"postgres,omitempty"`
	Replicas    *ReplicasSection  `json:"replicas,omitempty"`
	State       StateSection      `json:"state"`
	Findings    []sysinfo.Finding `json:"findings,omitempty"`

	// PostgresDisabled and ReplicasDisabled are set by --no-postgres/--no-replicas.
	PostgresDisabled bool `json:"postgresDisabled,omitempty"`
	ReplicasDisabled bool `json:"replicasDisabled,omitempty"`
}

type HostSection struct {
	Info sysinfo.Info `json:"info"`
	// CPU and Cores split usage by user/nice/system/steal (Linux only).
	CPU     *sysinfo.CoreUsage  `json:"cpu,omitempty"`
	Cores   []sysinfo.CoreUsage `json:"cores,omitempty"`
	Threads int                 `json:"threads,omitempty"`
	Running int                 `json:"running,omitempty"`
	Err     string              `json:"error,omitempty"`
}

type ServicesSection struct {
	Rows []Service `json:"rows,omitempty"`
	// Source is "engine" (exact counters) or "cli" (docker stats, rounded counters).
	Source string `json:"source,omitempty"`
	Err    string `json:"error,omitempty"`
}

// Service is one QabasOne container with live resource usage. Cumulative
// counters come from docker stats; *PerSec fields are deltas between refreshes.
type Service struct {
	Service       string    `json:"service"`
	Name          string    `json:"name"`
	State         string    `json:"state"`
	Health        string    `json:"health"`
	CPUPercent    float64   `json:"cpuPercent"`
	MemBytes      uint64    `json:"memBytes"`
	MemLimitBytes uint64    `json:"memLimitBytes"`
	MemPercent    float64   `json:"memPercent"`
	NetRxBytes    uint64    `json:"netRxBytes"`
	NetTxBytes    uint64    `json:"netTxBytes"`
	BlockRead     uint64    `json:"blockReadBytes"`
	BlockWrite    uint64    `json:"blockWriteBytes"`
	NetRxPerSec   float64   `json:"netRxPerSec"`
	NetTxPerSec   float64   `json:"netTxPerSec"`
	BlockRPerSec  float64   `json:"blockReadPerSec"`
	BlockWPerSec  float64   `json:"blockWritePerSec"`
	Restarts      int       `json:"restarts"`
	StartedAt     time.Time `json:"startedAt,omitempty"`

	cpuTotal   uint64
	systemCPU  uint64
	onlineCPUs uint32
}

// PostgresSection is the node-local database overview.
type PostgresSection struct {
	InRecovery      bool      `json:"inRecovery"`
	Connections     int       `json:"connections"`
	MaxConnections  int       `json:"maxConnections"`
	Active          int       `json:"active"`
	LockWaits       int       `json:"lockWaits"`
	Xacts           uint64    `json:"xactsTotal"`
	BlksHit         uint64    `json:"blksHit"`
	BlksRead        uint64    `json:"blksRead"`
	Deadlocks       uint64    `json:"deadlocks"`
	TPS             float64   `json:"tps"`
	CacheHitPercent float64   `json:"cacheHitPercent"`
	ArchiveMode     string    `json:"archiveMode,omitempty"`
	ArchivedCount   uint64    `json:"archivedCount"`
	ArchiveFailed   uint64    `json:"archiveFailedCount"`
	LastArchivedAt  time.Time `json:"lastArchivedAt,omitempty"`
	LastFailedAt    time.Time `json:"lastArchiveFailedAt,omitempty"`
	Queries         []Query   `json:"queries,omitempty"`
	Err             string    `json:"error,omitempty"`
}

// Query is one non-idle client backend; Text is truncated and redacted.
type Query struct {
	PID         int     `json:"pid"`
	User        string  `json:"user,omitempty"`
	State       string  `json:"state"`
	Wait        string  `json:"wait,omitempty"`
	DurationSec float64 `json:"durationSeconds"`
	Text        string  `json:"text"`
}

// ReplicasSection lists streaming standbys (on a writer) or this node's own
// replay lag (on a standby).
type ReplicasSection struct {
	Standby    bool        `json:"standby"`
	LagSeconds float64     `json:"lagSeconds,omitempty"`
	Upstream   string      `json:"walReceiver,omitempty"`
	Rows       []Replica   `json:"rows,omitempty"`
	Archive    *WALArchive `json:"walArchive,omitempty"`
	Err        string      `json:"error,omitempty"`
}

// WALArchive is the PITR archive stream (pg_receivewal in wal-archiver on its own
// slot). It stores WAL without replaying it, so lag is measured at the write position.
type WALArchive struct {
	Connected     bool    `json:"connected"`
	ClientAddr    string  `json:"clientAddr,omitempty"`
	Slot          string  `json:"slot,omitempty"`
	SlotActive    bool    `json:"slotActive"`
	LagBytes      int64   `json:"lagBytes"`
	LagSeconds    float64 `json:"lagSeconds"`
	RetainedBytes int64   `json:"retainedBytes"`
	WALStatus     string  `json:"walStatus,omitempty"`
}

type Replica struct {
	Name          string  `json:"name"`
	ClientAddr    string  `json:"clientAddr,omitempty"`
	State         string  `json:"state"`
	SyncState     string  `json:"syncState,omitempty"`
	LagBytes      int64   `json:"lagBytes"`
	LagSeconds    float64 `json:"lagSeconds"`
	Slot          string  `json:"slot,omitempty"`
	SlotActive    bool    `json:"slotActive"`
	RetainedBytes int64   `json:"retainedBytes"`
	WALStatus     string  `json:"walStatus,omitempty"`
}

// NotInstalled reports that the state files were read and QabasOne is not installed.
func (s StateSection) NotInstalled() bool { return s.Probed && !s.Installed }

// StateSection is the operational state read from the qabas state files.
type StateSection struct {
	Installed bool `json:"installed"`
	// Probed is set when the state files were read, so a zero Installed is meaningful.
	Probed          bool      `json:"-"`
	Release         string    `json:"release,omitempty"`
	Role            string    `json:"role,omitempty"`
	Channel         string    `json:"channel,omitempty"`
	Maintenance     bool      `json:"maintenance"`
	Lock            string    `json:"lock,omitempty"`
	LockStuck       bool      `json:"lockStuck,omitempty"`
	LastBackup      time.Time `json:"lastBackup,omitempty"`
	NextBackup      time.Time `json:"nextBackup,omitempty"`
	AutopilotLast   time.Time `json:"autopilotLastRun,omitempty"`
	AutopilotStatus string    `json:"autopilot,omitempty"`
	Warnings        []string  `json:"warnings,omitempty"`
	Err             string    `json:"error,omitempty"`
}
