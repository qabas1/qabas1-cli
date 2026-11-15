// Package pgctl drives PostgreSQL physical replication inside the QabasOne compose
// stack (postgres container) through a nodeexec.Executor. SQL is always sent on stdin,
// never on argv, so passwords never appear in process listings or logs.
package pgctl

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/redact"
)

const (
	Container       = "qabasone-postgres"
	DataVolumeKey   = "qabasone_postgres_data"
	DataDir         = "/var/lib/postgresql/data"
	ReplicationRole = "qabas_replica"
	BackupRole      = "qabas_backup"
	// DemotedMarker blocks a former primary from booting writable (see compose overlays).
	DemotedMarker = "qabas.DEMOTED"
)

// psqlScript runs SQL from stdin inside the postgres container as the owner role.
const psqlScript = `docker exec -i ` + Container + ` sh -c 'exec psql -X -q -v ON_ERROR_STOP=1 -At -F "|" -U "$POSTGRES_USER" -d "$POSTGRES_DB"'`

// Query runs sql and returns rows split on "|".
func Query(ctx context.Context, x nodeexec.Executor, sql string) ([][]string, error) {
	res, err := x.Run(ctx, psqlScript, strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, &nodeexec.ScriptError{Node: x.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "|"))
	}
	return rows, nil
}

// Exec runs sql and returns the raw result (callers inspect ExitCode/Stderr).
func Exec(ctx context.Context, x nodeexec.Executor, sql string) (nodeexec.Result, error) {
	return x.Run(ctx, psqlScript, strings.NewReader(sql))
}

// Literal quotes s as a SQL string literal.
func Literal(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Ident quotes s as a SQL identifier.
func Ident(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// Status is the node-local database role and replay position.
type Status struct {
	InRecovery    bool    `json:"inRecovery"`
	TxReadOnly    bool    `json:"transactionReadOnly"`
	ServerVersion int     `json:"serverVersionNum"`
	Timeline      int     `json:"timeline"`
	CurrentLSN    string  `json:"currentLSN,omitempty"`
	ReceiveLSN    string  `json:"receiveLSN,omitempty"`
	ReplayLSN     string  `json:"replayLSN,omitempty"`
	LastReplayAt  string  `json:"lastReplayAt,omitempty"`
	LagSeconds    float64 `json:"lagSeconds"`
	WalReceiver   string  `json:"walReceiver,omitempty"`
	SystemID      string  `json:"systemIdentifier"`
}

const statusSQL = `SELECT
  pg_is_in_recovery(),
  current_setting('default_transaction_read_only'),
  current_setting('server_version_num'),
  (SELECT timeline_id FROM pg_control_checkpoint()),
  CASE WHEN pg_is_in_recovery() THEN '' ELSE pg_current_wal_lsn()::text END,
  COALESCE(pg_last_wal_receive_lsn()::text, ''),
  COALESCE(pg_last_wal_replay_lsn()::text, ''),
  COALESCE(to_char(pg_last_xact_replay_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), ''),
  CASE
    WHEN NOT pg_is_in_recovery() THEN 0
    WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0
    ELSE COALESCE(EXTRACT(EPOCH FROM now() - pg_last_xact_replay_timestamp()), 0)
  END,
  COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1), ''),
  (SELECT system_identifier::text FROM pg_control_system());`

func GetStatus(ctx context.Context, x nodeexec.Executor) (Status, error) {
	rows, err := Query(ctx, x, statusSQL)
	if err != nil {
		return Status{}, err
	}
	if len(rows) != 1 || len(rows[0]) < 11 {
		return Status{}, fmt.Errorf("%s: unexpected status output", x.Node())
	}
	return parseStatus(rows[0]), nil
}

func parseStatus(r []string) Status {
	ver, _ := strconv.Atoi(r[2])
	tl, _ := strconv.Atoi(r[3])
	lag, _ := strconv.ParseFloat(r[8], 64)
	return Status{
		InRecovery:    r[0] == "t",
		TxReadOnly:    r[1] == "on",
		ServerVersion: ver,
		Timeline:      tl,
		CurrentLSN:    r[4],
		ReceiveLSN:    r[5],
		ReplayLSN:     r[6],
		LastReplayAt:  r[7],
		LagSeconds:    lag,
		WalReceiver:   r[9],
		SystemID:      r[10],
	}
}

// Position is the LSN a standby has made durable/visible; primaries report current LSN.
func (s Status) Position() string {
	if !s.InRecovery {
		return s.CurrentLSN
	}
	return s.ReplayLSN
}

// ReplicationRow is one pg_stat_replication entry on an upstream.
type ReplicationRow struct {
	Application string  `json:"applicationName"`
	ClientAddr  string  `json:"clientAddr"`
	State       string  `json:"state"`
	SentLSN     string  `json:"sentLSN"`
	ReplayLSN   string  `json:"replayLSN"`
	ReplayLag   float64 `json:"replayLagSeconds"`
	SyncState   string  `json:"syncState"`
	PID         int     `json:"pid"`
	WriteLSN    string  `json:"writeLSN"`
	WriteLag    float64 `json:"writeLagSeconds"`
}

// SlotRow is one physical replication slot.
type SlotRow struct {
	Name          string `json:"name"`
	Active        bool   `json:"active"`
	RestartLSN    string `json:"restartLSN"`
	WALStatus     string `json:"walStatus"`
	RetainedBytes int64  `json:"retainedBytes"`
	SafeWALBytes  int64  `json:"safeWALBytes"`
	ActivePID     int    `json:"activePID,omitempty"`
}

func Replication(ctx context.Context, x nodeexec.Executor) ([]ReplicationRow, error) {
	rows, err := Query(ctx, x, `SELECT application_name, COALESCE(host(client_addr),''), state,
  COALESCE(sent_lsn::text,''), COALESCE(replay_lsn::text,''),
  COALESCE(EXTRACT(EPOCH FROM replay_lag), 0), sync_state,
  pid, COALESCE(write_lsn::text,''), COALESCE(EXTRACT(EPOCH FROM write_lag), 0)
FROM pg_stat_replication ORDER BY application_name;`)
	if err != nil {
		return nil, err
	}
	out := make([]ReplicationRow, 0, len(rows))
	for _, r := range rows {
		if len(r) < 7 {
			continue
		}
		row := ReplicationRow{
			Application: r[0],
			ClientAddr:  r[1],
			State:       r[2],
			SentLSN:     r[3],
			ReplayLSN:   r[4],
			SyncState:   r[6],
		}
		row.ReplayLag, _ = strconv.ParseFloat(r[5], 64)
		if len(r) >= 10 {
			row.PID, _ = strconv.Atoi(r[7])
			row.WriteLSN = r[8]
			row.WriteLag, _ = strconv.ParseFloat(r[9], 64)
		}
		out = append(out, row)
	}
	return out, nil
}

func Slots(ctx context.Context, x nodeexec.Executor) ([]SlotRow, error) {
	rows, err := Query(ctx, x, `SELECT slot_name, active, COALESCE(restart_lsn::text,''), COALESCE(wal_status,''),
  CASE WHEN pg_is_in_recovery() OR restart_lsn IS NULL THEN 0 ELSE pg_wal_lsn_diff(pg_current_wal_lsn(), restart_lsn)::bigint END,
  COALESCE(safe_wal_size, -1), COALESCE(active_pid, 0)
FROM pg_replication_slots WHERE slot_type = 'physical' ORDER BY slot_name;`)
	if err != nil {
		return nil, err
	}
	out := make([]SlotRow, 0, len(rows))
	for _, r := range rows {
		if len(r) < 6 {
			continue
		}
		s := SlotRow{Name: r[0], Active: r[1] == "t", RestartLSN: r[2], WALStatus: r[3]}
		s.RetainedBytes, _ = strconv.ParseInt(r[4], 10, 64)
		s.SafeWALBytes, _ = strconv.ParseInt(r[5], 10, 64)
		if len(r) >= 7 {
			s.ActivePID, _ = strconv.Atoi(r[6])
		}
		out = append(out, s)
	}
	return out, nil
}

// EnsureRoles creates/updates the replication and backup roles (REPLICATION only, no
// superuser, no data access) with the given passwords.
func EnsureRoles(ctx context.Context, x nodeexec.Executor, replicationPassword, backupPassword string) error {
	redact.Register(replicationPassword, backupPassword)
	sql := fmt.Sprintf(`DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %[1]s) THEN
    EXECUTE format('CREATE ROLE %%I WITH LOGIN REPLICATION NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %%L', %[1]s, %[2]s);
  ELSE
    EXECUTE format('ALTER ROLE %%I WITH LOGIN REPLICATION NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %%L', %[1]s, %[2]s);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = %[3]s) THEN
    EXECUTE format('CREATE ROLE %%I WITH LOGIN REPLICATION NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %%L', %[3]s, %[4]s);
  ELSE
    EXECUTE format('ALTER ROLE %%I WITH LOGIN REPLICATION NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD %%L', %[3]s, %[4]s);
  END IF;
END $$;
-- pg_rewind as a non-superuser (failback of a former primary).
GRANT EXECUTE ON FUNCTION pg_catalog.pg_ls_dir(text, boolean, boolean) TO `+ReplicationRole+`;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_stat_file(text, boolean) TO `+ReplicationRole+`;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_read_binary_file(text) TO `+ReplicationRole+`;
GRANT EXECUTE ON FUNCTION pg_catalog.pg_read_binary_file(text, bigint, bigint, boolean) TO `+ReplicationRole+`;`, Literal(ReplicationRole), Literal(replicationPassword), Literal(BackupRole), Literal(backupPassword))
	res, err := Exec(ctx, x, sql)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &nodeexec.ScriptError{Node: x.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return nil
}

// EnsureSlot creates a physical slot if missing (idempotent).
func EnsureSlot(ctx context.Context, x nodeexec.Executor, slot string) error {
	_, err := Query(ctx, x, fmt.Sprintf(`SELECT pg_create_physical_replication_slot(%[1]s, true)
WHERE NOT EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name = %[1]s);`, Literal(slot)))
	return err
}

// DropSlot removes a slot if present and inactive.
func DropSlot(ctx context.Context, x nodeexec.Executor, slot string) error {
	_, err := Query(ctx, x, fmt.Sprintf(`SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots
WHERE slot_name = %s AND NOT active;`, Literal(slot)))
	return err
}

// ProbeWriteRejected proves the engine refuses writes. Returns nil only when PostgreSQL
// rejected a DDL inside a rolled-back transaction with a read-only error.
func ProbeWriteRejected(ctx context.Context, x nodeexec.Executor) error {
	res, err := Exec(ctx, x, "BEGIN;\nCREATE TABLE qabas_verify_write_probe (id int);\nROLLBACK;\n")
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return fmt.Errorf("%s ACCEPTED a write (CREATE TABLE); this node is NOT read-only", x.Node())
	}
	if !IsReadOnlyError(res.Stderr) {
		return fmt.Errorf("%s: write probe failed for an unexpected reason: %s", x.Node(), redact.String(strings.TrimSpace(res.Stderr)))
	}
	return nil
}

// IsReadOnlyError matches SQLSTATE 25006 messages.
func IsReadOnlyError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "read-only transaction") || strings.Contains(m, "recovery is in progress") ||
		strings.Contains(m, "25006")
}

// Promote runs pg_promote(wait => true) and confirms the node left recovery.
func Promote(ctx context.Context, x nodeexec.Executor) error {
	rows, err := Query(ctx, x, "SELECT pg_promote(true, 120);")
	if err != nil {
		return err
	}
	if len(rows) == 0 || len(rows[0]) == 0 || rows[0][0] != "t" {
		return fmt.Errorf("%s: pg_promote did not complete", x.Node())
	}
	st, err := GetStatus(ctx, x)
	if err != nil {
		return err
	}
	if st.InRecovery {
		return fmt.Errorf("%s: still in recovery after pg_promote", x.Node())
	}
	return nil
}

// SetDefaultReadOnly toggles default_transaction_read_only cluster-wide (fence / unfence)
// and, when fencing, terminates application sessions so no open transaction keeps writing.
func SetDefaultReadOnly(ctx context.Context, x nodeexec.Executor, on bool) error {
	val := "off"
	if on {
		val = "on"
	}
	sql := fmt.Sprintf("ALTER SYSTEM SET default_transaction_read_only = %s;\nSELECT pg_reload_conf();\n", val)
	if on {
		sql += `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
WHERE backend_type = 'client backend' AND pid <> pg_backend_pid()
  AND usename NOT IN ('` + ReplicationRole + `','` + BackupRole + `');` + "\n"
	}
	_, err := Query(ctx, x, sql)
	return err
}

// ParseLSN converts "X/Y" to a comparable integer.
func ParseLSN(lsn string) (uint64, error) {
	parts := strings.SplitN(strings.TrimSpace(lsn), "/", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid LSN %q", lsn)
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q", lsn)
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q", lsn)
	}
	return hi<<32 | lo, nil
}

// CompareLSN returns -1, 0, 1. Unparseable LSNs sort first.
func CompareLSN(a, b string) int {
	x, errA := ParseLSN(a)
	y, errB := ParseLSN(b)
	switch {
	case errA != nil && errB != nil:
		return 0
	case errA != nil:
		return -1
	case errB != nil:
		return 1
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// WaitReplayed polls until the standby replayed at least target.
func WaitReplayed(ctx context.Context, x nodeexec.Executor, target string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		st, err := GetStatus(ctx, x)
		if err == nil && CompareLSN(st.ReplayLSN, target) >= 0 {
			return nil
		}
		if time.Now().After(deadline) {
			got := ""
			if err == nil {
				got = st.ReplayLSN
			}
			return fmt.Errorf("%s did not replay %s within %s (at %s)", x.Node(), target, timeout, got)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// HBAEntry is one replication peer allowed in pg_hba.conf.
type HBAEntry struct {
	Node string
	CIDR string
}

// RenderHBA renders a pg_hba.conf: local socket peer/scram, compose-network app access
// (scram), and replication from explicitly listed WireGuard peers or the
// PostgreSQL Docker bridge gateway used by docker-proxy. No trust, no md5.
func RenderHBA(dockerNetworks []string, peers []HBAEntry) string {
	return RenderHBAWithGateways(dockerNetworks, nil, peers)
}

func RenderHBAWithGateways(dockerNetworks, gateways []string, peers []HBAEntry) string {
	var b strings.Builder
	b.WriteString("# Managed by qabas. scram-sha-256 only; replication from WireGuard peers or the PostgreSQL Docker gateway.\n")
	b.WriteString("# TYPE  DATABASE     USER            ADDRESS            METHOD\n")
	// Unix socket is only reachable inside the container (docker exec psql, backups).
	b.WriteString("local   all          all                                trust\n")
	b.WriteString("host    all          all             127.0.0.1/32       scram-sha-256\n")
	for _, n := range dockerNetworks {
		fmt.Fprintf(&b, "host    all          all             %-18s scram-sha-256\n", n)
		fmt.Fprintf(&b, "host    replication  %-15s %-18s scram-sha-256\n", BackupRole, n)
	}
	// Docker's published WireGuard port may be served by docker-proxy. In that
	// case PostgreSQL sees the bridge gateway, not the remote WireGuard address.
	// Restrict this exception to that gateway, TLS, and the replication role.
	for _, gateway := range gateways {
		fmt.Fprintf(&b, "hostssl replication  %-15s %-18s scram-sha-256\n", ReplicationRole, gateway+"/32")
	}
	for _, p := range peers {
		fmt.Fprintf(&b, "# replica %s\n", p.Node)
		fmt.Fprintf(&b, "hostssl replication  %-15s %-18s scram-sha-256\n", ReplicationRole, p.CIDR)
		fmt.Fprintf(&b, "hostssl postgres     %-15s %-18s scram-sha-256\n", ReplicationRole, p.CIDR)
	}
	b.WriteString("host    all          all             0.0.0.0/0          reject\n")
	b.WriteString("host    replication  all             0.0.0.0/0          reject\n")
	return b.String()
}

// BaseBackupScript seeds a standby's data volume from its upstream with pg_basebackup
// -R over WireGuard, using the same postgres image digest. The password is read from a
// 0600 env file, never argv. When wipe is false and the volume already holds a cluster,
// the script refuses (resume must not destroy data silently).
func BaseBackupScript(image, volume, upstreamIP, slot, appName, envFile string, wipe bool) string {
	wipeCmd := `if [ -n "$(ls -A /v 2>/dev/null)" ]; then echo "data volume is not empty (use --rebuild)" >&2; exit 65; fi`
	if wipe {
		wipeCmd = `find /v -mindepth 1 -delete`
	}
	conn := fmt.Sprintf("host=%s port=5432 user=%s application_name=%s sslmode=require", upstreamIP, ReplicationRole, appName)
	return fmt.Sprintf(`set -eu
docker volume create %[2]s >/dev/null
docker run --rm --user 0 -v %[2]s:/v --entrypoint sh %[1]s -c %[3]s
docker run --rm --user 0 -v %[2]s:/v --entrypoint sh %[1]s -c 'chown postgres:postgres /v && chmod 0700 /v'
docker run --rm --user postgres --network host --env-file %[4]s -v %[2]s:%[5]s --entrypoint pg_basebackup %[1]s \
  -d %[6]s -D %[5]s -X stream -R -S %[7]s -c fast -P -v
docker run --rm --user postgres -v %[2]s:%[5]s --entrypoint sh %[1]s -c 'test -f %[5]s/standby.signal'
`, image, volume, nodeexec.Quote(wipeCmd), nodeexec.Quote(envFile), DataDir, nodeexec.Quote(conn), nodeexec.Quote(slot))
}

// MarkDemotedScript writes the DEMOTED marker into a (stopped or running) data volume.
func MarkDemotedScript(image, volume string) string {
	return fmt.Sprintf(`set -eu
docker run --rm --user 0 -v %[2]s:/v --entrypoint sh %[1]s -c 'date -u +%%FT%%TZ > /v/%[3]s && chown postgres:postgres /v/%[3]s'
`, image, volume, DemotedMarker)
}

// ConninfoScript appends an explicit primary_conninfo/primary_slot_name to
// postgresql.auto.conf in a stopped data volume (later lines win). The full conninfo,
// including the password, arrives on stdin so it never touches argv.
func ConninfoScript(image, volume string) string {
	return fmt.Sprintf(`set -eu
docker run --rm -i --user postgres -v %[2]s:%[3]s --entrypoint sh %[1]s -c 'cat >> %[3]s/postgresql.auto.conf && chmod 0600 %[3]s/postgresql.auto.conf'
`, image, volume, DataDir)
}

// ConninfoLines renders the auto.conf lines for ConninfoScript.
func ConninfoLines(upstreamIP, slot, appName, password string) string {
	redact.Register(password)
	conn := fmt.Sprintf("host=%s port=5432 user=%s password=%s application_name=%s sslmode=require",
		upstreamIP, ReplicationRole, password, appName)
	return fmt.Sprintf("# qabas: upstream %s\nprimary_conninfo = %s\nprimary_slot_name = %s\n", upstreamIP, Literal(conn), Literal(slot))
}

// RepointSQL re-targets a running standby (PG13+ reloads primary_conninfo live).
func RepointSQL(upstreamIP, slot, appName, password string) string {
	redact.Register(password)
	conn := fmt.Sprintf("host=%s port=5432 user=%s password=%s application_name=%s sslmode=require",
		upstreamIP, ReplicationRole, password, appName)
	return fmt.Sprintf("ALTER SYSTEM SET primary_conninfo = %s;\nALTER SYSTEM SET primary_slot_name = %s;\nSELECT pg_reload_conf();\n",
		Literal(conn), Literal(slot))
}

// RewindScript re-attaches a stopped former primary as a standby of the new writer using
// pg_rewind (requires wal_log_hints=on, set by the overlays), then writes standby.signal
// and primary_conninfo, and clears the DEMOTED marker.
func RewindScript(image, volume, upstreamIP, slot, appName, envFile string) string {
	conn := fmt.Sprintf("host=%s port=5432 user=%s dbname=postgres application_name=%s sslmode=require", upstreamIP, ReplicationRole, appName)
	return fmt.Sprintf(`set -eu
docker run --rm --user postgres --network host --env-file %[3]s -v %[2]s:%[4]s --entrypoint pg_rewind %[1]s \
  --target-pgdata=%[4]s --source-server=%[5]s --write-recovery-conf --progress
docker run --rm --user postgres -v %[2]s:%[4]s --entrypoint sh %[1]s -c \
  'touch %[4]s/standby.signal && echo "primary_slot_name = %[6]s" >> %[4]s/postgresql.auto.conf && rm -f %[4]s/%[7]s'
`, image, volume, nodeexec.Quote(envFile), DataDir, nodeexec.Quote(conn), slot, DemotedMarker)
}
