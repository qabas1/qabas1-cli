package replica

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/minioctl"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
)

// Check is one verification result.
type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Report aggregates checks for a node.
type Report struct {
	Node   string  `json:"node"`
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

func (r *Report) add(name string, ok bool, detail string, args ...any) {
	if len(args) > 0 {
		detail = fmt.Sprintf(detail, args...)
	}
	r.Checks = append(r.Checks, Check{Name: name, OK: ok, Detail: detail})
}

// Failures renders failed checks.
func (r Report) Failures() string {
	var b strings.Builder
	for _, c := range r.Checks {
		if !c.OK {
			fmt.Fprintf(&b, "  - %s: %s\n", c.Name, c.Detail)
		}
	}
	return b.String()
}

const migrationsDigestSQL = `SELECT count(*), COALESCE(md5(string_agg(migration_name || ':' || COALESCE(checksum, ''), ',' ORDER BY migration_name)), '')
FROM "_prisma_migrations" WHERE finished_at IS NOT NULL AND rolled_back_at IS NULL;`

// DRHealth is the subset of /health/dr the CLI inspects.
type DRHealth struct {
	Mode     string `json:"mode"`
	ReadOnly bool   `json:"readOnly"`
	Node     string `json:"node"`
	Release  string `json:"release"`
}

// healthDRJS fetches /health/dr from inside the API container: the API port is only
// exposed on compose networks, and production rejects requests without an Origin.
const healthDRJS = `const o=process.env.API_HEALTHCHECK_ORIGIN||process.env.WEBAUTHN_ORIGIN||'';` +
	`require('http').get({hostname:'127.0.0.1',port:3030,path:'/health/dr',timeout:5000,headers:o?{Origin:o}:{}},r=>{` +
	`let b='';r.on('data',c=>b+=c);r.on('end',()=>process.stdout.write(b))})` +
	`.on('timeout',function(){this.destroy(new Error('timeout'))})` +
	`.on('error',e=>{console.error(e.message);process.exit(1)})`

// APIHealth reads /health/dr from the node's own API container.
func (e *Engine) APIHealth(ctx context.Context, name string) (DRHealth, error) {
	var h DRHealth
	x, err := e.Exec(ctx, name)
	if err != nil {
		return h, err
	}
	containers, err := apiContainerNames(ctx, x, false)
	if err != nil {
		return h, fmt.Errorf("list running API containers on %s: %w", name, err)
	}
	if len(containers) == 0 {
		return h, fmt.Errorf("no running API containers on %s", name)
	}
	script := "set -eu\ndocker exec " + nodeexec.Quote(containers[0]) + " node -e " + nodeexec.Quote(healthDRJS) + "\n"
	out, err := run(ctx, x, script)
	if err != nil {
		return h, err
	}
	var envelope struct {
		Data *DRHealth `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return h, fmt.Errorf("unexpected /health/dr response")
	}
	if envelope.Data != nil {
		return *envelope.Data, nil
	}
	if err := json.Unmarshal([]byte(out), &h); err != nil || h.Mode == "" {
		return h, fmt.Errorf("unexpected /health/dr response")
	}
	return h, nil
}

// Verify proves a replica is a correct, read-only, caught-up copy of the writer.
func (e *Engine) Verify(ctx context.Context, name string) Report {
	rep := Report{Node: name}
	n := e.Cluster.Node(name)
	w, err := e.writer()
	if n == nil || err != nil {
		rep.add("registry", false, "node or writer missing from registry")
		return rep
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		rep.add("ssh", false, "%v", err)
		return rep
	}
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		rep.add("writer", false, "%v", err)
		return rep
	}
	paths := e.CheckPaths(ctx, name)
	for _, p := range paths {
		rep.add("path."+p.Service, p.OK, "%s (%s) -> %s: %s", p.Container, p.Source, p.Destination, p.Detail)
	}

	st, err := pgctl.GetStatus(ctx, x)
	if err != nil {
		rep.add("postgres.status", false, "%v", err)
		return rep
	}
	rep.add("postgres.recovery", st.InRecovery, "pg_is_in_recovery()=%v", st.InRecovery)
	if err := pgctl.ProbeWriteRejected(ctx, x); err != nil {
		rep.add("postgres.write_rejected", false, "%v", err)
	} else {
		rep.add("postgres.write_rejected", true, "CREATE TABLE refused (read-only)")
	}
	rep.add("postgres.streaming", st.WalReceiver == "streaming", "wal receiver: %q", st.WalReceiver)

	ws, err := pgctl.GetStatus(ctx, wx)
	if err != nil {
		rep.add("writer.status", false, "%v", err)
	} else {
		rep.add("postgres.system_id", ws.SystemID == st.SystemID, "writer %s, replica %s", ws.SystemID, st.SystemID)
		if err := pgctl.WaitReplayed(ctx, x, ws.CurrentLSN, 90*time.Second); err != nil {
			rep.add("postgres.caught_up", false, "%v", err)
		} else {
			rep.add("postgres.caught_up", true, "replayed writer LSN %s", ws.CurrentLSN)
		}
	}

	wm, werr := pgctl.Query(ctx, wx, migrationsDigestSQL)
	rm, rerr := pgctl.Query(ctx, x, migrationsDigestSQL)
	switch {
	case werr != nil || rerr != nil:
		rep.add("schema.migrations", false, "query failed (writer: %v, replica: %v)", werr, rerr)
	case len(wm) == 1 && len(rm) == 1 && strings.Join(wm[0], "|") == strings.Join(rm[0], "|"):
		rep.add("schema.migrations", true, "%s applied migrations match", wm[0][0])
	default:
		rep.add("schema.migrations", false, "writer %v != replica %v", wm, rm)
	}

	if len(paths) == 2 && paths[1].OK {
		rep.add(e.verifySentinel(ctx, w.Name, name))
	} else {
		rep.add("minio.replication", false, "sentinel skipped: writer MinIO container cannot reach replica port 9000")
	}

	if e.ProbeTCP != nil && n.Host != "" && n.Host != n.WGIP {
		for _, port := range []string{"5432", "9000"} {
			open := e.ProbeTCP(ctx, net.JoinHostPort(n.Host, port))
			rep.add("ports.closed."+port, !open, "%s:%s reachable from outside WireGuard=%v", n.Host, port, open)
		}
	}

	if h, err := e.APIHealth(ctx, name); err != nil {
		rep.add("api.mode", false, "%v", err)
	} else {
		rep.add("api.mode", h.Mode == "DR_READ_ONLY" && h.ReadOnly, "mode=%s readOnly=%v", h.Mode, h.ReadOnly)
	}

	out, _ := run(ctx, x, "docker ps --format '{{.Names}}' --filter status=running")
	var running []string
	for _, wk := range []string{"operations-worker", "notifications-worker", "printables-worker", "documents-worker", "audit-cleanup", "migrate"} {
		if strings.Contains(out, "qabasone-"+wk) {
			running = append(running, wk)
		}
	}
	rep.add("workers.stopped", len(running) == 0, "running writers: %v", running)

	if cs, err := run(ctx, x, containersScript); err != nil {
		rep.add("containers.healthy", false, "%v", err)
	} else if problems, seen := containerProblems(cs); seen == 0 {
		rep.add("containers.healthy", false, "no qabasone containers on %s", name)
	} else {
		rep.add("containers.healthy", len(problems) == 0, "%d containers, problems: %v", seen, problems)
	}
	if err := e.WaitHealthy(ctx, w.Name, "qabasone-api", 60*time.Second); err != nil {
		rep.add("writer.api", false, "%v", err)
	} else {
		rep.add("writer.api", true, "writer %s API healthy", w.Name)
	}

	rep.OK = true
	for _, c := range rep.Checks {
		if !c.OK {
			rep.OK = false
		}
	}
	return rep
}

// verifySentinel writes a tiny object on the source and waits for it on the replica.
func (e *Engine) verifySentinel(ctx context.Context, src, dest string) (string, bool, string) {
	b := e.Bucket()
	key := fmt.Sprintf("%s%s-%d", minioctl.SentinelPrefix, dest, e.now().Unix())
	if _, err := e.MC(ctx, src, nil, minioctl.SentinelPutCommands("local", b, key)); err != nil {
		return "minio.replication", false, fmt.Sprintf("sentinel put on %s: %v", src, err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := e.MC(ctx, dest, nil, minioctl.SentinelStatCommands("local", b, key)); err == nil {
			return "minio.replication", true, "sentinel " + key + " replicated"
		}
		if time.Now().After(deadline) {
			return "minio.replication", false, "sentinel " + key + " not replicated within 90s"
		}
		select {
		case <-ctx.Done():
			return "minio.replication", false, ctx.Err().Error()
		case <-time.After(3 * time.Second):
		}
	}
}
