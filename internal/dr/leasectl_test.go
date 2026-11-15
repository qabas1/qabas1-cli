package dr

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/lease"
	"github.com/qabas1/qabasone-cli/internal/nodeexec"
	"github.com/qabas1/qabasone-cli/internal/replica"
)

// ctxExec behaves like nodeexec.Local: it fails once ctx is done, and scripts that
// contain hang block until then (a peer behind a dead tunnel).
type ctxExec struct {
	*nodeexec.Fake
	hang string
}

func (c ctxExec) Run(ctx context.Context, script string, stdin io.Reader) (nodeexec.Result, error) {
	if c.hang != "" && strings.Contains(script, c.hang) {
		<-ctx.Done()
		return nodeexec.Result{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nodeexec.Result{}, err
	}
	return c.Fake.Run(ctx, script, stdin)
}

// leaseWriter is hq as the renewing writer (system identifier 73) with the shared
// replication password present, so peers are probed.
func leaseWriter(t *testing.T, hq *nodeexec.Fake, hang string) *Engine {
	t.Helper()
	hq.OnOut("docker inspect -f", "running\n")
	e := newDR(t, nil, nil)
	e.Self = "hq"
	for _, name := range []string{"a", "b", "c"} {
		e.Witnesses = append(e.Witnesses, &lease.FileWitness{Label: name, Path: filepath.Join(e.Paths.StateDir, name+".json")})
	}
	e.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		return ctxExec{Fake: hq, hang: hang}, nil
	}
	if _, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword); err != nil {
		t.Fatal(err)
	}
	return e
}

func shortProbes(t *testing.T, d time.Duration) {
	old := probeTimeout
	probeTimeout = d
	t.Cleanup(func() { probeTimeout = old })
}

func TestRenewFailsClosedOnHungPeerProbe(t *testing.T) {
	shortProbes(t, time.Second)
	hq := nodeexec.NewFake("hq").OnOut("pg_is_in_recovery()", writerRow)
	e := leaseWriter(t, hq, "host=10.66.0.2 ")
	// The caller's budget runs out while the probe hangs: the writer fences.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := e.Renew(ctx)
	if err != nil || res.Renewed || !res.Fenced {
		t.Fatalf("hung peer must fence: %+v %v", res, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("renew took %v", time.Since(start))
	}
	if !strings.Contains(hq.Files[e.Layout("hq").WriterFile()], `"writerNode": "hq"`) || !hq.Ran("default_transaction_read_only = on") || !hq.Ran("docker stop -t 2 qabasone-postgres qabasone-minio") {
		t.Fatal("writer was not fenced")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("unknown promotable peer should produce a warning")
	}
}

func TestRenewFailsClosedWithoutWitnessQuorum(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("pg_is_in_recovery()", writerRow)
	e := leaseWriter(t, hq, "")
	e.Witnesses = nil
	res, err := e.Renew(context.Background())
	if err != nil || res.Renewed || !res.Fenced || !strings.Contains(res.Reason, "three independent") {
		t.Fatalf("missing witnesses must fence: %+v %v", res, err)
	}
	if hq.Ran("host=10.66.0.") {
		t.Fatal("must fail before peer probes")
	}
}

func TestRenewStoppedPostgresExpiresLeaseWithoutDemotion(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("docker inspect -f", "exited\n")
	e := leaseWriter(t, hq, "")
	res, err := e.Renew(context.Background())
	if err != nil || !res.Fenced || !strings.Contains(res.Reason, "container exited") {
		t.Fatalf("stopped writer must have a causal failure: %+v %v", res, err)
	}
	if hq.Ran("pg_is_in_recovery()") || hq.Ran("default_transaction_read_only = on") || hq.Ran("docker stop -t 2") || hq.Ran("qabas.DEMOTED") {
		t.Fatal("stopped container must not receive SQL, stop, or demotion operations")
	}
	if !strings.Contains(hq.Files[e.Layout("hq").WriterFile()], `"writerNode": "hq"`) {
		t.Fatal("local API lease was not expired")
	}
}

func TestRenewLocalOnlyRequiresExplicitMode(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("pg_is_in_recovery()", writerRow)
	e := leaseWriter(t, hq, "")
	e.Witnesses = nil
	e.Cluster.Witnesses = nil
	e.Cluster.LeaseMode = cluster.LeaseModeLocalOnly
	e.DurabilityProof = nil
	res, err := e.Renew(context.Background())
	if err != nil || !res.Renewed || !res.Outcome.LocalOnly || !res.Outcome.Granted {
		t.Fatalf("explicit local-only renewal: %+v %v", res, err)
	}
	if hq.Ran("host=10.66.0.") {
		t.Fatal("local-only renewal probed non-voting peers")
	}
	e.Cluster.LeaseMode = ""
	res, err = e.Renew(context.Background())
	if err != nil || res.Renewed || !res.Fenced || !strings.Contains(res.Reason, "lease policy invalid") {
		t.Fatalf("legacy implicit mode must fence: %+v %v", res, err)
	}
}

func TestRenewFailsClosedWithoutDurabilityProof(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("pg_is_in_recovery()", writerRow)
	e := leaseWriter(t, hq, "")
	e.DurabilityProof = nil
	res, err := e.Renew(context.Background())
	if err != nil || res.Renewed || !res.Fenced || !strings.Contains(res.Reason, "durability proof unavailable") {
		t.Fatalf("missing durability proof must fence: %+v %v", res, err)
	}
}

func TestTimerInstallationIncludesReadOnlyCheck(t *testing.T) {
	hq := nodeexec.NewFake("hq")
	e := leaseWriter(t, hq, "")
	if err := e.InstallTimer(context.Background(), "hq", "/usr/bin/qabas"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hq.Files["/etc/systemd/system/"+CheckService], "dr check") || !strings.Contains(hq.Files["/etc/systemd/system/"+CheckTimer], "OnCalendar=*:0/5") {
		t.Fatal("check units missing")
	}
	if !hq.Ran("systemctl enable --now " + LeaseTimer + " " + CheckTimer) {
		t.Fatal("both timers must be enabled")
	}
	if err := e.StopTimer(context.Background(), "hq"); err != nil {
		t.Fatal(err)
	}
	if !hq.Ran("systemctl disable --now "+LeaseTimer) || !hq.Ran("systemctl disable --now "+CheckTimer) {
		t.Fatal("both timers must stop on writer transition")
	}
}

func TestPeerProbeQuotesConninfoForContainerShell(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("host=10.66.0.2 ", "t|73")
	probeWritable(context.Background(), hq, "10.66.0.2", "secret-password", "73")
	if len(hq.Scripts) != 1 {
		t.Fatalf("expected one probe, got %d", len(hq.Scripts))
	}
	inner := `read -r PGPASSWORD; export PGPASSWORD; exec psql -X -At ` +
		nodeexec.Quote("host=10.66.0.2 port=5432 dbname=postgres user=qabas_replica sslmode=require gssencmode=disable connect_timeout=2") +
		` -c ` + nodeexec.Quote(`SELECT pg_is_in_recovery(), (SELECT system_identifier FROM pg_control_system())`)
	want := `docker exec -i qabasone-postgres sh -c ` + nodeexec.Quote(inner)
	if hq.Scripts[0] != want {
		t.Fatalf("probe must pass the full conninfo and SQL as one shell argument:\n%s", hq.Scripts[0])
	}
	if strings.Contains(hq.Scripts[0], "secret-password") {
		t.Fatal("replication password leaked into process arguments")
	}
}

func TestPeerProbeMalformedResultIsUnknown(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("host=10.66.0.2 ", "invalid-row")
	writable, unknown := probeWritableResult(context.Background(), hq, "10.66.0.2", "secret-password", "73")
	if writable || !unknown {
		t.Fatalf("malformed peer result: writable=%v unknown=%v", writable, unknown)
	}
}

func TestCheckWarnsOnLocalOnlyLease(t *testing.T) {
	hq := nodeexec.NewFake("hq").OnOut("nsenter -t", "10.66.0.2 via 172.21.0.1 dev eth0 src 172.21.0.2\nconnected=172.21.0.2")
	e := leaseWriter(t, hq, "")
	e.Witnesses = nil
	r := e.Check(context.Background())
	if r.OK || len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "three independent") {
		t.Fatalf("missing strict witness failure: %+v", r)
	}
}

func TestRenewProbesPeersInParallel(t *testing.T) {
	shortProbes(t, 300*time.Millisecond)
	hq := nodeexec.NewFake("hq").OnOut("pg_is_in_recovery()", writerRow)
	e := leaseWriter(t, hq, "host=10.66.0.")
	start := time.Now()
	res, err := e.Renew(context.Background())
	if err != nil || res.Renewed || !res.Fenced {
		t.Fatalf("unknown peers must fence: %+v %v", res, err)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("three hung peers took %v; probes must run in parallel", d)
	}
}

func TestRenewFencesOnlyForSameCluster(t *testing.T) {
	foreign := nodeexec.NewFake("hq").OnOut("host=10.66.0.2 ", "f|99").OnOut("host=10.66.0.", "t|73").OnOut("pg_is_in_recovery()", writerRow)
	e := leaseWriter(t, foreign, "")
	res, err := e.Renew(context.Background())
	if err != nil || !res.Renewed || res.Fenced {
		t.Fatalf("a writable PostgreSQL of another cluster must not fence: %+v %v", res, err)
	}
	if foreign.Ran("default_transaction_read_only = on") {
		t.Fatal("fenced for a foreign database")
	}

	same := nodeexec.NewFake("hq").OnOut("host=10.66.0.2 ", "f|73").OnOut("host=10.66.0.", "t|73").OnOut("pg_is_in_recovery()", writerRow)
	e = leaseWriter(t, same, "")
	res, err = e.Renew(context.Background())
	if err != nil || !res.Fenced || !strings.Contains(res.Reason, "dr1") {
		t.Fatalf("a second writer of this cluster must fence: %+v %v", res, err)
	}
}

func TestRenewSkipsUnprovisionedNodes(t *testing.T) {
	for _, phase := range []replica.Phase{replica.PhaseStack, replica.PhaseInvited} {
		hq := nodeexec.NewFake("hq").OnOut("host=10.66.0.3 ", "f|73").OnOut("host=10.66.0.", "t|73").OnOut("pg_is_in_recovery()", writerRow)
		e := leaseWriter(t, hq, "")
		if err := replica.SaveState(e.Paths, &replica.State{Node: "dr2", Phase: phase}); err != nil {
			t.Fatal(err)
		}
		res, err := e.Renew(context.Background())
		if err != nil || !res.Renewed || res.Fenced {
			t.Fatalf("%s: node still provisioning must not fence: %+v %v", phase, res, err)
		}
		if hq.Ran("host=10.66.0.3 ") {
			t.Fatalf("%s: unprovisioned node was probed", phase)
		}
		if !hq.Ran("host=10.66.0.2 ") {
			t.Fatalf("%s: provisioned peers must still be probed", phase)
		}
	}
}
