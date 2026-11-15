package top

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/pgctl"
	"github.com/qabas1/qabasone-cli/internal/sysinfo"
)

const statsNDJSON = `{"BlockIO":"1.5MB / 20MB","CPUPerc":"12.50%","Container":"abc","ID":"abc","MemPerc":"25.00%","MemUsage":"256MiB / 1GiB","Name":"qabasone-api","NetIO":"1kB / 2kB","PIDs":"20"}
{"BlockIO":"0B / 0B","CPUPerc":"0.10%","MemPerc":"1.00%","MemUsage":"10MiB / 1GiB","Name":"qabasone-redis","NetIO":"0B / 0B"}
not json
`

func TestParseStats(t *testing.T) {
	got := ParseStats([]byte(statsNDJSON))
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	api := got["qabasone-api"]
	if api.CPUPercent != 12.5 || api.MemPercent != 25 {
		t.Fatalf("percent: %+v", api)
	}
	if api.MemBytes != 256<<20 || api.MemLimitBytes != 1<<30 {
		t.Fatalf("mem: %+v", api)
	}
	if api.NetRx != 1000 || api.NetTx != 2000 || api.BlockRead != 1_500_000 || api.BlockWrite != 20_000_000 {
		t.Fatalf("io: %+v", api)
	}
}

func TestParseInspect(t *testing.T) {
	got := parseInspect([]byte("/qabasone-api|4|2026-10-01T10:00:00.123456789Z\nbad line\n"))
	r, ok := got["qabasone-api"]
	if !ok || r.Restarts != 4 || r.StartedAt.Hour() != 10 {
		t.Fatalf("got %+v", got)
	}
}

func fakeDocker(stats, inspect string) func(context.Context, ...string) ([]byte, error) {
	return func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "stats":
			return []byte(stats), nil
		case "inspect":
			return []byte(inspect), nil
		}
		return nil, errors.New("unexpected")
	}
}

func TestCollectServicesMergesStatsAndSkipsStopped(t *testing.T) {
	list := func(context.Context) ([]BaseService, error) {
		return []BaseService{
			{Service: "api", Name: "qabasone-api", State: "running", Health: "healthy"},
			{Service: "worker", Name: "qabasone-worker", State: "exited"},
		}, nil
	}
	var asked []string
	docker := func(ctx context.Context, args ...string) ([]byte, error) {
		asked = append(asked, strings.Join(args, " "))
		return fakeDocker(statsNDJSON, "/qabasone-api|1|2026-10-01T10:00:00Z")(ctx, args...)
	}
	sec := collectServices(context.Background(), list, docker, nil, true)
	if sec.Err != "" || len(sec.Rows) != 2 {
		t.Fatalf("got %+v", sec)
	}
	if sec.Rows[0].CPUPercent != 12.5 || sec.Rows[0].Restarts != 1 {
		t.Fatalf("api not merged: %+v", sec.Rows[0])
	}
	for _, a := range asked {
		if strings.Contains(a, "qabasone-worker") {
			t.Fatalf("stopped container queried: %s", a)
		}
	}
}

func TestCollectServicesListError(t *testing.T) {
	sec := collectServices(context.Background(), func(context.Context) ([]BaseService, error) {
		return nil, errors.New("docker down")
	}, nil, nil, true)
	if sec.Err != "docker down" {
		t.Fatalf("got %+v", sec)
	}
}

const pgOut = "o|f|12|100|3|1|5000|99000|1000|2|40|1|2026-10-01T09:59:00Z|2026-10-01T09:00:00Z|on\n" +
	"q|101|app|idle in transaction||5.5|BEGIN\n" +
	"q|102|app|active|Lock:relation|420|UPDATE invoices SET note = 'secret customer' WHERE id = 7\n"

func TestCollectPostgres(t *testing.T) {
	x := nodeexec.NewFake("local").OnOut("pg_stat_activity", pgOut)
	pg := collectPostgres(context.Background(), x)
	if pg.Err != "" {
		t.Fatal(pg.Err)
	}
	if pg.InRecovery || pg.Connections != 12 || pg.MaxConnections != 100 || pg.Active != 3 || pg.LockWaits != 1 {
		t.Fatalf("overview: %+v", pg)
	}
	if pg.Xacts != 5000 || pg.Deadlocks != 2 || pg.ArchivedCount != 40 || pg.ArchiveFailed != 1 {
		t.Fatalf("counters: %+v", pg)
	}
	if pg.CacheHitPercent != 99 {
		t.Fatalf("cache hit: %v", pg.CacheHitPercent)
	}
	if pg.ArchiveMode != "on" {
		t.Fatalf("archive mode: %q", pg.ArchiveMode)
	}
	if len(pg.Queries) != 2 || pg.Queries[0].PID != 102 {
		t.Fatalf("queries not sorted by duration: %+v", pg.Queries)
	}
	if strings.Contains(pg.Queries[0].Text, "secret customer") || !strings.Contains(pg.Queries[0].Text, "'?'") {
		t.Fatalf("literal not masked: %q", pg.Queries[0].Text)
	}
	if !strings.Contains(x.Log[0], "docker exec -i qabasone-postgres") {
		t.Fatalf("unexpected script: %s", x.Log[0])
	}
}

func TestCollectPostgresError(t *testing.T) {
	x := nodeexec.NewFake("local").On("psql", nodeexec.Result{ExitCode: 2, Stderr: "No such container"})
	if pg := collectPostgres(context.Background(), x); !strings.Contains(pg.Err, "No such container") {
		t.Fatalf("got %+v", pg)
	}
}

func TestSanitizeQueryTruncatesAndRedacts(t *testing.T) {
	q := SanitizeQuery("SELECT  *\n FROM t WHERE password = 'hunter22' AND x = " + strings.Repeat("a", 400))
	if strings.Contains(q, "hunter22") {
		t.Fatalf("secret leaked: %q", q)
	}
	if !strings.HasSuffix(q, "…") || len([]rune(q)) != queryTextMax {
		t.Fatalf("not truncated: %d", len([]rune(q)))
	}
	if strings.Contains(q, "  ") {
		t.Fatalf("whitespace not collapsed: %q", q)
	}
}

const primaryStatus = "f|off|160004|1|0/5000100||||0||7421234567890123456\n"

func TestCollectReplicasPrimary(t *testing.T) {
	x := nodeexec.NewFake("local").
		OnOut("pg_is_in_recovery(),\n  current_setting('default_transaction_read_only')", primaryStatus).
		OnOut("pg_stat_replication", "qabas_dr1|10.8.0.2|streaming|0/5000100|0/5000000|1.5|async|77|0/5000100|0.1\n").
		OnOut("pg_replication_slots", "qabas_replica_dr1|t|0/5000000|reserved|256|-1|77\nqabas_replica_dr2|f|0/4000000|extended|11811160064|-1|0\n")
	rs := collectReplicas(context.Background(), x)
	if rs.Err != "" || rs.Standby || rs.Archive != nil {
		t.Fatalf("got %+v", rs)
	}
	if len(rs.Rows) != 2 {
		t.Fatalf("rows: %+v", rs.Rows)
	}
	dr1, dr2 := rs.Rows[0], rs.Rows[1]
	if dr1.Name != "dr1" || dr1.LagBytes != 0x100 || dr1.Slot != "qabas_replica_dr1" || !dr1.SlotActive || dr1.LagSeconds != 1.5 {
		t.Fatalf("dr1: %+v", dr1)
	}
	if dr2.Name != "dr2" || dr2.State != "disconnected" || dr2.RetainedBytes != 11811160064 {
		t.Fatalf("dr2: %+v", dr2)
	}
}

// hqRepl and hqSlots are the primary from the first live run: the PITR archiver
// (pg_receivewal, pid 51, never replays) holds qabas_wal_archive, dr1 holds its slot.
var (
	hqRepl = []pgctl.ReplicationRow{
		{Application: "pg_receivewal", ClientAddr: "172.21.0.11", State: "streaming", SentLSN: "0/9000000",
			ReplayLag: 48060, SyncState: "async", PID: 51, WriteLSN: "0/9000000", WriteLag: 0.002},
		{Application: "qabas_dr1", ClientAddr: "10.66.0.2", State: "streaming", SentLSN: "0/9000000",
			ReplayLSN: "0/9000000", SyncState: "async", PID: 77, WriteLSN: "0/9000000"},
	}
	hqSlots = []pgctl.SlotRow{
		{Name: "qabas_replica_dr1", Active: true, WALStatus: "reserved", ActivePID: 77},
		{Name: "qabas_wal_archive", Active: true, WALStatus: "reserved", ActivePID: 51},
	}
)

func TestMergeReplicasSeparatesPITRArchive(t *testing.T) {
	rows, a := mergeReplicas("0/9000000", hqRepl, hqSlots)
	if len(rows) != 1 || rows[0].Name != "dr1" || rows[0].Slot != "qabas_replica_dr1" {
		t.Fatalf("standbys: %+v", rows)
	}
	if a == nil || !a.Connected || !a.SlotActive || a.Slot != "qabas_wal_archive" || a.ClientAddr != "172.21.0.11" ||
		a.LagBytes != 0 || a.LagSeconds != 0.002 {
		t.Fatalf("archive: %+v", a)
	}
	s := Snapshot{Replicas: &ReplicasSection{Rows: rows, Archive: a}, Postgres: &PostgresSection{ArchiveMode: "off"}}
	if fs := Evaluate(s); len(fs) != 0 {
		t.Fatalf("a healthy archive must not raise findings: %+v", fs)
	}
}

func TestMergeReplicasArchiveByNameWithoutSlot(t *testing.T) {
	repl := []pgctl.ReplicationRow{{Application: "pg_receivewal", State: "streaming", PID: 9, WriteLSN: "0/8000000"}}
	rows, a := mergeReplicas("0/9000000", repl, nil)
	if len(rows) != 0 || a == nil || !a.Connected || a.Slot != "" || a.LagBytes != 0x1000000 {
		t.Fatalf("rows %+v archive %+v", rows, a)
	}
}

func TestMergeReplicasSlotStates(t *testing.T) {
	slots := []pgctl.SlotRow{
		{Name: "qabas_wal_archive", Active: false, WALStatus: "extended", RetainedBytes: 1 << 30},
		{Name: "qabas_replica_dr2", Active: true, ActivePID: 99},
		{Name: "qabas_replica_dr3", Active: false},
	}
	rows, a := mergeReplicas("0/9000000", nil, slots)
	if len(rows) != 2 || rows[0].State != "streaming" || rows[1].State != "disconnected" {
		t.Fatalf("unmatched slots must report their own state: %+v", rows)
	}
	if a == nil || a.Connected || a.SlotActive {
		t.Fatalf("archive: %+v", a)
	}
	fs := Evaluate(Snapshot{Replicas: &ReplicasSection{Rows: rows, Archive: a}})
	var archiveCrit bool
	for _, f := range fs {
		if f.Check == "postgres:wal-archive" && f.Level == sysinfo.LevelCrit && strings.Contains(f.Detail, "qabas_wal_archive is inactive") {
			archiveCrit = true
		}
		if f.Check == "replica:dr2" {
			t.Fatalf("an active slot must not be reported: %+v", f)
		}
	}
	if !archiveCrit {
		t.Fatalf("inactive archive slot must be critical: %+v", fs)
	}
}

func TestEvaluateArchiveLagAndArchiveMode(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	s := Snapshot{
		CollectedAt: now,
		Replicas:    &ReplicasSection{Archive: &WALArchive{Connected: true, Slot: "qabas_wal_archive", SlotActive: true, LagBytes: 128 << 20}},
		Postgres:    &PostgresSection{ArchiveMode: "off", LastFailedAt: now.Add(-time.Minute)},
	}
	fs := Evaluate(s)
	if len(fs) != 1 || fs[0].Check != "postgres:wal-archive" || fs[0].Level != sysinfo.LevelWarn {
		t.Fatalf("want one lag warning and no archiver failure while archive_mode is off: %+v", fs)
	}
}

func TestCollectReplicasStandby(t *testing.T) {
	x := nodeexec.NewFake("local").OnOut("psql", "t|on|160004|1||0/5000060|0/5000000|2026-09-30T10:00:00Z|42|streaming|7421234567890123456\n")
	rs := collectReplicas(context.Background(), x)
	if !rs.Standby || rs.LagSeconds != 42 || rs.Upstream != "streaming" {
		t.Fatalf("got %+v", rs)
	}
}

func TestCollectorRates(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	started := now.Add(-time.Hour)
	stats := []string{
		`{"Name":"qabasone-api","NetIO":"1kB / 2kB","BlockIO":"0B / 0B","MemUsage":"1MiB / 1GiB"}`,
		`{"Name":"qabasone-api","NetIO":"3kB / 6kB","BlockIO":"10kB / 0B","MemUsage":"1MiB / 1GiB"}`,
	}
	pgs := []string{
		"o|f|1|100|1|0|1000|10|0|0|0|0|||off\n",
		"o|f|1|100|1|0|1500|10|0|0|0|0|||off\n",
	}
	i := 0
	c := NewCollector(Sources{
		Services: func(context.Context) ([]BaseService, error) {
			return []BaseService{{Service: "api", Name: "qabasone-api", State: "running"}}, nil
		},
		Docker: func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "stats" {
				return []byte(stats[i]), nil
			}
			return []byte("/qabasone-api|0|" + started.Format(time.RFC3339)), nil
		},
		PG:         &seqExecutor{outs: pgs, i: &i},
		NoReplicas: true,
	})
	c.Now = func() time.Time { return now.Add(time.Duration(i) * 2 * time.Second) }

	first := c.Collect(context.Background())
	if first.Services.Rows[0].NetRxPerSec != 0 || first.Postgres.TPS != 0 {
		t.Fatalf("first snapshot must have no rates: %+v", first)
	}
	i = 1
	second := c.Collect(context.Background())
	s := second.Services.Rows[0]
	if s.NetRxPerSec != 1000 || s.NetTxPerSec != 2000 || s.BlockRPerSec != 5000 {
		t.Fatalf("rates: %+v", s)
	}
	if second.Postgres.TPS != 250 || second.IntervalSec != 2 {
		t.Fatalf("tps %v interval %v", second.Postgres.TPS, second.IntervalSec)
	}
}

type seqExecutor struct {
	outs []string
	i    *int
}

func (s *seqExecutor) Node() string { return "local" }
func (s *seqExecutor) Close() error { return nil }
func (s *seqExecutor) Run(context.Context, string, io.Reader) (nodeexec.Result, error) {
	return nodeexec.Result{Stdout: s.outs[*s.i]}, nil
}

func TestRateIgnoresCounterReset(t *testing.T) {
	if r := rate(100, 50, 1); r != 0 {
		t.Fatalf("got %v", r)
	}
}

func TestCollectHostTimeoutKeepsPrevious(t *testing.T) {
	prev := &Snapshot{Host: HostSection{Info: sysinfo.Info{Hostname: "old"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	block := make(chan struct{})
	defer close(block)
	h := collectHost(ctx, func() sysinfo.Info { <-block; return sysinfo.Info{} }, prev)
	if h.Err == "" || h.Info.Hostname != "old" {
		t.Fatalf("got %+v", h)
	}
}

func TestCachedProbe(t *testing.T) {
	n := 0
	p := CachedProbe(time.Hour, func() int { n++; return n })
	if p() != 1 || p() != 1 || n != 1 {
		t.Fatalf("probe ran %d times", n)
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	s := Snapshot{
		CollectedAt: now,
		Host: HostSection{Info: sysinfo.Info{Findings: []sysinfo.Finding{
			{Check: "memory", Level: sysinfo.LevelOK}, {Check: "disk:state", Level: sysinfo.LevelWarn, Detail: "low"},
		}}},
		Services: ServicesSection{Rows: []Service{
			{Service: "api", Name: "qabasone-api", State: "running", Health: "unhealthy", Restarts: 5},
			{Service: "web", Name: "qabasone-web", State: "restarting"},
			{Service: "redis", Name: "qabasone-redis", State: "running", MemLimitBytes: 1, MemPercent: 95},
		}},
		Postgres: &PostgresSection{
			Connections: 85, MaxConnections: 100, LockWaits: 2,
			BlksHit: 8000, BlksRead: 4000, CacheHitPercent: 66.7,
			Queries:      []Query{{PID: 7, State: "active", DurationSec: 600}},
			LastFailedAt: now.Add(-time.Minute), LastArchivedAt: now.Add(-time.Hour),
		},
		Replicas: &ReplicasSection{Rows: []Replica{
			{Name: "dr1", State: "streaming", LagSeconds: 900},
			{Name: "dr2", State: "disconnected", Slot: "qabas_replica_dr2", RetainedBytes: 20 << 30, WALStatus: "lost"},
		}},
		State: StateSection{LockStuck: true, Lock: "pid 9", Warnings: []string{
			"no scheduled backup has run yet",
			"autopilot has not run yet (run now: sudo qabas autopilot run)",
		}},
	}
	fs := Evaluate(s)
	want := map[string]string{
		"disk:state":           sysinfo.LevelWarn,
		"service:web":          sysinfo.LevelCrit,
		"postgres:connections": sysinfo.LevelWarn,
		"postgres:locks":       sysinfo.LevelWarn,
		"postgres:cache":       sysinfo.LevelWarn,
		"postgres:query":       sysinfo.LevelWarn,
		"postgres:wal-archive": sysinfo.LevelCrit,
		"lock":                 sysinfo.LevelWarn,
		"backup-schedule":      sysinfo.LevelWarn,
		"autopilot":            sysinfo.LevelWarn,
	}
	got := map[string]string{}
	for _, f := range fs {
		if f.Check == "memory" {
			t.Fatal("ok host findings must be dropped")
		}
		if l, seen := got[f.Check]; !seen || levelRank(f.Level) > levelRank(l) {
			got[f.Check] = f.Level
		}
	}
	for check, lvl := range want {
		if got[check] != lvl {
			t.Errorf("%s: want %s, got %q", check, lvl, got[check])
		}
	}
	if got["service:api"] != sysinfo.LevelCrit || got["service:redis"] != sysinfo.LevelWarn {
		t.Errorf("services: %v", got)
	}
	if got["replica:dr1"] != sysinfo.LevelCrit || got["replica:dr2"] != sysinfo.LevelCrit {
		t.Errorf("replicas: %v", got)
	}
	for i := 1; i < len(fs); i++ {
		if levelRank(fs[i].Level) > levelRank(fs[i-1].Level) {
			t.Fatalf("findings not sorted by severity: %+v", fs)
		}
	}
}

func TestCollectorPerCoreUsage(t *testing.T) {
	reads := [][]sysinfo.CPUStat{
		{{User: 0, Idle: 0}, {User: 0, Idle: 0}},
		{{User: 50, Idle: 50}, {User: 0, Idle: 100}},
		{{User: 50, Idle: 150}, {User: 100, Idle: 100}},
	}
	i := 0
	c := NewCollector(Sources{
		Host: func() sysinfo.Info { return sysinfo.Info{} },
		CPUStat: func() (sysinfo.CPUStat, []sysinfo.CPUStat, bool) {
			cores := reads[min(i, len(reads)-1)]
			i++
			agg := sysinfo.CPUStat{}
			for _, c := range cores {
				agg.User += c.User
				agg.Idle += c.Idle
			}
			return agg, cores, true
		},
		Tasks: func() (int, int, bool) { return 275, 1, true },
	})
	c.FirstWait = time.Millisecond
	h := c.Collect(context.Background()).Host
	if len(h.Cores) != 2 || h.Cores[0].Total != 50 || h.Cores[1].Total != 0 || h.CPU.Total != 25 || h.Info.CPU.UsagePercent != 25 {
		t.Fatalf("first refresh samples immediately: %+v", h)
	}
	if h.Threads != 275 || h.Running != 1 {
		t.Fatalf("tasks: %+v", h)
	}
	h = c.Collect(context.Background()).Host
	if h.Cores[0].Total != 0 || h.Cores[1].Total != 100 {
		t.Fatalf("later refreshes use the previous reading: %+v", h.Cores)
	}
}

func TestEvaluateNotInstalled(t *testing.T) {
	fs := Evaluate(Snapshot{State: StateSection{Probed: true}})
	if len(fs) != 1 || fs[0].Check != "qabasone" || fs[0].Level != sysinfo.LevelWarn {
		t.Fatalf("got %+v", fs)
	}
	if fs := Evaluate(Snapshot{}); len(fs) != 0 {
		t.Fatalf("unknown install state must not warn: %+v", fs)
	}
}

func TestEvaluateHealthy(t *testing.T) {
	s := Snapshot{
		Services: ServicesSection{Rows: []Service{{Service: "api", Name: "qabasone-api", State: "running", Health: "healthy"}}},
		Postgres: &PostgresSection{Connections: 5, MaxConnections: 100},
		Replicas: &ReplicasSection{Rows: []Replica{{Name: "dr1", State: "streaming", LagSeconds: 0.2}}},
	}
	if fs := Evaluate(s); len(fs) != 0 {
		t.Fatalf("want no findings, got %+v", fs)
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0: "-", 45 * time.Second: "45s", 12 * time.Minute: "12m",
		3*time.Hour + 5*time.Minute: "3h5m", 50 * time.Hour: "2d2h",
	}
	for d, want := range cases {
		if got := ShortDuration(d); got != want {
			t.Errorf("%v: want %s, got %s", d, want, got)
		}
	}
}
