package backup

import (
	"testing"
	"time"
)

func scheduledAt(t time.Time) Meta {
	return Meta{ID: NewID(t), Name: ScheduledName, CreatedAt: t}
}

func ids(ms []Meta) map[string]bool {
	out := map[string]bool{}
	for _, m := range ms {
		out[m.ID] = true
	}
	return out
}

func TestSelectGFSUsesLocationDays(t *testing.T) {
	cairo, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	// Cairo is UTC+2 in January.
	a := scheduledAt(time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC))  // Cairo 15th 01:00
	b := scheduledAt(time.Date(2026, 1, 15, 21, 30, 0, 0, time.UTC)) // Cairo 15th 23:30
	c := scheduledAt(time.Date(2026, 1, 15, 23, 30, 0, 0, time.UTC)) // Cairo 16th 01:30
	metas := []Meta{a, b, c}

	_, drop := SelectGFS(metas, 2, 0, 0, cairo)
	if len(drop) != 1 || drop[0].ID != a.ID {
		t.Fatalf("Cairo drop = %v, want %s", drop, a.ID)
	}
	_, drop = SelectGFS(metas, 2, 0, 0, time.UTC)
	if len(drop) != 1 || drop[0].ID != b.ID {
		t.Fatalf("UTC drop = %v, want %s", drop, b.ID)
	}
}

func TestSelectGFSDailyWeeklyMonthly(t *testing.T) {
	// One backup per day at 01:30 UTC for 120 days ending Wed 2026-09-30.
	end := time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC)
	var metas []Meta
	for i := 0; i < 120; i++ {
		metas = append(metas, scheduledAt(end.AddDate(0, 0, -i)))
	}
	keep, drop := SelectGFS(metas, 7, 4, 6, time.UTC)
	if len(keep)+len(drop) != len(metas) {
		t.Fatalf("keep %d + drop %d != %d", len(keep), len(drop), len(metas))
	}
	k := ids(keep)
	for i := 0; i < 7; i++ {
		if !k[NewID(end.AddDate(0, 0, -i))] {
			t.Fatalf("daily backup %d days ago dropped", i)
		}
	}
	// Newest of each ISO week: Sundays before the current week, plus this week's newest.
	for _, d := range []string{"2026-09-27", "2026-09-20", "2026-09-13"} {
		day, _ := time.Parse("2006-01-02", d)
		if !k[NewID(day.Add(90*time.Minute))] {
			t.Fatalf("weekly backup %s dropped", d)
		}
	}
	// Newest of each month: last day of previous months.
	for _, d := range []string{"2026-08-31", "2026-07-31", "2026-06-30"} {
		day, _ := time.Parse("2006-01-02", d)
		if !k[NewID(day.Add(90*time.Minute))] {
			t.Fatalf("monthly backup %s dropped", d)
		}
	}
	// 120 days span 5 months (Jun..Sep) plus late May: at most 7+4+6 distinct keeps.
	if len(keep) > 17 {
		t.Fatalf("kept %d backups, want at most 17", len(keep))
	}
	if keep[0].ID != NewID(end) {
		t.Fatalf("newest not first: %s", keep[0].ID)
	}
}

func TestSelectGFSFewBackupsAndMultiplePerDay(t *testing.T) {
	base := time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC)
	metas := []Meta{scheduledAt(base), scheduledAt(base.Add(-time.Hour)), scheduledAt(base.AddDate(0, 0, -1))}
	keep, drop := SelectGFS(metas, 7, 4, 6, time.UTC)
	if len(drop) != 1 || drop[0].ID != NewID(base.Add(-time.Hour)) {
		t.Fatalf("drop = %v, want only the older same-day backup", drop)
	}
	if len(keep) != 2 {
		t.Fatalf("keep = %v", keep)
	}
}

func TestSelectGFSAlwaysKeepsNewest(t *testing.T) {
	base := time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC)
	keep, drop := SelectGFS([]Meta{scheduledAt(base), scheduledAt(base.AddDate(0, 0, -1))}, 0, 0, 0, time.UTC)
	if len(keep) != 1 || keep[0].ID != NewID(base) || len(drop) != 1 {
		t.Fatalf("keep=%v drop=%v", keep, drop)
	}
}

func TestOnlyScheduledIgnoresOtherBackups(t *testing.T) {
	base := time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC)
	metas := []Meta{
		scheduledAt(base),
		{ID: "qb-manual", Name: "manual_20260930-013000", CreatedAt: base},
		{ID: "qb-predeploy", Name: "pre-deploy", CreatedAt: base},
		{ID: "qb-prerestore", Name: "pre-restore", CreatedAt: base},
		{ID: "qb-unnamed", CreatedAt: base},
	}
	got := OnlyScheduled(metas)
	if len(got) != 1 || got[0].Name != ScheduledName {
		t.Fatalf("OnlyScheduled = %v", got)
	}
}
