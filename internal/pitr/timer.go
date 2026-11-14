package pitr

import (
	"context"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
)

// Weekly base backup units installed by `qabas backup pitr enable`.
const (
	BaseService = "qabas-pitr-base.service"
	BaseTimer   = "qabas-pitr-base.timer"
)

func baseUnits(binary string) (service, timer string) {
	service = `[Unit]
Description=QabasOne PITR weekly base backup
After=docker.service network-online.target

[Service]
Type=oneshot
Nice=10
IOSchedulingClass=idle
ExecStart=` + binary + ` backup pitr base --keep 4 --quiet --non-interactive
`
	timer = `[Unit]
Description=QabasOne PITR weekly base backup timer

[Timer]
OnCalendar=Sun *-*-* 02:30:00
RandomizedDelaySec=15m
Persistent=true

[Install]
WantedBy=timers.target
`
	return service, timer
}

// InstallBaseTimer installs and enables the weekly base backup timer on x.
func InstallBaseTimer(ctx context.Context, x nodeexec.Executor, binary string) error {
	svc, tmr := baseUnits(binary)
	if err := nodeexec.WriteFile(ctx, x, "/etc/systemd/system/"+BaseService, []byte(svc), 0o644); err != nil {
		return err
	}
	if err := nodeexec.WriteFile(ctx, x, "/etc/systemd/system/"+BaseTimer, []byte(tmr), 0o644); err != nil {
		return err
	}
	_, err := nodeexec.Output(ctx, x, "systemctl daemon-reload && systemctl enable --now "+BaseTimer+" >/dev/null")
	return err
}
