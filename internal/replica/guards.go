package replica

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/pgctl"
)

// DefaultWALBudgetGB bounds WAL a replication slot may retain on the writer when no
// site sets walBudgetGB.
const DefaultWALBudgetGB = 10

const diskFreeScript = `d=/var/lib/docker; [ -d "$d" ] || d=/
echo "writer_diskfree_kb=$(df -Pk "$d" | awk 'NR==2{print $4}')"
`

// WriterGuard refuses to touch the writer unless its database and API are healthy and
// it has room for the WAL a new slot may retain. It never restarts anything.
func (e *Engine) WriterGuard(ctx context.Context) error {
	w, err := e.writer()
	if err != nil {
		return err
	}
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	for _, c := range []string{pgctl.Container, "qabas1-api"} {
		if err := e.WaitHealthy(ctx, w.Name, c, 30*time.Second); err != nil {
			return fmt.Errorf("the writer is not healthy, refusing to change it: %w", err)
		}
	}
	out, _ := run(ctx, wx, diskFreeScript)
	if kb, err := strconv.ParseInt(facts(out)["writer_diskfree_kb"], 10, 64); err == nil && kb > 0 {
		needKB := int64(e.Cluster.WALBudgetGB(DefaultWALBudgetGB)+2) * 1024 * 1024
		if kb < needKB {
			return fmt.Errorf("the writer has only %d MiB free; a replication slot may retain up to %d GiB of WAL plus 2 GiB headroom - free disk on %s first",
				kb/1024, e.Cluster.WALBudgetGB(DefaultWALBudgetGB), w.Name)
		}
	}
	return nil
}

// EnsureSlotBound makes sure a stalled replica cannot fill the upstream's disk: an
// unlimited max_slot_wal_keep_size on the writer is set to the WAL budget with
// ALTER SYSTEM + pg_reload_conf() (no restart). Cascading upstreams only warn.
func (e *Engine) EnsureSlotBound(ctx context.Context, upstream string, isWriter bool, opt AddOptions) error {
	ux, err := e.Exec(ctx, upstream)
	if err != nil {
		return err
	}
	rows, err := pgctl.Query(ctx, ux, "SHOW max_slot_wal_keep_size;")
	if err != nil {
		return err
	}
	if len(rows) != 1 || len(rows[0]) == 0 || strings.TrimSpace(rows[0][0]) != "-1" {
		return nil
	}
	if !isWriter {
		e.logf("  warning: max_slot_wal_keep_size is unlimited on upstream %s; a disconnected replica can fill its disk", upstream)
		return nil
	}
	budget := e.Cluster.WALBudgetGB(DefaultWALBudgetGB)
	if opt.Confirm != nil {
		prompt := fmt.Sprintf("max_slot_wal_keep_size is unlimited on the writer %s: a disconnected replica would keep WAL until its disk fills. "+
			"Limit it to %dGB now (configuration reload, no restart)?", upstream, budget)
		if err := opt.Confirm(prompt); err != nil {
			return fmt.Errorf("refusing to create an unbounded replication slot on the writer: %w", err)
		}
	}
	sql := fmt.Sprintf("ALTER SYSTEM SET max_slot_wal_keep_size = %s;\nSELECT pg_reload_conf();\n", pgctl.Literal(strconv.Itoa(budget)+"GB"))
	if _, err := pgctl.Query(ctx, ux, sql); err != nil {
		return fmt.Errorf("limit max_slot_wal_keep_size on %s: %w", upstream, err)
	}
	msg := fmt.Sprintf("max_slot_wal_keep_size set to %dGB on %s (reload, no restart)", budget, upstream)
	e.logf("  %s", msg)
	if opt.Audit != nil {
		opt.Audit("writer-slot-bound", msg)
	}
	return nil
}

// slotHint tells the operator what a half-provisioned replica costs on its upstream.
func (e *Engine) slotHint(ctx context.Context, n cluster.Node) {
	up := e.Cluster.UpstreamOf(n)
	if up == nil {
		return
	}
	ux, err := e.Exec(ctx, up.Name)
	if err != nil {
		return
	}
	slot := cluster.SlotName(n.Name)
	rows, err := pgctl.Slots(ctx, ux)
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.Name == slot {
			e.logf("  note: slot %s on %s retains %d MiB of WAL until %s streams (bounded by max_slot_wal_keep_size).", slot, up.Name, r.RetainedBytes>>20, n.Name)
			e.logf("        continue with `qabas replica add %s --resume`, or release it with `qabas replica rm %s`.", n.Name, n.Name)
			return
		}
	}
}

// containerProblems lists unhealthy, restarting or failed qabasone containers from
// `docker ps -a --format '{{.Names}}|{{.Status}}'` output. One-shot init containers
// that exited 0 are fine.
func containerProblems(out string) (problems []string, seen int) {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, status, ok := strings.Cut(strings.TrimSpace(line), "|")
		if !ok || name == "" {
			continue
		}
		seen++
		switch {
		case strings.Contains(status, "(unhealthy)"):
			problems = append(problems, name+" unhealthy")
		case strings.HasPrefix(status, "Restarting"):
			problems = append(problems, name+" restarting")
		case strings.HasPrefix(status, "Exited") && !strings.HasPrefix(status, "Exited (0)"):
			problems = append(problems, name+" "+status)
		case strings.HasPrefix(status, "Dead"):
			problems = append(problems, name+" "+status)
		}
	}
	return problems, seen
}

const containersScript = "docker ps -a --filter name=^qabas1- --format '{{.Names}}|{{.Status}}'"
