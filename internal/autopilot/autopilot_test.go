package autopilot

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/sysinfo"
)

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func machine(level string) func() sysinfo.Info {
	return func() sysinfo.Info {
		return sysinfo.Info{Findings: []sysinfo.Finding{{Check: "disk:state", Level: level}, {Check: "memory", Level: sysinfo.LevelOK}}}
	}
}

type dockerLog struct{ calls []string }

func (d *dockerLog) run(args ...string) (string, error) {
	d.calls = append(d.calls, strings.Join(args, " "))
	return "Deleted Images:\nTotal reclaimed space: 1.5GB\n", nil
}

func TestRunHealthyDiskOnlyRoutineHygiene(t *testing.T) {
	dl := &dockerLog{}
	pruned := false
	rec := Run(Deps{
		Now:          func() time.Time { return now },
		Machine:      machine(sysinfo.LevelOK),
		Docker:       dl.run,
		PruneBackups: func(bool) ([]string, error) { pruned = true; return nil, nil },
	}, Record{})
	if len(dl.calls) != 2 || dl.calls[0] != "image prune -f" || !strings.Contains(dl.calls[1], "until="+BuildCacheMaxAge) {
		t.Fatalf("docker calls %v", dl.calls)
	}
	if pruned {
		t.Fatal("backups must not be pruned early when disk is healthy")
	}
	if rec.FreedBytes != 3e9 || rec.Health != sysinfo.LevelOK {
		t.Fatalf("record %+v", rec)
	}
	for _, c := range dl.calls {
		if strings.Contains(c, "volume") || strings.Contains(c, "system prune") || strings.Contains(c, "-a") {
			t.Fatalf("destructive docker call %q", c)
		}
	}
}

func TestRunDiskCriticalEscalates(t *testing.T) {
	dl := &dockerLog{}
	var modes []bool
	rec := Run(Deps{
		Now:     func() time.Time { return now },
		Machine: machine(sysinfo.LevelCrit),
		Docker:  dl.run,
		PruneBackups: func(minimal bool) ([]string, error) {
			modes = append(modes, minimal)
			return []string{"b1"}, nil
		},
	}, Record{})
	if len(dl.calls) != 3 || dl.calls[2] != "builder prune -af" {
		t.Fatalf("docker calls %v", dl.calls)
	}
	if len(modes) != 2 || modes[0] || !modes[1] {
		t.Fatalf("expected normal then emergency retention, got %v", modes)
	}
	if rec.Health != sysinfo.LevelCrit {
		t.Fatalf("health %s", rec.Health)
	}
}

func TestRunWarnPressureAppliesNormalRetentionOnly(t *testing.T) {
	var modes []bool
	Run(Deps{
		Now:          func() time.Time { return now },
		Machine:      machine(sysinfo.LevelWarn),
		PruneBackups: func(minimal bool) ([]string, error) { modes = append(modes, minimal); return nil, nil },
	}, Record{})
	if len(modes) != 1 || modes[0] {
		t.Fatalf("modes %v", modes)
	}
}

func TestSelfHealRestartsWithCap(t *testing.T) {
	var restarted []string
	svcs := []compose.ServiceStatus{
		{Service: "api", Health: "unhealthy"},
		{Service: "redis", Health: "healthy"},
		{Service: "app", Health: "unhealthy"},
	}
	prev := Record{Restarts: map[string][]time.Time{
		"app": {now.Add(-1 * time.Hour), now.Add(-2 * time.Hour), now.Add(-3 * time.Hour)},
		"api": {now.Add(-30 * time.Hour)},
	}}
	rec := Run(Deps{
		Now:      func() time.Time { return now },
		Healable: true,
		Services: func() ([]compose.ServiceStatus, error) { return svcs, nil },
		Restart:  func(s string) error { restarted = append(restarted, s); return nil },
	}, prev)
	if len(restarted) != 1 || restarted[0] != "api" {
		t.Fatalf("restarted %v", restarted)
	}
	if len(rec.Restarts["api"]) != 1 {
		t.Fatalf("old restarts must expire: %v", rec.Restarts["api"])
	}
	errs := rec.Errors()
	if len(errs) != 1 || !strings.Contains(errs[0].Error, "app restarted 3 times") {
		t.Fatalf("errors %+v", errs)
	}
	if rec.Health != sysinfo.LevelWarn {
		t.Fatalf("errors should raise health to warn, got %s", rec.Health)
	}
}

func TestSelfHealSkippedWhenNotHealable(t *testing.T) {
	called := false
	Run(Deps{
		Now:      func() time.Time { return now },
		Healable: false,
		Services: func() ([]compose.ServiceStatus, error) {
			called = true
			return nil, nil
		},
		Restart: func(string) error { return nil },
	}, Record{})
	if called {
		t.Fatal("services must not be inspected during maintenance or deploy")
	}
}

func TestMaintenanceExpiryAndOtherTasks(t *testing.T) {
	ended := false
	rec := Run(Deps{
		Now:                func() time.Time { return now },
		MaintenanceExpired: func(time.Time) bool { return true },
		EndMaintenance:     func() error { ended = true; return nil },
		PruneWAL:           func() (int, int64, error) { return 4, 64 << 20, nil },
		EnsureLogRotation:  func() (bool, error) { return true, nil },
	}, Record{})
	if !ended {
		t.Fatal("maintenance should end")
	}
	tasks := map[string]Action{}
	for _, a := range rec.Actions {
		tasks[a.Task] = a
	}
	if tasks["pitr-wal"].FreedBytes != 64<<20 || tasks["log-rotation"].Detail == "" || tasks["maintenance-expiry"].Error != "" {
		t.Fatalf("actions %+v", rec.Actions)
	}
	if !strings.Contains(rec.Summary(), "pitr-wal") {
		t.Fatalf("summary %q", rec.Summary())
	}
}

func TestDockerFailureRecorded(t *testing.T) {
	rec := Run(Deps{
		Now: func() time.Time { return now },
		Docker: func(...string) (string, error) {
			return "Cannot connect to the Docker daemon", errors.New("exit status 1")
		},
	}, Record{})
	if len(rec.Errors()) != 2 || !strings.Contains(rec.Errors()[0].Error, "Cannot connect") {
		t.Fatalf("errors %+v", rec.Errors())
	}
}

func TestReclaimedParsesBuilderOutput(t *testing.T) {
	if got := reclaimed("ID\tRECLAIMABLE\nabc\t1MB\nTotal:\t2.5GB\n"); got != 2.5e9 {
		t.Fatalf("got %d", got)
	}
}
