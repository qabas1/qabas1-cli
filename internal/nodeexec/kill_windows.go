//go:build windows

package nodeexec

import (
	"os"
	"os/exec"
)

func prepareLocalCommand(cmd *exec.Cmd) {}

func killLocalProcessTree(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}
