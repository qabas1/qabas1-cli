// Package wg generates WireGuard keys and renders hub-and-spoke configs.
// Private keys are returned to callers for storage in 0600 files only.
package wg

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// KeyPair is a Curve25519 WireGuard key pair (base64, as `wg genkey | wg pubkey`).
type KeyPair struct {
	Private string
	Public  string
}

func GenerateKeyPair() (KeyPair, error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return KeyPair{}, err
	}
	// Clamp per RFC 7748 (what `wg genkey` does).
	priv[0] &= 248
	priv[31] = (priv[31] & 127) | 64
	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return KeyPair{}, err
	}
	return KeyPair{
		Private: base64.StdEncoding.EncodeToString(priv[:]),
		Public:  base64.StdEncoding.EncodeToString(pub),
	}, nil
}

// PublicFromPrivate derives the public key.
func PublicFromPrivate(private string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(private))
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("invalid WireGuard private key")
	}
	pub, err := curve25519.X25519(b, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Peer is one [Peer] section.
type Peer struct {
	Name       string
	PublicKey  string
	AllowedIPs []string
	Endpoint   string
	Keepalive  int
}

// Interface renders a full wg-quick config. privateKey is embedded; write it 0600.
type Interface struct {
	Address    string
	ListenPort int
	PrivateKey string
	Peers      []Peer
}

func (i Interface) Render() string {
	var b strings.Builder
	b.WriteString("# Managed by qabas. Do not edit; re-render with: qabas replica sync\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", i.Address)
	if i.ListenPort > 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", i.ListenPort)
	}
	fmt.Fprintf(&b, "PrivateKey = %s\n", i.PrivateKey)
	peers := append([]Peer(nil), i.Peers...)
	sort.Slice(peers, func(a, c int) bool { return peers[a].Name < peers[c].Name })
	for _, p := range peers {
		fmt.Fprintf(&b, "\n# %s\n[Peer]\n", p.Name)
		fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
		fmt.Fprintf(&b, "AllowedIPs = %s\n", strings.Join(p.AllowedIPs, ", "))
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Keepalive > 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return b.String()
}

// SetupScript installs wireguard-tools, writes /etc/wireguard/qabas0.conf from stdin
// (already written by the caller), and enables the interface idempotently.
const SetupScript = "set -eu\n" + installTools + setupBody

const setupBody = `chmod 0600 /etc/wireguard/qabas0.conf
systemctl enable wg-quick@qabas0 >/dev/null 2>&1 || true
if ip link show qabas0 >/dev/null 2>&1; then
  umask 077
  if wg-quick strip qabas0 > /run/qabas0.strip 2>/dev/null && wg syncconf qabas0 /run/qabas0.strip; then :;
  else systemctl restart wg-quick@qabas0; fi
  rm -f /run/qabas0.strip
else
  systemctl start wg-quick@qabas0
fi
`

const ConfigPath = "/etc/wireguard/qabas0.conf"

// KeyPath holds the node's WireGuard private key. It is generated on the node and never
// leaves it; rendered configs carry PrivateKeyPlaceholder, substituted on the node.
const KeyPath = "/etc/qabasone/wg/qabas0.key"

const PrivateKeyPlaceholder = "@@QABAS_WG_PRIVATE_KEY@@"

const installTools = `if ! command -v wg >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then DEBIAN_FRONTEND=noninteractive apt-get install -y -qq wireguard-tools >/dev/null;
  elif command -v dnf >/dev/null 2>&1; then dnf install -y -q wireguard-tools >/dev/null;
  else echo "wireguard-tools missing and no supported package manager" >&2; exit 69; fi
fi
`

// NodeKeyScript installs wireguard-tools, creates the node key if missing, prints the public key.
const NodeKeyScript = "set -eu\n" + installTools + `umask 077
mkdir -p /etc/qabasone/wg
[ -s ` + KeyPath + ` ] || wg genkey > ` + KeyPath + `
chmod 0600 ` + KeyPath + `
wg pubkey < ` + KeyPath + `
`

// ApplyScript reads a rendered config (with the placeholder) on stdin, substitutes the
// node-local private key, installs it 0600 and brings qabas0 up / syncs it.
const ApplyScript = "set -eu\n" + installTools + `umask 077
mkdir -p /etc/wireguard
key=$(cat ` + KeyPath + `)
sed "s|` + PrivateKeyPlaceholder + `|$key|" > ` + ConfigPath + `.tmp
mv -f ` + ConfigPath + `.tmp ` + ConfigPath + `
` + setupBody

// DownScript removes the interface and its config (replica removal).
const DownScript = `set -eu
systemctl disable --now wg-quick@qabas0 >/dev/null 2>&1 || true
rm -f ` + ConfigPath + `
`
