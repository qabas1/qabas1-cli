//go:build windows

package execx_test

import (
	"syscall"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/execx"
	"github.com/qabas1/qabas1-cli/internal/interrupt"
)

func TestCommandNewProcessGroupOnlyInCriticalSection(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)

	cmd := execx.Command("cmd", "/c", "exit", "0")
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP != 0 {
		t.Fatal("unexpected new process group outside critical section")
	}

	_ = interrupt.Protect("TEST", func() error {
		cmd = execx.Command("cmd", "/c", "exit", "0")
		return nil
	})
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Fatal("expected CREATE_NEW_PROCESS_GROUP inside critical section")
	}
}
