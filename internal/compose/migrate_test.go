package compose

import "testing"

func TestParseFailedMigrationName(t *testing.T) {
	p3009 := `
Error: P3009

migrate found failed migrations in the target database, new migrations will not be applied.
The ` + "`20260912040000_workday_open_per_party_unique`" + ` migration started at 2026-09-12 01:46:49.792104 UTC failed
`
	if got := ParseFailedMigrationName(p3009); got != "20260912040000_workday_open_per_party_unique" {
		t.Fatalf("P3009 name = %q", got)
	}

	p3018 := `
Error: P3018
A migration failed to apply.
Migration name: 20260912040000_workday_open_per_party_unique

Database error code: 42P07
ERROR: relation "driver_days_open_per_party_unique" already exists
`
	if got := ParseFailedMigrationName(p3018); got != "20260912040000_workday_open_per_party_unique" {
		t.Fatalf("P3018 name = %q", got)
	}
}

func TestIsAlreadyExistsMigrationFailure(t *testing.T) {
	if !IsAlreadyExistsMigrationFailure(`ERROR: relation "driver_days_open_per_party_unique" already exists`) {
		t.Fatal("expected already-exists detection")
	}
	if !IsAlreadyExistsMigrationFailure("Database error code: 42P07") {
		t.Fatal("expected 42P07 detection")
	}
	if IsAlreadyExistsMigrationFailure("syntax error at or near CREATE") {
		t.Fatal("must not treat unrelated SQL errors as already-exists")
	}
}

func TestIsPrismaFailedMigrationError(t *testing.T) {
	if !IsPrismaFailedMigrationError("Error: P3009\nmigrate found failed migrations") {
		t.Fatal("expected P3009")
	}
	if !IsPrismaFailedMigrationError("Error: P3018\nA migration failed to apply") {
		t.Fatal("expected P3018")
	}
	if IsPrismaFailedMigrationError("connection refused") {
		t.Fatal("must not treat network errors as failed-migration")
	}
}
