package autopilot

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

func TestUnits(t *testing.T) {
	svc, tmr := Units("qabas")
	for _, want := range []string{"ExecStart=qabas autopilot run --quiet --non-interactive", "Nice=10", "IOSchedulingClass=idle"} {
		if !strings.Contains(svc, want) {
			t.Fatalf("service missing %q:\n%s", want, svc)
		}
	}
	for _, want := range []string{"OnCalendar=hourly", "Persistent=true", "OnBootSec=10min"} {
		if !strings.Contains(tmr, want) {
			t.Fatalf("timer missing %q:\n%s", want, tmr)
		}
	}
}

func TestSchedulerLinux(t *testing.T) {
	x := nodeexec.NewFake("local").OnOut("is-enabled", "enabled")
	s := &Scheduler{GOOS: "linux", X: x}
	if err := s.Install(context.Background(), "qabas"); err != nil {
		t.Fatal(err)
	}
	if !x.Ran("systemctl enable --now " + Timer) {
		t.Fatalf("timer not enabled: %v", x.Log)
	}
	if !s.Installed(context.Background()) {
		t.Fatal("expected installed")
	}
}

func TestSchedulerWindows(t *testing.T) {
	var calls [][]string
	installed := false
	s := &Scheduler{GOOS: "windows", Schtasks: func(args ...string) (string, error) {
		calls = append(calls, args)
		switch args[0] {
		case "/Create":
			installed = true
		case "/Delete":
			installed = false
		case "/Query":
			if !installed {
				return "", errors.New("not found")
			}
		}
		return "", nil
	}}
	if err := s.Install(context.Background(), `C:\Program Files\QabasOne\qabas.exe`); err != nil {
		t.Fatal(err)
	}
	create := strings.Join(calls[0], " ")
	for _, want := range []string{"/SC HOURLY", "/RU SYSTEM", `"C:\Program Files\QabasOne\qabas.exe" autopilot run`} {
		if !strings.Contains(create, want) {
			t.Fatalf("create missing %q: %s", want, create)
		}
	}
	if !s.Installed(context.Background()) {
		t.Fatal("expected installed")
	}
	if err := s.Remove(context.Background()); err != nil || s.Installed(context.Background()) {
		t.Fatalf("remove failed: %v", err)
	}
}

func TestOptOutMarkerAndRecord(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	if OptedOut(p) {
		t.Fatal("fresh state is not opted out")
	}
	if err := SetOptOut(p, true); err != nil || !OptedOut(p) {
		t.Fatalf("opt out: %v", err)
	}
	if err := SetOptOut(p, false); err != nil || OptedOut(p) {
		t.Fatalf("opt in: %v", err)
	}
	if err := Save(p, Record{Health: "ok", FreedBytes: 42}); err != nil {
		t.Fatal(err)
	}
	r, ok := Load(p)
	if !ok || r.FreedBytes != 42 {
		t.Fatalf("load %+v %v", r, ok)
	}
}
