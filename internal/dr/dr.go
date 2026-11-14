// Package dr changes and reports which node is the writer: status/reachability facts,
// promotion candidates, break-glass promote, planned switchover, rejoin and failback,
// and the writer lease. Replica provisioning lives in package replica.
package dr

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/lease"
	"github.com/qabasone/qabasone-cli/internal/pgctl"
	"github.com/qabasone/qabasone-cli/internal/replica"
)

// Engine wraps the replica engine (connections, stack configuration) with DR actions.
type Engine struct {
	*replica.Engine
	Witnesses []lease.Witness
	// DurabilityProof must verify acknowledged PostgreSQL and MinIO writes on the
	// selected failover target, and verify old-writer fencing for transitions.
	// No production implementation exists yet; nil deliberately blocks lease
	// renewal and writer transitions.
	DurabilityProof func(context.Context, string) error
}

func (e *Engine) requireDurabilityProof(ctx context.Context, target string) error {
	if e.DurabilityProof == nil {
		return fmt.Errorf("PostgreSQL and MinIO acknowledged-write durability proof is not implemented")
	}
	return e.DurabilityProof(ctx, target)
}

// Witnesses builds lease witnesses from the registry.
func Witnesses(c *cluster.Cluster, token string) []lease.Witness {
	var out []lease.Witness
	for _, w := range c.Witnesses {
		switch w.Kind {
		case "http":
			out = append(out, &lease.HTTPWitness{Label: w.Name, URL: w.URL, Token: token})
		case "file":
			out = append(out, &lease.FileWitness{Label: w.Name, Path: w.Path})
		}
	}
	return out
}

// Reach is one row of the reachability matrix, observed from Observer.
type Reach struct {
	Observer string          `json:"observer"`
	Targets  map[string]bool `json:"targets"`
	Internet *bool           `json:"internet,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// Facts is everything `qabas dr status` knows. It never acts.
type Facts struct {
	At               time.Time            `json:"at"`
	Self             string               `json:"self"`
	Writer           string               `json:"writer"`
	Epoch            int64                `json:"epoch"`
	WriterReachable  bool                 `json:"writerReachable"`
	WriterSeenBy     []string             `json:"writerSeenBy,omitempty"`
	LeaseLocalOnly   bool                 `json:"leaseLocalOnly"`
	LeaseMode        string               `json:"leaseMode"`
	WitnessCount     int                  `json:"witnessCount"`
	Lease            lease.Record         `json:"lease"`
	LeaseVotes       []lease.Vote         `json:"leaseVotes,omitempty"`
	LeaseValidQuorum bool                 `json:"leaseValidAtQuorum"`
	LeaseHolder      string               `json:"leaseHolder,omitempty"`
	Nodes            []replica.NodeStatus `json:"nodes"`
	Reachability     []Reach              `json:"reachability"`
	Candidates       []Candidate          `json:"candidates"`
	Verdict          string               `json:"verdict"`
	VerdictCode      string               `json:"verdictCode"`
	ReadinessCode    string               `json:"readinessCode"`
	Readiness        string               `json:"readiness"`
	Paths            []replica.PathCheck  `json:"paths,omitempty"`
}

// Candidate is a promotable standby with the facts that rank it.
type Candidate struct {
	Node       string  `json:"node"`
	Site       string  `json:"site"`
	Priority   int     `json:"priority"`
	ReplayLSN  string  `json:"replayLSN"`
	LastReplay string  `json:"lastReplayAt,omitempty"`
	LagSeconds float64 `json:"lagSeconds"`
	Timeline   int     `json:"timeline"`
	Rank       int     `json:"rank"`
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Status gathers facts from every reachable node plus the witnesses.
func (e *Engine) Status(ctx context.Context) Facts {
	f := Facts{At: e.now(), Self: e.Self, Writer: e.Cluster.Writer, Epoch: e.Cluster.Epoch, LeaseMode: e.Cluster.LeaseMode}
	if f.Writer == "" {
		if p := e.Cluster.Primary(); p != nil {
			f.Writer = p.Name
		}
	}
	f.Nodes = e.Engine.Status(ctx)
	for _, n := range f.Nodes {
		if n.Name == f.Writer && n.Postgres != nil && !n.Postgres.InRecovery {
			f.WriterReachable = true
		}
	}
	f.Reachability = e.matrix(ctx, f.Nodes)
	for _, r := range f.Reachability {
		if r.Observer != f.Writer && r.Targets[f.Writer] {
			f.WriterSeenBy = append(f.WriterSeenBy, r.Observer)
		}
	}
	f.WitnessCount = len(e.Witnesses)
	f.LeaseLocalOnly = e.Cluster.LeaseMode == cluster.LeaseModeLocalOnly
	if !f.LeaseLocalOnly {
		f.Lease, f.LeaseVotes = lease.Observe(ctx, e.Witnesses, f.At)
		f.LeaseValidQuorum, f.LeaseHolder = lease.ValidAtQuorum(f.LeaseVotes, f.At)
	}
	f.Candidates = rankCandidates(e.Cluster, f.Nodes)
	f.VerdictCode, f.Verdict = verdict(f)
	var issues []string
	if err := e.Cluster.ValidateLeaseMode(e.Cluster.LeaseMode); err != nil {
		issues = append(issues, "lease policy invalid: "+err.Error())
	}
	for _, n := range f.Nodes {
		if n.Name != f.Writer && !n.Reachable {
			issues = append(issues, n.Name+" unreachable")
		}
	}
	if e.Self == f.Writer {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		check := e.Check(checkCtx)
		cancel()
		f.Paths = check.Paths
		for _, p := range check.Paths {
			if !p.OK {
				issues = append(issues, p.Node+" "+p.Service+" container path failed")
			}
		}
	} else {
		issues = append(issues, "container paths must be checked on writer")
	}
	if len(issues) == 0 {
		f.ReadinessCode, f.Readiness = "CONNECTIVITY_OK", "Replica paths and witnesses are available; synchronous data durability is not verified"
	} else {
		f.ReadinessCode, f.Readiness = "DEGRADED", strings.Join(issues, "; ")
	}
	return f
}

// matrix asks every reachable node whether it can reach every other node over
// WireGuard (and the public probe), so a local outage is not mistaken for a dead writer.
func (e *Engine) matrix(ctx context.Context, nodes []replica.NodeStatus) []Reach {
	var out []Reach
	for _, obs := range nodes {
		r := Reach{Observer: obs.Name, Targets: map[string]bool{}}
		x, err := e.Exec(ctx, obs.Name)
		if err != nil || !obs.Reachable {
			r.Error = "unreachable from " + e.Self
			out = append(out, r)
			continue
		}
		var script strings.Builder
		script.WriteString("set +e\n")
		for _, t := range e.Cluster.Nodes {
			if t.Name == obs.Name || t.WGIP == "" {
				continue
			}
			fmt.Fprintf(&script, "if ping -c 1 -W 2 %s >/dev/null 2>&1; then echo '%s=1'; else echo '%s=0'; fi\n", t.WGIP, t.Name, t.Name)
		}
		if e.Cluster.PublicProbe != "" {
			fmt.Fprintf(&script, "if (curl -fsS -m 5 -o /dev/null %[1]q || wget -q -T 5 -O /dev/null %[1]q) >/dev/null 2>&1; then echo '@internet=1'; else echo '@internet=0'; fi\n", e.Cluster.PublicProbe)
		}
		res, err := x.Run(ctx, script.String(), nil)
		if err != nil {
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			if k == "@internet" {
				b := v == "1"
				r.Internet = &b
				continue
			}
			r.Targets[k] = v == "1"
		}
		out = append(out, r)
	}
	return out
}

func rankCandidates(c *cluster.Cluster, nodes []replica.NodeStatus) []Candidate {
	var out []Candidate
	for _, n := range nodes {
		reg := c.Node(n.Name)
		if reg == nil || reg.Role != cluster.RoleStandby || !reg.Promotable || n.Postgres == nil || !n.Postgres.InRecovery {
			continue
		}
		out = append(out, Candidate{
			Node: n.Name, Site: n.Site, Priority: reg.Priority,
			ReplayLSN: n.Postgres.ReplayLSN, LastReplay: n.Postgres.LastReplayAt,
			LagSeconds: n.Postgres.LagSeconds, Timeline: n.Postgres.Timeline,
		})
	}
	// Highest observed replay LSN first, then operator priority and name. An
	// observed LSN does not prove durability of every acknowledged write.
	sort.SliceStable(out, func(i, j int) bool {
		if cmp := pgctl.CompareLSN(out[i].ReplayLSN, out[j].ReplayLSN); cmp != 0 {
			return cmp > 0
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Node < out[j].Node
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// Verdict codes.
const (
	VerdictHealthy         = "HEALTHY"
	VerdictPartition       = "PARTIAL_PARTITION"
	VerdictLeaseHeld       = "WAIT_LEASE"
	VerdictWriterDown      = "WRITER_DOWN"
	VerdictNoCandidate     = "WRITER_DOWN_NO_CANDIDATE"
	VerdictThisSiteOffline = "THIS_SITE_ISOLATED"
)

func verdict(f Facts) (string, string) {
	if f.WriterReachable {
		return VerdictHealthy, fmt.Sprintf("Writer %s is up (epoch %d). No promotion indicated; check DR readiness and durability before relying on failover.", f.Writer, f.Epoch)
	}
	if len(f.WriterSeenBy) > 0 {
		return VerdictPartition, fmt.Sprintf("Writer %s is NOT reachable from %s but IS reachable from %s. It is alive: do NOT promote; fix this site's connectivity.",
			f.Writer, f.Self, strings.Join(f.WriterSeenBy, ", "))
	}
	reachablePeers := 0
	for _, n := range f.Nodes {
		if n.Reachable && n.Name != f.Self {
			reachablePeers++
		}
	}
	if reachablePeers == 0 && len(f.Nodes) > 2 {
		return VerdictThisSiteOffline, fmt.Sprintf("%s cannot reach any other node. This site is probably isolated; keep serving read-only and do NOT promote from here.", f.Self)
	}
	if f.LeaseValidQuorum {
		exp := f.Lease.LeaseExpiresAt.Format(time.RFC3339)
		return VerdictLeaseHeld, fmt.Sprintf("Writer %s is unreachable but a witness majority still grants it a lease until %s (it may be alive and isolated). Promotion is refused until then.", f.LeaseHolder, exp)
	}
	if len(f.Candidates) == 0 {
		return VerdictNoCandidate, fmt.Sprintf("Writer %s is unreachable from every reachable node and no promotable standby is healthy. Restore the writer or rebuild from backup.", f.Writer)
	}
	best := f.Candidates[0]
	lease := "witness majority confirms the old lease expired"
	if f.WitnessCount < 3 {
		return VerdictWriterDown, fmt.Sprintf("Writer %s is unreachable; only %d/3 required witnesses are configured, so promotion is refused. Restore and fence the writer before recovery. Candidate %s replayed %s.", f.Writer, f.WitnessCount, best.Node, best.ReplayLSN)
	}
	return VerdictWriterDown, fmt.Sprintf("Writer %s is unreachable from every reachable node; %s. Best candidate: %s (replay %s, last transaction %s). Promote with: qabas dr promote %s --break-glass",
		f.Writer, lease, best.Node, best.ReplayLSN, best.LastReplay, best.Node)
}
