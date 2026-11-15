package pitr

import (
	"context"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
)

func TestInstallBaseTimer(t *testing.T) {
	x := nodeexec.NewFake("hq")
	if err := InstallBaseTimer(context.Background(), x, "qabas"); err != nil {
		t.Fatal(err)
	}
	svc := x.Files["/etc/systemd/system/"+BaseService]
	if !strings.Contains(svc, "ExecStart=qabas backup pitr base --keep 4") {
		t.Fatalf("service unit missing ExecStart: %q", svc)
	}
	tmr := x.Files["/etc/systemd/system/"+BaseTimer]
	if !strings.Contains(tmr, "OnCalendar=Sun") || !strings.Contains(tmr, "Persistent=true") {
		t.Fatalf("timer unit not weekly/persistent: %q", tmr)
	}
	if !x.Ran("systemctl enable --now " + BaseTimer) {
		t.Fatal("timer was not enabled")
	}
}
