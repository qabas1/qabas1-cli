package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	installer "github.com/qabas1/qabasone-cli/install"
	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/dr"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/guide"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/qabas1/qabasone-cli/internal/replica"
	"github.com/qabas1/qabasone-cli/internal/sshx"
	"github.com/spf13/cobra"
)

func newReplicaCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replica",
		Short: "Provision and operate read-only replicas (other sites / DR)",
		Long: `Read-only replicas are physical PostgreSQL hot standbys with one-way MinIO
replication, reached over a WireGuard mesh. They serve the full UI in read-only mode
(login, browse, reports, print) and can later be promoted with ` + "`qabas dr`" + `.

Run replica commands on the writer. Replicas never run migrations or background
writers; schema and data arrive through WAL.`,
		Example: `  # One-time: register this machine as the writer
  sudo qabas replica init --name hq --endpoint hq.example.com:51820

  # Add a promotable DR replica on a bare Ubuntu/Debian host (root SSH key)
  sudo qabas replica add dr1 --host 203.0.113.10 --identity /root/.ssh/dr1_root --site branch --promotable

  # Replica behind NAT with no inbound access: one-time join script
  sudo qabas replica invite branch2 --site jeddah --promotable --wait

  # Resume after a failure (network, disk, ...)
  sudo qabas replica add dr1 --resume

  sudo qabas replica status
  sudo qabas replica verify dr1
  sudo qabas replica sync --all --restart`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.PersistentFlags().String("identity", "", "SSH private key for first contact with a new replica (the cluster key is used afterwards; default: ssh-agent, ~/.ssh/id_ed25519, $"+EnvSSHIdentity+")")
	cmd.AddCommand(
		newReplicaInitCmd(f),
		newReplicaAddCmd(f),
		newReplicaInviteCmd(f),
		newReplicaListCmd(f),
		newReplicaStatusCmd(f),
		newReplicaVerifyCmd(f),
		newReplicaSyncCmd(f),
		newReplicaRemoveCmd(f),
	)
	return cmd
}

func identityFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("identity")
	return v
}

func newReplicaInitCmd(f *flags) *cobra.Command {
	var name, site, endpoint, clusterName, probe string
	var wgPort int
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Register this machine as the writer in a new cluster registry",
		Example: `  sudo qabas replica init --name hq --endpoint hq.example.com:51820
  sudo qabas replica init --name hq --site riyadh --endpoint 198.51.100.7:51820 --public-probe https://1.1.1.1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				c, err := initRegistry(e, registryInit{
					name: name, site: site, endpoint: endpoint, clusterName: clusterName, probe: probe, wgPort: wgPort,
				})
				if err != nil || e.flags.DryRun {
					return err
				}
				w := c.Primary()
				if w.WGEndpoint == "" {
					e.Out.Warnf("writer %s has no WireGuard endpoint yet; add one with --endpoint before `qabas replica add`\n", w.Name)
				}
				return e.Out.Result(fmt.Sprintf("cluster %q: writer %s (%s), registry %s\n", c.Name, w.Name, w.WGIP, cluster.File(e.Paths)), c)
			})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "node name for this machine (e.g. hq)")
	cmd.Flags().StringVar(&site, "site", "", "site/location name (default: node name)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "public WireGuard endpoint of this machine (host:port)")
	cmd.Flags().StringVar(&clusterName, "cluster-name", "", "cluster name (default: qabasone)")
	cmd.Flags().StringVar(&probe, "public-probe", "", "URL used to test internet reachability from each node")
	cmd.Flags().IntVar(&wgPort, "wg-port", 0, "WireGuard UDP port (default 51820)")
	return cmd
}

type registryInit struct {
	name, site, endpoint, clusterName, probe string
	wgPort                                   int
}

// initRegistry creates the cluster registry with this machine as writer, or updates
// the writer's endpoint/site/probe in an existing one. Dry-run only reports.
func initRegistry(e *env, o registryInit) (*cluster.Cluster, error) {
	c, err := cluster.Load(e.Paths)
	if errors.Is(err, cluster.ErrNotFound) {
		if o.name == "" {
			return nil, exitcode.New(exitcode.InvalidInput, "--name is required for a new registry")
		}
		if err := cluster.ValidateNodeName(o.name); err != nil {
			return nil, exitcode.Wrap(exitcode.InvalidInput, err)
		}
		c = &cluster.Cluster{Name: o.clusterName, Writer: o.name, Epoch: 1}
		if c.Name == "" {
			c.Name = "qabasone"
		}
		if o.wgPort > 0 {
			c.WGPort = o.wgPort
		}
		c.Nodes = []cluster.Node{{
			Name: o.name, Site: o.site, Role: cluster.RolePrimary, Host: "localhost",
			WGIP: c.HubIP(), WGEndpoint: o.endpoint, Promotable: true, Priority: 100,
			AddedAt: time.Now().UTC(),
		}}
	} else if err != nil {
		return nil, err
	} else {
		w := c.Primary()
		if o.endpoint != "" {
			w.WGEndpoint = o.endpoint
		}
		if o.site != "" {
			w.Site = o.site
		}
	}
	if c.Node(c.Writer) == nil {
		c.Writer = c.Primary().Name
	}
	if o.probe != "" {
		c.PublicProbe = o.probe
	}
	if rel, _, err := e.installedRelease(); err == nil {
		c.Release = rel.Release.Version
	}
	if e.flags.DryRun {
		e.Out.Printf("would write %s (writer %s)\n", cluster.File(e.Paths), c.Writer)
		return c, nil
	}
	if err := cluster.Save(e.Paths, c); err != nil {
		return nil, err
	}
	for _, s := range []string{cluster.SecretReplicationPassword, cluster.SecretBackupPassword} {
		if _, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, s); err != nil {
			return nil, err
		}
	}
	audit(e, "replica-init", "", c.Primary().Name, nil, "registry created/updated")
	return c, nil
}

// defaultWriterName derives a registry node name from the hostname.
func defaultWriterName() string {
	h, _ := os.Hostname()
	if n := guide.NodeNameFromHostname(h); n != "" {
		return n
	}
	return "writer"
}

// loadOrInitCluster returns the registry; with --writer-endpoint a missing registry is
// created (this machine as writer) and a writer without an endpoint gets one.
func loadOrInitCluster(e *env, writerName, writerEndpoint string) (*cluster.Cluster, error) {
	c, err := cluster.Load(e.Paths)
	switch {
	case errors.Is(err, cluster.ErrNotFound) && writerEndpoint != "":
		if writerName == "" {
			writerName = defaultWriterName()
		}
		e.Out.Printf("No cluster registry yet: registering this machine as writer %s (%s)\n", writerName, writerEndpoint)
		return initRegistry(e, registryInit{name: writerName, endpoint: writerEndpoint})
	case err != nil:
		return e.cluster()
	case writerEndpoint != "" && c.Primary().WGEndpoint != writerEndpoint:
		if c.Primary().WGEndpoint != "" {
			return nil, exitcode.New(exitcode.InvalidInput, "the writer already has WireGuard endpoint %s; change it with `qabas replica init --endpoint`", c.Primary().WGEndpoint)
		}
		return initRegistry(e, registryInit{endpoint: writerEndpoint})
	}
	return c, nil
}

type bootstrapFlags struct {
	skip, noOSUpgrade, noReboot bool
}

func (b *bootstrapFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&b.skip, "no-bootstrap", false, "do not prepare the host (Docker, compose v2 and qabas must already be installed)")
	cmd.Flags().BoolVar(&b.noOSUpgrade, "no-os-upgrade", false, "skip `apt-get upgrade` on the new host")
	cmd.Flags().BoolVar(&b.noReboot, "no-reboot", false, "never reboot the new host, even when its update requires it")
}

func (b bootstrapFlags) options() *replica.BootstrapOptions {
	if b.skip {
		return nil
	}
	return &replica.BootstrapOptions{SkipOSUpgrade: b.noOSUpgrade, SkipReboot: b.noReboot, InstallScript: installer.Script}
}

func printPlan(e *env, n cluster.Node, boot *replica.BootstrapOptions) {
	host, port := n.SSHEndpoint()
	e.Out.Printf("would provision %s (ssh %s) phases: %v\n", n.Name, sshx.Addr(host, port), replica.Phases)
	if boot == nil {
		e.Out.Printf("  BOOTSTRAP skipped (--no-bootstrap)\n")
		return
	}
	for _, s := range replica.BootstrapSteps {
		e.Out.Printf("  BOOTSTRAP: %s\n", s)
	}
}

// runReplicaAdd provisions (or resumes) a replica with an existing engine.
func runReplicaAdd(cmd *cobra.Command, e *env, de *dr.Engine, n cluster.Node, resume, rebuild, relay, refreshStorage bool, boot *replica.BootstrapOptions) error {
	for _, err := range de.PruneInvites(cmd.Context()) {
		e.Out.Warnf("%v\n", err)
	}
	host, port := n.SSHEndpoint()
	e.Out.Printf("Provisioning replica %s (%s)\n", n.Name, sshx.Addr(host, port))
	writer := de.Cluster.Writer
	rep, err := de.Add(cmd.Context(), replica.AddOptions{
		Node: n, Resume: resume, Rebuild: rebuild, RefreshStorage: refreshStorage,
		Confirm:   e.confirm,
		Lease:     de.InitWriterLease(qabasCmd),
		Bootstrap: boot,
		Audit:     func(action, detail string) { audit(e, action, "", writer, nil, detail) },
		Relay:     relay,
	})
	audit(e, "replica-add", writer, n.Name, err, "")
	if err != nil {
		return drErr(err)
	}
	printReport(cmd, e, *rep)
	e.Out.Printf("\nReplica %s is READY (read-only). Promote later only with: qabas dr\n", n.Name)
	e.Out.Printf("qabas now reaches %s with the cluster SSH key (%s); a setup key you passed with --identity can be removed from the replica.\n",
		n.Name, cluster.SSHKeyPath(e.Paths))
	return nil
}

func newReplicaAddCmd(f *flags) *cobra.Command {
	var n cluster.Node
	var hostKey, wgEndpoint, writerName, writerEndpoint string
	var behindNAT, resume, rebuild, acceptHostKey, useRelay, refreshStorage bool
	var bf bootstrapFlags
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Provision a read-only replica on a bare Ubuntu/Debian host (resumable)",
		Long: `Provision a read-only replica end to end. Pass the host and an SSH key with root
(or passwordless sudo) access; everything else is automatic:

  PREFLIGHT  read-only checks on the new host (nothing changes if it fails)
  BOOTSTRAP  apt update + upgrade, base tools, Docker CE (signed repo), reboot if the
             kernel requires it, GitHub/GHCR credentials, qabas CLI at the writer's
             version, cluster SSH key (only the new host changes)
  NETWORK    WireGuard mesh (keys are generated on each node and never leave it)
  UPSTREAM   replication roles, slot, pg_hba (first run restarts the writer's PostgreSQL once)
  STACK      compose bundle + overlays, env files, image pins, registry
  SEED       pg_basebackup over WireGuard
  STORAGE    MinIO versioning + one-way bucket replication (app key read-only)
  START      replica-ro stack (no migrate, no workers)
  VERIFY     write rejected, streaming, caught up, schema, objects, closed ports, API
             mode, container health on the replica, writer API still healthy

Production safety: the writer is checked healthy before it is touched, its PostgreSQL
restarts once (with confirmation) only when replication is first enabled, and an
unlimited max_slot_wal_keep_size is bounded (reload, no restart) so a stalled replica
cannot fill the writer's disk.

The host key is pinned on first contact: pass --host-key SHA256:..., confirm it, or
pass --accept-host-key to trust it on first use (audited). Replicas with no inbound
access at all join with ` + "`qabas replica invite`" + `.`,
		Example: `  # Bare host, root key (writer registry created on the fly)
  sudo qabas replica add dr1 --host 203.0.113.10 --identity /root/.ssh/dr1_root --promotable \
      --writer-endpoint hq.example.com:51820

  sudo qabas replica add jeddah --host 10.20.0.5 --ssh-user ops --priority 50 --promotable
  sudo qabas replica add kiosk --host 192.0.2.44 --ssh-port 2222 --behind-nat  # SSH port-forward, no inbound UDP
  sudo qabas replica add dr1 --resume
  sudo qabas replica add dr1 --resume --refresh-storage     # recreate a stale MinIO link
  sudo qabas replica add dr1 --rebuild -y                              # re-seed from scratch`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				c, err := loadOrInitCluster(e, writerName, writerEndpoint)
				if err != nil {
					return err
				}
				n.Name = args[0]
				if existing := c.Node(n.Name); existing != nil {
					n = *existing
					if n.SSHHostKey == "" && n.Role == cluster.RoleStandby {
						host, port := n.SSHEndpoint()
						fp, err := pinHostKey(cmd.Context(), e, sshx.Addr(host, port), n.Name, hostKey, acceptHostKey)
						if err != nil {
							return err
						}
						n.SSHHostKey = fp
						c.Node(n.Name).SSHHostKey = fp
						if !e.flags.DryRun {
							if err := cluster.Save(e.Paths, c); err != nil {
								return err
							}
						}
					}
				} else {
					if n.Host == "" {
						return exitcode.New(exitcode.InvalidInput, "--host is required for a new replica")
					}
					if n.Site == "" {
						n.Site = n.Name
					}
					if n.SSHPort == 0 {
						n.SSHPort = 22
					}
					switch {
					case behindNAT:
						n.Mode = cluster.ModePull
					case wgEndpoint != "":
						n.WGEndpoint = wgEndpoint
					default:
						n.WGEndpoint = fmt.Sprintf("%s:%d", n.Host, c.WGPort)
					}
					fp, err := pinHostKey(cmd.Context(), e, sshx.Addr(n.Host, n.SSHPort), n.Name, hostKey, acceptHostKey)
					if err != nil {
						return err
					}
					n.SSHHostKey = fp
				}
				if rebuild {
					if err := e.confirm(fmt.Sprintf("--rebuild discards the QabasOne data on %s and re-seeds it from the writer. Continue?", n.Name)); err != nil {
						return err
					}
				}
				boot := bf.options()
				if e.flags.DryRun {
					printPlan(e, n, boot)
					return nil
				}
				de, err := e.newDREngine(c, engineOpts{identity: identityFlag(cmd)})
				if err != nil {
					return err
				}
				defer de.Close()
				return runReplicaAdd(cmd, e, de, n, resume, rebuild, useRelay, refreshStorage, boot)
			})
		},
	}
	cmd.Flags().StringVar(&n.Host, "host", "", "SSH host/IP of the new replica")
	cmd.Flags().StringVar(&n.SSHUser, "ssh-user", "", "SSH user (root, or a user with passwordless sudo)")
	cmd.Flags().IntVar(&n.SSHPort, "ssh-port", 22, "SSH port")
	cmd.Flags().StringVar(&hostKey, "host-key", "", "expected SSH host key fingerprint (SHA256:...)")
	cmd.Flags().StringVar(&n.Site, "site", "", "site/location (default: node name)")
	cmd.Flags().BoolVar(&n.Promotable, "promotable", false, "allow `qabas dr` to promote this replica")
	cmd.Flags().IntVar(&n.Priority, "priority", 0, "promotion preference among equally caught-up candidates (higher wins)")
	cmd.Flags().StringVar(&n.Upstream, "upstream", "", "stream from this standby instead of the writer (cascading, saves WAN)")
	cmd.Flags().StringVar(&wgEndpoint, "wg-endpoint", "", "public WireGuard endpoint of the replica (default <host>:<wg-port>)")
	cmd.Flags().BoolVar(&behindNAT, "behind-nat", false, "replica accepts no inbound UDP (it dials out to peers)")
	cmd.Flags().StringVar(&n.PublicURL, "public-url", "", "public URL served by this node after promotion (routing notes)")
	cmd.Flags().BoolVar(&resume, "resume", false, "continue a stopped provisioning")
	cmd.Flags().BoolVar(&refreshStorage, "refresh-storage", false, "recreate the MinIO replication link (requires --resume)")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "discard replica data and provision from scratch")
	cmd.Flags().BoolVar(&acceptHostKey, "accept-host-key", false, "trust the host key presented on first contact (audited; prefer --host-key)")
	cmd.Flags().StringVar(&writerEndpoint, "writer-endpoint", "", "create the cluster registry if missing, with this public WireGuard endpoint of the writer (host:port)")
	cmd.Flags().StringVar(&writerName, "writer-name", "", "node name for this machine when the registry is created (default: hostname)")
	cmd.Flags().BoolVar(&useRelay, "relay", false, "carry WireGuard over the writer's TLS relay from the start (automatic when WireGuard gets no handshake; relay nodes are not promotable)")
	bf.register(cmd)
	return cmd
}

// pinHostKey returns the fingerprint to pin: --host-key must match what the host
// presents; otherwise the operator confirms the scanned key, or --accept-host-key
// trusts it on first use (audited). Never blind trust.
func pinHostKey(ctx context.Context, e *env, addr, node, expected string, accept bool) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fp, kind, err := sshx.ScanHostKey(sctx, addr)
	if err != nil {
		return "", exitcode.Wrap(exitcode.Network, fmt.Errorf("ssh %s: %w", addr, err))
	}
	if expected != "" {
		if expected != fp {
			return "", exitcode.NewHint(exitcode.InvalidInput, "list the host's keys on "+node+" with: "+sshx.HostKeyListHint,
				"host key mismatch for %s: expected %s, got %s key %s", addr, expected, kind, fp)
		}
		return fp, nil
	}
	e.Out.Printf("%s presents %s host key %s\n", addr, kind, fp)
	if accept {
		e.Out.Warnf("trusting %s's host key %s on first use (--accept-host-key)\n", node, fp)
		audit(e, "replica-hostkey-tofu", "", node, nil, fp+" via "+addr)
		return fp, nil
	}
	if e.flags.NonInteractive || e.flags.Yes {
		return "", exitcode.NewHint(exitcode.InvalidInput, "verify the fingerprint out of band and pass --host-key "+fp+" (or --accept-host-key)",
			"refusing to trust an unverified host key non-interactively")
	}
	if err := e.confirm("Is this the correct fingerprint (check on the host with `" + sshx.HostKeyListHint + "`)?"); err != nil {
		return "", err
	}
	return fp, nil
}

func newReplicaInviteCmd(f *flags) *cobra.Command {
	var n cluster.Node
	var ttl time.Duration
	var out, hostKey string
	var printScript, wait, revoke, acceptHostKey, useRelay bool
	var bf bootstrapFlags
	cmd := &cobra.Command{
		Use:   "invite <name>",
		Short: "Add a replica that accepts no inbound connections (one-time join script)",
		Long: `For a replica behind NAT with no inbound TCP or UDP at all. The writer registers the
node, adds a WireGuard peer for it and writes a one-time join script (0600) holding a
temporary WireGuard key. Run the script as root on the new machine: it installs
wireguard-tools, dials out to the writer and authorizes the cluster SSH key for the
WireGuard network only. From then on the writer reaches the node over WireGuard and
provisions it exactly like ` + "`qabas replica add`" + ` (BOOTSTRAP ... VERIFY); NETWORK
replaces the temporary key with one generated on the node.

Networks that drop WireGuard: the script waits 20s for a handshake and then carries
WireGuard over the writer's TLS relay (qabas-relay.service, TCP 8443 by default, pinned
certificate, per-node token). The relay needs the qabas CLI on the new machine; relay
nodes peer with the writer only and are not promotable. --relay skips the direct try.

The script expires (--ttl). Expired, unused invites are revoked automatically.`,
		Example: `  sudo qabas replica invite branch2 --site jeddah --promotable --wait
  #   copy /etc/qabasone/invites/branch2.sh to the new machine, then there:
  #   sudo sh branch2.sh

  sudo qabas replica invite kiosk --print > kiosk.sh       # script on stdout
  sudo qabas replica add kiosk --resume                     # continue later, after the join
  sudo qabas replica invite kiosk --revoke`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				c, err := e.cluster()
				if err != nil {
					return err
				}
				name := args[0]
				if e.flags.DryRun {
					if revoke {
						e.Out.Printf("would revoke the invite for %s\n", name)
					} else {
						e.Out.Printf("would register %s (WireGuard only) and write %s\n", name, replica.InvitePath(e.Paths, name))
					}
					return nil
				}
				de, err := e.newDREngine(c, engineOpts{identity: identityFlag(cmd)})
				if err != nil {
					return err
				}
				defer de.Close()
				if revoke {
					err := de.RevokeInvite(cmd.Context(), name)
					audit(e, "replica-invite-revoke", "", name, err, "")
					if err != nil {
						return exitcode.Wrap(exitcode.InvalidInput, err)
					}
					e.Out.Printf("invite for %s revoked (its WireGuard key no longer works)\n", name)
					return nil
				}
				n.Name = name
				inv, err := de.Invite(cmd.Context(), replica.InviteOptions{Node: n, TTL: ttl, Relay: useRelay})
				audit(e, "replica-invite", c.Writer, name, err, "")
				if err != nil {
					return exitcode.Wrap(exitcode.InvalidInput, err)
				}
				if de.Cluster.RelayCertSHA256 != "" {
					e.Out.Warnf("TLS relay fallback: TCP %d on this machine must be reachable from the new replica (firewall / provider panel)\n", de.Cluster.RelayPortOrDefault())
				}
				if out != "" {
					if err := os.WriteFile(out, []byte(inv.Script), 0o600); err != nil {
						return err
					}
				}
				if printScript {
					fmt.Fprint(cmd.OutOrStdout(), inv.Script)
					if !wait {
						return nil
					}
				} else {
					where := inv.Path
					if out != "" {
						where = out
					}
					e.Out.Printf("Invited %s (WireGuard %s). Join script: %s (expires %s)\n", name, inv.WGIP, where, inv.ExpiresAt.Local().Format(time.RFC1123))
					e.Out.Printf("Copy it to the new machine (it holds a secret key; delete it after use) and run there:\n  sudo sh %s.sh\n", name)
				}
				if !wait {
					e.Out.Printf("After it joined, continue here with: sudo qabas replica add %s --resume\n", name)
					return nil
				}
				e.Out.Warnf("waiting for %s to join (until %s) ...\n", name, inv.ExpiresAt.Local().Format(time.Kitchen))
				if err := de.WaitInvite(cmd.Context(), name, time.Until(inv.ExpiresAt)); err != nil {
					return exitcode.Wrap(exitcode.Network, err)
				}
				node := c.Node(name)
				host, port := node.SSHEndpoint()
				fp, err := pinHostKey(cmd.Context(), e, sshx.Addr(host, port), name, hostKey, acceptHostKey)
				if err != nil {
					return err
				}
				node.SSHHostKey = fp
				if err := cluster.Save(e.Paths, c); err != nil {
					return err
				}
				return runReplicaAdd(cmd, e, de, *node, true, false, false, false, bf.options())
			})
		},
	}
	cmd.Flags().StringVar(&n.Site, "site", "", "site/location (default: node name)")
	cmd.Flags().BoolVar(&n.Promotable, "promotable", false, "allow `qabas dr` to promote this replica")
	cmd.Flags().IntVar(&n.Priority, "priority", 0, "promotion preference among equally caught-up candidates (higher wins)")
	cmd.Flags().StringVar(&n.Upstream, "upstream", "", "stream from this standby instead of the writer (cascading)")
	cmd.Flags().StringVar(&n.PublicURL, "public-url", "", "public URL served by this node after promotion (routing notes)")
	cmd.Flags().DurationVar(&ttl, "ttl", replica.DefaultInviteTTL, "how long the join script stays valid")
	cmd.Flags().StringVar(&out, "out", "", "also write the join script to this path (0600)")
	cmd.Flags().BoolVar(&printScript, "print", false, "print the join script on stdout")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the node to join, then provision it")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "revoke a pending invite")
	cmd.Flags().StringVar(&hostKey, "host-key", "", "expected SSH host key fingerprint (printed by the join script)")
	cmd.Flags().BoolVar(&acceptHostKey, "accept-host-key", false, "trust the host key seen over WireGuard on first use (audited)")
	cmd.Flags().BoolVar(&useRelay, "relay", false, "join over the writer's TLS relay without trying direct WireGuard (automatic after 20s without a handshake; relay nodes are not promotable)")
	bf.register(cmd)
	return cmd
}

func newReplicaListCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List registered nodes (no network access)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			c, err := e.cluster()
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(c)
			}
			rows := [][]string{}
			nodes := append([]cluster.Node(nil), c.Nodes...)
			sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
			for _, n := range nodes {
				phase := ""
				if st, _ := replica.LoadState(e.Paths, n.Name); st != nil {
					phase = string(st.Phase)
					if st.LastError != "" {
						phase += " (failed)"
					}
				}
				role := n.Role
				if n.Name == c.Writer {
					role += "*"
				}
				rows = append(rows, []string{n.Name, role, n.Site, n.Host, n.WGIP, strconv.FormatBool(n.Promotable), strconv.Itoa(n.Priority), n.Upstream, phase})
			}
			e.Out.Printf("cluster %s, writer %s, epoch %d\n", c.Name, c.Writer, c.Epoch)
			return output.Table(cmd.OutOrStdout(), []string{"NAME", "ROLE", "SITE", "HOST", "WG IP", "PROMOTABLE", "PRIORITY", "UPSTREAM", "PROVISION"}, rows)
		},
	}
}

func newReplicaStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:               "status [name...]",
		Short:             "Live replication status of nodes",
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			c, err := e.cluster()
			if err != nil {
				return err
			}
			re, err := e.newReplicaEngine(c, engineOpts{identity: identityFlag(cmd), offline: true})
			if err != nil {
				return err
			}
			defer re.Close()
			sts := re.Status(cmd.Context(), args...)
			if f.JSON {
				return e.Out.Encode(sts)
			}
			rows := [][]string{}
			for _, s := range sts {
				lag, state := "-", "-"
				if s.Replication != nil {
					state = s.Replication.State
					lag = fmt.Sprintf("%.1fs", s.Replication.ReplayLag)
				} else if s.Postgres != nil && s.Postgres.InRecovery && s.Postgres.WalReceiver == "streaming" {
					lag = fmt.Sprintf("%.1fs", s.Postgres.LagSeconds)
				}
				slot := "-"
				if s.Slot != nil {
					slot = s.Slot.WALStatus
					if s.Slot.RetainedBytes > 0 {
						slot += fmt.Sprintf(" %dMiB", s.Slot.RetainedBytes>>20)
					}
				}
				transport := s.Transport
				if transport == "" {
					transport = "-"
				}
				rows = append(rows, []string{s.Name, s.Role, transport, strconv.FormatBool(s.Reachable), state, lag, slot, s.Describe()})
			}
			return output.Table(cmd.OutOrStdout(), []string{"NAME", "ROLE", "LINK", "REACHABLE", "STREAM", "LAG", "SLOT", "DETAIL"}, rows)
		},
	}
}

func printReport(cmd *cobra.Command, e *env, rep replica.Report) {
	if e.flags.JSON {
		_ = e.Out.Encode(rep)
		return
	}
	rows := [][]string{}
	for _, c := range rep.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
		}
		rows = append(rows, []string{mark, c.Name, c.Detail})
	}
	_ = output.Table(cmd.OutOrStdout(), []string{"STATUS", "CHECK", "DETAIL"}, rows)
}

func newReplicaVerifyCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:               "verify <name>",
		Short:             "Prove a replica is read-only, streaming, caught up and complete",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			c, err := e.cluster()
			if err != nil {
				return err
			}
			re, err := e.newReplicaEngine(c, engineOpts{identity: identityFlag(cmd), offline: true})
			if err != nil {
				return err
			}
			defer re.Close()
			rep := re.Verify(cmd.Context(), args[0])
			printReport(cmd, e, rep)
			if !rep.OK {
				return exitcode.New(exitcode.Health, "replica %s failed verification", args[0])
			}
			return nil
		},
	}
}

func newReplicaSyncCmd(f *flags) *cobra.Command {
	var all, restart bool
	cmd := &cobra.Command{
		Use:   "sync [name]",
		Short: "Push the writer's release, config and registry to replicas",
		Long: `Re-materialize replica stacks from the writer: compose bundle and overlays,
env files, image pins, registry, pg_hba and WireGuard. With --restart the images are
pulled and the stack recreated (release propagation after ` + "`qabas deploy`" + `).
Replicas never run migrations; schema changes arrive through WAL.`,
		Example: `  sudo qabas replica sync dr1
  sudo qabas replica sync --all --restart`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if (len(args) == 0) == !all {
				return exitcode.New(exitcode.InvalidInput, "pass a replica name or --all")
			}
			return e.withLock(func() error {
				c, err := e.cluster()
				if err != nil {
					return err
				}
				re, err := e.newReplicaEngine(c, engineOpts{identity: identityFlag(cmd)})
				if err != nil {
					return err
				}
				defer re.Close()
				if e.flags.DryRun {
					e.Out.Printf("would sync %v (restart=%v)\n", args, restart)
					return nil
				}
				for _, err := range re.PruneInvites(cmd.Context()) {
					e.Out.Warnf("%v\n", err)
				}
				return syncReplicas(cmd.Context(), e, re, args, restart)
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "sync every replica")
	cmd.Flags().BoolVar(&restart, "restart", false, "pull images and recreate the replica stack")
	return cmd
}

func syncReplicas(ctx context.Context, e *env, re *replica.Engine, names []string, restart bool) error {
	opt := replica.SyncOptions{Restart: restart}
	results := map[string]error{}
	if len(names) == 0 {
		results = re.SyncAll(ctx, opt)
	} else {
		results[names[0]] = re.Sync(ctx, names[0], opt)
	}
	failed := 0
	keys := make([]string, 0, len(results))
	for k := range results {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := results[k]; err != nil {
			failed++
			e.Out.Printf("  %s: FAILED: %v\n", k, err)
		} else {
			e.Out.Printf("  %s: ok\n", k)
		}
		audit(e, "replica-sync", re.Self, k, results[k], "")
	}
	if failed > 0 {
		return exitcode.New(exitcode.Health, "%d replica(s) failed to sync", failed)
	}
	return nil
}

func newReplicaRemoveCmd(f *flags) *cobra.Command {
	var purge, force bool
	cmd := &cobra.Command{
		Use:               "rm <name>",
		Aliases:           []string{"remove"},
		Short:             "Detach and deprovision a replica",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		Example: `  sudo qabas replica rm dr1
  sudo qabas replica rm dr1 --purge-data -y
  sudo qabas replica rm dead-node --force -y        # node unreachable`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				c, err := e.cluster()
				if err != nil {
					return err
				}
				prompt := fmt.Sprintf("Detach replica %s (its stack stops; shared secrets are wiped there)?", args[0])
				if purge {
					prompt = fmt.Sprintf("Detach %s and DELETE its local data volumes?", args[0])
				}
				if err := e.confirm(prompt); err != nil {
					return err
				}
				if e.flags.DryRun {
					return nil
				}
				re, err := e.newReplicaEngine(c, engineOpts{identity: identityFlag(cmd)})
				if err != nil {
					return err
				}
				defer re.Close()
				warns := re.Remove(cmd.Context(), args[0], replica.RemoveOptions{PurgeData: purge, Force: force})
				var msgs []string
				for _, w := range warns {
					msgs = append(msgs, w.Error())
					e.Out.Warnf("%v\n", w)
				}
				if c.Node(args[0]) != nil {
					err := exitcode.New(exitcode.Generic, "replica %s was not removed: %s", args[0], strings.Join(msgs, "; "))
					audit(e, "replica-remove", "", args[0], err, "")
					return err
				}
				audit(e, "replica-remove", "", args[0], nil, strings.Join(msgs, "; "))
				e.Out.Printf("replica %s removed\n", args[0])
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&purge, "purge-data", false, "also delete the replica's docker volumes")
	cmd.Flags().BoolVar(&force, "force", false, "remove from the registry even if the node is unreachable")
	return cmd
}

func completeReplicaNodes(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	c, err := cluster.Load(load(&flags{}).Paths)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, n := range c.Nodes {
		if strings.HasPrefix(n.Name, toComplete) {
			out = append(out, n.Name)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
