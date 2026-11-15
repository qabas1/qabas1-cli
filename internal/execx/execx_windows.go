//go:build windows

package execx

import (
	"os/exec"
	"syscall"

	"github.com/qabas1/qabas1-cli/internal/interrupt"
)

// detach starts children in a new process group during critical sections so
// console Ctrl+C (delivered to every attached process) cannot kill docker/vpg
// while interrupt.Protect defers the parent's exit. Outside critical sections
// children share the group so Ctrl+C still stops `logs -f` and interactive runs.
func detach(cmd *exec.Cmd) {
	if !interrupt.Active() {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
