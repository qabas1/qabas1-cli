package backup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
)

func TestInstallScheduleTimer(t *testing.T) {
	x := nodeexec.NewFake("hq")
	if err := InstallScheduleTimer(context.Background(), x, "/usr/bin/qabas", "01:30", "Africa/Cairo"); err != nil {
		t.Fatal(err)
	}
	svc := x.Files[unitDir+ScheduleService]
	for _, want := range []string{
		"ExecStart=/usr/bin/qabas backup schedule run --quiet --non-interactive",
		"IOSchedulingClass=idle", "Nice=10", "UMask=0077", "Type=oneshot",
	} {
		if !strings.Contains(svc, want) {
			t.Fatalf("service unit missing %q:\n%s", want, svc)
		}
	}
	tmr := x.Files[unitDir+ScheduleTimer]
	if !strings.Contains(tmr, "OnCalendar=*-*-* 01:30:00 Africa/Cairo\n") || !strings.Contains(tmr, "Persistent=true") {
		t.Fatalf("timer unit not daily/persistent:\n%s", tmr)
	}
	if !x.Ran("systemctl enable --now " + ScheduleTimer) {
		t.Fatal("timer was not enabled")
	}
}

func TestRemoveScheduleTimer(t *testing.T) {
	x := nodeexec.NewFake("hq")
	if err := RemoveScheduleTimer(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if !x.Ran("systemctl disable --now "+ScheduleTimer) || !x.Ran("rm -f '"+unitDir+ScheduleTimer+"'") {
		t.Fatal("timer not disabled and removed")
	}
}

func TestNextScheduledRun(t *testing.T) {
	x := nodeexec.NewFake("hq").OnOut("NextElapseUSecRealtime", "@1790811000\n")
	if got := NextScheduledRun(context.Background(), x); !got.Equal(time.Unix(1790811000, 0)) {
		t.Fatalf("next = %v", got)
	}
	if got := NextScheduledRun(context.Background(), nodeexec.NewFake("hq").OnOut("NextElapseUSecRealtime", "n/a")); !got.IsZero() {
		t.Fatalf("inactive timer next = %v", got)
	}
}

func TestRequireZoneinfo(t *testing.T) {
	ctx := context.Background()
	if err := RequireZoneinfo(ctx, nodeexec.NewFake("hq").OnOut("zoneinfo", "ok"), "Africa/Cairo"); err != nil {
		t.Fatal(err)
	}
	err := RequireZoneinfo(ctx, nodeexec.NewFake("hq").OnOut("zoneinfo", "missing"), "Africa/Cairo")
	if !errors.Is(err, ErrNoZoneinfo) {
		t.Fatalf("err = %v", err)
	}
}

func TestNextDailyRun(t *testing.T) {
	cairo, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-01-15 is winter time in Cairo (UTC+2): 23:00 UTC = 01:00 Cairo on the 16th.
	now := time.Date(2026, 1, 15, 23, 0, 0, 0, time.UTC)
	got := NextDailyRun(now, "01:30", cairo)
	if want := time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	now = time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC)
	if got := NextDailyRun(now, "01:30", cairo); !got.Equal(time.Date(2026, 1, 16, 23, 30, 0, 0, time.UTC)) {
		t.Fatalf("next at boundary = %v", got)
	}
	if !NextDailyRun(now, "bad", cairo).IsZero() {
		t.Fatal("malformed time accepted")
	}
}
