// Package replica provisions and operates read-only physical standbys ("replicas") of
// the QabasOne writer: WireGuard mesh, PostgreSQL streaming replication, one-way MinIO
// bucket replication, and the replica-ro compose profile. It never changes which node
// is the writer; that is `qabas dr` (package dr).
package replica

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/nodeexec"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

// Connector opens an executor for a registered node (Local for this machine).
type Connector func(ctx context.Context, n cluster.Node) (nodeexec.Executor, error)

// Engine holds everything replica operations need. The control node is the writer.
type Engine struct {
	Paths   paths.Paths
	Cluster *cluster.Cluster
	// Self is this machine's node name.
	Self    string
	Connect Connector
	// Fetch returns a qabas1-deploy compose file for the writer's release.
	Fetch func(name string) ([]byte, error)
	// Images are the writer's pinned image references (same digests everywhere).
	Images map[string]string
	// CLI is the writer's qabas build; nodes running another build are updated to
	// the writer's channel (nil = skip the version check).
	CLI *CLIBuild
	// Registry returns GHCR credentials for image pulls on replicas.
	Registry func() (user, token string)
	// GitHubToken returns the writer's GitHub token (bootstrap installs the qabas CLI
	// from the private release repository with it).
	GitHubToken func() string
	// KeyCheck dials a node with only the cluster SSH key (nil = skip the proof).
	KeyCheck func(ctx context.Context, n cluster.Node) error
	Log      func(format string, args ...any)
	Now      func() time.Time
	// ProbeTCP reports whether host:port accepts TCP (closed-port verification).
	ProbeTCP func(ctx context.Context, addr string) bool

	conns map[string]nodeexec.Executor
}

func (e *Engine) logf(format string, args ...any) {
	if e.Log != nil {
		e.Log(format, args...)
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Exec returns a cached executor for node name.
func (e *Engine) Exec(ctx context.Context, name string) (nodeexec.Executor, error) {
	if x, ok := e.conns[name]; ok {
		return x, nil
	}
	n := e.Cluster.Node(name)
	if n == nil {
		return nil, fmt.Errorf("node %q is not registered", name)
	}
	x, err := e.Connect(ctx, *n)
	if err != nil {
		return nil, err
	}
	if e.conns == nil {
		e.conns = map[string]nodeexec.Executor{}
	}
	e.conns[name] = x
	return x, nil
}

// Reconnect drops the cached connection to name; the next Exec dials again (after a
// reboot, or when the node's SSH address or keys changed).
func (e *Engine) Reconnect(name string) {
	if x, ok := e.conns[name]; ok {
		_ = x.Close()
		delete(e.conns, name)
	}
}

// Close releases every open connection.
func (e *Engine) Close() {
	for _, x := range e.conns {
		_ = x.Close()
	}
	e.conns = nil
}

// Layout is the on-disk layout of a node (this machine follows its qabas paths).
func (e *Engine) Layout(name string) nodestack.Layout {
	if name == e.Self {
		return nodestack.LocalLayout(e.Paths.ConfigDir, e.Paths.StateDir)
	}
	return nodestack.Remote
}

func (e *Engine) writer() (*cluster.Node, error) {
	w := e.Cluster.Primary()
	if w == nil {
		return nil, fmt.Errorf("cluster registry has no primary")
	}
	return w, nil
}

func (e *Engine) image(key, fallback string) string {
	if v := e.Images[key]; v != "" {
		return v
	}
	return fallback
}

func (e *Engine) pgImage() string { return e.image("POSTGRES_IMAGE", nodestack.DefaultPostgresImage) }
func (e *Engine) mcImage() string { return e.image("MINIO_MC_IMAGE", nodestack.DefaultMinioMcImage) }

// PGVolume is the compose volume holding PGDATA.
const PGVolume = "qabasone_qabasone_postgres_data"

// sharedSecret reads/creates a cluster-wide secret on the control node.
func (e *Engine) sharedSecret(name string) (string, error) {
	return cluster.EnsureSecret(e.Paths, cluster.SharedScope, name)
}

// write is nodeexec.WriteFile with a forward-slash path.
func write(ctx context.Context, x nodeexec.Executor, p string, data []byte, mode uint32) error {
	return nodeexec.WriteFile(ctx, x, path.Clean(strings.ReplaceAll(p, `\`, "/")), data, osMode(mode))
}

// run executes a script and fails on non-zero exit.
func run(ctx context.Context, x nodeexec.Executor, script string) (string, error) {
	return nodeexec.Output(ctx, x, script)
}

// facts parses key=value lines.
func facts(out string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			m[k] = strings.TrimSpace(v)
		}
	}
	return m
}

func sortedNodes(ns []cluster.Node) []cluster.Node {
	out := append([]cluster.Node(nil), ns...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
