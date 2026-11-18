//go:build !windows

package nodeexec

import (
	"os"
	"os/exec"
	"syscall"
)

func prepareLocalCommand(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killLocalProcessTree(p *os.Process) {
	if p == nil {
		return
	}
	// execx.Command starts a fresh process group; terminate docker exec and the
	// shell together so a descendant cannot retain stdout/stderr pipes.
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
	_ = p.Kill()
}
