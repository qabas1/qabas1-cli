//go:build windows

package lock

import (
	"errors"

	"golang.org/x/sys/windows"
)

const stillActive = 259

func inspectProc(pid int) procInfo {
	if pid <= 0 {
		return procInfo{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return procInfo{Alive: true}
		}
		return procInfo{}
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return procInfo{}
	}
	p := procInfo{Alive: true}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err == nil {
		p.Exe = windows.UTF16ToString(buf[:n])
	}
	return p
}

func (p procInfo) stopped() bool { return false }

func resumeProc(int) error { return nil }

// Windows has no graceful SIGTERM for console processes; the grace period only
// gives a holder that is finishing on its own a chance to release.
func termProc(int) error { return nil }

func killProc(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}
