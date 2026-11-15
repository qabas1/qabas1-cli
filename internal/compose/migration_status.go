package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

// MigrationAttempt is one row in Prisma's database migration history.
type MigrationAttempt struct {
	Name         string  `json:"name"`
	StartedAt    string  `json:"startedAt"`
	FinishedAt   *string `json:"finishedAt"`
	RolledBackAt *string `json:"rolledBackAt"`
}

type MigrationStatusRow struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	LastAttempt string `json:"lastAttempt,omitempty"`
	InImage     bool   `json:"inImage"`
}

type MigrationStatusCounts struct {
	Applied    int `json:"applied"`
	Failed     int `json:"failed"`
	RolledBack int `json:"rolledBack"`
	Pending    int `json:"pending"`
}

type MigrationStatusReport struct {
	Release    string                `json:"release"`
	Counts     MigrationStatusCounts `json:"counts"`
	Migrations []MigrationStatusRow  `json:"migrations"`
}

const migrationHistorySQL = `SELECT coalesce(json_agg(json_build_object(
  'name', migration_name,
  'startedAt', to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
  'finishedAt', to_char(finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
  'rolledBackAt', to_char(rolled_back_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
) ORDER BY started_at, id), '[]'::json) FROM "_prisma_migrations";`

// MigrationHistory reads the live Prisma table without running migrations.
func (r *Runtime) MigrationHistory(p paths.Paths, env []string) ([]MigrationAttempt, error) {
	out, err := r.Exec(p, env, "postgres", "psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-U", "qabasone", "-d", "qabasone", "-c", migrationHistorySQL)
	if err != nil {
		return nil, fmt.Errorf("read Prisma migration history: %w", err)
	}
	var attempts []MigrationAttempt
	raw := strings.TrimSpace(out)
	if !strings.HasPrefix(raw, "[") {
		return nil, fmt.Errorf("parse Prisma migration history: expected a JSON array")
	}
	if err := json.Unmarshal([]byte(raw), &attempts); err != nil {
		return nil, fmt.Errorf("parse Prisma migration history: %w", err)
	}
	for _, attempt := range attempts {
		if !validMigrationName(attempt.Name) || attempt.StartedAt == "" {
			return nil, fmt.Errorf("Prisma migration history contains an invalid name or start time")
		}
	}
	return attempts, nil
}

const listImageMigrationsShell = `set -eu
if [ ! -d prisma/migrations ]; then
  echo 'prisma/migrations is missing from the installed migrate image' >&2
  exit 1
fi
for path in prisma/migrations/*; do
  [ -f "$path/migration.sql" ] || continue
  printf '%s\n' "${path##*/}"
done`

// ImageMigrations inspects the installed migrate image in an isolated, temporary
// container. --no-deps avoids starting services; no migration command is run.
func (r *Runtime) ImageMigrations(p paths.Paths, env []string) ([]string, error) {
	var stdout, stderr bytes.Buffer
	args := []string{"run", "--rm", "--no-deps", "--entrypoint", "sh", "migrate", "-ec", listImageMigrationsShell}
	if err := r.dockerOut(p, env, args, &stdout, &stderr); err != nil {
		return nil, fmt.Errorf("inspect installed migrate image: %w", err)
	}
	var names []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if !validMigrationName(name) || seen[name] {
			return nil, fmt.Errorf("installed migrate image returned an invalid or duplicate migration name %q", name)
		}
		seen[name] = true
		names = append(names, name)
	}
	return names, nil
}

// BuildMigrationStatus joins database attempts with migrations in the installed image.
func BuildMigrationStatus(release string, attempts []MigrationAttempt, imageNames []string) MigrationStatusReport {
	rows := map[string]*MigrationStatusRow{}
	for _, name := range imageNames {
		rows[name] = &MigrationStatusRow{Name: name, Status: "pending", InImage: true}
	}
	for _, attempt := range attempts {
		row := rows[attempt.Name]
		if row == nil {
			row = &MigrationStatusRow{Name: attempt.Name}
			rows[attempt.Name] = row
		}
		row.Attempts++
		if attempt.StartedAt > row.LastAttempt {
			row.LastAttempt = attempt.StartedAt
		}
		if attempt.FinishedAt != nil && attempt.RolledBackAt == nil {
			row.Status = "applied"
		} else if row.Status != "applied" && attempt.RolledBackAt == nil {
			row.Status = "failed"
		} else if row.Status != "applied" && row.Status != "failed" {
			row.Status = "rolled_back"
		}
	}
	report := MigrationStatusReport{Release: release, Migrations: make([]MigrationStatusRow, 0, len(rows))}
	for _, row := range rows {
		report.Migrations = append(report.Migrations, *row)
		switch row.Status {
		case "applied":
			report.Counts.Applied++
		case "failed":
			report.Counts.Failed++
		case "rolled_back":
			report.Counts.RolledBack++
		case "pending":
			report.Counts.Pending++
		}
	}
	sort.Slice(report.Migrations, func(i, j int) bool {
		return report.Migrations[i].Name < report.Migrations[j].Name
	})
	return report
}
