package clilog

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/redact"
)

func TestLineIsRedacted(t *testing.T) {
	redact.Reset()
	redact.Register("supersecretvalue")
	line := Entry{
		Time:     time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Args:     []string{"auth", "login", "--github-token", "ghp_abcdefghijklmnopqrstuvwxyz0123"},
		Exit:     3,
		Duration: 1500 * time.Millisecond,
		Error:    "connect failed: password=supersecretvalue",
	}.Line()
	for _, leak := range []string{"ghp_abcdefghijklmnopqrstuvwxyz0123", "supersecretvalue"} {
		if strings.Contains(line, leak) {
			t.Fatalf("line leaks %q: %s", leak, line)
		}
	}
	for _, want := range []string{"time=2026-01-02T03:04:05Z", "exit=3", "dur=1.5s", `cmd="qabas auth login --github-token`} {
		if !strings.Contains(line, want) {
			t.Fatalf("line missing %q: %s", want, line)
		}
	}
}

func TestRotateKeepsBoundedArchives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "qabas.log")
	for i := 0; i < 4; i++ {
		if err := Append(path, "line "+strings.Repeat("x", 10)); err != nil {
			t.Fatal(err)
		}
		if err := Rotate(path, 2); err != nil {
			t.Fatal(err)
		}
	}
	if got := Archives(path, 5); len(got) != 2 {
		t.Fatalf("archives = %v, want 2", got)
	}
	f, err := os.Open(path + ".1.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !strings.HasPrefix(string(body), "line ") {
		t.Fatalf("archive body = %q", body)
	}
	if st, _ := os.Stat(path); st.Size() != 0 {
		t.Fatalf("live log not truncated: %d", st.Size())
	}
}

func TestAppendRotatesAtMaxBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qabas.log")
	if err := os.WriteFile(path, make([]byte, MaxBytes), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "next"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Size() != int64(len("next\n")) {
		t.Fatalf("size = %d", st.Size())
	}
	if len(Archives(path, Keep)) != 1 {
		t.Fatal("expected one archive")
	}
}
