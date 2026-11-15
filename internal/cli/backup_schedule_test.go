package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/config"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

func scheduleFixture(t *testing.T, self string) paths.Paths {
	t.Helper()
	root := t.TempDir()
	t.Setenv(paths.EnvConfigDir, root+"/etc")
	t.Setenv(paths.EnvStateDir, root+"/state")
	t.Setenv(paths.EnvLogDir, root+"/log")
	t.Setenv(paths.EnvLockPath, root+"/qabas.lock")
	t.Setenv("QABAS_NODE", self)
	old := scheduleTimerPath
	scheduleTimerPath = root + "/qabas-backup.timer"
	t.Cleanup(func() { scheduleTimerPath = old })
	p := paths.Resolve()
	if err := config.Save(p, config.Default()); err != nil {
		t.Fatal(err)
	}
	c := &cluster.Cluster{SchemaVersion: 1, Name: "t", Writer: "hq", Epoch: 1, WGNetwork: "10.66.0.0/24", Nodes: []cluster.Node{
		{Name: "hq", Role: cluster.RolePrimary, Host: "203.0.113.10", WGIP: "10.66.0.1"},
		{Name: "dr1", Role: cluster.RoleStandby, Host: "203.0.113.20", WGIP: "10.66.0.2"},
	}}
	if err := cluster.Save(p, c); err != nil {
		t.Fatal(err)
	}
	return p
}

func runRoot(t *testing.T, args ...string) error {
	t.Helper()
	root := NewRoot()
	root.SetArgs(args)
	return root.Execute()
}

func TestScheduleEnableRefusesOnReplica(t *testing.T) {
	scheduleFixture(t, "dr1")
	err := runRoot(t, "backup", "schedule", "enable", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "run on the writer") {
		t.Fatalf("err = %v", err)
	}
}

func TestScheduleEnableRejectsUnknownOffsite(t *testing.T) {
	scheduleFixture(t, "hq")
	err := runRoot(t, "backup", "schedule", "enable", "--offsite", "nope", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "not a registered replica") {
		t.Fatalf("err = %v", err)
	}
	if err := runRoot(t, "backup", "schedule", "enable", "--offsite", "hq", "--dry-run"); err == nil {
		t.Fatal("accepted the writer itself as off-site target")
	}
}

func TestScheduleEnableDryRunAcceptsReplica(t *testing.T) {
	p := scheduleFixture(t, "hq")
	if err := runRoot(t, "backup", "schedule", "enable", "--offsite", "dr1", "--at", "02:15", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backup.Schedule.Enabled {
		t.Fatal("dry run saved the schedule")
	}
}

func TestScheduleEnableRejectsBadTime(t *testing.T) {
	scheduleFixture(t, "hq")
	if err := runRoot(t, "backup", "schedule", "enable", "--at", "25:00", "--dry-run"); err == nil {
		t.Fatal("accepted 25:00")
	}
}

func TestScheduleEnableTimezone(t *testing.T) {
	scheduleFixture(t, "hq")
	if err := runRoot(t, "backup", "schedule", "enable", "--timezone", "Mars/Olympus", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "unknown timezone") {
		t.Fatalf("err = %v", err)
	}
	if err := runRoot(t, "backup", "schedule", "enable", "--timezone", "UTC", "--dry-run"); err != nil {
		t.Fatal(err)
	}
}

func TestFormatNextRun(t *testing.T) {
	cairo, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC)
	if got := formatNextRun(next, false, cairo); got != "2026-01-16 01:30 EET" {
		t.Fatalf("got %q", got)
	}
	if got := formatNextRun(next, true, cairo); !strings.Contains(got, "approx") {
		t.Fatalf("approx not marked: %q", got)
	}
}

func TestScheduleWarnsOnUnpinnedTimer(t *testing.T) {
	p := scheduleFixture(t, "hq")
	cfg := config.Default()
	cfg.Backup.Schedule.Enabled = true
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if err := saveScheduleRecord(p, scheduleRecord{Time: now, LastSuccess: now, LastSuccessID: "qb-x"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scheduleTimerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:30:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := scheduleWarnings(p, now); len(w) != 1 || !strings.Contains(w[0], "not pinned to Africa/Cairo") {
		t.Fatalf("warnings = %v", w)
	}
	if err := os.WriteFile(scheduleTimerPath, []byte("[Timer]\nOnCalendar=*-*-* 01:30:00 Africa/Cairo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := scheduleWarnings(p, now); len(w) != 0 {
		t.Fatalf("pinned timer warned: %v", w)
	}
}

func TestScheduleRunSkipsOnReplica(t *testing.T) {
	scheduleFixture(t, "dr1")
	if err := runRoot(t, "backup", "schedule", "run"); err != nil {
		t.Fatalf("replica run should be a no-op, got %v", err)
	}
}

func TestScheduleWarnings(t *testing.T) {
	p := scheduleFixture(t, "hq")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if w := scheduleWarnings(p, now); len(w) != 0 {
		t.Fatalf("disabled schedule warned: %v", w)
	}
	cfg := config.Default()
	cfg.Backup.Schedule.Enabled = true
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	if w := scheduleWarnings(p, now); len(w) != 1 || !strings.Contains(w[0], "has run yet") {
		t.Fatalf("never-run warnings = %v", w)
	}

	fresh := scheduleRecord{Time: now.Add(-time.Hour), ID: "qb-20261001-110000", LastSuccess: now.Add(-time.Hour), LastSuccessID: "qb-20261001-110000"}
	if err := saveScheduleRecord(p, fresh); err != nil {
		t.Fatal(err)
	}
	if w := scheduleWarnings(p, now); len(w) != 0 {
		t.Fatalf("healthy schedule warned: %v", w)
	}

	stale := fresh
	stale.LastSuccess = now.Add(-48 * time.Hour)
	stale.Time = now.Add(-time.Hour)
	stale.Error = "vpg failed"
	if err := saveScheduleRecord(p, stale); err != nil {
		t.Fatal(err)
	}
	if w := scheduleWarnings(p, now); len(w) != 2 {
		t.Fatalf("stale+failed warnings = %v", w)
	}

	offsite := fresh
	offsite.Offsite, offsite.OffsiteStatus, offsite.OffsiteError = "dr1", "failed", "ssh: timeout"
	offsite.Error = "off-site push to dr1: ssh: timeout"
	if err := saveScheduleRecord(p, offsite); err != nil {
		t.Fatal(err)
	}
	w := scheduleWarnings(p, now)
	if len(w) != 1 || !strings.Contains(w[0], "off-site push to dr1 failed") {
		t.Fatalf("off-site warnings = %v", w)
	}
	checks := scheduleChecks(p, cfg, now)
	if len(checks) != 1 || checks[0].OK || checks[0].Level != "warn" {
		t.Fatalf("doctor checks = %+v (must warn, never error)", checks)
	}

	t.Setenv("QABAS_NODE", "dr1")
	if w := scheduleWarnings(p, now); len(w) != 0 {
		t.Fatalf("replica warned about the writer's schedule: %v", w)
	}
}
