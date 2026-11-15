package replica

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/redact"
	"github.com/qabas1/qabasone-cli/internal/wg"
)

// DefaultInviteTTL is how long a join script stays valid.
const DefaultInviteTTL = 2 * time.Hour

// InviteOptions describes a replica that cannot accept inbound connections.
type InviteOptions struct {
	// Node carries name, site, promotable, priority, upstream and public URL.
	Node cluster.Node
	TTL  time.Duration
	// Relay makes the join script use the TLS relay without trying direct WireGuard.
	Relay bool
}

// joinRelay is the relay part of a join script (Server empty: relay unavailable).
type joinRelay struct {
	Server, Token string
	Force         bool
}

// Invite is a registered pending node and its join script.
type Invite struct {
	Node      string    `json:"node"`
	WGIP      string    `json:"wgIP"`
	Path      string    `json:"path"`
	ExpiresAt time.Time `json:"expiresAt"`
	Script    string    `json:"-"`
}

// InvitePath is where the join script is written on the writer (0600).
func InvitePath(p paths.Paths, node string) string {
	return filepath.Join(p.ConfigDir, "invites", node+".sh")
}

// RemoveInviteFile deletes a node's join script (it holds a private key).
func RemoveInviteFile(p paths.Paths, node string) error {
	err := os.Remove(InvitePath(p, node))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Invite registers a pull-mode node reached over WireGuard only and renders the
// one-time join script that runs on it. The node's temporary WireGuard key is the only
// secret in the script; NETWORK replaces it with a key generated on the node.
func (e *Engine) Invite(ctx context.Context, opt InviteOptions) (*Invite, error) {
	name := opt.Node.Name
	if err := cluster.ValidateNodeName(name); err != nil {
		return nil, err
	}
	w, err := e.writer()
	if err != nil {
		return nil, err
	}
	if w.Name != e.Self {
		return nil, fmt.Errorf("run replica commands on the writer (%s); this node is %s", w.Name, e.Self)
	}
	for _, err := range e.PruneInvites(ctx) {
		e.logf("  warning: %v", err)
	}
	if e.Cluster.Node(name) != nil {
		return nil, fmt.Errorf("node %s is already registered (a pending invite is revoked with: qabas replica invite %s --revoke)", name, name)
	}
	if w.WGEndpoint == "" {
		return nil, fmt.Errorf("the writer %s has no WireGuard endpoint; set it with: qabas replica init --endpoint <public-host:port>", w.Name)
	}
	if opt.Node.Upstream != "" && e.Cluster.Node(opt.Node.Upstream) == nil {
		return nil, fmt.Errorf("upstream %q is not registered", opt.Node.Upstream)
	}
	if w.WGPublicKey == "" {
		if err := e.EnsureWGKey(ctx, w.Name); err != nil {
			return nil, err
		}
	}
	pub, err := cluster.EnsureSSHKey(e.Paths)
	if err != nil {
		return nil, fmt.Errorf("cluster SSH key: %w", err)
	}
	kp, err := wg.GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	redact.Register(kp.Private)
	ip, err := e.Cluster.NextWGIP()
	if err != nil {
		return nil, err
	}
	ttl := opt.TTL
	if ttl <= 0 {
		ttl = DefaultInviteTTL
	}
	now := e.now()
	exp := now.Add(ttl)

	var rl joinRelay
	if err := e.EnsureRelayServer(ctx); err != nil {
		if opt.Relay {
			return nil, fmt.Errorf("TLS relay: %w", err)
		}
		e.logf("  warning: TLS relay unavailable (%v); the join script uses direct WireGuard only", err)
	} else if rl.Server, err = e.relayAddr(*w); err != nil {
		return nil, err
	} else if rl.Token, err = e.ensureRelayToken(name); err != nil {
		return nil, err
	}
	rl.Force = opt.Relay
	abort := func() {
		_ = e.Cluster.Remove(name)
		_ = cluster.RemoveNodeSecrets(e.Paths, name)
	}

	n := opt.Node
	n.Role, n.Mode = cluster.RoleStandby, cluster.ModePull
	n.SSHVia, n.WGSSHPort, n.SSHPort = cluster.SSHViaWG, 22, 22
	n.Host, n.SSHUser, n.SSHHostKey, n.WGEndpoint = "", "", "", ""
	n.WGIP, n.WGPublicKey, n.AddedAt = ip, kp.Public, now
	if n.Site == "" {
		n.Site = n.Name
	}
	if opt.Relay {
		e.demoteForRelay(&n)
	}
	e.Cluster.Upsert(n)
	if err := e.Cluster.Validate(); err != nil {
		abort()
		return nil, err
	}
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		abort()
		return nil, err
	}
	if err := e.ApplyWG(ctx, w.Name); err != nil {
		abort()
		_ = cluster.Save(e.Paths, e.Cluster)
		_ = e.ApplyWG(ctx, w.Name)
		return nil, err
	}
	if err := SaveState(e.Paths, &State{Node: name, Phase: PhaseInvited, StartedAt: now, UpdatedAt: now, InviteExpires: &exp, TempWGKey: true}); err != nil {
		return nil, err
	}

	script := e.renderJoin(n, *w, kp.Private, e.clusterKeyLine(n, pub), exp, rl)
	path := InvitePath(e.Paths, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		return nil, err
	}
	return &Invite{Node: name, WGIP: ip, Path: path, ExpiresAt: exp, Script: script}, nil
}

func (e *Engine) renderJoin(n, w cluster.Node, private, keyLine string, exp time.Time, rl joinRelay) string {
	bits := "24"
	if _, b, ok := strings.Cut(e.Cluster.WGNetwork, "/"); ok {
		bits = b
	}
	conf := wg.Interface{
		Address: n.WGIP + "/" + bits, ListenPort: e.Cluster.WGPort, PrivateKey: private,
		Peers: []wg.Peer{{Name: w.Name, PublicKey: w.WGPublicKey, AllowedIPs: []string{w.WGIP + "/32"}, Endpoint: w.WGEndpoint, Keepalive: 25}},
	}.Render()
	r := strings.NewReplacer(
		"@NODE@", n.Name,
		"@CLUSTER@", e.Cluster.Name,
		"@WRITER@", w.Name,
		"@WRITER_WGIP@", w.WGIP,
		"@ENDPOINT@", w.WGEndpoint,
		"@EXPIRES@", exp.UTC().Format(time.RFC3339),
		"@EXPIRES_UNIX@", strconv.FormatInt(exp.Unix(), 10),
		"@PRIVATE@", private,
		"@CONF@", strings.TrimRight(conf, "\n"),
		"@KEYLINE@", keyLine,
		"@KEYPATH@", wg.KeyPath,
		"@CONFPATH@", wg.ConfigPath,
		"@COMMENT@", cluster.SSHKeyComment,
		"@WRITER_PUB@", w.WGPublicKey,
		"@FORCE_RELAY@", map[bool]string{true: "1", false: "0"}[rl.Force],
		"@RELAY_SERVER@", rl.Server,
		"@RELAY_ENV@", strings.TrimRight(e.relayClientEnv(n.Name, rl.Server, rl.Token), "\n"),
		"@RELAY_UNIT@", strings.TrimRight(RelayClientUnitText, "\n"),
		"@RELAY_DIR@", relayNodeDir,
		"@RELAY_LOCAL@", "127.0.0.1:"+strconv.Itoa(cluster.RelayLocalPort),
	)
	return r.Replace(joinTemplate)
}

const joinTemplate = `#!/bin/sh
# qabas replica join: node @NODE@ of cluster @CLUSTER@ (writer @WRITER@).
# Expires @EXPIRES@. This file holds a one-time WireGuard private key: keep it
# secret and delete it after use. Run it on the new replica as root:
#   sudo sh qabas-join-@NODE@.sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo "run as root: sudo sh $0" >&2; exit 1; }
command -v systemctl >/dev/null 2>&1 || { echo "systemd is required" >&2; exit 1; }
if [ "$(date -u +%s)" -ge @EXPIRES_UNIX@ ]; then
  echo "this invite expired at @EXPIRES@; create a new one on the writer: sudo qabas replica invite @NODE@" >&2
  exit 1
fi
need=""
command -v wg >/dev/null 2>&1 || need="$need wireguard-tools"
command -v ping >/dev/null 2>&1 || need="$need iputils-ping"
[ -x /usr/sbin/sshd ] || need="$need openssh-server"
if [ -n "$need" ]; then
  command -v apt-get >/dev/null 2>&1 || { echo "Debian/Ubuntu (apt-get) required; missing:$need" >&2; exit 1; }
  export DEBIAN_FRONTEND=noninteractive
  apt-get -q -o DPkg::Lock::Timeout=600 update >/dev/null
  apt-get -y -q -o DPkg::Lock::Timeout=600 install --no-install-recommends $need >/dev/null
fi
umask 077
mkdir -p "$(dirname @KEYPATH@)" /etc/wireguard
cat > @KEYPATH@ <<'QABAS_EOF'
@PRIVATE@
QABAS_EOF
cat > @CONFPATH@ <<'QABAS_EOF'
@CONF@
QABAS_EOF
chmod 600 @KEYPATH@ @CONFPATH@
systemctl enable wg-quick@qabas0 >/dev/null 2>&1 || true
systemctl restart wg-quick@qabas0
home=$(getent passwd root | cut -d: -f6)
d="$home/.ssh"; f="$d/authorized_keys"
mkdir -p "$d"
touch "$f"
{ grep -v ' @COMMENT@$' "$f" || true; echo '@KEYLINE@'; } > "$f.qabas-tmp"
chmod 600 "$f.qabas-tmp"
mv -f "$f.qabas-tmp" "$f"
chmod 700 "$d"
systemctl enable --now ssh >/dev/null 2>&1 || systemctl enable --now sshd >/dev/null 2>&1 || true

# handshake succeeds once the writer answered over WireGuard (direct or relayed).
handshake() {
  wg show qabas0 latest-handshakes 2>/dev/null | awk -v k='@WRITER_PUB@' '$1 == k && $2 > 0 { f = 1 } END { exit !f }'
}
wait_handshake() {
  n=$(( $1 / 2 ))
  while [ "$n" -gt 0 ]; do
    handshake && return 0
    ping -c 1 -W 1 @WRITER_WGIP@ >/dev/null 2>&1 || true
    sleep 1
    n=$((n - 1))
  done
  handshake
}
use_relay=@FORCE_RELAY@
if [ "$use_relay" = 0 ]; then
  if wait_handshake 20; then
    echo "WireGuard tunnel to @WRITER@ is up (direct)."
  elif [ -n "@RELAY_SERVER@" ]; then
    echo "No WireGuard handshake with @WRITER@ in 20s (the network may block WireGuard); switching to the TLS relay @RELAY_SERVER@."
    use_relay=1
  else
    echo "warning: no reply from @WRITER@ over WireGuard yet; outbound UDP to @ENDPOINT@ must be allowed. The writer keeps waiting." >&2
  fi
fi
if [ "$use_relay" = 1 ]; then
  if [ -z "@RELAY_SERVER@" ]; then
    echo "the writer has no TLS relay; create a new invite after fixing it on the writer" >&2
    exit 1
  fi
  if ! command -v qabas >/dev/null 2>&1 || ! qabas relay connect --help >/dev/null 2>&1; then
    echo "the TLS relay needs a current qabas CLI on this machine; install or update it (qabas update --dev), then run this script again" >&2
    exit 1
  fi
  mkdir -p @RELAY_DIR@
  chmod 700 @RELAY_DIR@
  cat > @RELAY_DIR@/client.env <<'QABAS_EOF'
@RELAY_ENV@
QABAS_EOF
  chmod 600 @RELAY_DIR@/client.env
  cat > /etc/systemd/system/qabas-relay-client.service <<'QABAS_EOF'
@RELAY_UNIT@
QABAS_EOF
  systemctl daemon-reload
  systemctl enable qabas-relay-client.service >/dev/null 2>&1 || true
  systemctl restart qabas-relay-client.service
  sed -i 's|^Endpoint = .*|Endpoint = @RELAY_LOCAL@|' @CONFPATH@
  wg set qabas0 peer '@WRITER_PUB@' endpoint @RELAY_LOCAL@
  if wait_handshake 30; then
    echo "WireGuard tunnel to @WRITER@ is up (via the TLS relay)."
  else
    echo "warning: no handshake through the relay yet; outbound TCP to @RELAY_SERVER@ must be allowed (journalctl -u qabas-relay-client). The writer keeps waiting." >&2
  fi
fi
echo "SSH host key fingerprints of this machine (confirm one of them on the writer):"
for k in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$k"; done
echo "Joined. The writer continues provisioning (sudo qabas replica add @NODE@ --resume)."
`

// pendingInvite reports whether a node is invited and has not started provisioning.
func pendingInvite(st *State) bool {
	return st != nil && st.Phase == PhaseInvited && !st.Done(PhasePreflight)
}

// RevokeInvite removes a pending invited node: registry entry, the writer's WireGuard
// peer (its temporary key stops working), state and the join script.
func (e *Engine) RevokeInvite(ctx context.Context, name string) error {
	st, err := LoadState(e.Paths, name)
	if err != nil {
		return err
	}
	if !pendingInvite(st) {
		return fmt.Errorf("%s has no pending invite (a provisioned or provisioning node is removed with: qabas replica rm %s)", name, name)
	}
	if e.Cluster.Node(name) != nil {
		if err := e.Cluster.Remove(name); err != nil {
			return err
		}
		if err := cluster.Save(e.Paths, e.Cluster); err != nil {
			return err
		}
	}
	if w, err := e.writer(); err == nil {
		if err := e.ApplyWG(ctx, w.Name); err != nil {
			return fmt.Errorf("remove the invite peer from the writer's WireGuard: %w", err)
		}
	}
	_ = RemoveInviteFile(e.Paths, name)
	_ = cluster.RemoveNodeSecrets(e.Paths, name)
	if err := RemoveState(e.Paths, name); err != nil {
		return err
	}
	if err := e.StopRelayServerIfUnused(ctx); err != nil {
		e.logf("  warning: stop the unused relay: %v", err)
	}
	return nil
}

// PruneInvites revokes invites that expired before their node started provisioning.
func (e *Engine) PruneInvites(ctx context.Context) []error {
	var errs []error
	for _, n := range e.Cluster.Standbys() {
		st, err := LoadState(e.Paths, n.Name)
		if err != nil || !pendingInvite(st) || st.InviteExpires == nil || e.now().Before(*st.InviteExpires) {
			continue
		}
		e.logf("  invite for %s expired at %s; revoking it", n.Name, st.InviteExpires.Format(time.RFC3339))
		if err := e.RevokeInvite(ctx, n.Name); err != nil {
			errs = append(errs, fmt.Errorf("revoke expired invite %s: %w", n.Name, err))
		}
	}
	return errs
}

const handshakesScript = "wg show qabas0 latest-handshakes 2>/dev/null || true"

// WaitInvite waits until the invited node completes a WireGuard handshake with the
// writer (the join script ran), or the invite expires.
func (e *Engine) WaitInvite(ctx context.Context, name string, timeout time.Duration) error {
	n := e.Cluster.Node(name)
	if n == nil {
		return fmt.Errorf("node %q is not registered", name)
	}
	w, err := e.writer()
	if err != nil {
		return err
	}
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if st, _ := LoadState(e.Paths, name); st != nil && st.InviteExpires != nil {
		if until := time.Until(*st.InviteExpires); until < timeout {
			deadline = time.Now().Add(until)
		}
	}
	for {
		out, _ := run(ctx, wx, handshakesScript)
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == n.WGPublicKey {
				if ts, _ := strconv.ParseInt(f[1], 10, 64); ts > 0 {
					return e.detectRelay(ctx, name)
				}
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not join before the deadline; run the join script on it, then: qabas replica add %s --resume", name, name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconnectPoll):
		}
	}
}
