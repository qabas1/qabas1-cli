package dr

import (
	"context"
	"sync"
	"time"

	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/replica"
)

type CheckReport struct {
	At       time.Time           `json:"at"`
	Writer   string              `json:"writer"`
	OK       bool                `json:"ok"`
	Paths    []replica.PathCheck `json:"paths"`
	Warnings []string            `json:"warnings,omitempty"`
}

// Check is read-only and has no sentinel object writes or firewall mutations.
func (e *Engine) Check(ctx context.Context) CheckReport {
	r := CheckReport{At: e.now(), Writer: e.Cluster.Writer, OK: true}
	if e.Self != e.Cluster.Writer {
		r.OK = false
		r.Warnings = append(r.Warnings, "run this check on the registered writer")
		return r
	}
	if err := e.Cluster.ValidateLeaseMode(e.Cluster.LeaseMode); err != nil {
		r.OK = false
		r.Warnings = append(r.Warnings, "lease policy invalid: "+err.Error())
	} else if e.Cluster.LeaseMode == cluster.LeaseModeStrict && len(e.Witnesses) < 3 {
		r.OK = false
		r.Warnings = append(r.Warnings, "strict DR requires at least three independent lease witnesses; current lease cannot safely renew")
	}
	// Populate the cached writer executor before parallel checks. Engine.Exec's
	// connection cache is not safe for concurrent first writes.
	if _, err := e.Exec(ctx, e.Self); err != nil {
		r.OK = false
		r.Warnings = append(r.Warnings, "writer executor: "+err.Error())
		return r
	}
	type result struct {
		index int
		paths []replica.PathCheck
	}
	results := make([]result, len(e.Cluster.Nodes))
	var wg sync.WaitGroup
	for i, n := range e.Cluster.Nodes {
		if n.Name == e.Self || !e.provisioned(n.Name) {
			continue
		}
		wg.Add(1)
		go func(i int, name string) { defer wg.Done(); results[i] = result{i, e.CheckPaths(ctx, name)} }(i, n.Name)
	}
	wg.Wait()
	for _, x := range results {
		for _, p := range x.paths {
			r.Paths = append(r.Paths, p)
			if !p.OK {
				r.OK = false
			}
		}
	}
	return r
}
