//go:build windows

package nodeexec

import "os"

func killLocalProcessTree(p *os.Process) {
	if p != nil {
		_ = p.Kill()
	}
}
