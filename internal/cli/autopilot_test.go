package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/config"
	"github.com/qabasone/qabasone-cli/internal/lock"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

func TestAutopilotRunSkipsOnlyWhenBusy(t *testing.T) {
	p := scheduleFixture(t, "hq")
	held, err := lock.Acquire(p.LockPath, lock.Info{Command: "qabas backup schedule run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runRoot(t, "autopilot", "run"); err != nil {
		t.Fatalf("busy lock must skip quietly, got %v", err)
	}
	_ = held.Release()

	dir := filepath.Join(t.TempDir(), "lock-is-a-dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(paths.EnvLockPath, dir)
	if err := runRoot(t, "autopilot", "run"); err == nil {
		t.Fatal("an unopenable lock must be reported, not skipped")
	}
}

func TestAutopilotRetention(t *testing.T) {
	s := config.Schedule{KeepDaily: 14, KeepWeekly: 8, KeepMonthly: 12}
	if got := autopilotRetention(s, false); got.KeepDaily != 14 || got.KeepWeekly != 8 || got.KeepMonthly != 12 {
		t.Fatalf("normal retention changed: %+v", got)
	}
	if got := autopilotRetention(s, true); got.KeepDaily != 2 || got.KeepWeekly != 1 || got.KeepMonthly != 0 {
		t.Fatalf("minimal retention: %+v", got)
	}
	unset := autopilotRetention(config.Schedule{}, false)
	def := config.DefaultSchedule()
	if unset.KeepDaily != def.KeepDaily || unset.KeepWeekly != def.KeepWeekly || unset.KeepMonthly != def.KeepMonthly {
		t.Fatalf("unset schedule must fall back to defaults, never prune everything: %+v", unset)
	}
	if got := autopilotRetention(config.Schedule{KeepMonthly: 3}, true); got.KeepDaily+got.KeepWeekly == 0 {
		t.Fatalf("minimal retention must keep at least one backup: %+v", got)
	}
}
