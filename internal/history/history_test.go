package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/redact"
)

func testPaths(t *testing.T) paths.Paths {
	d := t.TempDir()
	return paths.Paths{ConfigDir: filepath.Join(d, "etc"), StateDir: filepath.Join(d, "state"), LogDir: filepath.Join(d, "log")}
}

func TestAppendRedactsMessage(t *testing.T) {
	redact.Reset()
	p := testPaths(t)
	if err := Append(p, Entry{Action: "x", Status: "failed", Message: "dial: password=hunter22"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p.HistoryFile())
	if strings.Contains(string(raw), "hunter22") {
		t.Fatalf("history leaks secret: %s", raw)
	}
}

func TestEnforceRotatesAtEntryCap(t *testing.T) {
	p := testPaths(t)
	if err := os.MkdirAll(p.StateDir, 0o750); err != nil {
		t.Fatal(err)
	}
	line := `{"time":"2026-01-01T00:00:00Z","action":"old","status":"ok","message":"` + strings.Repeat("m", 100) + `"}` + "\n"
	if err := os.WriteFile(p.HistoryFile(), []byte(strings.Repeat(line, MaxEntries)), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, Entry{Action: "new", Status: "ok"}); err != nil {
		t.Fatal(err)
	}
	live, err := List(p)
	if err != nil || len(live) != 1 || live[0].Action != "new" {
		t.Fatalf("live = %+v, %v", live, err)
	}
	all, err := ListAll(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != MaxEntries+1 || all[0].Action != "old" || all[len(all)-1].Action != "new" {
		t.Fatalf("ListAll len=%d first=%q last=%q", len(all), all[0].Action, all[len(all)-1].Action)
	}
}

func TestEnforceNoopWhenSmall(t *testing.T) {
	p := testPaths(t)
	_ = Append(p, Entry{Action: "a", Status: "ok"})
	rotated, err := Enforce(p)
	if err != nil || rotated {
		t.Fatalf("rotated=%v err=%v", rotated, err)
	}
	if len(Archives(p)) != 0 {
		t.Fatal("unexpected archives")
	}
}
