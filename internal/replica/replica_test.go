package replica

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

const (
	testPub     = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	writerRow   = "f|off|160004|1|0/5000000||||0||7300000000000000001"
	standbyRow  = "t|on|160004|1||0/5000000|0/5000000|2026-09-30T08:00:00Z|0|streaming|7300000000000000001"
	healthJSON  = `{"success":true,"data":{"mode":"DR_READ_ONLY","readOnly":true,"node":"dr1","release":"1.0.9"}}`
	preflightOK = "os=Linux\narch=x86_64\ndocker=27.1.0\ncompose=2.29.0\nsystemd=yes\ndiskfree_kb=104857600\nstack=\npgvolume=\nqabas=/usr/bin/qabas\n"
)

type fixture struct {
	eng     *Engine
	writer  *nodeexec.Fake
	replica *nodeexec.Fake
	p       paths.Paths
}

func newFixture(t *testing.T, preflight string) *fixture {
	t.Helper()
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc"), StateDir: filepath.Join(dir, "state"), LogDir: dir, LockPath: filepath.Join(dir, "lock")}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(p.ConfigDir, 0o755))
	must(os.WriteFile(filepath.Join(p.ConfigDir, ".env"), []byte("POSTGRES_USER=qabasone\nMINIO_ROOT_USER=writerroot\nMINIO_ROOT_PASSWORD=writerrootpass\nAPI_BIND=127.0.0.1:3030\n"), 0o600))
	must(os.WriteFile(filepath.Join(p.ConfigDir, ".env.api"), []byte("JWT_SECRET=jwt\nSTORAGE_ACCESS_KEY_ID=appkey\nSTORAGE_SECRET_ACCESS_KEY=appsecret\nSTORAGE_BUCKET=qabasone\n"), 0o600))

	c := &cluster.Cluster{Name: "t", Writer: "hq", Epoch: 1, Nodes: []cluster.Node{{
		Name: "hq", Role: cluster.RolePrimary, Host: "localhost", WGIP: "10.66.0.1", WGEndpoint: "hq.example.com:51820", Promotable: true,
	}}}
	must(cluster.Save(p, c))
	c, err := cluster.Load(p)
	must(err)

	w := nodeexec.NewFake("hq").
		OnOut("nsenter -t", "10.66.0.2 via 172.21.0.1 dev eth0 src 172.21.0.2\nconnected=172.21.0.2\n").
		On("CREATE TABLE qabas_verify_write_probe", nodeexec.Result{}).
		OnOut("_prisma_migrations", "12|abc").
		OnOut("pg_database_size", "1073741824").
		OnOut("pg_is_in_recovery()", writerRow).
		OnOut("wg pubkey", testPub).
		OnOut("uname -m", "x86_64").
		OnOut("{{if .State.Health}}", "healthy").
		OnOut("docker ps -a --format '{{.Names}}'", "qabasone-api\n")
	r := nodeexec.NewFake("dr1").
		OnOut("diskfree_kb", preflight).
		On("CREATE TABLE qabas_verify_write_probe", nodeexec.Result{ExitCode: 1, Stderr: "ERROR:  cannot execute CREATE TABLE in a read-only transaction"}).
		OnOut("_prisma_migrations", "12|abc").
		OnOut("pg_is_in_recovery()", standbyRow).
		OnOut("wg pubkey", testPub).
		OnOut("{{if .State.Health}}", "healthy").
		OnOut("health/dr", healthJSON).
		OnOut("docker ps -a --format '{{.Names}}'", "qabasone-api\n").
		OnOut("docker ps --format", "qabasone-api\nqabasone-postgres\nqabasone-minio").
		OnOut("{{.Names}}|{{.Status}}", "qabasone-api|Up 2 minutes (healthy)\nqabasone-postgres|Up 3 minutes (healthy)\nqabasone-minio-init|Exited (0) 3 minutes ago")

	eng := &Engine{
		Paths: p, Cluster: c, Self: "hq",
		Connect: func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
			if n.Name == "hq" {
				return w, nil
			}
			return r, nil
		},
		Fetch:  func(name string) ([]byte, error) { return []byte("# " + name + "\n"), nil },
		Images: map[string]string{"POSTGRES_IMAGE": "postgres:16-alpine@sha256:abc"},
	}
	return &fixture{eng: eng, writer: w, replica: r, p: p}
}

func TestAddProvisionsReadOnlyReplica(t *testing.T) {
	fx := newFixture(t, preflightOK)
	confirmed, leased := false, false
	rep, err := fx.eng.Add(context.Background(), AddOptions{
		Node:    cluster.Node{Name: "dr1", Host: "203.0.113.10", SSHHostKey: "SHA256:x", Site: "branch", Promotable: true, WGEndpoint: "203.0.113.10:51820"},
		Confirm: func(string) error { confirmed = true; return nil },
		Lease:   func(context.Context) error { leased = true; return nil },
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !rep.OK {
		t.Fatalf("verify not OK:\n%s", rep.Failures())
	}
	if !confirmed || !leased {
		t.Fatalf("writer enable must confirm (%v) and write the lease (%v) first", confirmed, leased)
	}
	st, _ := LoadState(fx.p, "dr1")
	if st == nil || st.Phase != PhaseReady {
		t.Fatalf("state = %+v", st)
	}
	c, err := cluster.Load(fx.p)
	if err != nil {
		t.Fatal(err)
	}
	n := c.Node("dr1")
	if n == nil || n.WGIP != "10.66.0.2" || n.WGPublicKey != testPub || n.Role != cluster.RoleStandby {
		t.Fatalf("registry node = %+v", n)
	}

	files := fx.replica.Files
	if !strings.Contains(files["/var/lib/qabasone/compose/overlays"], "file:compose.replica-ro.yaml") ||
		strings.Contains(files["/var/lib/qabasone/compose/overlays"], "profile:writers") {
		t.Fatalf("replica overlays wrong:\n%s", files["/var/lib/qabasone/compose/overlays"])
	}
	for _, f := range []string{"compose.yaml", "compose.replica-ro.yaml", "compose.dr-primary.yaml", "compose.primary-replication.yaml", "compose.gateway.yaml"} {
		if _, ok := files["/var/lib/qabasone/compose/"+f]; !ok {
			t.Errorf("replica missing %s (promotion must work offline)", f)
		}
	}
	if !strings.Contains(files["/etc/qabasone/dr/node.env"], "QABAS_SITE_ROLE=standby") {
		t.Fatalf("node.env = %q", files["/etc/qabasone/dr/node.env"])
	}
	env := files["/etc/qabasone/.env"]
	if strings.Contains(env, "writerroot") || !strings.Contains(env, "MINIO_ROOT_USER=qabas-") || !strings.Contains(env, "POSTGRES_USER=qabasone") {
		t.Fatalf("replica .env must keep settings but get its own MinIO root:\n%s", env)
	}
	for _, fk := range []*nodeexec.Fake{fx.writer, fx.replica} {
		l := fx.eng.Layout(fk.Name)
		if !fk.Ran("chmod 0755 " + nodeexec.Quote(l.PGConfDir) + " " + nodeexec.Quote(l.TLSDir()) + " " + nodeexec.Quote(l.DRDir)) {
			t.Fatalf("%s: container-mounted config dirs not made traversable", fk.Name)
		}
		if !fk.Ran("docker network create edge-network") {
			t.Fatalf("%s: external edge-network not ensured", fk.Name)
		}
	}
	if !fx.writer.Ran("pg_create_physical_replication_slot('qabas_replica_dr1'") {
		t.Fatal("slot not created on the writer")
	}
	if !fx.replica.Ran("pg_basebackup") || !fx.replica.Ran("primary_slot_name = 'qabas_replica_dr1'") {
		t.Fatal("replica not seeded / conninfo not pinned")
	}
	if !fx.writer.Ran("mc replicate add") {
		t.Fatal("minio replication rule not added on the writer")
	}

	// Secrets must never appear in a script argument (argv / process list).
	repl, _ := cluster.ReadSecret(fx.p, cluster.SharedScope, cluster.SecretReplicationPassword)
	replSecret, _ := cluster.ReadSecret(fx.p, "dr1", cluster.SecretMinioReplSecretKey)
	for _, fk := range []*nodeexec.Fake{fx.writer, fx.replica} {
		for _, s := range fk.Scripts {
			for _, secret := range []string{repl, replSecret, "writerrootpass", "appsecret"} {
				if secret != "" && strings.Contains(s, secret) {
					t.Fatalf("secret leaked into a %s script argument:\n%s", fk.Name, s)
				}
			}
		}
	}
}

func TestAddResumeRefreshesStorageLink(t *testing.T) {
	fx := newFixture(t, preflightOK)
	node := cluster.Node{Name: "dr1", Host: "203.0.113.10", SSHHostKey: "SHA256:x", Site: "branch"}
	if _, err := fx.eng.Add(context.Background(), AddOptions{
		Node: node, Confirm: func(string) error { return nil }, Lease: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("initial Add: %v", err)
	}

	before := len(fx.writer.Scripts)
	if _, err := fx.eng.Add(context.Background(), AddOptions{Node: node, Resume: true, RefreshStorage: true}); err != nil {
		t.Fatalf("resume with storage refresh: %v", err)
	}
	var unlink, relink bool
	for _, script := range fx.writer.Scripts[before:] {
		if strings.Contains(script, "mc replicate rm") {
			unlink = true
		}
		if strings.Contains(script, "mc replicate add") {
			relink = true
		}
	}
	if !unlink || !relink {
		t.Fatalf("refresh did not unlink and relink MinIO rule (unlink=%v relink=%v)", unlink, relink)
	}
}

func TestRefreshStorageRequiresResume(t *testing.T) {
	fx := newFixture(t, preflightOK)
	_, err := fx.eng.Add(context.Background(), AddOptions{
		Node:           cluster.Node{Name: "dr1", Host: "203.0.113.10"},
		RefreshStorage: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--refresh-storage requires --resume") {
		t.Fatalf("err = %v", err)
	}
	if len(fx.writer.Scripts) != 0 || len(fx.replica.Scripts) != 0 {
		t.Fatal("invalid option combination contacted a node")
	}
}

func TestPreflightFailureLeavesReplicaUntouched(t *testing.T) {
	fx := newFixture(t, strings.Replace(preflightOK, "docker=27.1.0", "docker=", 1))
	_, err := fx.eng.Add(context.Background(), AddOptions{Node: cluster.Node{Name: "dr1", Host: "203.0.113.10"}})
	if err == nil || !errors.Is(err, ErrNotModified) || !strings.Contains(err.Error(), "Docker Engine") {
		t.Fatalf("err = %v", err)
	}
	if len(fx.replica.Files) != 0 {
		t.Fatalf("preflight wrote files: %v", fx.replica.Files)
	}
	if c, _ := cluster.Load(fx.p); c.Node("dr1") != nil {
		t.Fatal("failed preflight must not register the node")
	}
	if st, _ := LoadState(fx.p, "dr1"); st != nil {
		t.Fatal("failed preflight must not leave provisioning state")
	}
}

func TestAddRefusesSilentRestartWithoutResume(t *testing.T) {
	fx := newFixture(t, preflightOK)
	if err := SaveState(fx.p, &State{Node: "dr1", Phase: PhaseSeed, Completed: []Phase{PhasePreflight, PhaseNetwork, PhaseUpstream, PhaseStack}, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	_, err := fx.eng.Add(context.Background(), AddOptions{Node: cluster.Node{Name: "dr1", Host: "h"}})
	if err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("err = %v", err)
	}
}

func TestAddResumeRefreshesUpstreamBeforeSeed(t *testing.T) {
	fx := newFixture(t, preflightOK)
	if err := SaveState(fx.p, &State{
		Node: "dr1", Phase: PhaseStack,
		Completed: []Phase{PhasePreflight, PhaseNetwork, PhaseUpstream, PhaseStack},
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	w := &hookExec{Fake: fx.writer}
	w.hook = func(script string) (nodeexec.Result, bool) {
		if strings.Contains(script, ".NetworkSettings.Networks") {
			return nodeexec.Result{Stdout: "172.21.0.1"}, true
		}
		return nodeexec.Result{}, false
	}
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return fx.replica, nil
	}
	_, err := fx.eng.Add(context.Background(), AddOptions{
		Node:   cluster.Node{Name: "dr1", Host: "203.0.113.10", SSHHostKey: "SHA256:x"},
		Resume: true,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !fx.writer.Ran(".NetworkSettings.Networks") {
		t.Fatal("resuming before seed did not refresh the writer's pg_hba")
	}
}

func TestRenderWGMesh(t *testing.T) {
	fx := newFixture(t, preflightOK)
	c := fx.eng.Cluster
	c.Nodes[0].WGPublicKey = testPub
	c.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, WGIP: "10.66.0.2", WGPublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", Mode: cluster.ModePull})
	conf, err := fx.eng.RenderWG("dr1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Address = 10.66.0.2/24", "PrivateKey = @@QABAS_WG_PRIVATE_KEY@@", "Endpoint = hq.example.com:51820", "AllowedIPs = 10.66.0.1/32", "PersistentKeepalive = 25"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in:\n%s", want, conf)
		}
	}
}

func TestWriteHBAUsesPostgresContainerGateway(t *testing.T) {
	fx := newFixture(t, preflightOK)
	w := &hookExec{Fake: fx.writer}
	queryCount := 0
	w.hook = func(script string) (nodeexec.Result, bool) {
		switch {
		case strings.Contains(script, ".NetworkSettings.Networks"):
			return nodeexec.Result{Stdout: "172.21.0.1"}, true
		case strings.Contains(script, ".IPAM.Config"):
			return nodeexec.Result{Stdout: "172.21.0.0/16"}, true
		case strings.Contains(script, ".State.Running"):
			return nodeexec.Result{Stdout: "true"}, true
		case strings.Contains(script, "docker exec -i qabasone-postgres"):
			queryCount++
			if queryCount == 1 {
				return nodeexec.Result{Stdout: "t"}, true
			}
			return nodeexec.Result{Stdout: "1"}, true
		default:
			return nodeexec.Result{}, false
		}
	}
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return fx.replica, nil
	}
	if err := fx.eng.WriteHBA(context.Background(), "hq"); err != nil {
		t.Fatal(err)
	}
	if queryCount != 2 {
		t.Fatalf("pg_hba reload and verification queries = %d, want 2", queryCount)
	}
	for _, body := range fx.writer.Files {
		if strings.Contains(body, "hostssl replication  qabas_replica") &&
			strings.Contains(body, "172.21.0.1/32") {
			return
		}
	}
	t.Fatal("pg_hba.conf does not allow replication through the PostgreSQL container gateway")
}

func TestLinkStorageFailsFastWhenSourceMinIONetworkIsBlocked(t *testing.T) {
	fx := newFixture(t, preflightOK)
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, WGIP: "10.66.0.2"})
	w := &hookExec{Fake: fx.writer}
	w.hook = func(script string) (nodeexec.Result, bool) {
		if strings.Contains(script, "qabasone-minio") && strings.Contains(script, "docker inspect") {
			return nodeexec.Result{ExitCode: 1, Stderr: "connection timed out"}, true
		}
		return nodeexec.Result{}, false
	}
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return fx.replica, nil
	}
	err := fx.eng.LinkStorage(context.Background(), "hq", "dr1", 1)
	if err == nil || !strings.Contains(err.Error(), PathGuidance) {
		t.Fatalf("blocked source path should fail with firewall guidance, got %v", err)
	}
	if strings.Contains(err.Error(), "minio destination") || strings.Contains(err.Error(), "minio replication") {
		t.Fatalf("network path should be checked before MC setup: %v", err)
	}
}
