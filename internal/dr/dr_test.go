package dr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/lease"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/replica"
)

func testCluster() *cluster.Cluster {
	return &cluster.Cluster{SchemaVersion: 1, Name: "t", Writer: "hq", Epoch: 3, WGNetwork: "10.66.0.0/24", LeaseMode: cluster.LeaseModeStrict,
		Witnesses: []cluster.Witness{
			{Name: "a", Kind: "file", Path: "/witness/a", FailureDomain: "a"},
			{Name: "b", Kind: "file", Path: "/witness/b", FailureDomain: "b"},
			{Name: "c", Kind: "file", Path: "/witness/c", FailureDomain: "c"},
		}, Nodes: []cluster.Node{
			{Name: "hq", Site: "hq", Role: cluster.RolePrimary, WGIP: "10.66.0.1", Mode: "push", Promotable: true},
			{Name: "dr1", Site: "branch", Role: cluster.RoleStandby, WGIP: "10.66.0.2", Mode: "push", Promotable: true, Priority: 10},
			{Name: "dr2", Site: "branch", Role: cluster.RoleStandby, WGIP: "10.66.0.3", Mode: "push", Promotable: true, Priority: 50},
			{Name: "kiosk", Site: "shop", Role: cluster.RoleStandby, WGIP: "10.66.0.4", Mode: "pull", Promotable: false},
		}}
}

func standby(name, lsn string) replica.NodeStatus {
	return replica.NodeStatus{Name: name, Role: cluster.RoleStandby, Reachable: true, Postgres: &pgctl.Status{InRecovery: true, ReplayLSN: lsn}}
}

func TestRankCandidatesPrefersMostDataThenPriority(t *testing.T) {
	c := testCluster()
	nodes := []replica.NodeStatus{standby("dr1", "0/9000000"), standby("dr2", "0/8000000"), standby("kiosk", "0/F000000")}
	got := rankCandidates(c, nodes)
	if len(got) != 2 || got[0].Node != "dr1" || got[1].Node != "dr2" {
		t.Fatalf("rank = %+v (kiosk is not promotable; dr1 has more WAL)", got)
	}
	nodes[0].Postgres.ReplayLSN = "0/8000000"
	got = rankCandidates(c, nodes)
	if got[0].Node != "dr2" {
		t.Fatalf("equal LSN must fall back to priority: %+v", got)
	}
}

func TestVerdicts(t *testing.T) {
	base := Facts{Self: "dr1", Writer: "hq", Epoch: 3, Nodes: []replica.NodeStatus{{Name: "hq"}, {Name: "dr1", Reachable: true}, {Name: "dr2", Reachable: true}}}
	cases := []struct {
		name string
		mod  func(*Facts)
		want string
	}{
		{"healthy", func(f *Facts) { f.WriterReachable = true }, VerdictHealthy},
		{"partition", func(f *Facts) { f.WriterSeenBy = []string{"dr2"} }, VerdictPartition},
		{"lease held", func(f *Facts) {
			f.LeaseValidQuorum, f.LeaseHolder = true, "hq"
			f.Lease = lease.Record{WriterNode: "hq", LeaseExpiresAt: time.Now().Add(time.Minute)}
		}, VerdictLeaseHeld},
		{"no candidate", func(f *Facts) {}, VerdictNoCandidate},
		{"writer down", func(f *Facts) {
			f.WitnessCount = 3
			f.Candidates = []Candidate{{Node: "dr1", ReplayLSN: "0/1"}}
		}, VerdictWriterDown},
		{"isolated", func(f *Facts) {
			f.Nodes = []replica.NodeStatus{{Name: "hq"}, {Name: "dr1", Reachable: true}, {Name: "dr2"}}
			f.Candidates = []Candidate{{Node: "dr1"}}
		}, VerdictThisSiteOffline},
	}
	for _, c := range cases {
		f := base
		f.Nodes = append([]replica.NodeStatus(nil), base.Nodes...)
		c.mod(&f)
		if code, msg := verdict(f); code != c.want {
			t.Errorf("%s: got %s (%s)", c.name, code, msg)
		}
	}
}

func TestVerdictRefusesPromotionWithUndersizedWitnessSet(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		f := Facts{Self: "dr1", Writer: "hq", WitnessCount: count,
			Nodes:      []replica.NodeStatus{{Name: "hq"}, {Name: "dr1", Reachable: true}, {Name: "dr2", Reachable: true}},
			Candidates: []Candidate{{Node: "dr1", ReplayLSN: "0/1"}}}
		_, msg := verdict(f)
		if !strings.Contains(msg, "promotion is refused") || strings.Contains(msg, "qabas dr promote") {
			t.Errorf("%d witnesses: %s", count, msg)
		}
	}
}

func TestRenderHAProxy(t *testing.T) {
	cfg, err := RenderHAProxy(testCluster(), RoutingOptions{Site: "branch", HealthHost: "api.qabas.one"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server hq 10.66.0.1:443 check\n", "server dr2 10.66.0.3:443 check backup", "server dr1 10.66.0.2:443 check backup", "uri /health/ready", "sni api.qabas.one"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("missing %q:\n%s", want, cfg)
		}
	}
	if strings.Index(cfg, "server dr2") > strings.Index(cfg, "server dr1") {
		t.Error("higher-priority standby must be listed first")
	}
	if strings.Contains(cfg, "kiosk") {
		t.Error("other sites' standbys must not be backends")
	}
}

func newDR(t *testing.T, fakes map[string]*nodeexec.Fake, ws []lease.Witness) *Engine {
	t.Helper()
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state")}
	_ = os.MkdirAll(p.ConfigDir, 0o755)
	c := testCluster()
	return &Engine{Engine: &replica.Engine{
		Paths: p, Cluster: c, Self: "dr1",
		Connect: func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
			if f, ok := fakes[n.Name]; ok {
				return f, nil
			}
			return nil, errors.New("unreachable")
		},
	}, Witnesses: ws, DurabilityProof: func(context.Context, string) error { return nil }}
}

func TestDurabilityProofIsRequired(t *testing.T) {
	e := newDR(t, nil, nil)
	e.DurabilityProof = nil
	if err := e.requireDurabilityProof(context.Background(), "dr1"); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("missing durability proof must fail closed: %v", err)
	}
}

const standbyRow = "t|on|160004|3||0/9000000|0/9000000|2026-09-30T08:00:00Z|0|streaming|73"
const writerRow = "f|off|160004|3|0/9000000||||0||73"

func TestPromoteRefusals(t *testing.T) {
	dr1 := nodeexec.NewFake("dr1").OnOut("pg_is_in_recovery()", standbyRow)
	ctx := context.Background()

	e := newDR(t, map[string]*nodeexec.Fake{"dr1": dr1}, nil)
	if _, err := e.Promote(ctx, "dr1", PromoteOptions{BreakGlass: true, ConfirmName: "dr1"}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "three independent") {
		t.Fatalf("missing witnesses: %v", err)
	}
	dir := t.TempDir()
	var ws []lease.Witness
	for _, n := range []string{"a", "b", "c"} {
		ws = append(ws, &lease.FileWitness{Label: n, Path: filepath.Join(dir, n+".json")})
	}
	e.Witnesses = ws
	if _, err := e.Promote(ctx, "dr1", PromoteOptions{ConfirmName: "dr1"}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "--break-glass") {
		t.Fatalf("missing break-glass: %v", err)
	}
	if _, err := e.Promote(ctx, "dr1", PromoteOptions{BreakGlass: true, ConfirmName: "dr2"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("wrong typed name: %v", err)
	}
	if _, err := e.Promote(ctx, "kiosk", PromoteOptions{BreakGlass: true, ConfirmName: "kiosk"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("unpromotable: %v", err)
	}

	hq := nodeexec.NewFake("hq").OnOut("docker inspect -f", "running\n").OnOut("pg_is_in_recovery()", writerRow)
	e = newDR(t, map[string]*nodeexec.Fake{"dr1": dr1, "hq": hq}, ws)
	if _, err := e.Promote(ctx, "dr1", PromoteOptions{BreakGlass: true, ConfirmName: "dr1"}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "switchover") {
		t.Fatalf("reachable writer must refuse: %v", err)
	}

	// Writer down but a witness majority still holds its lease.
	dir = t.TempDir()
	ws = nil
	for _, n := range []string{"a", "b", "c"} {
		w := &lease.FileWitness{Label: n, Path: filepath.Join(dir, n+".json")}
		_, _, _ = w.Propose(ctx, lease.Record{WriterNode: "hq", Epoch: 3, LeaseExpiresAt: time.Now().Add(time.Minute)})
		ws = append(ws, w)
	}
	e = newDR(t, map[string]*nodeexec.Fake{"dr1": dr1}, ws)
	if _, err := e.Promote(ctx, "dr1", PromoteOptions{BreakGlass: true, ConfirmName: "dr1"}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("live lease must refuse: %v", err)
	}
	if dr1.Ran("pg_promote") {
		t.Fatal("refusals must never promote")
	}
}

func TestSwitchoverRequiresWitnessesBeforeMutating(t *testing.T) {
	e := newDR(t, nil, nil)
	e.Self = "hq"
	if _, err := e.Switchover(context.Background(), "dr1", SwitchoverOptions{}); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "three independent") {
		t.Fatalf("switchover should refuse before fencing old writer: %v", err)
	}
}

func TestRenewSelfFencesOnNewerEpoch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var ws []lease.Witness
	for _, n := range []string{"a", "b", "c"} {
		w := &lease.FileWitness{Label: n, Path: filepath.Join(dir, n+".json")}
		_, _, _ = w.Propose(ctx, lease.Record{WriterNode: "dr1", Epoch: 4, LeaseExpiresAt: time.Now().Add(time.Minute)})
		ws = append(ws, w)
	}
	hq := nodeexec.NewFake("hq").OnOut("docker inspect -f", "running\n").OnOut("pg_is_in_recovery()", writerRow)
	e := newDR(t, map[string]*nodeexec.Fake{"hq": hq}, ws)
	e.Self = "hq"
	if _, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword); err != nil {
		t.Fatal(err)
	}
	res, err := e.Renew(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Renewed || !res.Fenced {
		t.Fatalf("old writer must self-fence: %+v", res)
	}
	if !hq.Ran("default_transaction_read_only = on") || hq.Ran(pgctl.DemotedMarker) {
		t.Fatal("self-fence must set read-only without permanently demoting the writer")
	}
}

func TestRenewGrantsWithQuorum(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ws := []lease.Witness{
		&lease.FileWitness{Label: "a", Path: filepath.Join(dir, "a.json")},
		&lease.FileWitness{Label: "b", Path: filepath.Join(dir, "b.json")},
		&lease.FileWitness{Label: "c", Path: filepath.Join(dir, "c.json")},
	}
	hq := nodeexec.NewFake("hq").OnOut("docker inspect -f", "running\n").OnOut("host=10.66.0.", "t|73").OnOut("pg_is_in_recovery()", writerRow)
	e := newDR(t, map[string]*nodeexec.Fake{"hq": hq}, ws)
	e.Self = "hq"
	if _, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, cluster.SecretReplicationPassword); err != nil {
		t.Fatal(err)
	}
	res, err := e.Renew(ctx)
	if err != nil || !res.Renewed {
		t.Fatalf("renew: %+v %v", res, err)
	}
	if !strings.Contains(hq.Files[e.Layout("hq").WriterFile()], `"writerNode": "hq"`) {
		t.Fatalf("writer.json not written: %v", hq.Files)
	}
}
