package replica

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/minioctl"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/redact"
)

const mcEnvFile = "/run/qabas/mc.env"

// Bucket is the application bucket (same on every node).
func (e *Engine) Bucket() string {
	for _, f := range []string{".env.api", ".env"} {
		b, err := os.ReadFile(filepath.Join(e.Paths.ConfigDir, f))
		if err != nil {
			continue
		}
		env := nodestack.ParseEnv(b)
		for _, k := range []string{"STORAGE_BUCKET", "MINIO_BUCKET"} {
			if v := env[k]; v != "" {
				return v
			}
		}
	}
	return "qabasone"
}

func compact(s string) string {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// MC runs mc commands in a one-off container on a node. Node-local root and app
// credentials are read from the node's own env files there; extra values travel on
// stdin into a 0600 env file that is removed afterwards.
func (e *Engine) MC(ctx context.Context, name string, extra map[string]string, cmds string) (string, error) {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return "", err
	}
	cfg := e.Layout(name).ConfigDir
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var in strings.Builder
	for _, k := range keys {
		redact.Register(extra[k])
		fmt.Fprintf(&in, "%s=%s\n", k, extra[k])
	}
	prelude := fmt.Sprintf(`set -eu
umask 077
mkdir -p /run/qabas
get() { sed -n "s/^$1=//p" "$2" 2>/dev/null | tail -n 1 | sed -e 's/^"//' -e 's/"$//'; }
{
  printf 'MC_HOST_local=http://%%s:%%s@minio:9000\n' "$(get MINIO_ROOT_USER %[1]s/.env)" "$(get MINIO_ROOT_PASSWORD %[1]s/.env)"
  printf 'ROOT_USER=%%s\n' "$(get MINIO_ROOT_USER %[1]s/.env)"
  printf 'APP_ACCESS=%%s\nAPP_SECRET=%%s\n' "$(get STORAGE_ACCESS_KEY_ID %[1]s/.env.api)" "$(get STORAGE_SECRET_ACCESS_KEY %[1]s/.env.api)"
  cat
} > %[2]s
`, nodeexec.Quote(cfg), mcEnvFile)
	res, err := x.Run(ctx, prelude+minioctl.Script(e.mcImage(), mcEnvFile, cmds), strings.NewReader(in.String()))
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", &nodeexec.ScriptError{Node: name, ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return strings.TrimSpace(res.Stdout), nil
}

// appPolicyCommands attaches the RO/RW policy to the app key unless the app still uses
// the MinIO root credentials (then there is nothing to scope; doctor warns about it).
func appPolicyCommands(bucket string, readOnly bool) string {
	return `if [ -z "$APP_ACCESS" ] || [ "$APP_ACCESS" = "$ROOT_USER" ]; then
  echo "app key is the MinIO root user; policy not applied"
else
` + minioctl.EnsureAppUserCommands("local", bucket, readOnly) + `fi
`
}

// SetAppPolicy flips a node's app key between read-only (standby) and read-write (writer).
func (e *Engine) SetAppPolicy(ctx context.Context, name string, readOnly bool) error {
	b := e.Bucket()
	_, err := e.MC(ctx, name, map[string]string{
		"POLICY_JSON_RO": compact(minioctl.AppReadOnlyPolicy(b)),
		"POLICY_JSON_RW": compact(minioctl.AppReadWritePolicy(b)),
	}, appPolicyCommands(b, readOnly))
	return err
}

func (e *Engine) replicationCreds(dest string) (string, string, error) {
	access, err := cluster.ReadSecret(e.Paths, dest, cluster.SecretMinioReplAccessKey)
	if err != nil || access == "" {
		tok, terr := cluster.RandomToken(12)
		if terr != nil {
			return "", "", terr
		}
		access = "qabasrepl" + strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(tok))
		if len(access) > 20 {
			access = access[:20]
		}
		if err := cluster.WriteSecret(e.Paths, dest, cluster.SecretMinioReplAccessKey, access); err != nil {
			return "", "", err
		}
	}
	secret, err := cluster.EnsureSecret(e.Paths, dest, cluster.SecretMinioReplSecretKey)
	return access, secret, err
}

// LinkStorage makes dest a one-way replication destination of src (never site
// replication): replication-target user + versioning on dest, rule on src.
func (e *Engine) LinkStorage(ctx context.Context, src, dest string, priority int) error {
	d := e.Cluster.Node(dest)
	if d == nil || d.WGIP == "" {
		return fmt.Errorf("node %q has no WireGuard address", dest)
	}
	source, err := e.Exec(ctx, src)
	if err != nil {
		return err
	}
	e.logf("    replication checking MinIO path %s -> %s", src, dest)
	pathCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	path := checkContainerPath(pathCtx, source, PathCheck{
		Node: src, Service: "minio", Container: "qabasone-minio",
		Destination: net.JoinHostPort(d.WGIP, "9000"),
	}, d.WGIP, "9000")
	cancel()
	if !path.OK {
		return fmt.Errorf("MinIO path %s -> %s: %s", src, dest, path.Detail)
	}
	b := e.Bucket()
	access, secret, err := e.replicationCreds(dest)
	if err != nil {
		return err
	}
	e.logf("    replication preparing destination on %s", dest)
	if _, err := e.MC(ctx, dest, map[string]string{
		"REPL_ACCESS":      access,
		"REPL_SECRET":      secret,
		"POLICY_JSON_REPL": compact(minioctl.ReplicationTargetPolicy(b)),
	}, minioctl.EnsureReplicationTargetCommands("local", b)); err != nil {
		return fmt.Errorf("minio destination %s: %w", dest, err)
	}
	url := fmt.Sprintf("http://%s:%s@%s:9000/%s", access, secret, d.WGIP, b)
	e.logf("    replication adding source rule on %s", src)
	if _, err := e.MC(ctx, src, map[string]string{"REMOTE_BUCKET_URL": url},
		minioctl.AddReplicationCommands("local", b, d.WGIP, priority)); err != nil {
		return fmt.Errorf("minio replication %s -> %s: %w", src, dest, err)
	}
	return nil
}

// UnlinkStorage removes src's rule(s) targeting dest.
func (e *Engine) UnlinkStorage(ctx context.Context, src, dest string) error {
	d := e.Cluster.Node(dest)
	if d == nil || d.WGIP == "" {
		return nil
	}
	_, err := e.MC(ctx, src, nil, minioctl.RemoveReplicationCommands("local", e.Bucket(), d.WGIP))
	return err
}

// ClearStorageRules drops every outgoing rule on a node (it stops being a source).
func (e *Engine) ClearStorageRules(ctx context.Context, name string) error {
	_, err := e.MC(ctx, name, nil, minioctl.RemoveAllReplicationCommands("local", e.Bucket()))
	return err
}

// StoragePriority is a stable per-destination rule priority.
func (e *Engine) StoragePriority(dest string) int {
	for i, n := range sortedNodes(e.Cluster.Nodes) {
		if n.Name == dest {
			return i + 1
		}
	}
	return len(e.Cluster.Nodes) + 1
}
