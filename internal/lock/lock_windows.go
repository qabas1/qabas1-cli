//go:build windows

package lock

import (
	"os"

	"golang.org/x/sys/windows"
)

func exclusive(f *os.File) error {
	var ov windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&ov,
	)
}

func unlock(f *os.File) error {
	var ov windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov)
}

// The locked byte range cannot be read by other processes, so the holder
// record lives in a sidecar file.
func recordPath(path string) string { return path + ".holder" }

func writeRecord(_ *os.File, path string, b []byte) error {
	tmp := recordPath(path) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, recordPath(path))
}

func readRecord(path string) ([]byte, error) {
	return os.ReadFile(recordPath(path))
}

func clearRecord(path string) { _ = os.Remove(recordPath(path)) }
