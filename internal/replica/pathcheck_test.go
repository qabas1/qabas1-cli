package replica

import (
	"context"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/nodeexec"
)

func TestCheckPathsUsesDistinctContainerRoutes(t *testing.T) {
	f := newFixture(t, preflightOK)
	f.eng.Cluster.Nodes = append(f.eng.Cluster.Nodes, cluster.Node{Name: "dr1", Role: cluster.RoleStandby, WGIP: "10.66.0.2"})
	w := nodeexec.NewFake("hq").OnOut("qabas1-postgres", "10.66.0.2 via 172.21.0.1 dev eth0 src 172.21.0.2\nconnected=172.21.0.2").OnOut("qabas1-minio", "10.66.0.2 via 172.18.0.1 dev eth0 src 172.18.0.2\nconnected=172.18.0.2")
	f.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) { return w, nil }
	paths := f.eng.CheckPaths(context.Background(), "dr1")
	if len(paths) != 2 || !paths[0].OK || !paths[1].OK || paths[0].Source != "172.21.0.2" || paths[1].Source != "172.18.0.2" {
		t.Fatalf("paths: %+v", paths)
	}
	if !w.Ran("python3 -c") {
		t.Fatal("no namespace TCP probe")
	}
}

func TestVerifySkipsSentinelWhenMinioPathFails(t *testing.T) {
	f := newFixture(t, preflightOK)
	f.eng.Cluster.Nodes = append(f.eng.Cluster.Nodes, cluster.Node{Name: "dr1", Role: cluster.RoleStandby, WGIP: "10.66.0.2"})
	w := nodeexec.NewFake("hq").OnOut("qabas1-postgres", "10.66.0.2 via 172.21.0.1 dev eth0 src 172.21.0.2\nconnected=172.21.0.2").On("qabas1-minio", nodeexec.Result{ExitCode: 1, Stderr: "timed out"}).OnOut("pg_is_in_recovery()", writerRow).OnOut("_prisma_migrations", "12|abc").OnOut("{{if .State.Health}}", "healthy")
	f.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return f.replica, nil
	}
	r := f.eng.Verify(context.Background(), "dr1")
	if r.OK || !strings.Contains(r.Failures(), "sentinel skipped") {
		t.Fatalf("expected immediate path failure: %s", r.Failures())
	}
	if w.Ran("sentinel") {
		t.Fatal("sentinel should not be written when MinIO path is blocked")
	}
}
