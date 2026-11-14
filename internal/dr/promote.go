package dr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/lease"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/replica"
)

// ErrRefused marks a safety refusal (nothing was changed).
var ErrRefused = errors.New("refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// PromoteOptions for break-glass promotion.
type PromoteOptions struct {
	BreakGlass bool
	// ConfirmName must equal the target node name (typed by the operator).
	ConfirmName string
	// Binary is the qabas path on the target (lease timer).
	Binary string
}

// ActionResult summarizes a writer change.
type ActionResult struct {
	NewWriter string   `json:"newWriter"`
	OldWriter string   `json:"oldWriter"`
	Epoch     int64    `json:"epoch"`
	ReplayLSN string   `json:"replayLSN,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	NextSteps []string `json:"nextSteps,omitempty"`
}

// Promote turns a standby into the writer while the old writer is DOWN. It refuses when
// the old writer is reachable (use Switchover) or still holds a witness-majority lease.
func (e *Engine) Promote(ctx context.Context, target string, opt PromoteOptions) (*ActionResult, error) {
	if e.Cluster.LeaseMode != cluster.LeaseModeStrict {
		return nil, refuse("cross-node promotion requires explicit strict lease mode")
	}
	if err := e.Cluster.ValidateLeaseMode(cluster.LeaseModeStrict); err != nil {
		return nil, refuse("strict lease policy invalid: %v", err)
	}
	if len(e.Witnesses) < 3 {
		return nil, refuse("strict DR promotion requires at least three independent lease witnesses; configure them before promotion")
	}
	t := e.Cluster.Node(target)
	if t == nil {
		return nil, refuse("node %q is not registered", target)
	}
	if t.Role != cluster.RoleStandby {
		return nil, refuse("%s is already the writer", target)
	}
	if !t.Promotable {
		return nil, refuse("%s is registered as not promotable (read-only branch replica)", target)
	}
	if !opt.BreakGlass {
		return nil, refuse("promotion while the writer is down requires --break-glass")
	}
	if opt.ConfirmName != target {
		return nil, refuse("typed confirmation %q does not match %q", opt.ConfirmName, target)
	}
	old := e.Cluster.Primary()
	f := e.Status(ctx)
	if f.WriterReachable {
		return nil, refuse("writer %s is reachable and writable; use `qabas dr switchover %s` (planned, no data loss)", old.Name, target)
	}
	if len(f.WriterSeenBy) > 0 {
		return nil, refuse("writer %s is still reachable from %s; this is a partition, not an outage", old.Name, strings.Join(f.WriterSeenBy, ", "))
	}
	if f.LeaseValidQuorum {
		return nil, refuse("a witness majority still grants %s a lease until %s; wait for it to expire", f.LeaseHolder, f.Lease.LeaseExpiresAt.Format(time.RFC3339))
	}
	if err := e.requireDurabilityProof(ctx, target); err != nil {
		return nil, refuse("cannot prove target durability: %v", err)
	}
	x, err := e.Exec(ctx, target)
	if err != nil {
		return nil, err
	}
	st, err := pgctl.GetStatus(ctx, x)
	if err != nil {
		return nil, err
	}
	if !st.InRecovery {
		return nil, refuse("%s is not in recovery; refusing to touch it", target)
	}
	res := &ActionResult{NewWriter: target, OldWriter: old.Name, Epoch: e.Cluster.Epoch + 1, ReplayLSN: st.ReplayLSN}
	if _, err := e.Grant(ctx, target, res.Epoch); err != nil {
		return nil, err
	}
	if err := e.activate(ctx, target, res, opt.Binary); err != nil {
		return res, err
	}
	res.NextSteps = append(res.NextSteps,
		fmt.Sprintf("when %s is back: qabas dr rejoin %s   (rewinds it into a standby of %s)", old.Name, old.Name, target),
		fmt.Sprintf("later, to return: qabas dr failback %s", old.Name),
		"update routing: qabas dr routing --site <site>")
	return res, nil
}

// activate promotes PostgreSQL on target, switches its stack to dr-primary, flips
// storage, re-points other standbys and saves the registry everywhere reachable.
func (e *Engine) activate(ctx context.Context, target string, res *ActionResult, binary string) error {
	x, err := e.Exec(ctx, target)
	if err != nil {
		return err
	}
	if err := pgctl.Promote(ctx, x); err != nil {
		return err
	}
	oldName := e.Cluster.Primary().Name
	if err := e.Cluster.Promote(target, res.Epoch); err != nil {
		return err
	}
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		return err
	}
	opt := replica.StackOptions{Profile: nodestack.ProfileDRPrimary}
	if err := e.ConfigureNode(ctx, target, opt); err != nil {
		return err
	}
	if err := e.StartStack(ctx, target, opt); err != nil {
		return err
	}
	if err := e.SetAppPolicy(ctx, target, false); err != nil {
		res.Warnings = append(res.Warnings, "minio app policy on "+target+": "+err.Error())
	}
	if binary != "" {
		if err := e.InstallTimer(ctx, target, binary); err != nil {
			res.Warnings = append(res.Warnings, "lease timer on "+target+": "+err.Error())
		}
	}
	repl, _ := cluster.ReadSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword)
	t := e.Cluster.Node(target)
	for _, n := range e.Cluster.Standbys() {
		if n.Name == oldName || n.Upstream != "" {
			continue
		}
		if err := e.repoint(ctx, n.Name, *t, repl); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("re-point %s: %v (rebuild with: qabas replica add %s --rebuild)", n.Name, err, n.Name))
			continue
		}
		if err := e.LinkStorage(ctx, target, n.Name, e.StoragePriority(n.Name)); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("storage %s -> %s: %v", target, n.Name, err))
		}
		if err := e.Sync(ctx, n.Name, replica.SyncOptions{}); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("sync %s: %v", n.Name, err))
		}
	}
	return nil
}

// repoint makes a running standby follow the new writer (timeline=latest follows the
// promotion when the standby is not ahead of it).
func (e *Engine) repoint(ctx context.Context, name string, writer cluster.Node, password string) error {
	wx, err := e.Exec(ctx, writer.Name)
	if err != nil {
		return err
	}
	if err := pgctl.EnsureSlot(ctx, wx, cluster.SlotName(name)); err != nil {
		return err
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	if _, err := pgctl.Query(ctx, x, pgctl.RepointSQL(writer.WGIP, cluster.SlotName(name), cluster.ApplicationName(name), password)); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, err := pgctl.GetStatus(ctx, x)
		if err == nil && st.WalReceiver == "streaming" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not streaming from %s after 60s", writer.Name)
		}
		time.Sleep(2 * time.Second)
	}
}

// SwitchoverOptions for a planned writer change.
type SwitchoverOptions struct {
	MaxLagSeconds float64
	Binary        string
	// Confirm is asked once the plan is known.
	Confirm func(prompt string) error
}

// Switchover moves the writer role while both nodes are up. The durability
// verifier must establish acknowledged PostgreSQL and MinIO writes on target
// before this operation may proceed.
func (e *Engine) Switchover(ctx context.Context, target string, opt SwitchoverOptions) (*ActionResult, error) {
	if e.Cluster.LeaseMode != cluster.LeaseModeStrict {
		return nil, refuse("cross-node switchover requires explicit strict lease mode")
	}
	if err := e.Cluster.ValidateLeaseMode(cluster.LeaseModeStrict); err != nil {
		return nil, refuse("strict lease policy invalid: %v", err)
	}
	if len(e.Witnesses) < 3 {
		return nil, refuse("strict DR switchover requires at least three independent lease witnesses")
	}
	t := e.Cluster.Node(target)
	old := e.Cluster.Primary()
	switch {
	case t == nil:
		return nil, refuse("node %q is not registered", target)
	case t.Role != cluster.RoleStandby:
		return nil, refuse("%s is already the writer", target)
	case !t.Promotable:
		return nil, refuse("%s is not promotable", target)
	case old.Name != e.Self:
		return nil, refuse("run switchover on the current writer (%s)", old.Name)
	}
	ox, err := e.Exec(ctx, old.Name)
	if err != nil {
		return nil, err
	}
	tx, err := e.Exec(ctx, target)
	if err != nil {
		return nil, err
	}
	tst, err := pgctl.GetStatus(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !tst.InRecovery || tst.WalReceiver != "streaming" {
		return nil, refuse("%s is not a streaming standby (wal receiver %q)", target, tst.WalReceiver)
	}
	maxLag := opt.MaxLagSeconds
	if maxLag <= 0 {
		maxLag = 30
	}
	if tst.LagSeconds > maxLag {
		return nil, refuse("%s lags %.0fs (> %.0fs); wait or pass --max-lag", target, tst.LagSeconds, maxLag)
	}
	if err := e.requireDurabilityProof(ctx, target); err != nil {
		return nil, refuse("cannot prove target durability: %v", err)
	}
	if opt.Confirm != nil {
		if err := opt.Confirm(fmt.Sprintf("Switch the writer from %s to %s? Writes pause for about a minute.", old.Name, target)); err != nil {
			return nil, err
		}
	}
	res := &ActionResult{NewWriter: target, OldWriter: old.Name, Epoch: e.Cluster.Epoch + 1}

	// 1. Fence the old writer: stop renewing, release the lease, refuse new writes.
	if err := e.StopTimer(ctx, old.Name); err != nil {
		return nil, err
	}
	e.Release(ctx, old.Name, e.Cluster.Epoch)
	if err := pgctl.SetDefaultReadOnly(ctx, ox, true); err != nil {
		return nil, err
	}
	rows, err := pgctl.Query(ctx, ox, "CHECKPOINT;\nSELECT pg_current_wal_lsn();")
	if err != nil || len(rows) == 0 {
		e.unfence(ctx, old.Name)
		return nil, fmt.Errorf("read writer LSN: %v", err)
	}
	lsn := rows[len(rows)-1][0]
	res.ReplayLSN = lsn
	if err := pgctl.WaitReplayed(ctx, tx, lsn, 120*time.Second); err != nil {
		e.unfence(ctx, old.Name)
		return nil, fmt.Errorf("%w (old writer unfenced; nothing changed)", err)
	}

	// 2. Demote the old writer: marker first, then stop its stack (volumes kept).
	if _, err := nodeexec.Output(ctx, ox, pgctl.MarkDemotedScript(e.pgImageRef(), replica.PGVolume)); err != nil {
		e.unfence(ctx, old.Name)
		return nil, err
	}
	if _, err := e.Compose(ctx, old.Name, replica.StackOptions{Profile: nodestack.ProfilePrimary}, "stop"); err != nil {
		return res, fmt.Errorf("old writer stack did not stop; refusing new lease and promotion: %w", err)
	}

	// 3. Promote the target under a new epoch.
	if _, err := e.Grant(ctx, target, res.Epoch); err != nil {
		return res, fmt.Errorf("%w; old writer %s is stopped and DEMOTED; recover with: qabas dr promote %s --break-glass", err, old.Name, target)
	}
	if err := e.activate(ctx, target, res, opt.Binary); err != nil {
		return res, err
	}

	// 4. Bring the old writer back as a standby of the new one.
	if err := e.Rejoin(ctx, old.Name, RejoinOptions{}); err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("rejoin %s: %v (retry: qabas dr rejoin %s [--rebuild])", old.Name, err, old.Name))
	}
	res.NextSteps = append(res.NextSteps, "update routing: qabas dr routing --site <site>",
		fmt.Sprintf("run replica/dr commands on %s from now on", target))
	return res, nil
}

func (e *Engine) unfence(ctx context.Context, node string) {
	// Never reopen PostgreSQL before a durable lease grant succeeds.
	if _, err := e.Grant(ctx, node, e.Cluster.Epoch); err != nil {
		return
	}
	if x, err := e.Exec(ctx, node); err == nil {
		_ = pgctl.SetDefaultReadOnly(ctx, x, false)
	}
}

// RejoinOptions for bringing a former writer back.
type RejoinOptions struct {
	// Rebuild re-seeds from scratch with pg_basebackup when pg_rewind is impossible.
	Rebuild bool
}

// Rejoin turns a former writer (now registered as standby) into a streaming standby of
// the current writer with pg_rewind (or a full re-seed with Rebuild).
func (e *Engine) Rejoin(ctx context.Context, name string, opt RejoinOptions) error {
	n := e.Cluster.Node(name)
	w := e.Cluster.Primary()
	if n == nil || w == nil {
		return refuse("node %q or writer missing from registry", name)
	}
	if n.Role != cluster.RoleStandby {
		return refuse("%s is the registered writer; nothing to rejoin", name)
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	if st, err := pgctl.GetStatus(ctx, x); err == nil && !st.InRecovery {
		// Still running writable (came back after a break-glass promotion): fence first.
		_ = pgctl.SetDefaultReadOnly(ctx, x, true)
	}
	if err := e.StopTimer(ctx, name); err != nil {
		return err
	}
	ro := replica.StackOptions{Profile: nodestack.ProfileReplicaRO}
	if _, err := nodeexec.Output(ctx, x, "docker stop -t 60 "+pgctl.Container+" >/dev/null 2>&1 || true"); err != nil {
		return err
	}
	if _, err := nodeexec.Output(ctx, x, pgctl.MarkDemotedScript(e.pgImageRef(), replica.PGVolume)); err != nil {
		return err
	}
	if _, err := e.Compose(ctx, name, ro, "down --remove-orphans"); err != nil {
		return err
	}
	if err := e.ConfigureNode(ctx, name, ro); err != nil {
		return err
	}
	if err := e.WriteLeaseFile(ctx, name, lease.Record{WriterNode: w.Name, Epoch: e.Cluster.Epoch}); err != nil {
		return err
	}
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	if err := pgctl.EnsureSlot(ctx, wx, cluster.SlotName(name)); err != nil {
		return err
	}
	repl, err := cluster.ReadSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword)
	if err != nil {
		return err
	}
	if opt.Rebuild {
		if err := e.Seed(ctx, name, true); err != nil {
			return err
		}
	} else {
		envFile := "/run/qabas/repl.env"
		if err := nodeexec.WriteFile(ctx, x, envFile, []byte("PGPASSWORD="+repl+"\n"), 0o600); err != nil {
			return err
		}
		_, rerr := nodeexec.Output(ctx, x, pgctl.RewindScript(e.pgImageRef(), replica.PGVolume, w.WGIP, cluster.SlotName(name), cluster.ApplicationName(name), envFile))
		_, _ = x.Run(context.Background(), "rm -f "+envFile, nil)
		if rerr != nil {
			return fmt.Errorf("pg_rewind failed (%v); re-run with --rebuild to re-seed from %s", rerr, w.Name)
		}
		if err := e.PinConninfo(ctx, name, w.WGIP, repl); err != nil {
			return err
		}
	}
	if err := e.StartStack(ctx, name, ro); err != nil {
		return err
	}
	if err := e.ClearStorageRules(ctx, name); err != nil {
		return err
	}
	if err := e.SetAppPolicy(ctx, name, true); err != nil {
		return err
	}
	if err := e.LinkStorage(ctx, w.Name, name, e.StoragePriority(name)); err != nil {
		return err
	}
	rep := e.Verify(ctx, name)
	if !rep.OK {
		return fmt.Errorf("rejoined but verification failed:\n%s", rep.Failures())
	}
	return nil
}

// Failback returns the writer role to a former writer: rejoin it if needed, then a
// planned switchover to it.
func (e *Engine) Failback(ctx context.Context, name string, opt SwitchoverOptions, rj RejoinOptions) (*ActionResult, error) {
	n := e.Cluster.Node(name)
	if n == nil {
		return nil, refuse("node %q is not registered", name)
	}
	if x, err := e.Exec(ctx, name); err == nil {
		st, err := pgctl.GetStatus(ctx, x)
		if err != nil || !st.InRecovery || st.WalReceiver != "streaming" {
			if err := e.Rejoin(ctx, name, rj); err != nil {
				return nil, err
			}
		}
	} else {
		return nil, err
	}
	return e.Switchover(ctx, name, opt)
}
