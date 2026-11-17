//go:build !windows

package execx_test

import (
	"testing"

	"github.com/qabas1/qabas1-cli/internal/execx"
	"github.com/qabas1/qabas1-cli/internal/interrupt"
)

func TestCommandNewProcessGroupOnlyInCriticalSection(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)

	cmd := execx.Command("true")
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		t.Fatal("unexpected new process group outside critical section")
	}
	_ = interrupt.Protect("TEST", func() error {
		cmd = execx.Command("true")
		return nil
	})
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("expected Setpgid on SysProcAttr, got %#v", cmd.SysProcAttr)
	}
	cmd = execx.Command("true")
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid {
		t.Fatal("process group isolation persisted after critical section")
	}
}
