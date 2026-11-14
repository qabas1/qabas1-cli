//go:build unix

package lock

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func inspectProc(pid int) procInfo {
	if pid <= 0 {
		return procInfo{}
	}
	if err := unix.Kill(pid, 0); err != nil && !errors.Is(err, unix.EPERM) {
		return procInfo{}
	}
	p := procInfo{Alive: true}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		// "pid (comm) S ..."; comm may itself contain spaces or parentheses.
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
			p.State = s[i+2 : i+3]
		}
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		p.Exe, _, _ = strings.Cut(string(b), "\x00")
	}
	if p.State == "Z" || p.State == "X" {
		p.Alive = false
	}
	return p
}

func (p procInfo) stopped() bool { return p.State == "T" || p.State == "t" }

func resumeProc(pid int) error { return unix.Kill(pid, unix.SIGCONT) }

func termProc(pid int) error { return unix.Kill(pid, unix.SIGTERM) }

func killProc(pid int) error { return unix.Kill(pid, unix.SIGKILL) }
