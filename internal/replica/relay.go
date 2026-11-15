package replica

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/relay"
)

const (
	RelayServerUnit = "qabas-relay.service"
	RelayClientUnit = "qabas-relay-client.service"
	// relayNodeDir is the relay directory on a replica (client.env lives here).
	relayNodeDir = "/etc/qabasone/relay"
)

const relayServerUnitText = `[Unit]
Description=QabasOne WireGuard-over-TLS relay (writer side)
After=network-online.target wg-quick@qabas0.service
Wants=network-online.target

[Service]
ExecStart=/usr/bin/env qabas relay serve
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`

// RelayClientUnitText is the replica's relay client unit (also embedded in join scripts).
const RelayClientUnitText = `[Unit]
Description=QabasOne WireGuard-over-TLS relay (replica side)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/env qabas relay connect
Restart=always
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
`

// installUnitScript installs the unit on stdin and (re)starts it only when it changed
// or is not running (args: unit name).
const installUnitScript = `set -eu
u=/etc/systemd/system/%[1]s
cat > "$u.new"
if cmp -s "$u.new" "$u"; then
  rm -f "$u.new"
  systemctl enable %[1]s >/dev/null 2>&1 || true
  systemctl is-active --quiet %[1]s || systemctl start %[1]s
else
  mv -f "$u.new" "$u"
  systemctl daemon-reload
  systemctl enable %[1]s >/dev/null 2>&1 || true
  systemctl restart %[1]s
fi
`

// removeUnitScript stops and deletes a unit (args: unit name, extra path to delete).
const removeUnitScript = `systemctl disable --now %[1]s >/dev/null 2>&1 || true
rm -f /etc/systemd/system/%[1]s %[2]s
systemctl daemon-reload >/dev/null 2>&1 || true
`

// RelayRestartScript makes running relay units pick up a replaced qabas binary.
const RelayRestartScript = "systemctl try-restart " + RelayServerUnit + " " + RelayClientUnit + " >/dev/null 2>&1 || true\n"

const relayCLICheck = QabasCmd + " relay connect --help >/dev/null 2>&1 && echo ok || true"

// relayAddr is where replicas reach the writer's relay: the WireGuard endpoint host
// on the relay TCP port.
func (e *Engine) relayAddr(w cluster.Node) (string, error) {
	host, _, err := net.SplitHostPort(w.WGEndpoint)
	if err != nil || host == "" {
		return "", fmt.Errorf("the writer %s has no usable WireGuard endpoint (%q) to derive the relay address from", w.Name, w.WGEndpoint)
	}
	return net.JoinHostPort(host, strconv.Itoa(e.Cluster.RelayPortOrDefault())), nil
}

// EnsureRelayServer prepares the writer's relay: certificate (pinned in the registry)
// and qabas-relay.service. Idempotent; never touches WireGuard or PostgreSQL.
func (e *Engine) EnsureRelayServer(ctx context.Context) error {
	w, err := e.writer()
	if err != nil {
		return err
	}
	if w.Name != e.Self {
		return fmt.Errorf("the relay is set up from the writer (%s); this node is %s", w.Name, e.Self)
	}
	fp, err := relay.GenerateCert(filepath.Join(e.Paths.ConfigDir, "relay"))
	if err != nil {
		return fmt.Errorf("relay certificate: %w", err)
	}
	if e.Cluster.RelayCertSHA256 != fp || e.Cluster.RelayPort == 0 {
		e.Cluster.RelayCertSHA256 = fp
		e.Cluster.RelayPort = e.Cluster.RelayPortOrDefault()
		if err := cluster.Save(e.Paths, e.Cluster); err != nil {
			return err
		}
	}
	x, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	if out, _ := run(ctx, x, relayCLICheck); strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("the installed qabas on %s has no relay support; update it (qabas update) and retry", w.Name)
	}
	res, err := x.Run(ctx, fmt.Sprintf(installUnitScript, RelayServerUnit), strings.NewReader(relayServerUnitText))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("start %s on %s: %s", RelayServerUnit, w.Name, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (e *Engine) ensureRelayToken(node string) (string, error) {
	return cluster.EnsureSecret(e.Paths, node, cluster.SecretRelayToken)
}

// relayClientEnv renders client.env for a node.
func (e *Engine) relayClientEnv(node, server, token string) string {
	return fmt.Sprintf("# qabas relay client (WireGuard over TLS to the writer). Secret: keep 0600.\nRELAY_SERVER=%s\nRELAY_CERT_SHA256=%s\nRELAY_NODE=%s\nRELAY_TOKEN=%s\nRELAY_LISTEN=127.0.0.1:%d\n",
		server, e.Cluster.RelayCertSHA256, node, token, cluster.RelayLocalPort)
}

// demoteForRelay applies the relay constraints to a node and explains what changed.
func (e *Engine) demoteForRelay(n *cluster.Node) {
	if n.Promotable {
		e.logf("  warning: %s uses the relay, which only the writer can reach; it is registered as NOT promotable", n.Name)
		n.Promotable = false
	}
	if w, _ := e.writer(); n.Upstream != "" && (w == nil || n.Upstream != w.Name) {
		e.logf("  warning: %s uses the relay; it streams from the writer instead of %s", n.Name, n.Upstream)
		n.Upstream = ""
	}
	n.Relay, n.Mode = true, cluster.ModePull
}

// ConfigureRelayClient switches an SSH-reachable node to the relay: client config and
// unit on the node, Relay in the registry, WireGuard re-applied on both ends.
func (e *Engine) ConfigureRelayClient(ctx context.Context, name string) error {
	if err := e.EnsureRelayServer(ctx); err != nil {
		return err
	}
	w, err := e.writer()
	if err != nil {
		return err
	}
	server, err := e.relayAddr(*w)
	if err != nil {
		return err
	}
	token, err := e.ensureRelayToken(name)
	if err != nil {
		return err
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	if out, _ := run(ctx, x, relayCLICheck); strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("qabas on %s has no relay support; update it there (qabas update) and retry", name)
	}
	if err := write(ctx, x, relayNodeDir+"/client.env", []byte(e.relayClientEnv(name, server, token)), 0o600); err != nil {
		return err
	}
	res, err := x.Run(ctx, fmt.Sprintf(installUnitScript, RelayClientUnit), strings.NewReader(RelayClientUnitText))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("start %s on %s: %s", RelayClientUnit, name, strings.TrimSpace(res.Stderr))
	}
	n := e.Cluster.Node(name)
	e.demoteForRelay(n)
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		return err
	}
	if err := e.ApplyWG(ctx, name); err != nil {
		return err
	}
	return e.ApplyWG(ctx, w.Name)
}

// relayInUse reports whether a relay node or a pending invite still needs the relay.
func (e *Engine) relayInUse() bool {
	if e.Cluster.HasRelayNodes() {
		return true
	}
	for _, n := range e.Cluster.Standbys() {
		if st, err := LoadState(e.Paths, n.Name); err == nil && pendingInvite(st) {
			return true
		}
	}
	return false
}

// StopRelayServerIfUnused disables qabas-relay.service on the writer once nothing
// needs it. The certificate is kept so its pinned fingerprint stays valid.
func (e *Engine) StopRelayServerIfUnused(ctx context.Context) error {
	if e.relayInUse() || e.Cluster.RelayCertSHA256 == "" {
		return nil
	}
	w, err := e.writer()
	if err != nil || w.Name != e.Self {
		return err
	}
	x, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	_, err = run(ctx, x, fmt.Sprintf(removeUnitScript, RelayServerUnit, ""))
	return err
}
