package replica

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/wg"
)

// NodeStatus is the live view of one node.
type NodeStatus struct {
	Name        string                `json:"name"`
	Site        string                `json:"site"`
	Role        string                `json:"role"`
	WGIP        string                `json:"wgIP"`
	Upstream    string                `json:"upstream,omitempty"`
	Promotable  bool                  `json:"promotable"`
	Priority    int                   `json:"priority"`
	Transport   string                `json:"transport,omitempty"`
	Provision   Phase                 `json:"provisionPhase,omitempty"`
	ProvisionEr string                `json:"provisionError,omitempty"`
	Reachable   bool                  `json:"reachable"`
	Error       string                `json:"error,omitempty"`
	Postgres    *pgctl.Status         `json:"postgres,omitempty"`
	Replication *pgctl.ReplicationRow `json:"replication,omitempty"`
	Slot        *pgctl.SlotRow        `json:"slot,omitempty"`
	API         *DRHealth             `json:"api,omitempty"`
}

// Status probes every node (or only the named ones). Unreachable nodes are reported,
// never fatal.
func (e *Engine) Status(ctx context.Context, only ...string) []NodeStatus {
	want := map[string]bool{}
	for _, o := range only {
		want[o] = true
	}
	var repl map[string]pgctl.ReplicationRow
	var slots map[string]pgctl.SlotRow
	if w, err := e.writer(); err == nil {
		if wx, err := e.Exec(ctx, w.Name); err == nil {
			repl, slots = map[string]pgctl.ReplicationRow{}, map[string]pgctl.SlotRow{}
			if rows, err := pgctl.Replication(ctx, wx); err == nil {
				for _, r := range rows {
					repl[r.Application] = r
				}
			}
			if rows, err := pgctl.Slots(ctx, wx); err == nil {
				for _, r := range rows {
					slots[r.Name] = r
				}
			}
		}
	}
	var out []NodeStatus
	for _, n := range sortedNodes(e.Cluster.Nodes) {
		if len(want) > 0 && !want[n.Name] {
			continue
		}
		s := NodeStatus{Name: n.Name, Site: n.Site, Role: n.Role, WGIP: n.WGIP, Upstream: n.Upstream, Promotable: n.Promotable, Priority: n.Priority}
		if n.Role == cluster.RoleStandby {
			s.Transport = "direct"
			if n.Relay {
				s.Transport = "relay"
			}
		}
		if st, _ := LoadState(e.Paths, n.Name); st != nil {
			s.Provision, s.ProvisionEr = st.Phase, st.LastError
		}
		if r, ok := repl[cluster.ApplicationName(n.Name)]; ok {
			s.Replication = &r
		}
		if r, ok := slots[cluster.SlotName(n.Name)]; ok {
			s.Slot = &r
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		x, err := e.Exec(pctx, n.Name)
		if err != nil {
			s.Error = err.Error()
		} else if st, err := pgctl.GetStatus(pctx, x); err != nil {
			s.Reachable, s.Error = true, err.Error()
		} else {
			s.Reachable, s.Postgres = true, &st
			if h, err := e.APIHealth(pctx, n.Name); err == nil {
				s.API = &h
			}
		}
		cancel()
		out = append(out, s)
	}
	return out
}

// SyncOptions tunes Sync.
type SyncOptions struct {
	// Restart pulls images and recreates the stack (release propagation).
	Restart bool
}

// Sync re-materializes a standby's stack files (compose bundle, env, pins, registry,
// pg_hba, WireGuard) and optionally pulls + recreates it on the writer's release.
// migrate never runs on a standby: schema arrives through WAL.
func (e *Engine) Sync(ctx context.Context, name string, opt SyncOptions) error {
	n := e.Cluster.Node(name)
	if n == nil {
		return fmt.Errorf("node %q is not registered", name)
	}
	if n.Role != cluster.RoleStandby {
		return fmt.Errorf("%s is the writer; use `qabas deploy` there", name)
	}
	ro := StackOptions{Profile: nodestack.ProfileReplicaRO}
	if err := e.ConfigureNode(ctx, name, ro); err != nil {
		return err
	}
	if err := e.ApplyWG(ctx, name); err != nil {
		return err
	}
	if !opt.Restart {
		return nil
	}
	if _, err := e.Compose(ctx, name, ro, "pull --quiet"); err != nil {
		return err
	}
	return e.StartStack(ctx, name, ro)
}

// SyncAll syncs every standby; failures are collected (one node never blocks others).
func (e *Engine) SyncAll(ctx context.Context, opt SyncOptions) map[string]error {
	res := map[string]error{}
	for _, n := range e.Cluster.Standbys() {
		if st, _ := LoadState(e.Paths, n.Name); pendingInvite(st) {
			e.logf("sync %s skipped (invite pending)", n.Name)
			continue
		}
		e.logf("sync %s ...", n.Name)
		res[n.Name] = e.Sync(ctx, n.Name, opt)
	}
	return res
}

// RemoveOptions tunes Remove.
type RemoveOptions struct {
	// PurgeData deletes the replica's docker volumes (database copy, objects).
	PurgeData bool
	// Force removes the node from the registry even if it is unreachable.
	Force bool
}

// Remove detaches a standby: replication rule + slot on the upstream, stack and
// WireGuard on the node, shared secrets wiped there, registry and mesh updated.
func (e *Engine) Remove(ctx context.Context, name string, opt RemoveOptions) []error {
	var warns []error
	n := e.Cluster.Node(name)
	if n == nil {
		return []error{fmt.Errorf("node %q is not registered", name)}
	}
	if n.Role == cluster.RolePrimary {
		return []error{fmt.Errorf("refusing to remove the writer %s", name)}
	}
	if d := e.Cluster.Downstreams(name); len(d) > 0 {
		return []error{fmt.Errorf("%s still feeds %s; re-point or remove it first", name, d[0].Name)}
	}
	up := e.Cluster.UpstreamOf(*n)

	if x, err := e.Exec(ctx, name); err != nil {
		if !opt.Force {
			return []error{fmt.Errorf("%s unreachable (%v); re-run with --force to drop it from the registry anyway", name, err)}
		}
		warns = append(warns, fmt.Errorf("%s unreachable; its stack was left in place: %v", name, err))
	} else {
		ro := StackOptions{Profile: nodestack.ProfileReplicaRO}
		down := "down --remove-orphans"
		if opt.PurgeData {
			down += " --volumes"
		}
		if _, err := e.Compose(ctx, name, ro, down); err != nil {
			warns = append(warns, fmt.Errorf("compose down on %s: %w", name, err))
		}
		l := e.Layout(name)
		cleanup := "set -eu\nrm -rf " + nodeexec.Quote(l.ConfigDir+"/nodes/"+cluster.SharedScope) + " " +
			nodeexec.Quote(l.ConfigDir+"/cluster.yaml") + " " + nodeexec.Quote(l.RoleFile()) + " " +
			nodeexec.Quote(l.ComposeDir+"/"+nodestack.OverlaysFile) + "\n"
		if _, err := run(ctx, x, cleanup); err != nil {
			warns = append(warns, fmt.Errorf("cleanup on %s: %w", name, err))
		}
		revoke := fmt.Sprintf(revokeKeyScript, nodeexec.Quote(sshUser(*n)), nodeexec.Quote(l.ConfigDir+"/ssh"))
		if _, err := run(ctx, x, revoke); err != nil {
			warns = append(warns, fmt.Errorf("cluster SSH key not revoked on %s: %w", name, err))
		}
		if _, err := run(ctx, x, tunnelTeardownScript(*n)); err != nil {
			warns = append(warns, fmt.Errorf("WireGuard/relay teardown on %s: %w", name, err))
		}
	}
	_ = RemoveInviteFile(e.Paths, name)
	if up != nil {
		if err := e.UnlinkStorage(ctx, up.Name, name); err != nil {
			warns = append(warns, fmt.Errorf("minio rule on %s: %w", up.Name, err))
		}
		if ux, err := e.Exec(ctx, up.Name); err == nil {
			if err := pgctl.DropSlot(ctx, ux, cluster.SlotName(name)); err != nil {
				warns = append(warns, fmt.Errorf("slot on %s: %w", up.Name, err))
			}
		}
	}
	if err := e.Cluster.Remove(name); err != nil {
		return append(warns, err)
	}
	if err := cluster.Save(e.Paths, e.Cluster); err != nil {
		return append(warns, err)
	}
	warns = append(warns, e.SyncMesh(ctx)...)
	for _, o := range e.Cluster.Nodes {
		if err := e.WriteHBA(ctx, o.Name); err != nil {
			warns = append(warns, err)
		}
	}
	_ = RemoveState(e.Paths, name)
	_ = cluster.RemoveNodeSecrets(e.Paths, name)
	if err := e.StopRelayServerIfUnused(ctx); err != nil {
		warns = append(warns, fmt.Errorf("stop the unused relay: %w", err))
	}
	return warns
}

// tunnelTeardownScript removes the relay client and WireGuard on a node. For a node
// reached over WireGuard (the session runs through the tunnel) it is scheduled a few
// seconds later so this session can return first.
func tunnelTeardownScript(n cluster.Node) string {
	body := fmt.Sprintf(removeUnitScript, RelayClientUnit, nodeexec.Quote(relayNodeDir+"/client.env")) + wg.DownScript
	if n.SSHVia != cluster.SSHViaWG {
		return body
	}
	return "set -eu\numask 077\ncat > /run/qabas-teardown.sh <<'QABAS_EOF'\n" + body + "QABAS_EOF\n" +
		"if command -v systemd-run >/dev/null 2>&1; then\n  systemd-run --quiet --collect --on-active=3 /bin/sh /run/qabas-teardown.sh\n" +
		"else\n  nohup sh -c 'sleep 3; sh /run/qabas-teardown.sh' >/dev/null 2>&1 &\nfi\n"
}

// Describe renders a short human line for a status row.
func (s NodeStatus) Describe() string {
	var parts []string
	if s.Postgres != nil {
		if s.Postgres.InRecovery {
			if s.Postgres.WalReceiver == "streaming" {
				parts = append(parts, fmt.Sprintf("standby lag %.1fs replay %s", s.Postgres.LagSeconds, s.Postgres.ReplayLSN))
			} else {
				parts = append(parts, fmt.Sprintf("WAL receiver stopped (replay %s)", s.Postgres.ReplayLSN))
			}
		} else {
			parts = append(parts, "writable at "+s.Postgres.CurrentLSN)
		}
	}
	if s.API != nil {
		parts = append(parts, "api "+s.API.Mode)
	}
	if s.Error != "" {
		parts = append(parts, "error: "+s.Error)
	}
	return strings.Join(parts, ", ")
}
