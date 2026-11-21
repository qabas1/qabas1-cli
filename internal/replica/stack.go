package replica

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/nodeexec"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/pgctl"
	"github.com/qabas1/qabas1-cli/internal/secrets"
	"gopkg.in/yaml.v3"
)

// envFiles are copied from the writer to every other node. Secrets stay 0600.
var envFiles = []string{".env", ".env.api", ".env.worker", ".env.app", ".env.dash", ".env.siraj", ".env.migrate"}

// sharedSecretNames are pushed to every node so any promotable node can take over.
var sharedSecretNames = []string{cluster.SecretReplicationPassword, cluster.SecretBackupPassword, cluster.SecretWitnessToken}

// StackOptions tunes ConfigureNode.
type StackOptions struct {
	Profile nodestack.Profile
	PITR    bool
}

// ConfigureNode materializes everything a node's compose stack needs for its role:
// compose bundle + overlays, env files, node identity, image pins, pg_hba/TLS, registry
// and shared secrets. It never starts or stops containers.
func (e *Engine) ConfigureNode(ctx context.Context, name string, opt StackOptions) error {
	n := e.Cluster.Node(name)
	if n == nil {
		return fmt.Errorf("node %q is not registered", name)
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	l := e.Layout(name)
	if _, err := run(ctx, x, mountDirsScript(l)); err != nil {
		return err
	}
	if _, err := run(ctx, x, edgeNetworkScript); err != nil {
		return err
	}

	// Compose bundle and overlays (same release as the writer). Every node carries all
	// role overlays so promotion works offline (no GitHub access during a disaster).
	files := []string{"compose.yaml", nodestack.OverlayPrimary, nodestack.OverlayReplicaRO, nodestack.OverlayDRPrimary, "compose.gateway.yaml"}
	if opt.PITR {
		files = append(files, nodestack.OverlayPITR)
	}
	for _, f := range files {
		raw, err := e.Fetch(f)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", f, err)
		}
		if err := write(ctx, x, l.ComposeDir+"/"+f, raw, 0o644); err != nil {
			return err
		}
	}
	// The gateway is installed locally on each node. Keep its directives when
	// refreshing role overlays, otherwise the next compose up removes it as an orphan.
	existing, err := run(ctx, x, "cat "+nodeexec.Quote(l.ComposeDir+"/"+nodestack.OverlaysFile)+" 2>/dev/null || true")
	if err != nil {
		return err
	}
	overlays := nodestack.LocalOverlays(l, opt.Profile, opt.PITR)
	for _, directive := range []string{"file:compose.gateway.yaml", "env-file:" + l.ConfigDir + "/gateway.env"} {
		if strings.Contains("\n"+existing, "\n"+directive+"\n") || strings.HasSuffix(existing, "\n"+directive) {
			overlays += directive + "\n"
		}
	}
	if err := write(ctx, x, l.ComposeDir+"/"+nodestack.OverlaysFile, []byte(overlays), 0o644); err != nil {
		return err
	}

	siteRole := cluster.RoleStandby
	if n.Role == cluster.RolePrimary && opt.Profile == nodestack.ProfilePrimary {
		siteRole = cluster.RolePrimary
	}
	if err := write(ctx, x, l.NodeEnvFile(), []byte(nodestack.NodeEnv(l, n.Name, n.Site, siteRole, n.WGIP, e.Cluster.Release)), 0o644); err != nil {
		return err
	}
	if err := write(ctx, x, l.ImagesEnvFile(), []byte(nodestack.ImagesEnv(e.Images)), 0o644); err != nil {
		return err
	}
	if err := write(ctx, x, l.RoleFile(), []byte(string(opt.Profile)+"\n"), 0o644); err != nil {
		return err
	}

	if name != e.Self {
		if err := e.pushEnvFiles(ctx, x, l); err != nil {
			return err
		}
		if err := e.pushRegistry(ctx, x, l); err != nil {
			return err
		}
		if err := e.pushClusterKey(ctx, x, l, *n); err != nil {
			return err
		}
		if err := e.ensureCLI(ctx, x); err != nil {
			return err
		}
		if err := e.registryLogin(ctx, x); err != nil {
			return err
		}
	}
	if err := e.ensureTLS(ctx, x, n, l); err != nil {
		return err
	}
	return e.WriteHBA(ctx, name)
}

// mountDirsScript makes the directories bind-mounted into containers traversable by
// their non-root users (postgres reads pg_hba/TLS, the API reads writer.json).
// WriteFile creates missing parents under umask 077, which would leave them 0700.
func mountDirsScript(l nodestack.Layout) string {
	dirs := nodeexec.Quote(l.PGConfDir) + " " + nodeexec.Quote(l.TLSDir()) + " " + nodeexec.Quote(l.DRDir)
	return "set -eu\nmkdir -p " + dirs + "\nchmod 0755 " + dirs + "\n"
}

// compose.yaml joins the reverse proxy's external network, which only exists where an
// edge proxy was set up; a fresh node needs it created for compose to start.
const edgeNetworkScript = "docker network inspect edge-network >/dev/null 2>&1 || docker network create edge-network >/dev/null\n"

// pushEnvFiles copies the writer's env files; each node keeps its own MinIO root
// credentials (existing ones are preserved, new nodes get fresh random ones).
func (e *Engine) pushEnvFiles(ctx context.Context, x nodeexec.Executor, l nodestack.Layout) error {
	existing, _ := run(ctx, x, "cat "+nodeexec.Quote(l.ConfigDir+"/.env")+" 2>/dev/null || true")
	cur := nodestack.ParseEnv([]byte(existing))
	for _, f := range envFiles {
		body, err := os.ReadFile(filepath.Join(e.Paths.ConfigDir, f))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if f == ".env" {
			user, pass := cur["MINIO_ROOT_USER"], cur["MINIO_ROOT_PASSWORD"]
			if user == "" || pass == "" {
				if user, err = cluster.RandomToken(12); err != nil {
					return err
				}
				if pass, err = cluster.RandomToken(32); err != nil {
					return err
				}
				user = "qabas-" + strings.ToLower(user)
			}
			body = nodestack.SetEnv(body, map[string]string{"MINIO_ROOT_USER": user, "MINIO_ROOT_PASSWORD": pass})
		}
		if err := write(ctx, x, l.ConfigDir+"/"+f, body, 0o600); err != nil {
			return err
		}
	}
	catalogRaw, err := os.ReadFile(secrets.CatalogFile(e.Paths))
	if os.IsNotExist(err) {
		return nil
	} // pre-catalog installations
	if err != nil {
		return err
	}
	catalog, err := secrets.ParseCatalog(catalogRaw)
	if err != nil {
		return err
	}
	if err := write(ctx, x, l.ConfigDir+"/secrets.catalog.yaml", catalogRaw, 0o600); err != nil {
		return err
	}
	for _, s := range catalog {
		if s.Delivery != "file" {
			continue
		}
		value, err := os.ReadFile(secrets.SecretValueFile(e.Paths, s.Key))
		if err != nil {
			return fmt.Errorf("read secret file %s for replica: %w", s.Name, err)
		}
		if err := write(ctx, x, l.ConfigDir+"/secret-files/"+s.Key, value, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// pushRegistry sends cluster.yaml (topology) and the shared secrets (0600).
func (e *Engine) pushRegistry(ctx context.Context, x nodeexec.Executor, l nodestack.Layout) error {
	raw, err := yaml.Marshal(e.Cluster)
	if err != nil {
		return err
	}
	header := []byte("# Managed by qabas (qabas replica / qabas dr). Topology only - never secrets.\n")
	if err := write(ctx, x, l.ConfigDir+"/cluster.yaml", append(header, raw...), 0o644); err != nil {
		return err
	}
	for _, s := range sharedSecretNames {
		v, err := cluster.ReadSecret(e.Paths, cluster.SharedScope, s)
		if err != nil || v == "" {
			continue
		}
		if err := write(ctx, x, l.ConfigDir+"/nodes/"+cluster.SharedScope+"/"+s, []byte(v+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// pushClusterKey copies the cluster SSH key to promotable nodes (after a promotion
// they manage the cluster) and makes sure other nodes do not hold it.
func (e *Engine) pushClusterKey(ctx context.Context, x nodeexec.Executor, l nodestack.Layout, n cluster.Node) error {
	dir := l.ConfigDir + "/ssh"
	priv, err := cluster.ReadSSHPrivateKey(e.Paths)
	if err != nil || !n.Promotable {
		_, err := run(ctx, x, "rm -f "+nodeexec.Quote(dir+"/cluster_ed25519")+" "+nodeexec.Quote(dir+"/cluster_ed25519.pub"))
		return err
	}
	pub, err := cluster.ReadSSHPublicKey(e.Paths)
	if err != nil {
		return err
	}
	if err := write(ctx, x, dir+"/cluster_ed25519", priv, 0o600); err != nil {
		return err
	}
	return write(ctx, x, dir+"/cluster_ed25519.pub", []byte(pub+"\n"), 0o644)
}

func (e *Engine) registryLogin(ctx context.Context, x nodeexec.Executor) error {
	if e.Registry == nil {
		return nil
	}
	user, token := e.Registry()
	if user == "" || token == "" {
		return nil
	}
	res, err := x.Run(ctx, "docker login ghcr.io -u "+nodeexec.Quote(user)+" --password-stdin >/dev/null", strings.NewReader(token))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &nodeexec.ScriptError{Node: x.Node(), ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return nil
}

func (e *Engine) ensureTLS(ctx context.Context, x nodeexec.Executor, n *cluster.Node, l nodestack.Layout) error {
	out, _ := run(ctx, x, "test -s "+nodeexec.Quote(l.TLSDir()+"/server.key")+" && echo yes || echo no")
	if strings.TrimSpace(out) != "yes" {
		crt, key, err := pgctl.SelfSignedCert("qabas-"+n.Name, n.WGIP)
		if err != nil {
			return err
		}
		if err := write(ctx, x, l.TLSDir()+"/server.crt", crt, 0o644); err != nil {
			return err
		}
		if err := write(ctx, x, l.TLSDir()+"/server.key", key, 0o600); err != nil {
			return err
		}
	}
	_, err := run(ctx, x, pgctl.ChownTLSScript(e.pgImage(), l.TLSDir()))
	return err
}

// WriteHBA renders pg_hba.conf on a node: the compose network(s) plus every registered
// node's WireGuard /32 (any node may become an upstream after a promotion).
func (e *Engine) WriteHBA(ctx context.Context, name string) error {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	nets, _ := run(ctx, x, "docker network inspect qabasone_qabasone_data --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}' 2>/dev/null || true")
	subnets := strings.Fields(nets)
	if len(subnets) == 0 {
		return fmt.Errorf("cannot determine Docker data network subnet on %s; refusing to write a guessed pg_hba.conf (ensure qabasone_qabasone_data exists, then retry)", name)
	}
	for _, subnet := range subnets {
		if _, _, err := net.ParseCIDR(subnet); err != nil {
			return fmt.Errorf("Docker data network on %s returned invalid subnet %q", name, subnet)
		}
	}
	var gateways []string
	containerGateways, _ := run(ctx, x, "docker inspect --format '{{range .NetworkSettings.Networks}}{{.Gateway}} {{end}}' "+nodeexec.Quote(pgctl.Container)+" 2>/dev/null || true")
	for _, gateway := range strings.Fields(containerGateways) {
		if ip := net.ParseIP(gateway); ip != nil && ip.To4() != nil {
			gateways = append(gateways, ip.String())
		}
	}
	if len(gateways) == 0 {
		gatewaysRaw, err := run(ctx, x, "docker network inspect qabasone_qabasone_data --format '{{range .IPAM.Config}}{{.Gateway}} {{end}}' 2>/dev/null || true")
		if err != nil {
			return fmt.Errorf("inspect Docker data network gateway on %s: %w", name, err)
		}
		for _, gateway := range strings.Fields(gatewaysRaw) {
			ip := net.ParseIP(gateway)
			if ip == nil || ip.To4() == nil {
				continue
			}
			for _, subnet := range subnets {
				_, network, err := net.ParseCIDR(subnet)
				if err == nil && network.Contains(ip) {
					gateways = append(gateways, ip.String())
					break
				}
			}
		}
	}
	var peers []pgctl.HBAEntry
	for _, o := range sortedNodes(e.Cluster.Nodes) {
		if o.Name != name && o.WGIP != "" {
			peers = append(peers, pgctl.HBAEntry{Node: o.Name, CIDR: o.WGIP + "/32"})
		}
	}
	if err := write(ctx, x, e.Layout(name).HBAFile(), []byte(pgctl.RenderHBAWithGateways(subnets, gateways, peers)), 0o644); err != nil {
		return err
	}
	if len(gateways) > 0 {
		e.logf("  pg_hba on %s allows replication via Docker gateway(s): %s", name, strings.Join(gateways, ", "))
	}
	running, _ := run(ctx, x, "docker inspect --format '{{.State.Running}}' "+nodeexec.Quote(pgctl.Container)+" 2>/dev/null || true")
	if strings.TrimSpace(running) == "true" {
		if _, err := pgctl.Query(ctx, x, "SELECT pg_reload_conf();"); err != nil {
			return fmt.Errorf("reload pg_hba on %s: %w", name, err)
		}
		for _, gateway := range gateways {
			sql := fmt.Sprintf(`SELECT count(*) FROM pg_hba_file_rules
WHERE type = 'hostssl'
  AND database @> ARRAY['replication']::text[]
  AND user_name @> ARRAY[%s]::text[]
  AND address = %s
  AND error IS NULL;`, pgctl.Literal(pgctl.ReplicationRole), pgctl.Literal(gateway))
			rows, err := pgctl.Query(ctx, x, sql)
			if err != nil {
				return fmt.Errorf("verify pg_hba Docker gateway %s on %s: %w", gateway, name, err)
			}
			if len(rows) != 1 || len(rows[0]) != 1 || rows[0][0] == "0" {
				return fmt.Errorf("pg_hba on %s did not load the replication rule for Docker gateway %s", name, gateway)
			}
		}
	}
	return nil
}

// Compose runs docker compose on a node with its role's overlays.
func (e *Engine) Compose(ctx context.Context, name string, opt StackOptions, args string) (string, error) {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return "", err
	}
	command := nodestack.ComposeCommand(e.Layout(name), opt.Profile, opt.PITR, args)
	// Replica-local overlays such as the independently managed gateway are kept
	// in the node's overlays file. Include them in Compose invocations so stack
	// recreation does not remove those services as orphans.
	directives := nodestack.ReadDirectives(e.Layout(name).ComposeDir)
	for _, file := range directives.Files {
		if filepath.Base(file) == "compose.gateway.yaml" {
			command = strings.Replace(command, " --env-file ", " -f "+nodeexec.Quote(file)+" --env-file ", 1)
		}
	}
	return run(ctx, x, command)
}
