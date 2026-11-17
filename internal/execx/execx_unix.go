//go:build !windows

package execx

import (
	"os/exec"
	"syscall"

	"github.com/qabas1/qabas1-cli/internal/interrupt"
)

func detach(cmd *exec.Cmd) {
	// Keep interactive commands in the terminal's foreground process group so
	// Ctrl+C reaches Docker and its Compose plugin as well as the CLI.
	if !interrupt.Active() {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
