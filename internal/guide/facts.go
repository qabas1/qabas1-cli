// Package guide renders context-aware, step-by-step walkthroughs for the
// qabas command line (`qabas guide <topic>`).
package guide

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/backup"
	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/config"
	"github.com/qabasone/qabasone-cli/internal/host"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/pitr"
	"github.com/qabasone/qabasone-cli/internal/replica"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
	"golang.org/x/crypto/ssh"
)

// Role is this machine's place in a QabasOne deployment.
type Role string

const (
	RoleUnknown Role = ""
	// RoleFresh: QabasOne is not installed and no cluster registry exists.
	RoleFresh Role = "fresh"
	// RoleSingle: QabasOne is installed but no cluster registry exists.
	RoleSingle  Role = "single-node"
	RoleWriter  Role = "writer"
	RoleReplica Role = "replica"
)

// NodeFact is one registered cluster node as seen from this machine.
type NodeFact struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Site       string `json:"site,omitempty"`
	Host       string `json:"host,omitempty"`
	Promotable bool   `json:"promotable"`
	Phase      string `json:"phase,omitempty"`
	LastError  string `json:"lastError,omitempty"`
}

// Facts are read-only observations about this machine. Zero values mean
// "unknown"; detection never fails.
type Facts struct {
	Detected bool `json:"detected"`

	Hostname      string   `json:"hostname,omitempty"`
	OS            string   `json:"os,omitempty"`
	Arch          string   `json:"arch,omitempty"`
	IPs           []string `json:"ips,omitempty"`
	Docker        bool     `json:"docker"`
	DockerVersion string   `json:"dockerVersion,omitempty"`
	Compose       bool     `json:"compose"`
	Systemd       bool     `json:"systemd"`
	SSHD          *bool    `json:"sshd,omitempty"`
	HostKey       string   `json:"hostKey,omitempty"`
	HostKeyType   string   `json:"hostKeyType,omitempty"`
	DiskFreeMiB   int64    `json:"diskFreeMiB,omitempty"`
	Stack         []string `json:"stack,omitempty"`

	Installed   bool   `json:"installed"`
	Release     string `json:"release,omitempty"`
	Maintenance bool   `json:"maintenance"`

	Role      Role       `json:"role"`
	Cluster   string     `json:"cluster,omitempty"`
	Writer    string     `json:"writer,omitempty"`
	Self      string     `json:"self,omitempty"`
	Epoch     int64      `json:"epoch,omitempty"`
	Nodes     []NodeFact `json:"nodes,omitempty"`
	Witnesses int        `json:"witnesses,omitempty"`

	ScheduleEnabled bool   `json:"scheduleEnabled"`
	ScheduleAt      string `json:"scheduleAt,omitempty"`
	ScheduleTZ      string `json:"scheduleTimezone,omitempty"`
	Offsite         string `json:"offsite,omitempty"`
	PITREnabled     bool   `json:"pitrEnabled"`
	Backups         int    `json:"backups"`
	LatestBackup    string `json:"latestBackup,omitempty"`
}

// Standbys returns the registered non-writer nodes.
func (f Facts) Standbys() []NodeFact {
	var out []NodeFact
	for _, n := range f.Nodes {
		if n.Name != f.Writer {
			out = append(out, n)
		}
	}
	return out
}

// Node returns the registered node with this name.
func (f Facts) Node(name string) *NodeFact {
	for i := range f.Nodes {
		if f.Nodes[i].Name == name {
			return &f.Nodes[i]
		}
	}
	return nil
}

// RoleText describes the role for humans.
func (f Facts) RoleText() string {
	switch f.Role {
	case RoleFresh:
		return "not registered (QabasOne not installed)"
	case RoleSingle:
		return "single-node writer (no cluster registry)"
	case RoleWriter:
		return "writer of cluster " + f.Cluster
	case RoleReplica:
		return "replica " + f.Self + " of writer " + f.Writer
	}
	return "unknown"
}

// Probes are the host lookups Detect relies on. Injectable for tests.
type Probes struct {
	Host        func() host.Report
	Hostname    func() (string, error)
	IPs         func() []string
	Systemd     func() bool
	SSHD        func() *bool
	HostKey     func() (fingerprint, keyType string)
	Stack       func() []string
	DiskFreeMiB func(dir string) int64
	// SelfName resolves this machine's node name in the registry.
	SelfName func(c *cluster.Cluster) string
}

// DefaultProbes inspects the real host.
func DefaultProbes() Probes {
	return Probes{
		Host:        host.Inspect,
		Hostname:    os.Hostname,
		IPs:         localIPv4s,
		Systemd:     func() bool { _, err := exec.LookPath("systemctl"); return err == nil },
		SSHD:        sshdListening,
		HostKey:     localHostKey,
		Stack:       qabasContainers,
		DiskFreeMiB: diskFreeMiB,
		SelfName: func(c *cluster.Cluster) string {
			if p := c.Primary(); p != nil {
				return p.Name
			}
			return ""
		},
	}
}

// Detect gathers Facts from local files and the probes. It never fails.
func Detect(p paths.Paths, pr Probes) Facts {
	f := Facts{Detected: true}
	if pr.Host != nil {
		r := pr.Host()
		f.OS, f.Arch = r.OS, r.Arch
		f.Docker, f.DockerVersion, f.Compose = r.Docker, r.DockerVer, r.Compose
	}
	if pr.Hostname != nil {
		if h, err := pr.Hostname(); err == nil {
			f.Hostname = h
		}
	}
	if pr.IPs != nil {
		f.IPs = pr.IPs()
	}
	if pr.Systemd != nil {
		f.Systemd = pr.Systemd()
	}
	if pr.SSHD != nil {
		f.SSHD = pr.SSHD()
	}
	if pr.HostKey != nil {
		f.HostKey, f.HostKeyType = pr.HostKey()
	}
	if pr.Stack != nil {
		f.Stack = pr.Stack()
	}
	if pr.DiskFreeMiB != nil {
		f.DiskFreeMiB = pr.DiskFreeMiB(p.StateDir)
	}

	if st, err := state.Load(p); err == nil {
		f.Installed = st.Installed()
		f.Release = st.CurrentRelease
		f.Maintenance = st.Maintenance
	}

	c, err := cluster.Load(p)
	switch {
	case errors.Is(err, cluster.ErrNotFound):
		f.Role = RoleFresh
		if f.Installed {
			f.Role = RoleSingle
		}
	case err == nil:
		f.Cluster, f.Writer, f.Epoch, f.Witnesses = c.Name, c.Writer, c.Epoch, len(c.Witnesses)
		if pr.SelfName != nil {
			f.Self = pr.SelfName(c)
		}
		f.Role = RoleReplica
		if f.Self == "" || f.Self == c.Writer {
			f.Role = RoleWriter
		}
		for _, n := range c.Nodes {
			nf := NodeFact{Name: n.Name, Role: n.Role, Site: n.Site, Host: n.Host, Promotable: n.Promotable}
			if rs, _ := replica.LoadState(p, n.Name); rs != nil {
				nf.Phase, nf.LastError = string(rs.Phase), rs.LastError
			}
			f.Nodes = append(f.Nodes, nf)
		}
	}

	if cfg, err := config.Load(p); err == nil {
		s := cfg.Backup.Schedule
		f.ScheduleEnabled, f.ScheduleAt, f.ScheduleTZ, f.Offsite = s.Enabled, s.At, s.Zone(), s.Offsite
	}
	f.PITREnabled = (&pitr.Manager{ConfigDir: p.ConfigDir, StateDir: p.StateDir}).Enabled()
	if items, err := backup.List(p); err == nil {
		f.Backups = len(items)
		var newest time.Time
		for _, m := range items {
			if m.CreatedAt.After(newest) {
				newest, f.LatestBackup = m.CreatedAt, m.ID
			}
		}
	}
	return f
}

func localIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(ifc.Name)
		if strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") ||
			strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "wg") {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

func sshdListening() *bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:22", 500*time.Millisecond)
	ok := err == nil
	if ok {
		_ = c.Close()
	}
	return &ok
}

// hostKeyFiles are tried in the order the writer's SSH client prefers algorithms.
var hostKeyFiles = []string{
	"/etc/ssh/ssh_host_ed25519_key.pub",
	"/etc/ssh/ssh_host_ecdsa_key.pub",
	"/etc/ssh/ssh_host_rsa_key.pub",
}

func localHostKey() (string, string) {
	for _, f := range hostKeyFiles {
		if fp, kind := hostKeyFromFile(f); fp != "" {
			return fp, kind
		}
	}
	return "", ""
}

func hostKeyFromFile(file string) (string, string) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", ""
	}
	key, _, _, _, err := ssh.ParseAuthorizedKey(b)
	if err != nil {
		return "", ""
	}
	return ssh.FingerprintSHA256(key), key.Type()
}

func qabasContainers() []string {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil
	}
	out, err := exec.Command("docker", "ps", "-a", "--filter", "name=^qabasone-", "--format", "{{.Names}}").Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

func diskFreeMiB(dir string) int64 {
	d, err := sysinfo.DiskUsage(dir)
	if err != nil {
		return 0
	}
	return int64(d.FreeBytes >> 20)
}

var nodeNameUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// NodeNameFromHostname derives a valid registry node name from a hostname.
func NodeNameFromHostname(h string) string {
	h = strings.ToLower(strings.SplitN(h, ".", 2)[0])
	h = strings.Trim(nodeNameUnsafe.ReplaceAllString(h, "-"), "-")
	if h == "" || cluster.ValidateNodeName(h) != nil {
		return ""
	}
	return h
}
