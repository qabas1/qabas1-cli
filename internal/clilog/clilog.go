// Package clilog writes the qabas audit log (qabas.log): one redacted logfmt
// line per command, size-rotated into gzip archives so it can never fill the disk.
package clilog

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/redact"
)

const (
	MaxBytes = 10 << 20
	Keep     = 5
)

type Entry struct {
	Time     time.Time
	Args     []string
	Exit     int
	Duration time.Duration
	Error    string
	Hint     string
}

// Line renders e as a single redacted logfmt line.
func (e Entry) Line() string {
	var b strings.Builder
	t := e.Time
	if t.IsZero() {
		t = time.Now()
	}
	fmt.Fprintf(&b, "time=%s pid=%d exit=%d dur=%s cmd=%s",
		t.UTC().Format(time.RFC3339), os.Getpid(), e.Exit, e.Duration.Round(time.Millisecond), quote(redact.Args(append([]string{"qabas"}, e.Args...))))
	if e.Error != "" {
		b.WriteString(" err=" + quote(redact.String(e.Error)))
	}
	if e.Hint != "" {
		b.WriteString(" hint=" + quote(redact.String(e.Hint)))
	}
	return b.String()
}

func quote(s string) string { return strconv.Quote(s) }

// Record appends e to path. Logging is best-effort: callers ignore errors so
// an unwritable log directory never fails a command.
func Record(path string, e Entry) error { return Append(path, e.Line()) }

// Append writes one line, rotating first when the file has reached MaxBytes.
func Append(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil && st.Size() >= MaxBytes {
		if err := Rotate(path, Keep); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.TrimRight(line, "\n") + "\n")
	return err
}

// Rotate gzips path into path.1.gz, shifting older archives up to keep.
func Rotate(path string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	_ = os.Remove(archive(path, keep))
	for i := keep - 1; i >= 1; i-- {
		if _, err := os.Stat(archive(path, i)); err == nil {
			if err := os.Rename(archive(path, i), archive(path, i+1)); err != nil {
				return err
			}
		}
	}
	src, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	tmp := archive(path, 1) + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		src.Close()
		return err
	}
	zw := gzip.NewWriter(dst)
	_, cerr := io.Copy(zw, src)
	src.Close()
	if err := zw.Close(); cerr == nil {
		cerr = err
	}
	if err := dst.Close(); cerr == nil {
		cerr = err
	}
	if cerr != nil {
		_ = os.Remove(tmp)
		return cerr
	}
	if err := os.Rename(tmp, archive(path, 1)); err != nil {
		return err
	}
	return os.Truncate(path, 0)
}

func archive(path string, n int) string { return fmt.Sprintf("%s.%d.gz", path, n) }

// Archives lists existing rotated files for path, newest first.
func Archives(path string, keep int) []string {
	var out []string
	for i := 1; i <= keep; i++ {
		if _, err := os.Stat(archive(path, i)); err == nil {
			out = append(out, archive(path, i))
		}
	}
	return out
}

// Size is the total bytes of path and its archives.
func Size(path string, keep int) int64 {
	var n int64
	for _, f := range append([]string{path}, Archives(path, keep)...) {
		if st, err := os.Stat(f); err == nil {
			n += st.Size()
		}
	}
	return n
}
