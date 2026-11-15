package pitr

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
)

func TestPruneWALUsesOldestBase(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	x := nodeexec.NewFake("local").
		OnOut("archiver=", "archiver=running\nbase=20260924T023000Z:100\nbase=20260917T023000Z:90\n").
		OnOut("-printf", "16777216\n16777216\n")
	m := &Manager{X: x, StateDir: "/var/lib/qabas", ConfigDir: t.TempDir(), Now: func() time.Time { return now }}
	res, err := m.PruneWAL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 || res.Bytes != 2*16777216 {
		t.Fatalf("got %+v", res)
	}
	want := time.Date(2026, 9, 17, 1, 30, 0, 0, time.UTC)
	if !res.Cutoff.Equal(want) {
		t.Fatalf("cutoff %s, want %s", res.Cutoff, want)
	}
	mins := int(now.Sub(want).Minutes())
	var script string
	for _, s := range x.Scripts {
		if strings.Contains(s, "-printf") {
			script = s
		}
	}
	for _, needle := range []string{"-mmin +" + strconv.Itoa(mins), "! -name '*.partial'", "! -name '*.history'"} {
		if !strings.Contains(script, needle) {
			t.Fatalf("script missing %q:\n%s", needle, script)
		}
	}
}

func TestPruneWALWithoutBaseKeepsEverything(t *testing.T) {
	x := nodeexec.NewFake("local").OnOut("archiver=", "archiver=running\n")
	m := &Manager{X: x, StateDir: "/var/lib/qabas", ConfigDir: t.TempDir()}
	res, err := m.PruneWAL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 0 || x.Ran("-delete") {
		t.Fatalf("expected no deletion, got %+v", res)
	}
}
