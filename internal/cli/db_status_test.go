package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/spf13/cobra"
)

func TestDBMigrateStatusCommand(t *testing.T) {
	root := NewRoot()
	cmd, _, err := root.Find([]string{"db", "migrate", "status"})
	if err != nil || cmd.Name() != "status" {
		t.Fatalf("status command: %v, %v", cmd, err)
	}
	apply, _, err := root.Find([]string{"db", "migrations"})
	if err != nil || apply.Name() != "migrate" {
		t.Fatalf("existing migrations alias changed: %v, %v", apply, err)
	}
}

func TestPrintMigrationStatus(t *testing.T) {
	report := compose.MigrationStatusReport{
		Release: "dev.1",
		Counts:  compose.MigrationStatusCounts{Applied: 1, Pending: 1},
		Migrations: []compose.MigrationStatusRow{
			{Name: "20261020_applied", Status: "applied", Attempts: 1, LastAttempt: "2026-10-20T10:00:00.000Z", InImage: true},
			{Name: "20261021_pending", Status: "pending", InImage: true},
		},
	}
	for _, jsonMode := range []bool{false, true} {
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		if err := printMigrationStatus(cmd, &flags{JSON: jsonMode}, report); err != nil {
			t.Fatal(err)
		}
		if jsonMode {
			var parsed compose.MigrationStatusReport
			if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if parsed.Release != report.Release || len(parsed.Migrations) != 2 || parsed.Migrations[1].Status != "pending" {
				t.Fatalf("JSON report: %+v", parsed)
			}
		} else if text := out.String(); !strings.Contains(text, "MIGRATION") || !strings.Contains(text, "20261021_pending  pending") || !strings.Contains(text, "1 applied, 0 failed, 0 rolled back, 1 pending") {
			t.Fatalf("table output: %s", text)
		}
	}
}
