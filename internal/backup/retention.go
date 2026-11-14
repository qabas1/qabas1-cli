package backup

import (
	"fmt"
	"sort"
	"time"
)

// ScheduledName labels backups created by `qabas backup schedule run`. Retention only
// ever considers backups with this name.
const ScheduledName = "scheduled"

// OnlyScheduled returns the scheduled backups in metas.
func OnlyScheduled(metas []Meta) []Meta {
	out := make([]Meta, 0, len(metas))
	for _, m := range metas {
		if m.Name == ScheduledName {
			out = append(out, m)
		}
	}
	return out
}

// SelectGFS applies grandfather-father-son retention: keep the newest backup of each of
// the `daily` most recent days, `weekly` most recent ISO weeks and `monthly` most recent
// months that have backups. The newest backup is always kept. Buckets use loc.
// keep and drop are returned newest first.
func SelectGFS(metas []Meta, daily, weekly, monthly int, loc *time.Location) (keep, drop []Meta) {
	if loc == nil {
		loc = time.Local
	}
	sorted := append([]Meta(nil), metas...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })

	kept := map[string]bool{}
	if len(sorted) > 0 {
		kept[sorted[0].ID] = true
	}
	bucket := func(n int, key func(time.Time) string) {
		seen := map[string]bool{}
		for _, m := range sorted {
			if len(seen) >= n {
				return
			}
			k := key(m.CreatedAt.In(loc))
			if seen[k] {
				continue
			}
			seen[k] = true
			kept[m.ID] = true
		}
	}
	bucket(daily, func(t time.Time) string { return t.Format("2006-01-02") })
	bucket(weekly, func(t time.Time) string {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	})
	bucket(monthly, func(t time.Time) string { return t.Format("2006-01") })

	for _, m := range sorted {
		if kept[m.ID] {
			keep = append(keep, m)
		} else {
			drop = append(drop, m)
		}
	}
	return keep, drop
}
