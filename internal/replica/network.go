package replica

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/nodeexec"
	"github.com/qabas1/qabas1-cli/internal/wg"
)

// EnsureWGKey creates the node's key on the node (if missing) and records the public key.
func (e *Engine) EnsureWGKey(ctx context.Context, name string) error {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	pub, err := run(ctx, x, wg.NodeKeyScript)
	if err != nil {
		return fmt.Errorf("wireguard key on %s: %w", name, err)
	}
	pub = strings.TrimSpace(pub)
	n := e.Cluster.Node(name)
	if len(pub) != 44 {
		return fmt.Errorf("wireguard key on %s: unexpected public key", name)
	}
	n.WGPublicKey = pub
	return nil
}

// RenderWG renders the full-mesh config for a node (private key left as placeholder).
// Peers without an endpoint (behind NAT) are reached through their keepalive.
func (e *Engine) RenderWG(name string) (string, error) {
	n := e.Cluster.Node(name)
	if n == nil || n.WGIP == "" {
		return "", fmt.Errorf("node %q has no WireGuard address", name)
	}
	bits := "24"
	if _, b, ok := strings.Cut(e.Cluster.WGNetwork, "/"); ok {
		bits = b
	}
	writer := ""
	if w := e.Cluster.Primary(); w != nil {
		writer = w.Name
	}
	iface := wg.Interface{Address: n.WGIP + "/" + bits, ListenPort: e.Cluster.WGPort, PrivateKey: wg.PrivateKeyPlaceholder}
	for _, o := range e.Cluster.Nodes {
		if o.Name == name || o.WGPublicKey == "" || o.WGIP == "" {
			continue
		}
		// Relay nodes reach only the writer (through the relay); nobody else can reach them.
		if (n.Relay && o.Name != writer) || (o.Relay && name != writer) {
			continue
		}
		p := wg.Peer{Name: o.Name, PublicKey: o.WGPublicKey, AllowedIPs: []string{o.WGIP + "/32"}, Endpoint: o.WGEndpoint}
		switch {
		case n.Relay:
			p.Endpoint, p.Keepalive = "127.0.0.1:"+strconv.Itoa(cluster.RelayLocalPort), 25
		case o.Relay:
			p.Endpoint = ""
		case o.WGEndpoint != "" || n.WGEndpoint == "":
			p.Keepalive = 25
		}
		iface.Peers = append(iface.Peers, p)
	}
	return iface.Render(), nil
}

const endpointsScript = "wg show qabas0 endpoints 2>/dev/null || true"

// detectRelay marks a WireGuard-reached node as a relay node when the writer sees its
// peer at a loopback endpoint (the join script switched to the TLS relay). Rendering
// such a node with a direct endpoint would cut the tunnel its SSH session runs over.
func (e *Engine) detectRelay(ctx context.Context, name string) error {
	n := e.Cluster.Node(name)
	w, err := e.writer()
	if err != nil || n == nil || n.Relay || n.WGPublicKey == "" || n.Name == w.Name {
		return err
	}
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	out, _ := run(ctx, wx, endpointsScript)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[0] != n.WGPublicKey || !strings.HasPrefix(f[1], "127.0.0.1:") {
			continue
		}
		e.logf("  %s reaches the writer through the TLS relay", name)
		e.demoteForRelay(n)
		return cluster.Save(e.Paths, e.Cluster)
	}
	return nil
}

// ApplyWG pushes the rendered mesh config to a node and syncs the interface.
func (e *Engine) ApplyWG(ctx context.Context, name string) error {
	if n := e.Cluster.Node(name); n != nil && n.SSHVia == cluster.SSHViaWG && !n.Relay {
		if err := e.detectRelay(ctx, name); err != nil {
			return err
		}
	}
	conf, err := e.RenderWG(name)
	if err != nil {
		return err
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	res, err := x.Run(ctx, wg.ApplyScript, strings.NewReader(conf))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("wireguard on %s: exit %d: %s", name, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// SyncMesh re-applies the mesh on every node. Unreachable nodes are returned as
// warnings (they keep their previous peers until the next sync).
func (e *Engine) SyncMesh(ctx context.Context, skip ...string) []error {
	var errs []error
	skipSet := map[string]bool{}
	for _, s := range skip {
		skipSet[s] = true
	}
	for _, n := range sortedNodes(e.Cluster.Nodes) {
		if skipSet[n.Name] || n.WGPublicKey == "" {
			continue
		}
		if err := e.ApplyWG(ctx, n.Name); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// rotatePrepareScript stages a fresh node-local key next to the current one.
const rotatePrepareScript = "set -eu\numask 077\n[ -s " + wg.KeyPath + ".next ] || wg genkey > " + wg.KeyPath + ".next\nwg pubkey < " + wg.KeyPath + ".next\n"

// rotateSwapScript switches the live interface to the staged key a few seconds later,
// so the writer can learn the new public key first (the session runs over WireGuard).
const rotateSwapScript = `set -eu
umask 077
cat > /run/qabas-wg-rotate.sh <<'EOF'
set -eu
[ -s ` + wg.KeyPath + `.next ] || exit 0
mv -f ` + wg.KeyPath + `.next ` + wg.KeyPath + `
key=$(cat ` + wg.KeyPath + `)
sed -i "s|^PrivateKey = .*|PrivateKey = $key|" ` + wg.ConfigPath + `
wg set qabas0 private-key ` + wg.KeyPath + `
EOF
if command -v systemd-run >/dev/null 2>&1; then
  systemd-run --quiet --collect --on-active=5 /bin/sh /run/qabas-wg-rotate.sh
else
  nohup sh -c 'sleep 5; sh /run/qabas-wg-rotate.sh' >/dev/null 2>&1 &
fi
`

var (
	rotateSettle  = 8 * time.Second
	rotateTimeout = 90 * time.Second
	reconnectPoll = 3 * time.Second
	// peerTimeout bounds each WireGuard reachability wait in NETWORK (direct, then relay).
	peerTimeout = 60 * time.Second
	peerPoll    = 2 * time.Second
)

// RotateWGKey replaces a node's WireGuard key (the invite key the writer generated)
// with one generated on the node, without losing the WireGuard-carried SSH session:
// the node stages the key and swaps it after a delay while the writer switches the
// peer's public key. If the node never comes back on the new key, the writer reverts.
func (e *Engine) RotateWGKey(ctx context.Context, name string) error {
	n := e.Cluster.Node(name)
	w, err := e.writer()
	if err != nil {
		return err
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	pub, err := run(ctx, x, rotatePrepareScript)
	if err != nil {
		return err
	}
	pub = strings.TrimSpace(pub)
	if len(pub) != 44 {
		return fmt.Errorf("wireguard key on %s: unexpected public key", name)
	}
	old := n.WGPublicKey
	if pub == old {
		return nil
	}
	if _, err := run(ctx, x, rotateSwapScript); err != nil {
		return err
	}
	e.Reconnect(name)
	n.WGPublicKey = pub
	if err := e.ApplyWG(ctx, w.Name); err != nil {
		n.WGPublicKey = old
		_ = e.ApplyWG(ctx, w.Name)
		return err
	}
	if err := e.awaitNode(ctx, name, rotateSettle, rotateTimeout, func(x nodeexec.Executor) bool {
		got, err := run(ctx, x, "wg pubkey < "+wg.KeyPath)
		return err == nil && strings.TrimSpace(got) == pub
	}); err != nil {
		n.WGPublicKey = old
		_ = e.ApplyWG(ctx, w.Name)
		return fmt.Errorf("%s did not come back on its new WireGuard key (%v); the writer reverted to the invite key - re-run with --resume", name, err)
	}
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		return err
	}
	for _, err := range e.SyncMesh(ctx, w.Name, name) {
		e.logf("  warning: %v", err)
	}
	e.logf("  %s: WireGuard key rotated (generated on the node; the invite key is gone)", name)
	return nil
}

// awaitNode waits settle, then reconnects to a node until ok reports true or timeout
// passes.
func (e *Engine) awaitNode(ctx context.Context, name string, settle, timeout time.Duration, ok func(nodeexec.Executor) bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(settle):
	}
	deadline := time.Now().Add(timeout)
	var last error
	for {
		x, err := e.Exec(ctx, name)
		if err == nil {
			if ok(x) {
				return nil
			}
			last = fmt.Errorf("%s reachable but not in the expected state", name)
			e.Reconnect(name)
		} else {
			last = err
		}
		if time.Now().After(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectPoll):
		}
	}
}

const sshdPortScript = `p=$(sshd -T 2>/dev/null | awk '$1=="port"{print $2; exit}'); echo "${p:-22}"`

// SwitchSSHToWG moves a pull-mode node's SSH path onto WireGuard (it accepts no
// inbound traffic on its public address once provisioned). The cluster key is
// re-authorized for the WireGuard network only. Reverts if sshd is not reachable.
func (e *Engine) SwitchSSHToWG(ctx context.Context, name string) error {
	n := e.Cluster.Node(name)
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	out, err := run(ctx, x, sshdPortScript)
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(strings.TrimSpace(out))
	if port <= 0 || port > 65535 {
		port = 22
	}
	prevVia, prevPort := n.SSHVia, n.WGSSHPort
	n.SSHVia, n.WGSSHPort = cluster.SSHViaWG, port
	e.Reconnect(name)
	if _, err := e.Exec(ctx, name); err != nil {
		n.SSHVia, n.WGSSHPort = prevVia, prevPort
		e.Reconnect(name)
		return fmt.Errorf("sshd on %s is not reachable over WireGuard (%s:%d): %w", name, n.WGIP, port, err)
	}
	if err := e.AuthorizeClusterKey(ctx, name); err != nil {
		return err
	}
	e.logf("  %s: SSH now runs over WireGuard (%s:%d)", name, n.WGIP, port)
	return cluster.Save(e.Paths, e.Cluster)
}

// WaitPeer pings dst's WireGuard address from src until it answers.
func (e *Engine) WaitPeer(ctx context.Context, src string, dst cluster.Node, timeout time.Duration) error {
	x, err := e.Exec(ctx, src)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for {
		res, err := x.Run(ctx, "ping -c 1 -W 2 "+dst.WGIP+" >/dev/null 2>&1", nil)
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s cannot reach %s over WireGuard (%s); check UDP %d and the endpoint", src, dst.Name, dst.WGIP, e.Cluster.WGPort)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(peerPoll):
		}
	}
}
