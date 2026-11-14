package backup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
)

// Daily full backup units installed by `qabas backup schedule enable`.
const (
	ScheduleService = "qabas-backup.service"
	ScheduleTimer   = "qabas-backup.timer"
	unitDir         = "/etc/systemd/system/"
	zoneinfoDir     = "/usr/share/zoneinfo/"
)

// ScheduleUnits renders the service and timer. at is HH:MM in the IANA zone tz.
func ScheduleUnits(binary, at, tz string) (service, timer string) {
	service = `[Unit]
Description=QabasOne scheduled full backup
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Nice=10
IOSchedulingClass=idle
CPUSchedulingPolicy=batch
UMask=0077
TimeoutStartSec=6h
ExecStart=` + binary + ` backup schedule run --quiet --non-interactive
`
	timer = `[Unit]
Description=QabasOne scheduled full backup timer

[Timer]
OnCalendar=*-*-* ` + at + `:00 ` + tz + `
RandomizedDelaySec=10m
Persistent=true

[Install]
WantedBy=timers.target
`
	return service, timer
}

// ScheduleTimerPath is where InstallScheduleTimer writes the timer unit.
const ScheduleTimerPath = unitDir + ScheduleTimer

// TimerHasZone reports whether the timer unit's OnCalendar line is pinned to tz.
func TimerHasZone(unit, tz string) bool {
	for _, line := range strings.Split(unit, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "OnCalendar="); ok {
			return strings.HasSuffix(strings.TrimSpace(v), " "+tz)
		}
	}
	return false
}

// ErrNoZoneinfo means systemd cannot resolve the schedule timezone on the host.
var ErrNoZoneinfo = errors.New("timezone data not found")

// RequireZoneinfo checks that /usr/share/zoneinfo/<tz> exists on x; systemd reads it
// to evaluate OnCalendar.
func RequireZoneinfo(ctx context.Context, x nodeexec.Executor, tz string) error {
	out, err := nodeexec.Output(ctx, x, "test -e "+nodeexec.Quote(zoneinfoDir+tz)+" && echo ok || echo missing")
	if err != nil {
		return err
	}
	if out == "missing" {
		return fmt.Errorf("%w: %s%s", ErrNoZoneinfo, zoneinfoDir, tz)
	}
	return nil
}

// InstallScheduleTimer writes the units and enables the timer on x.
func InstallScheduleTimer(ctx context.Context, x nodeexec.Executor, binary, at, tz string) error {
	svc, tmr := ScheduleUnits(binary, at, tz)
	if err := nodeexec.WriteFile(ctx, x, unitDir+ScheduleService, []byte(svc), 0o644); err != nil {
		return err
	}
	if err := nodeexec.WriteFile(ctx, x, unitDir+ScheduleTimer, []byte(tmr), 0o644); err != nil {
		return err
	}
	_, err := nodeexec.Output(ctx, x, "systemctl daemon-reload && systemctl enable --now "+ScheduleTimer+" >/dev/null")
	return err
}

// RemoveScheduleTimer stops and deletes the units; existing backups are untouched.
func RemoveScheduleTimer(ctx context.Context, x nodeexec.Executor) error {
	_, err := nodeexec.Output(ctx, x, "systemctl disable --now "+ScheduleTimer+" >/dev/null 2>&1 || true\n"+
		"rm -f "+nodeexec.Quote(unitDir+ScheduleTimer)+" "+nodeexec.Quote(unitDir+ScheduleService)+"\n"+
		"systemctl daemon-reload")
	return err
}

// NextScheduledRun reports when the timer fires next (zero if unknown or inactive).
func NextScheduledRun(ctx context.Context, x nodeexec.Executor) time.Time {
	out, err := nodeexec.Output(ctx, x, "systemctl show "+ScheduleTimer+" --property=NextElapseUSecRealtime --value --timestamp=unix 2>/dev/null || true")
	if err != nil {
		return time.Time{}
	}
	v := strings.TrimPrefix(strings.TrimSpace(out), "@")
	if v == "" || v == "n/a" {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// NextDailyRun is the next occurrence of HH:MM in loc strictly after now (zero if at is
// malformed). It ignores the timer's random delay.
func NextDailyRun(now time.Time, at string, loc *time.Location) time.Time {
	hm, err := time.Parse("15:04", at)
	if err != nil || loc == nil {
		return time.Time{}
	}
	n := now.In(loc)
	next := time.Date(n.Year(), n.Month(), n.Day(), hm.Hour(), hm.Minute(), 0, 0, loc)
	if !next.After(n) {
		next = time.Date(n.Year(), n.Month(), n.Day()+1, hm.Hour(), hm.Minute(), 0, 0, loc)
	}
	return next
}
