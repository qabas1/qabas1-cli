package autopilot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/nodeexec"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

// Scheduled entry points installed by `qabas autopilot enable`.
const (
	Service     = "qabas-autopilot.service"
	Timer       = "qabas-autopilot.timer"
	WindowsTask = `QabasOne\Autopilot`
	unitDir     = "/etc/systemd/system/"
	runArgs     = "autopilot run --quiet --non-interactive"
)

// Units renders the hourly systemd service and timer.
func Units(binary string) (service, timer string) {
	service = `[Unit]
Description=QabasOne autopilot (self-maintenance)
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
Nice=10
IOSchedulingClass=idle
CPUSchedulingPolicy=batch
UMask=0077
TimeoutStartSec=1h
ExecStart=` + binary + ` ` + runArgs + `
`
	timer = `[Unit]
Description=QabasOne autopilot timer

[Timer]
OnBootSec=10min
OnCalendar=hourly
RandomizedDelaySec=5m
Persistent=true

[Install]
WantedBy=timers.target
`
	return service, timer
}

// SchtasksCreateArgs renders the Windows Task Scheduler entry (hourly, SYSTEM).
func SchtasksCreateArgs(binary string) []string {
	return []string{"/Create", "/F", "/TN", WindowsTask, "/SC", "HOURLY", "/MO", "1",
		"/RU", "SYSTEM", "/RL", "HIGHEST", "/TR", `"` + binary + `" ` + runArgs}
}

// Scheduler installs the periodic autopilot run for the current OS.
type Scheduler struct {
	GOOS string
	X    nodeexec.Executor
	// Schtasks runs schtasks.exe; injectable for tests.
	Schtasks func(args ...string) (string, error)
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		GOOS: runtime.GOOS,
		X:    &nodeexec.Local{Name: "local"},
		Schtasks: func(args ...string) (string, error) {
			out, err := exec.Command("schtasks", args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
	}
}

func (s *Scheduler) Install(ctx context.Context, binary string) error {
	if s.GOOS == "windows" {
		_, err := s.Schtasks(SchtasksCreateArgs(binary)...)
		return err
	}
	svc, tmr := Units(binary)
	if err := nodeexec.WriteFile(ctx, s.X, unitDir+Service, []byte(svc), 0o644); err != nil {
		return err
	}
	if err := nodeexec.WriteFile(ctx, s.X, unitDir+Timer, []byte(tmr), 0o644); err != nil {
		return err
	}
	_, err := nodeexec.Output(ctx, s.X, "systemctl daemon-reload && systemctl enable --now "+Timer+" >/dev/null")
	return err
}

func (s *Scheduler) Remove(ctx context.Context) error {
	if s.GOOS == "windows" {
		if !s.Installed(ctx) {
			return nil
		}
		_, err := s.Schtasks("/Delete", "/F", "/TN", WindowsTask)
		return err
	}
	_, err := nodeexec.Output(ctx, s.X, "systemctl disable --now "+Timer+" >/dev/null 2>&1 || true\n"+
		"rm -f "+nodeexec.Quote(unitDir+Timer)+" "+nodeexec.Quote(unitDir+Service)+"\n"+
		"systemctl daemon-reload")
	return err
}

func (s *Scheduler) Installed(ctx context.Context) bool {
	if s.GOOS == "windows" {
		_, err := s.Schtasks("/Query", "/TN", WindowsTask)
		return err == nil
	}
	out, err := nodeexec.Output(ctx, s.X, "systemctl is-enabled "+Timer+" 2>/dev/null || true")
	return err == nil && strings.TrimSpace(out) == "enabled"
}

// Describe names the scheduler entry for messages.
func (s *Scheduler) Describe() string {
	if s.GOOS == "windows" {
		return "Task Scheduler task " + WindowsTask + " (hourly)"
	}
	return "systemd timer " + Timer + " (hourly)"
}

// DisabledMarker records that an operator turned autopilot off on purpose.
func DisabledMarker(p paths.Paths) string { return filepath.Join(p.StateDir, "autopilot.disabled") }

func OptedOut(p paths.Paths) bool {
	_, err := os.Stat(DisabledMarker(p))
	return err == nil
}

func SetOptOut(p paths.Paths, out bool) error {
	if !out {
		err := os.Remove(DisabledMarker(p))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(DisabledMarker(p), []byte("disabled by qabas autopilot disable\n"), 0o644)
}
