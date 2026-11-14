// Package pitr manages point-in-time recovery for the writer: a continuous WAL archive
// (wal-archiver service, pg_receivewal on its own slot), compressed base backups, and
// restore tests into a throwaway container on an internal network. It is deliberately
// separate from replication: replicas copy mistakes within seconds, the archive does not.
package pitr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/redact"
)

const (
	Slot          = "qabas_wal_archive"
	Container     = "qabasone-wal-archiver"
	EnvFile       = ".env.pitr"
	dataNetwork   = "qabasone_qabasone_data"
	testContainer = "qabas-pitr-restore-test"
)

// Manager runs PITR operations on the local writer.
type Manager struct {
	X         nodeexec.Executor
	ConfigDir string
	StateDir  string
	PGImage   string
	Now       func() time.Time
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) env(name string) map[string]string {
	b, err := os.ReadFile(filepath.Join(m.ConfigDir, name))
	if err != nil {
		return map[string]string{}
	}
	return nodestack.ParseEnv(b)
}

// Dir is the archive repository (QABAS_PITR_DIR, default <state>/pitr).
func (m *Manager) Dir() string {
	if v := m.env(".env")["QABAS_PITR_DIR"]; v != "" {
		return v
	}
	return filepath.ToSlash(filepath.Join(m.StateDir, "pitr"))
}

// Enabled reports whether .env.pitr exists.
func (m *Manager) Enabled() bool {
	_, err := os.Stat(filepath.Join(m.ConfigDir, EnvFile))
	return err == nil
}

// Enable writes .env.pitr (0600), prepares the repository for the postgres uid, and
// makes sure the backup role may open replication connections from the compose network.
func (m *Manager) Enable(ctx context.Context, backupPassword string) error {
	redact.Register(backupPassword)
	body := fmt.Sprintf("# Managed by qabas backup pitr. Secret: keep 0600.\nQABAS_BACKUP_USER=%s\nQABAS_BACKUP_PASSWORD=%s\n", pgctl.BackupRole, backupPassword)
	if err := nodeexec.WriteFile(ctx, m.X, filepath.ToSlash(filepath.Join(m.ConfigDir, EnvFile)), []byte(body), 0o600); err != nil {
		return err
	}
	dir := m.Dir()
	script := fmt.Sprintf(`set -eu
uid=$(docker run --rm --entrypoint id %[1]s -u postgres)
gid=$(docker run --rm --entrypoint id %[1]s -g postgres)
mkdir -p %[2]s/wal %[2]s/base
chown -R "$uid:$gid" %[2]s
chmod 0700 %[2]s
`, m.PGImage, nodeexec.Quote(dir))
	if _, err := nodeexec.Output(ctx, m.X, script); err != nil {
		return err
	}
	return m.ensureHBA(ctx)
}

// ensureHBA appends a replication rule for the backup role to the live pg_hba.conf when
// the node does not use the qabas-rendered hba file (single-node installs).
func (m *Manager) ensureHBA(ctx context.Context) error {
	rows, err := pgctl.Query(ctx, m.X, "SHOW hba_file;")
	if err != nil || len(rows) == 0 {
		return err
	}
	hba := rows[0][0]
	if strings.HasPrefix(hba, "/etc/qabasone/") {
		return nil
	}
	subnets, _ := nodeexec.Output(ctx, m.X, "docker network inspect "+dataNetwork+" --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null || true")
	var lines strings.Builder
	for _, s := range strings.Fields(subnets) {
		fmt.Fprintf(&lines, "host replication %s %s scram-sha-256\n", pgctl.BackupRole, s)
	}
	if lines.Len() == 0 {
		return fmt.Errorf("compose network %s not found; is the stack running?", dataNetwork)
	}
	script := fmt.Sprintf(`set -eu
docker exec -i %[1]s sh -c 'f=%[2]s; grep -q "replication %[3]s" "$f" || { echo "# qabas: PITR archive"; cat; } >> "$f"'
docker exec %[1]s sh -c 'kill -HUP 1'
`, pgctl.Container, nodeexec.Quote(hba), pgctl.BackupRole)
	res, err := m.X.Run(ctx, script, strings.NewReader(lines.String()))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &nodeexec.ScriptError{Node: m.X.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return nil
}

// Base is one base backup.
type Base struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Bytes int64     `json:"bytes"`
}

// Status of the archive.
type Status struct {
	Enabled     bool           `json:"enabled"`
	Dir         string         `json:"dir"`
	Archiver    string         `json:"archiver"`
	Slot        *pgctl.SlotRow `json:"slot,omitempty"`
	NewestWAL   string         `json:"newestWAL,omitempty"`
	NewestWALAt time.Time      `json:"newestWALAt,omitempty"`
	WALSegments int            `json:"walSegments"`
	WALBytes    int64          `json:"walBytes"`
	Bases       []Base         `json:"bases"`
	Problems    []string       `json:"problems,omitempty"`
}

var baseIDRe = regexp.MustCompile(`^\d{8}T\d{6}Z$`)

// Status inspects the archiver, the slot and the repository.
func (m *Manager) Status(ctx context.Context) Status {
	st := Status{Enabled: m.Enabled(), Dir: m.Dir()}
	dir := nodeexec.Quote(st.Dir)
	out, _ := nodeexec.Output(ctx, m.X, fmt.Sprintf(`set +e
echo "archiver=$(docker inspect -f '{{.State.Status}}' %[1]s 2>/dev/null || echo missing)"
newest=$(ls -1t %[2]s/wal 2>/dev/null | grep -v partial | head -n 1)
echo "newest=$newest"
[ -n "$newest" ] && echo "newest_at=$(stat -c %%Y %[2]s/wal/$newest)"
echo "wal_count=$(ls -1 %[2]s/wal 2>/dev/null | grep -vc partial)"
echo "wal_kb=$(du -sk %[2]s/wal 2>/dev/null | cut -f1)"
for b in $(ls -1 %[2]s/base 2>/dev/null); do echo "base=$b:$(du -sk %[2]s/base/$b | cut -f1)"; done
`, Container, dir))
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "archiver":
			st.Archiver = v
		case "newest":
			st.NewestWAL = v
		case "newest_at":
			if sec, err := strconv.ParseInt(v, 10, 64); err == nil {
				st.NewestWALAt = time.Unix(sec, 0).UTC()
			}
		case "wal_count":
			st.WALSegments, _ = strconv.Atoi(v)
		case "wal_kb":
			kb, _ := strconv.ParseInt(v, 10, 64)
			st.WALBytes = kb * 1024
		case "base":
			id, kb, _ := strings.Cut(v, ":")
			if !baseIDRe.MatchString(id) {
				continue
			}
			at, _ := time.Parse("20060102T150405Z", id)
			n, _ := strconv.ParseInt(kb, 10, 64)
			st.Bases = append(st.Bases, Base{ID: id, At: at, Bytes: n * 1024})
		}
	}
	sort.Slice(st.Bases, func(i, j int) bool { return st.Bases[i].At.After(st.Bases[j].At) })
	if slots, err := pgctl.Slots(ctx, m.X); err == nil {
		for _, s := range slots {
			if s.Name == Slot {
				s := s
				st.Slot = &s
			}
		}
	}
	switch {
	case !st.Enabled:
		st.Problems = append(st.Problems, "PITR is not enabled (qabas backup pitr enable)")
	case st.Archiver != "running":
		st.Problems = append(st.Problems, "wal-archiver is "+st.Archiver)
	}
	if st.Enabled && st.Slot == nil {
		st.Problems = append(st.Problems, "replication slot "+Slot+" is missing")
	} else if st.Slot != nil && !st.Slot.Active {
		st.Problems = append(st.Problems, "slot "+Slot+" is inactive (WAL accumulates on the writer)")
	}
	if st.Enabled && len(st.Bases) == 0 {
		st.Problems = append(st.Problems, "no base backup yet (qabas backup pitr base)")
	} else if len(st.Bases) > 0 && m.now().Sub(st.Bases[0].At) > 8*24*time.Hour {
		st.Problems = append(st.Problems, "newest base backup is older than 8 days")
	}
	return st
}

// TakeBase runs pg_basebackup (tar, gzip, WAL fetched) into <dir>/base/<id>.
func (m *Manager) TakeBase(ctx context.Context, backupPassword string) (Base, error) {
	redact.Register(backupPassword)
	at := m.now()
	id := at.Format("20060102T150405Z")
	envFile := "/run/qabas/pitr-base.env"
	if err := nodeexec.WriteFile(ctx, m.X, envFile, []byte("PGPASSWORD="+backupPassword+"\n"), 0o600); err != nil {
		return Base{}, err
	}
	defer func() { _, _ = m.X.Run(context.Background(), "rm -f "+envFile, nil) }()
	script := fmt.Sprintf(`set -eu
uid=$(docker run --rm --entrypoint id %[1]s -u postgres)
gid=$(docker run --rm --entrypoint id %[1]s -g postgres)
docker run --rm --network %[2]s --user "$uid:$gid" --env-file %[3]s -v %[4]s:/pitr --entrypoint pg_basebackup %[1]s \
  -h postgres -p 5432 -U %[5]s -D /pitr/base/%[6]s -Ft -z -X fetch -c fast --label qabas-%[6]s
du -sk %[4]s/base/%[6]s | cut -f1
`, m.PGImage, dataNetwork, nodeexec.Quote(envFile), nodeexec.Quote(m.Dir()), pgctl.BackupRole, id)
	out, err := nodeexec.Output(ctx, m.X, script)
	if err != nil {
		return Base{}, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	kb, _ := strconv.ParseInt(strings.TrimSpace(lines[len(lines)-1]), 10, 64)
	return Base{ID: id, At: at, Bytes: kb * 1024}, nil
}

// RestoreResult reports a restore test.
type RestoreResult struct {
	Base            string        `json:"base"`
	TargetTime      string        `json:"targetTime,omitempty"`
	Migrations      int           `json:"migrations"`
	LatestMigration string        `json:"latestMigrationAt,omitempty"`
	Duration        time.Duration `json:"durationNs"`
}

// RestoreTest restores the newest base (or baseID) plus archived WAL into a throwaway
// container on an internal network, waits for recovery to finish, checks the schema,
// and removes everything. Production is never touched.
func (m *Manager) RestoreTest(ctx context.Context, baseID, targetTime string) (RestoreResult, error) {
	start := m.now()
	res := RestoreResult{Base: baseID, TargetTime: targetTime}
	if res.Base == "" {
		st := m.Status(ctx)
		if len(st.Bases) == 0 {
			return res, fmt.Errorf("no base backup in %s", st.Dir)
		}
		res.Base = st.Bases[0].ID
	}
	if !baseIDRe.MatchString(res.Base) {
		return res, fmt.Errorf("invalid base id %q", res.Base)
	}
	if targetTime != "" {
		if _, err := time.Parse(time.RFC3339, targetTime); err != nil {
			return res, fmt.Errorf("--target-time must be RFC3339 (e.g. 2026-09-30T08:15:00Z)")
		}
	}
	user := m.env(".env")["POSTGRES_USER"]
	if user == "" {
		user = "qabasone"
	}
	db := m.env(".env")["POSTGRES_DB"]
	if db == "" {
		db = "qabasone"
	}
	target := ""
	if targetTime != "" {
		target = fmt.Sprintf("recovery_target_time = '%s'\n", targetTime)
	}
	// Runs inside the restore volume as root; stdin of the first docker run.
	inner := fmt.Sprintf(`set -eu
tar -xzf /pitr/base/%[1]s/base.tar.gz -C /d
cat > /d/qabas-restore.sh <<'EOF'
#!/bin/sh
f=/pitr/wal/$1
if [ -f "$f.gz" ]; then gunzip -c "$f.gz" > "$2"; elif [ -f "$f" ]; then cp "$f" "$2"; else exit 1; fi
EOF
chmod 0755 /d/qabas-restore.sh
rm -f /d/standby.signal /d/qabas.DEMOTED
touch /d/recovery.signal
cat >> /d/postgresql.auto.conf <<'EOF'
# qabas restore test
restore_command = '/var/lib/postgresql/data/qabas-restore.sh %%f %%p'
recovery_target_timeline = 'latest'
recovery_target_action = 'promote'
default_transaction_read_only = 'off'
%[2]sEOF
chown -R postgres:postgres /d
chmod 0700 /d
`, res.Base, target)
	script := fmt.Sprintf(`set -eu
tag="$$"
vol="qabas_pitr_test_$tag"
net="qabas_pitr_test_$tag"
cleanup() { docker rm -f %[1]s >/dev/null 2>&1 || true; docker volume rm "$vol" >/dev/null 2>&1 || true; docker network rm "$net" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
docker volume create "$vol" >/dev/null
docker network create --internal "$net" >/dev/null
docker run --rm -i --user 0 -v "$vol":/d -v %[2]s:/pitr:ro --entrypoint sh %[3]s -s
docker run -d --name %[1]s --network "$net" -v "$vol":/var/lib/postgresql/data -v %[2]s:/pitr:ro %[3]s postgres -c hba_file=/var/lib/postgresql/data/pg_hba.conf >/dev/null
i=0
while :; do
  r=$(docker exec %[1]s psql -X -At -U %[4]s -d %[5]s -c 'SELECT pg_is_in_recovery()' 2>/dev/null || true)
  [ "$r" = "f" ] && break
  if [ "$(docker inspect -f '{{.State.Running}}' %[1]s 2>/dev/null)" != "true" ]; then docker logs --tail 30 %[1]s >&2; exit 70; fi
  i=$((i+1)); [ $i -gt 600 ] && { echo "recovery did not finish within 20 minutes" >&2; exit 71; }
  sleep 2
done
docker exec %[1]s psql -X -At -U %[4]s -d %[5]s -c 'SELECT count(*) FROM "_prisma_migrations" WHERE finished_at IS NOT NULL'
docker exec %[1]s psql -X -At -U %[4]s -d %[5]s -c "SELECT COALESCE(to_char(max(finished_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'), '') FROM \"_prisma_migrations\""
`, testContainer, nodeexec.Quote(m.Dir()), m.PGImage, nodeexec.Quote(user), nodeexec.Quote(db))
	out, err := m.X.Run(ctx, script, strings.NewReader(inner))
	res.Duration = m.now().Sub(start)
	if err != nil {
		return res, err
	}
	if out.ExitCode != 0 {
		return res, &nodeexec.ScriptError{Node: m.X.Node(), ExitCode: out.ExitCode, Stderr: out.Stderr}
	}
	lines := strings.Split(strings.TrimSpace(out.Stdout), "\n")
	if len(lines) >= 1 {
		res.Migrations, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	}
	if len(lines) >= 2 {
		res.LatestMigration = strings.TrimSpace(lines[1])
	}
	if res.Migrations == 0 {
		return res, fmt.Errorf("restored database has no applied migrations")
	}
	return res, nil
}

// PruneBases keeps the newest keep base backups.
func (m *Manager) PruneBases(ctx context.Context, keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	st := m.Status(ctx)
	var removed []string
	for i, b := range st.Bases {
		if i < keep {
			continue
		}
		if _, err := nodeexec.Output(ctx, m.X, "rm -rf "+nodeexec.Quote(st.Dir+"/base/"+b.ID)); err != nil {
			return removed, err
		}
		removed = append(removed, b.ID)
	}
	return removed, nil
}

// walPruneMargin keeps WAL completed shortly before the oldest base, guarding
// against clock skew between the archiver and pg_basebackup.
const walPruneMargin = time.Hour

// WALPrune reports what PruneWAL removed.
type WALPrune struct {
	Cutoff  time.Time `json:"cutoff,omitempty"`
	Removed int       `json:"removed"`
	Bytes   int64     `json:"bytes"`
}

// PruneWAL deletes archived WAL segments that were completed before the oldest
// kept base backup started; no base can replay them. Partial segments and
// timeline history files are always kept. Without a base nothing is removed.
func (m *Manager) PruneWAL(ctx context.Context) (WALPrune, error) {
	st := m.Status(ctx)
	if len(st.Bases) == 0 {
		return WALPrune{}, nil
	}
	cutoff := st.Bases[len(st.Bases)-1].At.Add(-walPruneMargin)
	res := WALPrune{Cutoff: cutoff}
	mins := int(m.now().Sub(cutoff).Minutes())
	if mins < 1 {
		return res, nil
	}
	script := fmt.Sprintf(`set -eu
[ -d %[1]s/wal ] || exit 0
find %[1]s/wal -maxdepth 1 -type f ! -name '*.partial' ! -name '*.history' ! -name '*.history.gz' -mmin +%[2]d -printf '%%s\n' -delete
`, nodeexec.Quote(st.Dir), mins)
	out, err := nodeexec.Output(ctx, m.X, script)
	if err != nil {
		return res, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if n, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64); err == nil {
			res.Removed++
			res.Bytes += n
		}
	}
	return res, nil
}
