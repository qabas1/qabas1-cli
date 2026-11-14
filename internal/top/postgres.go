package top

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/pitr"
	"github.com/qabasone/qabasone-cli/internal/redact"
)

// MaxQueries is how many non-idle backends the Postgres pane keeps.
const MaxQueries = 10

const queryTextMax = 200

const pgTimeFmt = `'YYYY-MM-DD"T"HH24:MI:SS"Z"'`

// postgresSQL returns one overview row tagged "o" and up to MaxQueries rows tagged "q",
// in a single psql round trip.
var postgresSQL = `SELECT 'o',
  pg_is_in_recovery(),
  (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'),
  current_setting('max_connections'),
  (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend' AND state = 'active'),
  (SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'),
  (SELECT COALESCE(sum(xact_commit + xact_rollback), 0) FROM pg_stat_database),
  (SELECT COALESCE(sum(blks_hit), 0) FROM pg_stat_database),
  (SELECT COALESCE(sum(blks_read), 0) FROM pg_stat_database),
  (SELECT COALESCE(sum(deadlocks), 0) FROM pg_stat_database),
  archived_count, failed_count,
  COALESCE(to_char(last_archived_time AT TIME ZONE 'UTC', ` + pgTimeFmt + `), ''),
  COALESCE(to_char(last_failed_time AT TIME ZONE 'UTC', ` + pgTimeFmt + `), ''),
  current_setting('archive_mode')
FROM pg_stat_archiver;
SELECT 'q', pid, COALESCE(usename, ''), COALESCE(state, ''),
  COALESCE(wait_event_type || ':' || wait_event, ''),
  COALESCE(EXTRACT(EPOCH FROM now() - COALESCE(query_start, backend_start)), 0),
  regexp_replace(left(query, 400), '[[:space:]|]+', ' ', 'g')
FROM pg_stat_activity
WHERE backend_type = 'client backend' AND state IS DISTINCT FROM 'idle' AND pid <> pg_backend_pid()
ORDER BY COALESCE(query_start, backend_start) NULLS LAST
LIMIT ` + strconv.Itoa(MaxQueries) + `;`

func collectPostgres(ctx context.Context, x nodeexec.Executor) *PostgresSection {
	rows, err := pgctl.Query(ctx, x, postgresSQL)
	if err != nil {
		return &PostgresSection{Err: redact.Error(err)}
	}
	return parsePostgres(rows)
}

func parsePostgres(rows [][]string) *PostgresSection {
	pg := &PostgresSection{}
	seen := false
	for _, r := range rows {
		switch {
		case len(r) >= 15 && r[0] == "o":
			seen = true
			pg.InRecovery = r[1] == "t"
			pg.Connections = atoi(r[2])
			pg.MaxConnections = atoi(r[3])
			pg.Active = atoi(r[4])
			pg.LockWaits = atoi(r[5])
			pg.Xacts = atou(r[6])
			pg.BlksHit = atou(r[7])
			pg.BlksRead = atou(r[8])
			pg.Deadlocks = atou(r[9])
			pg.ArchivedCount = atou(r[10])
			pg.ArchiveFailed = atou(r[11])
			pg.LastArchivedAt = parseTime(r[12])
			pg.LastFailedAt = parseTime(r[13])
			pg.ArchiveMode = strings.TrimSpace(r[14])
		case len(r) >= 7 && r[0] == "q":
			dur, _ := strconv.ParseFloat(r[5], 64)
			pg.Queries = append(pg.Queries, Query{
				PID: atoi(r[1]), User: r[2], State: r[3], Wait: r[4], DurationSec: dur,
				Text: SanitizeQuery(strings.Join(r[6:], " ")),
			})
		}
	}
	if !seen {
		pg.Err = "unexpected postgres output"
		return pg
	}
	if total := pg.BlksHit + pg.BlksRead; total > 0 {
		pg.CacheHitPercent = float64(pg.BlksHit) / float64(total) * 100
	}
	sort.SliceStable(pg.Queries, func(i, j int) bool { return pg.Queries[i].DurationSec > pg.Queries[j].DurationSec })
	return pg
}

var sqlLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)

// SanitizeQuery masks string literals (business data) and secrets, then truncates.
func SanitizeQuery(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	q = redact.String(sqlLiteral.ReplaceAllString(q, "'?'"))
	if utf8.RuneCountInString(q) > queryTextMax {
		q = string([]rune(q)[:queryTextMax-1]) + "…"
	}
	return q
}

func collectReplicas(ctx context.Context, x nodeexec.Executor) *ReplicasSection {
	st, err := pgctl.GetStatus(ctx, x)
	if err != nil {
		return &ReplicasSection{Err: redact.Error(err)}
	}
	if st.InRecovery {
		return &ReplicasSection{Standby: true, LagSeconds: st.LagSeconds, Upstream: st.WalReceiver}
	}
	repl, err := pgctl.Replication(ctx, x)
	if err != nil {
		return &ReplicasSection{Err: redact.Error(err)}
	}
	slots, err := pgctl.Slots(ctx, x)
	if err != nil {
		return &ReplicasSection{Err: redact.Error(err)}
	}
	rows, archive := mergeReplicas(st.CurrentLSN, repl, slots)
	return &ReplicasSection{Rows: rows, Archive: archive}
}

// receiveWALApp is pg_receivewal's default application_name.
const receiveWALApp = "pg_receivewal"

// mergeReplicas joins pg_stat_replication rows with their physical slots, by the
// slot's active_pid first and then by name (qabas_<node> holds qabas_replica_<node>).
// The PITR archive (slot pitr.Slot, or a slotless pg_receivewal) is returned
// separately: it never replays WAL, so replay lag means nothing for it. Slots with
// no matching connection show as streaming when active, otherwise disconnected.
func mergeReplicas(currentLSN string, repl []pgctl.ReplicationRow, slots []pgctl.SlotRow) ([]Replica, *WALArchive) {
	byPID := map[int]pgctl.SlotRow{}
	byName := map[string]pgctl.SlotRow{}
	for _, s := range slots {
		if s.ActivePID > 0 {
			byPID[s.ActivePID] = s
		}
		byName[s.Name] = s
	}
	slotFor := func(r pgctl.ReplicationRow) (pgctl.SlotRow, bool) {
		if s, ok := byPID[r.PID]; ok && r.PID > 0 {
			return s, true
		}
		s, ok := byName["qabas_replica_"+strings.TrimPrefix(r.Application, "qabas_")]
		return s, ok
	}

	var archive *WALArchive
	if s, ok := byName[pitr.Slot]; ok {
		archive = &WALArchive{Slot: s.Name, SlotActive: s.Active, RetainedBytes: s.RetainedBytes, WALStatus: s.WALStatus}
	}
	var out []Replica
	used := map[string]bool{pitr.Slot: true}
	for _, r := range repl {
		s, hasSlot := slotFor(r)
		if (hasSlot && s.Name == pitr.Slot) || (!hasSlot && r.Application == receiveWALApp) {
			if archive == nil {
				archive = &WALArchive{}
			}
			archive.Connected = true
			archive.ClientAddr = r.ClientAddr
			archive.LagBytes = lsnDiff(currentLSN, r.WriteLSN)
			archive.LagSeconds = r.WriteLag
			continue
		}
		rep := Replica{Name: strings.TrimPrefix(r.Application, "qabas_"), ClientAddr: r.ClientAddr, State: r.State,
			SyncState: r.SyncState, LagSeconds: r.ReplayLag, LagBytes: lsnDiff(currentLSN, r.ReplayLSN)}
		if hasSlot {
			used[s.Name] = true
			rep.Slot, rep.SlotActive, rep.RetainedBytes, rep.WALStatus = s.Name, s.Active, s.RetainedBytes, s.WALStatus
		}
		out = append(out, rep)
	}
	for _, s := range slots {
		if used[s.Name] {
			continue
		}
		state := "disconnected"
		if s.Active {
			state = "streaming"
		}
		out = append(out, Replica{
			Name: strings.TrimPrefix(s.Name, "qabas_replica_"), State: state,
			Slot: s.Name, SlotActive: s.Active, RetainedBytes: s.RetainedBytes, WALStatus: s.WALStatus,
		})
	}
	return out, archive
}

func lsnDiff(a, b string) int64 {
	x, err1 := pgctl.ParseLSN(a)
	y, err2 := pgctl.ParseLSN(b)
	if err1 != nil || err2 != nil || x < y {
		return 0
	}
	return int64(x - y)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atou(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, strings.TrimSpace(s))
	return t
}
