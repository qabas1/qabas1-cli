//go:build unix

package lock

import (
	"os"

	"golang.org/x/sys/unix"
)

func exclusive(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlock(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func writeRecord(f *os.File, _ string, b []byte) error {
	if _, err := f.WriteAt(b, 0); err != nil {
		return err
	}
	return f.Truncate(int64(len(b)))
}

func readRecord(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func clearRecord(string) {}
