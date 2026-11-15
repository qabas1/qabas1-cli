// Package cluster is the node registry for multi-node / multi-site QabasOne.
//
// The registry (/etc/qabasone/cluster.yaml) holds topology only: nodes, sites, roles,
// WireGuard public keys, pinned SSH host-key fingerprints, image digests, writer epoch.
// It never holds secrets. Per-node secrets live in /etc/qabasone/nodes/<name>/ (0700 dir,
// 0600 files), see secrets.go.
package cluster

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/qabas1/qabasone-cli/internal/paths"
)

const (
	SchemaVersion = 1

	RolePrimary = "primary"
	RoleStandby = "standby"

	ModePush = "push"
	ModePull = "pull"

	// SSHViaWG reaches a node's sshd over its WireGuard address (nodes behind NAT).
	SSHViaWG = "wg"

	// Default WireGuard hub network. The primary is always .1.
	DefaultWGNetwork = "10.66.0.0/24"
	DefaultWGPort    = 51820

	// DefaultRelayPort is the writer's TLS relay port (WireGuard carried over TLS for
	// replicas whose network drops WireGuard).
	DefaultRelayPort = 8443
	// RelayLocalPort is the loopback UDP port the relay client listens on; a relay
	// node's WireGuard peer endpoint for the writer is 127.0.0.1:RelayLocalPort.
	RelayLocalPort = 51821
)

var nodeNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}[a-z0-9]$`)

// Node is one QabasOne installation (primary or standby).
type Node struct {
	Name    string `yaml:"name" json:"name"`
	Site    string `yaml:"site" json:"site"`
	Role    string `yaml:"role" json:"role"`
	Host    string `yaml:"host" json:"host"`
	SSHUser string `yaml:"sshUser,omitempty" json:"sshUser,omitempty"`
	SSHPort int    `yaml:"sshPort,omitempty" json:"sshPort,omitempty"`
	// SSHHostKey is the pinned SHA256 fingerprint (ssh-keygen -lf style, "SHA256:...").
	SSHHostKey string `yaml:"sshHostKey,omitempty" json:"sshHostKey,omitempty"`
	// SSHVia is "" (Host:SSHPort) or "wg" (WGIP:WGSSHPort, for nodes behind NAT).
	SSHVia      string `yaml:"sshVia,omitempty" json:"sshVia,omitempty"`
	WGSSHPort   int    `yaml:"wgSSHPort,omitempty" json:"wgSSHPort,omitempty"`
	WGIP        string `yaml:"wgIP,omitempty" json:"wgIP,omitempty"`
	WGPublicKey string `yaml:"wgPublicKey,omitempty" json:"wgPublicKey,omitempty"`
	WGEndpoint  string `yaml:"wgEndpoint,omitempty" json:"wgEndpoint,omitempty"`
	// Relay carries this node's WireGuard traffic to the writer over the TLS relay
	// (its network drops WireGuard). Relay nodes peer with the writer only.
	Relay bool `yaml:"relay,omitempty" json:"relay,omitempty"`
	// Upstream is the node this standby streams from (empty = current writer; set for cascading).
	Upstream   string    `yaml:"upstream,omitempty" json:"upstream,omitempty"`
	Promotable bool      `yaml:"promotable" json:"promotable"`
	Priority   int       `yaml:"priority,omitempty" json:"priority,omitempty"`
	Mode       string    `yaml:"mode,omitempty" json:"mode,omitempty"`
	PublicURL  string    `yaml:"publicURL,omitempty" json:"publicURL,omitempty"`
	AddedAt    time.Time `yaml:"addedAt,omitempty" json:"addedAt,omitempty"`
}

// Site groups nodes in one location (office, datacenter, region).
type Site struct {
	Name string `yaml:"name" json:"name"`
	// LocalDomain is the split-horizon name company DNS resolves to the site HAProxy.
	LocalDomain string `yaml:"localDomain,omitempty" json:"localDomain,omitempty"`
	// WALBudgetGB bounds max_slot_wal_keep_size contribution for this site's standbys.
	WALBudgetGB int `yaml:"walBudgetGB,omitempty" json:"walBudgetGB,omitempty"`
}

// Witness is a vote in the writer-lease quorum.
type Witness struct {
	Name          string `yaml:"name" json:"name"`
	Kind          string `yaml:"kind" json:"kind"` // http | file
	URL           string `yaml:"url,omitempty" json:"url,omitempty"`
	Path          string `yaml:"path,omitempty" json:"path,omitempty"`
	FailureDomain string `yaml:"failureDomain,omitempty" json:"failureDomain,omitempty"`
}

const (
	LeaseModeLocalOnly = "local-only"
	LeaseModeStrict    = "strict"
)

// Cluster is the registry document.
type Cluster struct {
	SchemaVersion int               `yaml:"schemaVersion" json:"schemaVersion"`
	Name          string            `yaml:"name" json:"name"`
	Writer        string            `yaml:"writer" json:"writer"`
	Epoch         int64             `yaml:"epoch" json:"epoch"`
	WGNetwork     string            `yaml:"wgNetwork,omitempty" json:"wgNetwork,omitempty"`
	WGPort        int               `yaml:"wgPort,omitempty" json:"wgPort,omitempty"`
	Release       string            `yaml:"release,omitempty" json:"release,omitempty"`
	Images        map[string]string `yaml:"images,omitempty" json:"images,omitempty"`
	LeaseTTLSec   int               `yaml:"leaseTTLSeconds,omitempty" json:"leaseTTLSeconds,omitempty"`
	LeaseMode     string            `yaml:"leaseMode,omitempty" json:"leaseMode,omitempty"`
	PublicProbe   string            `yaml:"publicProbe,omitempty" json:"publicProbe,omitempty"`
	RelayPort     int               `yaml:"relayPort,omitempty" json:"relayPort,omitempty"`
	// RelayCertSHA256 pins the writer relay's self-signed TLS certificate (hex SHA-256 of DER).
	RelayCertSHA256 string    `yaml:"relayCertSHA256,omitempty" json:"relayCertSHA256,omitempty"`
	Sites           []Site    `yaml:"sites,omitempty" json:"sites,omitempty"`
	Nodes           []Node    `yaml:"nodes" json:"nodes"`
	Witnesses       []Witness `yaml:"witnesses,omitempty" json:"witnesses,omitempty"`
}

var ErrNotFound = errors.New("cluster registry not found")

func File(p paths.Paths) string { return filepath.Join(p.ConfigDir, "cluster.yaml") }

// Load reads the registry. Returns ErrNotFound when absent.
func Load(p paths.Paths) (*Cluster, error) {
	b, err := os.ReadFile(File(p))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var c Cluster
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", File(p), err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", File(p), err)
	}
	return &c, nil
}

// Save writes the registry atomically (0644: topology is not secret).
func Save(p paths.Paths, c *Cluster) error {
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		return err
	}
	tmp := File(p) + ".tmp"
	header := []byte("# Managed by qabas (qabas replica / qabas dr). Topology only - never secrets.\n")
	if err := os.WriteFile(tmp, append(header, b...), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, File(p))
}

func (c *Cluster) applyDefaults() {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if c.WGNetwork == "" {
		c.WGNetwork = DefaultWGNetwork
	}
	if c.WGPort == 0 {
		c.WGPort = DefaultWGPort
	}
	if c.LeaseTTLSec == 0 {
		c.LeaseTTLSec = 60
	}
	for i := range c.Nodes {
		if c.Nodes[i].SSHPort == 0 {
			c.Nodes[i].SSHPort = 22
		}
		if c.Nodes[i].Mode == "" {
			c.Nodes[i].Mode = ModePush
		}
		if c.Nodes[i].Site == "" {
			c.Nodes[i].Site = c.Nodes[i].Name
		}
	}
	c.repairPrimaryWGIP()
}

// repairPrimaryWGIP fixes registries written by `replica init` before WGNetwork was
// defaulted, which stored the primary's WireGuard IP as "1".
func (c *Cluster) repairPrimaryWGIP() {
	p := c.Primary()
	if p == nil || p.WGIP == "" {
		return
	}
	if _, err := netip.ParseAddr(p.WGIP); err == nil {
		return
	}
	hub := c.HubIP()
	for _, n := range c.Nodes {
		if n.WGIP == hub {
			return
		}
	}
	p.WGIP = hub
}

// Validate enforces registry invariants, most importantly exactly one writer.
func (c *Cluster) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schemaVersion %d", c.SchemaVersion)
	}
	if c.LeaseMode != "" && c.LeaseMode != LeaseModeLocalOnly && c.LeaseMode != LeaseModeStrict {
		return fmt.Errorf("leaseMode %q must be %q or %q", c.LeaseMode, LeaseModeLocalOnly, LeaseModeStrict)
	}
	wgNet, err := netip.ParsePrefix(c.wgNetwork())
	if err != nil || !wgNet.Addr().Is4() {
		return fmt.Errorf("wgNetwork %q must be an IPv4 x.y.z.0/24 network", c.wgNetwork())
	}
	seen := map[string]bool{}
	wgSeen := map[string]string{}
	primaries := 0
	for _, n := range c.Nodes {
		if err := ValidateNodeName(n.Name); err != nil {
			return err
		}
		if n.Name == SharedScope {
			return fmt.Errorf("node name %q is reserved", n.Name)
		}
		if seen[n.Name] {
			return fmt.Errorf("duplicate node %q", n.Name)
		}
		seen[n.Name] = true
		switch n.Role {
		case RolePrimary:
			primaries++
		case RoleStandby:
		default:
			return fmt.Errorf("node %q: role must be %q or %q", n.Name, RolePrimary, RoleStandby)
		}
		if n.Mode != ModePush && n.Mode != ModePull {
			return fmt.Errorf("node %q: mode must be push or pull", n.Name)
		}
		if n.WGIP != "" {
			if ip, err := netip.ParseAddr(n.WGIP); err != nil || !ip.Is4() || !wgNet.Contains(ip) {
				return fmt.Errorf("node %q: wgIP %q is not an IPv4 address in %s", n.Name, n.WGIP, wgNet)
			}
			if other, ok := wgSeen[n.WGIP]; ok {
				return fmt.Errorf("nodes %q and %q share WireGuard IP %s", other, n.Name, n.WGIP)
			}
			wgSeen[n.WGIP] = n.Name
		}
		if n.Upstream == n.Name {
			return fmt.Errorf("node %q cannot stream from itself", n.Name)
		}
		switch n.SSHVia {
		case "":
		case SSHViaWG:
			if n.WGIP == "" {
				return fmt.Errorf("node %q: sshVia wg requires a WireGuard address", n.Name)
			}
		default:
			return fmt.Errorf("node %q: sshVia must be empty or %q", n.Name, SSHViaWG)
		}
	}
	if len(c.Nodes) > 0 && primaries != 1 {
		return fmt.Errorf("registry must have exactly one primary node (found %d)", primaries)
	}
	if c.Writer != "" {
		w := c.Node(c.Writer)
		if w == nil {
			return fmt.Errorf("writer %q is not a registered node", c.Writer)
		}
		if w.Role != RolePrimary {
			return fmt.Errorf("writer %q must have role primary", c.Writer)
		}
	}
	for _, n := range c.Nodes {
		if n.Upstream != "" && c.Node(n.Upstream) == nil {
			return fmt.Errorf("node %q: upstream %q is not registered", n.Name, n.Upstream)
		}
		if u := c.Node(n.Upstream); u != nil && u.Relay {
			return fmt.Errorf("node %q: upstream %q uses the relay and is reachable only from the writer", n.Name, n.Upstream)
		}
		if !n.Relay {
			continue
		}
		switch {
		case n.Role != RoleStandby:
			return fmt.Errorf("node %q: only standbys can use the relay", n.Name)
		case n.Mode != ModePull:
			return fmt.Errorf("node %q: relay nodes must use mode pull", n.Name)
		case n.Promotable:
			return fmt.Errorf("node %q: relay nodes cannot be promotable (other nodes cannot reach them)", n.Name)
		case n.Upstream != "" && n.Upstream != c.Writer:
			return fmt.Errorf("node %q: relay nodes stream from the writer only", n.Name)
		}
	}
	return nil
}

// ValidateLeaseMode checks the topology required before explicitly selecting a mode.
// Empty mode is retained only so older registries can load and be migrated safely.
func (c *Cluster) ValidateLeaseMode(mode string) error {
	switch mode {
	case LeaseModeLocalOnly:
		if len(c.Witnesses) != 0 {
			return fmt.Errorf("local-only mode requires zero witnesses; remove them explicitly before changing mode")
		}
	case LeaseModeStrict:
		if len(c.Witnesses) < 3 {
			return fmt.Errorf("strict mode requires at least three independent witnesses")
		}
		names, domains, endpoints := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, w := range c.Witnesses {
			if w.Name == "" || names[w.Name] {
				return fmt.Errorf("witness names must be nonempty and unique")
			}
			names[w.Name] = true
			if w.FailureDomain == "" || domains[w.FailureDomain] {
				return fmt.Errorf("witness %s must declare a unique failure domain", w.Name)
			}
			domains[w.FailureDomain] = true
			endpoint := w.URL
			if w.Kind == "file" {
				endpoint = w.Path
			}
			if endpoint == "" || endpoints[endpoint] || (w.Kind != "http" && w.Kind != "file") {
				return fmt.Errorf("witness %s must have a valid unique endpoint", w.Name)
			}
			endpoints[endpoint] = true
		}
	default:
		return fmt.Errorf("lease mode must be explicitly selected as %q or %q", LeaseModeLocalOnly, LeaseModeStrict)
	}
	return nil
}

// RelayPortOrDefault is the writer's TLS relay port.
func (c *Cluster) RelayPortOrDefault() int {
	if c.RelayPort > 0 {
		return c.RelayPort
	}
	return DefaultRelayPort
}

// HasRelayNodes reports whether any node uses the TLS relay.
func (c *Cluster) HasRelayNodes() bool {
	for _, n := range c.Nodes {
		if n.Relay {
			return true
		}
	}
	return false
}

func ValidateNodeName(name string) error {
	if !nodeNameRe.MatchString(name) {
		return fmt.Errorf("invalid node name %q (lowercase letters, digits, dashes; 2-40 chars)", name)
	}
	return nil
}

// SSHEndpoint is where qabas dials the node's sshd.
func (n Node) SSHEndpoint() (host string, port int) {
	if n.SSHVia == SSHViaWG {
		if n.WGSSHPort > 0 {
			return n.WGIP, n.WGSSHPort
		}
		return n.WGIP, 22
	}
	if n.SSHPort > 0 {
		return n.Host, n.SSHPort
	}
	return n.Host, 22
}

// WALBudgetGB is the per-slot WAL retention bound on upstreams: the largest site
// budget, or defaultGB when no site sets one.
func (c *Cluster) WALBudgetGB(defaultGB int) int {
	best := 0
	for _, s := range c.Sites {
		if s.WALBudgetGB > best {
			best = s.WALBudgetGB
		}
	}
	if best == 0 {
		return defaultGB
	}
	return best
}

// SlotName is the per-node physical replication slot on the upstream.
func SlotName(node string) string {
	return "qabas_replica_" + strings.ReplaceAll(node, "-", "_")
}

// ApplicationName is the per-node libpq application_name (shows in pg_stat_replication).
func ApplicationName(node string) string { return "qabas_" + strings.ReplaceAll(node, "-", "_") }

func (c *Cluster) Node(name string) *Node {
	for i := range c.Nodes {
		if c.Nodes[i].Name == name {
			return &c.Nodes[i]
		}
	}
	return nil
}

// Primary returns the node holding role primary.
func (c *Cluster) Primary() *Node {
	for i := range c.Nodes {
		if c.Nodes[i].Role == RolePrimary {
			return &c.Nodes[i]
		}
	}
	return nil
}

// Standbys returns standby nodes sorted by name.
func (c *Cluster) Standbys() []Node {
	var out []Node
	for _, n := range c.Nodes {
		if n.Role == RoleStandby {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UpstreamOf resolves where a standby streams from (cascading or writer).
func (c *Cluster) UpstreamOf(n Node) *Node {
	if n.Upstream != "" {
		return c.Node(n.Upstream)
	}
	return c.Primary()
}

// Downstreams returns nodes that stream directly from name.
func (c *Cluster) Downstreams(name string) []Node {
	var out []Node
	primary := c.Primary()
	for _, n := range c.Nodes {
		if n.Role != RoleStandby {
			continue
		}
		if n.Upstream == name || (n.Upstream == "" && primary != nil && primary.Name == name) {
			out = append(out, n)
		}
	}
	return out
}

// Upsert inserts or replaces a node by name.
func (c *Cluster) Upsert(n Node) {
	for i := range c.Nodes {
		if c.Nodes[i].Name == n.Name {
			c.Nodes[i] = n
			return
		}
	}
	c.Nodes = append(c.Nodes, n)
}

// Remove deletes a node; refuses to remove the primary.
func (c *Cluster) Remove(name string) error {
	for i := range c.Nodes {
		if c.Nodes[i].Name != name {
			continue
		}
		if c.Nodes[i].Role == RolePrimary {
			return fmt.Errorf("refusing to remove the primary %q", name)
		}
		for _, d := range c.Downstreams(name) {
			return fmt.Errorf("node %q still feeds %q; re-point it first", name, d.Name)
		}
		c.Nodes = append(c.Nodes[:i], c.Nodes[i+1:]...)
		return nil
	}
	return fmt.Errorf("node %q not found", name)
}

// NextWGIP allocates the next free host address in the WireGuard /24.
func (c *Cluster) NextWGIP() (string, error) {
	prefix := strings.TrimSuffix(strings.SplitN(c.wgNetwork(), "/", 2)[0], "0")
	if !strings.HasSuffix(prefix, ".") {
		return "", fmt.Errorf("wgNetwork %q must be an x.y.z.0/24 network", c.wgNetwork())
	}
	used := map[string]bool{}
	for _, n := range c.Nodes {
		used[n.WGIP] = true
	}
	for host := 2; host < 255; host++ {
		ip := fmt.Sprintf("%s%d", prefix, host)
		if !used[ip] {
			return ip, nil
		}
	}
	return "", errors.New("WireGuard network is full")
}

// HubIP is the primary's WireGuard address (.1).
func (c *Cluster) HubIP() string {
	prefix := strings.TrimSuffix(strings.SplitN(c.wgNetwork(), "/", 2)[0], "0")
	return prefix + "1"
}

func (c *Cluster) wgNetwork() string {
	if c.WGNetwork == "" {
		return DefaultWGNetwork
	}
	return c.WGNetwork
}

// Promote rewrites roles after a successful promotion: target becomes primary, the old
// primary becomes a standby, epoch advances, and nodes that streamed from either
// re-point to the new writer (timeline-follow).
func (c *Cluster) Promote(target string, newEpoch int64) error {
	t := c.Node(target)
	if t == nil {
		return fmt.Errorf("node %q not found", target)
	}
	if t.Role == RolePrimary {
		return fmt.Errorf("node %q is already primary", target)
	}
	if newEpoch <= c.Epoch {
		return fmt.Errorf("epoch must advance (current %d, got %d)", c.Epoch, newEpoch)
	}
	old := c.Primary()
	for i := range c.Nodes {
		n := &c.Nodes[i]
		switch {
		case n.Name == target:
			n.Role = RolePrimary
			n.Upstream = ""
		case old != nil && n.Name == old.Name:
			n.Role = RoleStandby
			n.Upstream = ""
		case n.Upstream == target:
			n.Upstream = ""
		}
	}
	c.Writer = target
	c.Epoch = newEpoch
	return c.Validate()
}
