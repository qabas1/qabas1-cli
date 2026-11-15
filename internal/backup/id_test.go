package backup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/backup"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
)

func TestNewID(t *testing.T) {
	id := backup.NewID(time.Date(2026, 8, 20, 2, 15, 30, 0, time.UTC))
	if id != "qb-20260820-021530" {
		t.Fatalf("id %s", id)
	}
	if !strings.HasPrefix(backup.NewID(time.Now()), "qb-") {
		t.Fatal("prefix")
	}
}

func TestNormalizeName(t *testing.T) {
	got, err := backup.NormalizeName("  backup v1  ")
	if err != nil || got != "backup v1" {
		t.Fatalf("got %q err %v", got, err)
	}
	got, err = backup.NormalizeName("   ")
	if err != nil || got != "" {
		t.Fatalf("empty got %q err %v", got, err)
	}
	if _, err := backup.NormalizeName("bad/name"); exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("slash: %v", err)
	}
	if _, err := backup.NormalizeName("bad\nname"); exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("control: %v", err)
	}
	long := strings.Repeat("a", backup.MaxNameLen+1)
	if _, err := backup.NormalizeName(long); exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("length: %v", err)
	}
}

func TestAutoName(t *testing.T) {
	got := backup.AutoName(time.Date(2026, 9, 15, 2, 30, 0, 0, time.UTC))
	if got != "manual_20260915-023000" {
		t.Fatalf("got %q", got)
	}
}
