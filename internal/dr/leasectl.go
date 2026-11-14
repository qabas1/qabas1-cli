package dr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/lease"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/replica"
)

// RenewResult reports one lease renewal round.
type RenewResult struct {
	Renewed        bool          `json:"renewed"`
	Fenced         bool          `json:"fenced"`
	Reason         string        `json:"reason,omitempty"`
	Duration       time.Duration `json:"duration"`
	LeaseExpiresAt time.Time     `json:"leaseExpiresAt,omitempty"`
	Record         lease.Record  `json:"record"`
	Outcome        lease.Outcome `json:"outcome"`
	Warnings       []string      `json:"warnings,omitempty"`
}

func (e *Engine) ttl() time.Duration {
	if e.Cluster.LeaseTTLSec > 0 {
		return time.Duration(e.Cluster.LeaseTTLSec) * time.Second
	}
	return 60 * time.Second
}

// WriteLeaseFile writes writer.json on a node.
func (e *Engine) WriteLeaseFile(ctx context.Context, node string, r lease.Record) error {
	x, err := e.Exec(ctx, node)
	if err != nil {
		return err
	}
	return nodeexec.WriteFile(ctx, x, e.Layout(node).WriterFile(), lease.Render(r), 0o644)
}

// Renew is run by the lease timer on the writer. Both modes require a registered
// writer and an active writable database. Strict mode also verifies promotable peers,
// durability, and witness quorum. Unsafe states trigger local data-service fencing.
func (e *Engine) Renew(ctx context.Context) (res RenewResult, err error) {
	started := time.Now()
	defer func() { res.Duration = time.Since(started) }()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e.Cluster.Writer != e.Self {
		res.Reason = fmt.Sprintf("%s is not the registered writer (%s): self-fencing", e.Self, e.Cluster.Writer)
		return res, e.selfFence(ctx, &res)
	}
	if err := e.Cluster.ValidateLeaseMode(e.Cluster.LeaseMode); err != nil {
		res.Reason = "lease policy invalid: " + err.Error()
		return res, e.selfFence(ctx, &res)
	}
	x, err := e.Exec(ctx, e.Self)
	if err != nil {
		return res, err
	}
	containerState, inspectErr := postgresContainerState(ctx, x)
	if inspectErr != nil {
		res.Reason = "cannot inspect designated writer PostgreSQL container: " + inspectErr.Error()
		return res, e.selfFence(ctx, &res)
	}
	if containerState != "running" {
		res.Reason = "designated writer PostgreSQL container " + containerState + "; database unavailable"
		return res, e.selfFence(ctx, &res)
	}
	st, err := pgctl.GetStatus(ctx, x)
	if err != nil {
		res.Reason = "database unreachable: " + err.Error()
		return res, e.selfFence(ctx, &res)
	}
	if st.InRecovery {
		res.Reason = "registered writer database is in recovery: self-fencing"
		return res, e.selfFence(ctx, &res)
	}
	if st.TxReadOnly {
		res.Reason = "database is fenced read-only; manual recovery is required"
		return res, e.selfFence(ctx, &res)
	}
	if e.Cluster.LeaseMode == cluster.LeaseModeStrict && len(e.Witnesses) < 3 {
		res.Reason = "strict DR requires at least three independent lease witnesses; refusing renewal"
		return res, e.selfFence(ctx, &res)
	}
	if e.Cluster.LeaseMode == cluster.LeaseModeStrict {
		if err := e.requireDurabilityProof(ctx, e.Self); err != nil {
			res.Reason = "strict DR durability proof unavailable: " + err.Error()
			return res, e.selfFence(ctx, &res)
		}
		other, warnings := e.otherWriter(ctx, st.SystemID)
		res.Warnings = warnings
		if other != "" {
			res.Reason = fmt.Sprintf("another writable node detected (%s): self-fencing", other)
			return res, e.selfFence(ctx, &res)
		}
		if len(warnings) > 0 {
			res.Reason = "promotable peer state unknown; refusing renewal: " + strings.Join(warnings, "; ")
			return res, e.selfFence(ctx, &res)
		}
	}
	now := e.now()
	res.Record = lease.Record{WriterNode: e.Self, Epoch: e.Cluster.Epoch, LeaseExpiresAt: now.Add(e.ttl()), RenewedAt: now}
	if e.Cluster.LeaseMode == cluster.LeaseModeStrict {
		res.Outcome = lease.Acquire(ctx, e.Witnesses, res.Record)
		if !res.Outcome.Granted {
			for _, v := range res.Outcome.Votes {
				if v.Error == "" && v.Stored.Epoch > e.Cluster.Epoch {
					res.Reason = fmt.Sprintf("witness %s holds newer epoch %d for %s: self-fencing", v.Witness, v.Stored.Epoch, v.Stored.WriterNode)
					return res, e.selfFence(ctx, &res)
				}
			}
			res.Reason = fmt.Sprintf("witness quorum not reached (%d/%d); self-fencing", res.Outcome.Accepted, res.Outcome.Needed)
			return res, e.selfFence(ctx, &res)
		}
	} else {
		res.Outcome = lease.Outcome{Granted: true, LocalOnly: true}
	}
	wctx, cancel := localCtx(ctx)
	defer cancel()
	if err := e.WriteLeaseFile(wctx, e.Self, res.Record); err != nil {
		res.Reason = "could not persist renewed writer lease: self-fencing"
		return res, errors.Join(fmt.Errorf("write writer.json: %w", err), e.selfFence(ctx, &res))
	}
	res.Renewed = true
	res.LeaseExpiresAt = res.Record.LeaseExpiresAt
	return res, nil
}

// localWriteTimeout bounds local writes (writer.json, fencing) independently of the
// caller's deadline, which peer probes or witnesses may have used up.
var localWriteTimeout = 5 * time.Second

func localCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), localWriteTimeout)
}

func (e *Engine) selfFence(ctx context.Context, res *RenewResult) error {
	ctx, cancel := localCtx(ctx)
	defer cancel()
	x, err := e.Exec(ctx, e.Self)
	if err != nil {
		return err
	}
	var problems []error
	if err := e.WriteLeaseFile(ctx, e.Self, lease.Record{WriterNode: e.Self, Epoch: e.Cluster.Epoch, LeaseExpiresAt: e.now()}); err != nil {
		problems = append(problems, fmt.Errorf("expire API lease: %w", err))
	}
	state, stateErr := postgresContainerState(ctx, x)
	if stateErr != nil {
		problems = append(problems, fmt.Errorf("inspect PostgreSQL container: %w", stateErr))
	}
	if state == "running" {
		if err := pgctl.SetDefaultReadOnly(ctx, x, true); err != nil {
			problems = append(problems, fmt.Errorf("set PostgreSQL read-only: %w", err))
		}
		// Read-only defaults do not revoke existing sessions. Stopping the services
		// fences current connections without creating a durable demotion marker.
		if _, err := nodeexec.Output(ctx, x, "docker stop -t 2 qabasone-postgres qabasone-minio >/dev/null"); err != nil {
			problems = append(problems, fmt.Errorf("stop writer data services: %w", err))
		}
	} else if state == "" {
		// An unknown Docker state is unsafe. Try to stop services, but never mark
		// the data directory DEMOTED on a lease failure.
		if _, err := nodeexec.Output(ctx, x, "docker stop -t 2 qabasone-postgres qabasone-minio >/dev/null"); err != nil {
			problems = append(problems, fmt.Errorf("stop writer data services: %w", err))
		}
	}
	res.Fenced = len(problems) == 0
	return errors.Join(problems...)
}

func postgresContainerState(ctx context.Context, x nodeexec.Executor) (string, error) {
	state, err := nodeexec.Output(ctx, x, "docker inspect -f '{{.State.Status}}' "+pgctl.Container)
	if err != nil {
		return "", err
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return "", fmt.Errorf("Docker returned an empty container state")
	}
	return state, nil
}

func (e *Engine) pgImageRef() string {
	if v := e.Images["POSTGRES_IMAGE"]; v != "" {
		return v
	}
	return nodestack.DefaultPostgresImage
}

// probeTimeout bounds each peer probe; an unreachable or hung promotable peer
// has unknown writer state and causes renewal to self-fence.
var probeTimeout = 4 * time.Second

// otherWriter asks every provisioned peer's PostgreSQL (over WireGuard, as the
// replication role) whether it is out of recovery, in parallel. Only a peer of this
// cluster (same system identifier as the writer, when known) counts. Returns the
// first writable peer in registry order.
func (e *Engine) otherWriter(ctx context.Context, systemID string) (string, []string) {
	pw, err := cluster.ReadSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword)
	if err != nil || pw == "" {
		return "", []string{"peer writer probes unavailable: replication password missing"}
	}
	x, err := e.Exec(ctx, e.Self)
	if err != nil {
		return "", []string{"peer writer probes unavailable: writer executor failed"}
	}
	writable := make([]bool, len(e.Cluster.Nodes))
	unknown := make([]bool, len(e.Cluster.Nodes))
	var wg sync.WaitGroup
	for i, n := range e.Cluster.Nodes {
		if n.Name == e.Self || n.WGIP == "" || !e.provisioned(n.Name) {
			continue
		}
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			writable[i], unknown[i] = probeWritableResult(ctx, x, ip, pw, systemID)
		}(i, n.WGIP)
	}
	wg.Wait()
	for i, w := range writable {
		if w {
			return e.Cluster.Nodes[i].Name, nil
		}
	}
	var warnings []string
	unknownCount := 0
	for i, bad := range unknown {
		if bad && e.Cluster.Nodes[i].Promotable {
			unknownCount++
			if len(warnings) < 3 {
				warnings = append(warnings, fmt.Sprintf("promotable peer %s PostgreSQL probe unknown; check container-to-WireGuard path with qabas dr check", e.Cluster.Nodes[i].Name))
			}
		}
	}
	if unknownCount > len(warnings) {
		warnings = append(warnings, fmt.Sprintf("%d additional promotable peer probes unknown", unknownCount-len(warnings)))
	}
	return "", warnings
}

// provisioned is false for nodes still being added or invited: whatever PostgreSQL
// answers at their address is not (yet) a member of this cluster.
func (e *Engine) provisioned(node string) bool {
	st, err := replica.LoadState(e.Paths, node)
	return err != nil || st == nil || st.Phase == replica.PhaseReady
}

func probeWritable(ctx context.Context, x nodeexec.Executor, ip, pw, systemID string) bool {
	writable, _ := probeWritableResult(ctx, x, ip, pw, systemID)
	return writable
}

func probeWritableResult(ctx context.Context, x nodeexec.Executor, ip, pw, systemID string) (bool, bool) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn := fmt.Sprintf("host=%s port=5432 dbname=postgres user=%s sslmode=require gssencmode=disable connect_timeout=2", ip, pgctl.ReplicationRole)
	inner := `read -r PGPASSWORD; export PGPASSWORD; exec psql -X -At ` + nodeexec.Quote(conn) +
		` -c ` + nodeexec.Quote(`SELECT pg_is_in_recovery(), (SELECT system_identifier FROM pg_control_system())`)
	script := `docker exec -i ` + pgctl.Container + ` sh -c ` + nodeexec.Quote(inner)
	res, err := x.Run(ctx, script, strings.NewReader(pw+"\n"))
	if err != nil || res.ExitCode != 0 {
		return false, true
	}
	inRecovery, sysID, _ := strings.Cut(strings.TrimSpace(res.Stdout), "|")
	if (inRecovery != "t" && inRecovery != "f") || sysID == "" {
		return false, true
	}
	return inRecovery == "f" && (systemID == "" || sysID == systemID), false
}

// Release expires the writer's lease immediately (planned switchover).
func (e *Engine) Release(ctx context.Context, node string, epoch int64) lease.Outcome {
	now := e.now()
	r := lease.Record{WriterNode: node, Epoch: epoch, LeaseExpiresAt: now, RenewedAt: now}
	_ = e.WriteLeaseFile(ctx, node, r)
	return lease.Acquire(ctx, e.Witnesses, r)
}

// Grant acquires a new epoch for node at the witnesses and writes its writer.json.
func (e *Engine) Grant(ctx context.Context, node string, epoch int64) (lease.Outcome, error) {
	if err := e.Cluster.ValidateLeaseMode(e.Cluster.LeaseMode); err != nil {
		return lease.Outcome{}, fmt.Errorf("refuse lease grant: %w", err)
	}
	if e.Cluster.LeaseMode == cluster.LeaseModeStrict {
		if err := e.requireDurabilityProof(ctx, node); err != nil {
			return lease.Outcome{}, fmt.Errorf("refuse writer lease grant without durability proof: %w", err)
		}
	}
	now := e.now()
	r := lease.Record{WriterNode: node, Epoch: epoch, LeaseExpiresAt: now.Add(e.ttl()), RenewedAt: now}
	if e.Cluster.LeaseMode == cluster.LeaseModeLocalOnly {
		return lease.Outcome{Granted: true, LocalOnly: true}, e.WriteLeaseFile(ctx, node, r)
	}
	out := lease.Acquire(ctx, e.Witnesses, r)
	if !out.Granted {
		return out, fmt.Errorf("witness quorum refused epoch %d for %s (%d/%d accepted)", epoch, node, out.Accepted, out.Needed)
	}
	return out, e.WriteLeaseFile(ctx, node, r)
}

// Timer units.
const (
	LeaseService = "qabas-dr-lease.service"
	LeaseTimer   = "qabas-dr-lease.timer"
	CheckService = "qabas-dr-check.service"
	CheckTimer   = "qabas-dr-check.timer"
)

func leaseUnits(binary string) (service, timer string) {
	service = `[Unit]
Description=QabasOne writer lease renewal
After=docker.service network-online.target

[Service]
Type=oneshot
ExecStart=` + binary + ` dr lease renew --quiet
TimeoutStartSec=25s
`
	timer = `[Unit]
Description=QabasOne writer lease renewal timer

[Timer]
OnBootSec=10s
OnUnitActiveSec=20s
AccuracySec=1s

[Install]
WantedBy=timers.target
`
	return service, timer
}

func checkUnits(binary string) (service, timer string) {
	service = `[Unit]
Description=QabasOne DR container path check
After=docker.service network-online.target

[Service]
Type=oneshot
ExecStart=` + binary + ` dr check
TimeoutStartSec=30s
`
	timer = `[Unit]
Description=QabasOne DR container path check timer

[Timer]
OnBootSec=30s
OnCalendar=*:0/5
AccuracySec=10s

[Install]
WantedBy=timers.target
`
	return service, timer
}

// InstallTimer installs and starts the lease timer on a node.
func (e *Engine) InstallTimer(ctx context.Context, node, binary string) error {
	if err := e.Cluster.ValidateLeaseMode(e.Cluster.LeaseMode); err != nil {
		return fmt.Errorf("refuse lease timer installation: %w", err)
	}
	x, err := e.Exec(ctx, node)
	if err != nil {
		return err
	}
	svc, tmr := leaseUnits(binary)
	if err := nodeexec.WriteFile(ctx, x, "/etc/systemd/system/"+LeaseService, []byte(svc), 0o644); err != nil {
		return err
	}
	if err := nodeexec.WriteFile(ctx, x, "/etc/systemd/system/"+LeaseTimer, []byte(tmr), 0o644); err != nil {
		return err
	}
	csvc, ctmr := checkUnits(binary)
	if err := nodeexec.WriteFile(ctx, x, "/etc/systemd/system/"+CheckService, []byte(csvc), 0o644); err != nil {
		return err
	}
	if err := nodeexec.WriteFile(ctx, x, "/etc/systemd/system/"+CheckTimer, []byte(ctmr), 0o644); err != nil {
		return err
	}
	_, err = nodeexec.Output(ctx, x, "systemctl daemon-reload && systemctl enable --now "+LeaseTimer+" "+CheckTimer+" >/dev/null")
	return err
}

// StopTimer disables the lease timer (the node stops renewing).
func (e *Engine) StopTimer(ctx context.Context, node string) error {
	x, err := e.Exec(ctx, node)
	if err != nil {
		return err
	}
	_, err = nodeexec.Output(ctx, x, "systemctl disable --now "+LeaseTimer+" >/dev/null 2>&1 || true; systemctl disable --now "+CheckTimer+" >/dev/null 2>&1 || true")
	return err
}

// InitWriterLease is the replica.AddOptions.Lease hook: first epoch, first lease,
// timer on the writer.
func (e *Engine) InitWriterLease(binary string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		w := e.Cluster.Primary()
		if w == nil {
			return fmt.Errorf("no primary in registry")
		}
		if e.Cluster.Writer == "" {
			e.Cluster.Writer = w.Name
		}
		if e.Cluster.Epoch == 0 {
			e.Cluster.Epoch = 1
		}
		if err := cluster.Save(e.Paths, e.Cluster); err != nil {
			return err
		}
		if _, err := e.Grant(ctx, w.Name, e.Cluster.Epoch); err != nil {
			return err
		}
		return e.InstallTimer(ctx, w.Name, binary)
	}
}
