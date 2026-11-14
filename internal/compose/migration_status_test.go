package compose

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

func TestBuildMigrationStatus(t *testing.T) {
	date1 := "2026-10-20T10:00:00.000Z"
	date2 := "2026-10-21T10:00:00.000Z"
	attempts := []MigrationAttempt{
		{Name: "20261020_applied", StartedAt: date1, FinishedAt: &date1},
		{Name: "20261021_failed", StartedAt: date1},
		{Name: "20261021_failed", StartedAt: date2},
		{Name: "20261022_rolled_back", StartedAt: date2, RolledBackAt: &date2},
		{Name: "20261023_database_only", StartedAt: date2, FinishedAt: &date2},
	}
	report := BuildMigrationStatus("dev.1", attempts, []string{
		"20261024_pending", "20261022_rolled_back", "20261021_failed", "20261020_applied",
	})
	if report.Release != "dev.1" || report.Counts != (MigrationStatusCounts{Applied: 2, Failed: 1, RolledBack: 1, Pending: 1}) {
		t.Fatalf("report header: %+v", report)
	}
	wantNames := []string{"20261020_applied", "20261021_failed", "20261022_rolled_back", "20261023_database_only", "20261024_pending"}
	wantStatuses := []string{"applied", "failed", "rolled_back", "applied", "pending"}
	for i, row := range report.Migrations {
		if row.Name != wantNames[i] || row.Status != wantStatuses[i] {
			t.Fatalf("row %d: %+v", i, row)
		}
	}
	if report.Migrations[1].Attempts != 2 || report.Migrations[1].LastAttempt != date2 {
		t.Fatalf("repeated attempt: %+v", report.Migrations[1])
	}
	if report.Migrations[3].InImage || report.Migrations[4].Attempts != 0 {
		t.Fatalf("database-only/pending rows: %+v", report.Migrations)
	}
}

func TestMigrationStatusReadsOnly(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	var calls []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, stderr *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			calls = append(calls, joined)
			switch {
			case strings.Contains(joined, "exec -T postgres psql"):
				stdout.WriteString(`[{"name":"20261020_applied","startedAt":"2026-10-20T10:00:00.000Z","finishedAt":"2026-10-20T10:01:00.000Z","rolledBackAt":null}]`)
			case strings.Contains(joined, "run --rm --no-deps --entrypoint sh migrate -ec"):
				stdout.WriteString("20261020_applied\n20261021_pending\n")
			default:
				t.Fatalf("unexpected docker call: %s", joined)
			}
			return nil
		},
	}
	attempts, err := rt.MigrationHistory(p, nil)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("history: %+v, %v", attempts, err)
	}
	names, err := rt.ImageMigrations(p, nil)
	if err != nil || len(names) != 2 {
		t.Fatalf("image: %+v, %v", names, err)
	}
	for _, call := range calls {
		if strings.Contains(call, "migrate deploy") || strings.Contains(call, "migrate resolve") || strings.Contains(call, "--remove-orphans") {
			t.Fatalf("status invoked mutating command: %s", call)
		}
	}
}

func TestMigrationStatusSourceErrors(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	for _, tc := range []struct {
		name   string
		stdout string
		runErr error
		image  bool
	}{
		{name: "database unavailable", runErr: errors.New("connection refused")},
		{name: "migration table missing", runErr: errors.New("relation _prisma_migrations does not exist")},
		{name: "invalid database output", stdout: "not json"},
		{name: "image unavailable", runErr: errors.New("image not found"), image: true},
		{name: "image listing invalid", stdout: "../bad\n", image: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &Runtime{
				LookPath: func(string) (string, error) { return "docker", nil },
				Run: func(_ string, _ []string, _ []string, stdout, stderr *bytes.Buffer) error {
					stdout.WriteString(tc.stdout)
					return tc.runErr
				},
			}
			var err error
			if tc.image {
				_, err = rt.ImageMigrations(p, nil)
			} else {
				_, err = rt.MigrationHistory(p, nil)
			}
			if err == nil {
				t.Fatal("expected source error")
			}
		})
	}
}
