package replica

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
)

// AddOptions describes a replica to provision.
type AddOptions struct {
	Node    cluster.Node
	Resume  bool
	Rebuild bool
	// RefreshStorage removes and recreates the MinIO replication link on resume.
	RefreshStorage bool
	// Confirm is asked before disruptive actions on the writer (enabling replication
	// restarts PostgreSQL once).
	Confirm func(prompt string) error
	// Lease writes the writer lease (writer.json) before the writer API is recreated
	// with lease enforcement.
	Lease func(ctx context.Context) error
	// Bootstrap prepares a bare Debian/Ubuntu host (nil = the host must already have
	// Docker, compose v2 and the qabas CLI).
	Bootstrap *BootstrapOptions
	// Audit records changes made on the writer outside the replica (nil = log only).
	Audit func(action, detail string)
	// Relay carries WireGuard over the writer's TLS relay from the start (otherwise it
	// is used only when direct WireGuard gets no handshake).
	Relay bool
}

// ErrNotModified wraps preflight failures: nothing was changed on the replica.
var ErrNotModified = errors.New("Replica was NOT modified.")

// Preflight gathers read-only facts from the new node and the writer.
type Preflight struct {
	OS, Arch, Docker, Compose string
	Systemd                   bool
	DiskFreeKB                int64
	ExistingStack             []string
	PGVolume                  bool
	QabasPath                 string
	WriterArch                string
	WriterDBBytes             int64
	OSID, OSVersion           string
	MachineID                 string
	RebootRequired            bool
	WireGuard                 bool
	// ClusterName is the cluster.yaml name already on the host ("" = none).
	ClusterName string
	// Installable lists what BOOTSTRAP installs (only when bootstrap is enabled).
	Installable []string
}

const preflightScript = `set -u
echo "os=$(uname -s)"
if [ -r /etc/os-release ]; then (. /etc/os-release; echo "osid=${ID:-}"; echo "osversion=${VERSION_ID:-}"); fi
echo "machineid=$(cat /etc/machine-id 2>/dev/null)"
if [ -e /var/run/reboot-required ]; then echo "reboot=yes"; else echo "reboot=no"; fi
if command -v wg >/dev/null 2>&1; then echo "wg=yes"; else echo "wg=no"; fi
echo "cluster=$(sed -n 's/^name: *//p' /etc/qabasone/cluster.yaml 2>/dev/null | head -n 1)"
echo "arch=$(uname -m)"
if command -v docker >/dev/null 2>&1; then echo "docker=$(docker version --format '{{.Server.Version}}' 2>/dev/null)"; else echo "docker="; fi
if docker compose version --short >/dev/null 2>&1; then echo "compose=$(docker compose version --short)"; else echo "compose="; fi
if command -v systemctl >/dev/null 2>&1; then echo "systemd=yes"; else echo "systemd=no"; fi
d=/var/lib/docker; [ -d "$d" ] || d=/
echo "diskfree_kb=$(df -Pk "$d" | awk 'NR==2{print $4}')"
echo "stack=$(docker ps -a --filter name=^qabasone- --format '{{.Names}}' 2>/dev/null | tr '\n' ',')"
echo "pgvolume=$(docker volume ls -q --filter name=^` + PGVolume + `$ 2>/dev/null)"
echo "qabas=$(command -v qabas 2>/dev/null)"
`

func (e *Engine) preflight(ctx context.Context, name string, opt AddOptions) (Preflight, error) {
	var pf Preflight
	x, err := e.Exec(ctx, name)
	if err != nil {
		return pf, err
	}
	out, err := run(ctx, x, preflightScript)
	if err != nil {
		return pf, err
	}
	f := facts(out)
	pf.OS, pf.Arch, pf.Docker, pf.Compose = f["os"], f["arch"], f["docker"], f["compose"]
	pf.Systemd = f["systemd"] == "yes"
	pf.DiskFreeKB, _ = strconv.ParseInt(f["diskfree_kb"], 10, 64)
	for _, s := range strings.Split(f["stack"], ",") {
		if s != "" {
			pf.ExistingStack = append(pf.ExistingStack, s)
		}
	}
	pf.PGVolume = f["pgvolume"] != ""
	pf.QabasPath = f["qabas"]
	pf.OSID, pf.OSVersion = strings.ToLower(f["osid"]), f["osversion"]
	pf.MachineID = f["machineid"]
	pf.RebootRequired = f["reboot"] == "yes"
	pf.WireGuard = f["wg"] == "yes"
	pf.ClusterName = strings.Trim(f["cluster"], `"'`)

	w, err := e.writer()
	if err != nil {
		return pf, err
	}
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		return pf, err
	}
	pf.WriterArch, _ = run(ctx, wx, "uname -m")
	if rows, err := pgctl.Query(ctx, wx, "SELECT sum(pg_database_size(datname))::bigint FROM pg_database;"); err == nil && len(rows) == 1 {
		pf.WriterDBBytes, _ = strconv.ParseInt(rows[0][0], 10, 64)
	}
	writerMachineID, _ := run(ctx, wx, "cat /etc/machine-id 2>/dev/null || true")

	var problems []string
	if pf.OS != "Linux" {
		problems = append(problems, fmt.Sprintf("OS is %q (Linux required)", pf.OS))
	}
	if pf.MachineID != "" && strings.TrimSpace(writerMachineID) == pf.MachineID {
		problems = append(problems, fmt.Sprintf("%s is the writer itself (same /etc/machine-id); a replica must be another machine", name))
	}
	if pf.ClusterName != "" && pf.ClusterName != e.Cluster.Name {
		problems = append(problems, fmt.Sprintf("the host already belongs to cluster %q (/etc/qabasone/cluster.yaml); remove it from that cluster first", pf.ClusterName))
	}
	if pf.WriterArch != "" && pf.Arch != pf.WriterArch {
		problems = append(problems, fmt.Sprintf("CPU architecture %s differs from the writer's %s (physical replication needs the same)", pf.Arch, pf.WriterArch))
	}
	if !pf.Systemd {
		problems = append(problems, "systemd is required (WireGuard and the lease timer)")
	}
	if opt.Bootstrap != nil {
		if err := SupportedOS(pf.OSID, pf.OSVersion); err != nil {
			problems = append(problems, err.Error()+" (or prepare the host yourself and pass --no-bootstrap)")
		}
		if pf.Docker == "" || pf.Compose == "" {
			pf.Installable = append(pf.Installable, "Docker Engine + compose v2")
		}
		if !pf.WireGuard {
			pf.Installable = append(pf.Installable, "wireguard-tools")
		}
		if pf.QabasPath == "" {
			pf.Installable = append(pf.Installable, "qabas CLI")
		}
	} else {
		if pf.Docker == "" {
			problems = append(problems, "Docker Engine is not installed or not running")
		}
		if pf.Compose == "" {
			problems = append(problems, "docker compose v2 plugin is missing")
		}
		if pf.QabasPath == "" {
			problems = append(problems, "qabas CLI is not installed ("+InstallHint+")")
		}
	}
	needKB := pf.WriterDBBytes*2/1024 + 5*1024*1024
	if pf.DiskFreeKB > 0 && pf.DiskFreeKB < needKB {
		problems = append(problems, fmt.Sprintf("only %d MiB free; need at least %d MiB (2x database + 5 GiB)", pf.DiskFreeKB/1024, needKB/1024))
	}
	if len(pf.ExistingStack) > 0 && !opt.Rebuild && !opt.Resume {
		problems = append(problems, fmt.Sprintf("an existing QabasOne stack is present (%s); use --rebuild to replace it (its local data is discarded)", strings.Join(pf.ExistingStack, ", ")))
	}
	if len(problems) > 0 {
		return pf, fmt.Errorf("preflight failed on %s:\n  - %s\n%w", name, strings.Join(problems, "\n  - "), ErrNotModified)
	}
	if len(pf.Installable) > 0 {
		e.logf("  %-10s will install on %s: %s", "", name, strings.Join(pf.Installable, ", "))
	}
	return pf, nil
}

// SupportedOS accepts the Debian-family releases BOOTSTRAP can prepare.
func SupportedOS(id, version string) error {
	major := func() int {
		v, _, _ := strings.Cut(version, ".")
		n, _ := strconv.Atoi(v)
		return n
	}
	switch id {
	case "ubuntu":
		if major() >= 22 {
			return nil
		}
		return fmt.Errorf("Ubuntu %s is too old (22.04 or newer required)", version)
	case "debian":
		if major() >= 12 {
			return nil
		}
		return fmt.Errorf("Debian %s is too old (12 or newer required)", version)
	case "":
		return fmt.Errorf("cannot identify the Linux distribution (/etc/os-release missing)")
	default:
		return fmt.Errorf("distribution %q is not supported for automatic setup (Ubuntu 22.04+ or Debian 12+)", id)
	}
}

// Add provisions (or resumes provisioning of) a read-only replica.
func (e *Engine) Add(ctx context.Context, opt AddOptions) (*Report, error) {
	if opt.RefreshStorage && !opt.Resume {
		return nil, fmt.Errorf("--refresh-storage requires --resume")
	}
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
	st, err := LoadState(e.Paths, name)
	if err != nil {
		return nil, err
	}
	switch {
	case st == nil || opt.Rebuild:
		invited := st != nil && st.TempWGKey
		st = &State{Node: name, StartedAt: e.now(), TempWGKey: invited}
	case st.Phase == PhaseInvited:
		// An invited node continues where the join script left it.
	case st.Phase == PhaseReady && !opt.Resume:
		return nil, fmt.Errorf("replica %s is already provisioned; use `qabas replica sync %s` or --rebuild", name, name)
	case !opt.Resume:
		return nil, fmt.Errorf("a previous provisioning of %s stopped after %s; re-run with --resume (or --rebuild)", name, st.Phase)
	}

	isNew := e.Cluster.Node(name) == nil
	if isNew {
		n := opt.Node
		n.Role = cluster.RoleStandby
		if n.Mode == "" {
			n.Mode = cluster.ModePush
		}
		if n.SSHPort == 0 {
			n.SSHPort = 22
		}
		if n.Site == "" {
			n.Site = n.Name
		}
		if n.WGIP == "" {
			ip, err := e.Cluster.NextWGIP()
			if err != nil {
				return nil, err
			}
			n.WGIP = ip
		}
		if n.AddedAt.IsZero() {
			n.AddedAt = e.now()
		}
		e.Cluster.Upsert(n)
		if err := e.Cluster.Validate(); err != nil {
			_ = e.Cluster.Remove(name)
			return nil, err
		}
	} else if e.Cluster.Node(name).Role == cluster.RolePrimary {
		return nil, fmt.Errorf("%s is the writer", name)
	}
	n := e.Cluster.Node(name)

	fail := func(p Phase, err error) (*Report, error) {
		st.LastError = err.Error()
		st.UpdatedAt = e.now()
		_ = SaveState(e.Paths, st)
		if st.Done(PhaseUpstream) {
			hintCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			e.slotHint(hintCtx, *n)
			cancel()
		}
		return nil, fmt.Errorf("%s: %w (resume with: qabas replica add %s --resume)", p, err, name)
	}
	step := func(p Phase, fn func() error) error {
		recheckUpstream := p == PhaseUpstream && !st.Done(PhaseSeed)
		refreshStorage := p == PhaseStorage && opt.RefreshStorage
		if st.Done(p) && p != PhasePreflight && p != PhaseVerify && !recheckUpstream && !refreshStorage {
			e.logf("  %-10s done (resumed)", p)
			return nil
		}
		e.logf("  %-10s ...", p)
		if err := fn(); err != nil {
			return err
		}
		st.mark(p, e.now())
		return SaveState(e.Paths, st)
	}

	// PREFLIGHT is read-only on the replica; failures leave it untouched.
	var pf Preflight
	if err := step(PhasePreflight, func() error {
		var perr error
		pf, perr = e.preflight(ctx, name, opt)
		return perr
	}); err != nil {
		if st.TempWGKey {
			// Invited nodes stay registered (their WireGuard peer is the only way in).
			st.LastError, st.UpdatedAt = err.Error(), e.now()
			_ = SaveState(e.Paths, st)
			return nil, err
		}
		if isNew {
			_ = e.Cluster.Remove(name)
		}
		_ = RemoveState(e.Paths, name)
		return nil, err
	}
	if !st.Done(PhaseSeed) {
		st.SeedOwned = opt.Rebuild || !pf.PGVolume || st.SeedOwned
	}
	st.InviteExpires = nil
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		return nil, err
	}

	if opt.Bootstrap == nil {
		if !st.Done(PhaseBootstrap) {
			e.logf("  %-10s skipped (--no-bootstrap)", PhaseBootstrap)
		}
	} else if err := step(PhaseBootstrap, func() error { return e.Bootstrap(ctx, name, *opt.Bootstrap) }); err != nil {
		return fail(PhaseBootstrap, err)
	}

	if err := step(PhaseNetwork, func() error { return e.provisionNetwork(ctx, name, st, opt.Relay) }); err != nil {
		return fail(PhaseNetwork, err)
	}
	if err := step(PhaseUpstream, func() error { return e.prepareUpstream(ctx, *n, opt) }); err != nil {
		return fail(PhaseUpstream, err)
	}
	ro := StackOptions{Profile: nodestack.ProfileReplicaRO}
	if err := step(PhaseStack, func() error {
		if err := e.ConfigureNode(ctx, name, ro); err != nil {
			return err
		}
		_, err := e.Compose(ctx, name, ro, "pull --quiet")
		return err
	}); err != nil {
		return fail(PhaseStack, err)
	}
	if err := step(PhaseSeed, func() error { return e.Seed(ctx, name, st.SeedOwned) }); err != nil {
		return fail(PhaseSeed, err)
	}
	if err := step(PhaseStorage, func() error {
		e.logf("    minio      starting containers")
		startCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		if _, err := e.Compose(startCtx, name, ro, "up -d minio minio-init"); err != nil {
			cancel()
			return fmt.Errorf("start MinIO on %s: %w", name, err)
		}
		cancel()
		e.logf("    minio      waiting for health")
		if err := e.WaitHealthy(ctx, name, "qabasone-minio", 2*time.Minute); err != nil {
			return fmt.Errorf("wait for MinIO on %s: %w", name, err)
		}
		e.logf("    policy     applying read-only app policy on %s", name)
		policyCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := e.SetAppPolicy(policyCtx, name, true)
		cancel()
		if err != nil {
			return fmt.Errorf("set app policy on %s: %w", name, err)
		}
		up := e.Cluster.UpstreamOf(*n)
		if opt.RefreshStorage {
			e.logf("    replication removing previous source rule on %s", up.Name)
			unlinkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := e.UnlinkStorage(unlinkCtx, up.Name, name)
			cancel()
			if err != nil {
				return fmt.Errorf("remove existing MinIO replication link %s -> %s: %w", up.Name, name, err)
			}
		}
		e.logf("    replication linking storage %s -> %s", up.Name, name)
		linkCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = e.LinkStorage(linkCtx, up.Name, name, e.StoragePriority(name))
		cancel()
		return err
	}); err != nil {
		return fail(PhaseStorage, err)
	}
	if err := step(PhaseStart, func() error { return e.StartStack(ctx, name, ro) }); err != nil {
		return fail(PhaseStart, err)
	}
	var rep Report
	if err := step(PhaseVerify, func() error {
		rep = e.Verify(ctx, name)
		if !rep.OK {
			return fmt.Errorf("verification failed:\n%s", rep.Failures())
		}
		return nil
	}); err != nil {
		return fail(PhaseVerify, err)
	}
	if err := step(PhaseReady, func() error { return cluster.Save(e.Paths, e.Cluster) }); err != nil {
		return fail(PhaseReady, err)
	}
	return &rep, nil
}

func (e *Engine) provisionNetwork(ctx context.Context, name string, st *State, forceRelay bool) error {
	w, _ := e.writer()
	if w.WGEndpoint == "" {
		return fmt.Errorf("the writer %s has no WireGuard endpoint; set it with: qabas replica init --endpoint <public-host:port>", w.Name)
	}
	for _, nn := range []string{w.Name, name} {
		if e.Cluster.Node(nn).WGPublicKey == "" {
			if err := e.EnsureWGKey(ctx, nn); err != nil {
				return err
			}
		}
	}
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		return err
	}
	n := e.Cluster.Node(name)
	// Only a node SSH reaches directly can be switched to the relay from here; a node
	// reached over WireGuard already joined (directly or through its join script).
	canSwitch := n.SSHVia != cluster.SSHViaWG && !n.Relay
	if forceRelay && canSwitch {
		if err := e.ConfigureRelayClient(ctx, name); err != nil {
			return fmt.Errorf("TLS relay: %w", err)
		}
	} else if err := e.ApplyWG(ctx, name); err != nil {
		return err
	}
	for _, err := range e.SyncMesh(ctx, name) {
		e.logf("  warning: %v", err)
	}
	if err := e.WaitPeer(ctx, name, *w, peerTimeout); err != nil {
		if !canSwitch || forceRelay {
			return err
		}
		e.logf("  no WireGuard handshake between %s and %s (the network may block WireGuard); switching to the TLS relay", name, w.Name)
		if rerr := e.ConfigureRelayClient(ctx, name); rerr != nil {
			return fmt.Errorf("%v; the TLS relay fallback failed too: %w", err, rerr)
		}
		if err := e.WaitPeer(ctx, name, *w, peerTimeout); err != nil {
			return fmt.Errorf("%w (through the TLS relay: TCP %d on the writer must be reachable from %s)", err, e.Cluster.RelayPortOrDefault(), name)
		}
	}
	if st.TempWGKey {
		if err := e.RotateWGKey(ctx, name); err != nil {
			return fmt.Errorf("rotate the invite WireGuard key: %w", err)
		}
		st.TempWGKey = false
		if err := SaveState(e.Paths, st); err != nil {
			return err
		}
	}
	if n := e.Cluster.Node(name); n.Mode == cluster.ModePull && n.SSHVia != cluster.SSHViaWG {
		return e.SwitchSSHToWG(ctx, name)
	}
	return nil
}

// prepareUpstream enables replication on the writer (once), creates the replication
// roles, the node's slot on its upstream, and refreshes pg_hba everywhere.
func (e *Engine) prepareUpstream(ctx context.Context, n cluster.Node, opt AddOptions) error {
	if err := e.WriterGuard(ctx); err != nil {
		return err
	}
	if err := e.EnableWriter(ctx, opt.Confirm, opt.Lease); err != nil {
		return err
	}
	w, _ := e.writer()
	wx, err := e.Exec(ctx, w.Name)
	if err != nil {
		return err
	}
	repl, err := e.sharedSecret(cluster.SecretReplicationPassword)
	if err != nil {
		return err
	}
	bak, err := e.sharedSecret(cluster.SecretBackupPassword)
	if err != nil {
		return err
	}
	if err := pgctl.EnsureRoles(ctx, wx, repl, bak); err != nil {
		return err
	}
	up := e.Cluster.UpstreamOf(n)
	ux, err := e.Exec(ctx, up.Name)
	if err != nil {
		return err
	}
	if up.Name != w.Name {
		// Cascading: the slot lives on the upstream standby (PG16 supports slots there).
		if err := e.WaitPeer(ctx, n.Name, *up, 30*time.Second); err != nil {
			return err
		}
	}
	if err := e.EnsureSlotBound(ctx, up.Name, up.Name == w.Name, opt); err != nil {
		return err
	}
	if err := pgctl.EnsureSlot(ctx, ux, cluster.SlotName(n.Name)); err != nil {
		return err
	}
	for _, o := range sortedNodes(e.Cluster.Nodes) {
		if o.Name == n.Name {
			continue
		}
		if err := e.WriteHBA(ctx, o.Name); err != nil {
			if o.Name == up.Name {
				return err
			}
			e.logf("  warning: pg_hba on %s not refreshed: %v", o.Name, err)
		}
	}
	return nil
}

// WriterEnabled reports whether this node already runs the primary-replication overlay.
func (e *Engine) WriterEnabled() bool {
	for _, f := range nodestack.ReadDirectives(e.Paths.ComposeDir()).Files {
		if filepath.Base(f) == nodestack.OverlayPrimary {
			return true
		}
	}
	return false
}

// EnableWriter switches the local writer onto the primary-replication overlay. This
// restarts PostgreSQL once (listen on WireGuard, pg_hba, TLS, wal settings).
func (e *Engine) EnableWriter(ctx context.Context, confirm func(string) error, writeLease func(context.Context) error) error {
	w, err := e.writer()
	if err != nil {
		return err
	}
	opt := StackOptions{Profile: nodestack.ProfilePrimary, PITR: e.pitrEnabled()}
	if e.WriterEnabled() {
		return e.WriteHBA(ctx, w.Name)
	}
	if confirm != nil {
		if err := confirm(fmt.Sprintf("Enabling replication restarts PostgreSQL and the API on %s once (about 15-30 seconds). Continue?", w.Name)); err != nil {
			return err
		}
	}
	if err := e.ConfigureNode(ctx, w.Name, opt); err != nil {
		return err
	}
	if writeLease != nil {
		if err := writeLease(ctx); err != nil {
			return fmt.Errorf("writer lease: %w", err)
		}
	}
	if _, err := e.Compose(ctx, w.Name, opt, "up -d --remove-orphans"); err != nil {
		return err
	}
	return e.WaitHealthy(ctx, w.Name, pgctl.Container, 3*time.Minute)
}

func (e *Engine) pitrEnabled() bool {
	_, err := os.Stat(filepath.Join(e.Paths.ConfigDir, ".env.pitr"))
	return err == nil
}

// Seed (re)creates the node's PGDATA from its upstream with pg_basebackup. wipe allows
// clearing a data volume this provisioning owns (fresh or --rebuild).
func (e *Engine) Seed(ctx context.Context, name string, wipe bool) error {
	n := e.Cluster.Node(name)
	up := e.Cluster.UpstreamOf(*n)
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	repl, err := e.sharedSecret(cluster.SecretReplicationPassword)
	if err != nil {
		return err
	}
	if _, err := run(ctx, x, "docker rm -f "+pgctl.Container+" >/dev/null 2>&1 || true"); err != nil {
		return err
	}
	envFile := "/run/qabas/repl.env"
	if err := write(ctx, x, envFile, []byte("PGPASSWORD="+repl+"\n"), 0o600); err != nil {
		return err
	}
	defer func() { _, _ = x.Run(context.Background(), "rm -f "+envFile, nil) }()
	script := pgctl.BaseBackupScript(e.pgImage(), PGVolume, up.WGIP, cluster.SlotName(name), cluster.ApplicationName(name), envFile, wipe)
	if _, err := run(ctx, x, script); err != nil {
		return fmt.Errorf("pg_basebackup from %s: %w", up.Name, err)
	}
	return e.PinConninfo(ctx, name, up.WGIP, repl)
}

// PinConninfo writes the explicit upstream connection into a stopped standby's PGDATA.
func (e *Engine) PinConninfo(ctx context.Context, name, upstreamIP, password string) error {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	lines := pgctl.ConninfoLines(upstreamIP, cluster.SlotName(name), cluster.ApplicationName(name), password)
	res, err := x.Run(ctx, pgctl.ConninfoScript(e.pgImage(), PGVolume), strings.NewReader(lines))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &nodeexec.ScriptError{Node: name, ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	return nil
}

// StartStack brings the full role stack up and waits for the API.
func (e *Engine) StartStack(ctx context.Context, name string, opt StackOptions) error {
	if _, err := e.Compose(ctx, name, opt, "up -d --remove-orphans"); err != nil {
		return err
	}
	if err := e.WaitHealthy(ctx, name, pgctl.Container, 3*time.Minute); err != nil {
		return err
	}
	return e.WaitHealthy(ctx, name, "qabasone-api", 4*time.Minute)
}

// WaitHealthy polls a container's health status (or running state without healthcheck).
func (e *Engine) WaitHealthy(ctx context.Context, name, container string, timeout time.Duration) error {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	inspect := func(c string) string {
		return "docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' " + nodeexec.Quote(c) + " 2>/dev/null || echo missing"
	}
	last := ""
	logContainer := container
	for {
		if container == "qabasone-api" {
			containers, err := apiContainerNames(ctx, x, true)
			if err != nil {
				return fmt.Errorf("list API containers on %s: %w", name, err)
			}
			if len(containers) == 0 {
				last = "missing"
			} else {
				statuses := make([]string, 0, len(containers))
				allHealthy := true
				for _, c := range containers {
					out, _ := run(ctx, x, inspect(c))
					status := strings.TrimSpace(out)
					statuses = append(statuses, c+"="+status)
					if status != "healthy" && status != "running" {
						allHealthy = false
						logContainer = c
					}
				}
				if allHealthy {
					return nil
				}
				last = strings.Join(statuses, ", ")
			}
		} else {
			out, _ := run(ctx, x, inspect(container))
			last = strings.TrimSpace(out)
			if last == "healthy" || last == "running" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s on %s is %q after %s (see: docker logs %s)", container, name, last, timeout, logContainer)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

func apiContainerNames(ctx context.Context, x nodeexec.Executor, includeStopped bool) ([]string, error) {
	command := "docker ps"
	if includeStopped {
		command += " -a"
	}
	out, err := run(ctx, x, command+" --format '{{.Names}}'")
	if err != nil {
		return nil, err
	}
	var containers []string
	for _, name := range strings.Fields(out) {
		if name == "qabasone-api" {
			containers = append(containers, name)
			continue
		}
		suffix, ok := strings.CutPrefix(name, "qabasone-api-")
		if ok && suffix != "" && strings.Trim(suffix, "0123456789") == "" {
			containers = append(containers, name)
		}
	}
	sort.Strings(containers)
	return containers, nil
}
