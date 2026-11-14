package top

import (
	"context"
	"sync"
	"time"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
)

// DefaultSourceTimeout bounds each source so one slow probe never stalls a refresh.
const DefaultSourceTimeout = 8 * time.Second

// BaseService is a compose service row before live stats are merged in.
type BaseService struct {
	Service string
	Name    string
	State   string
	Health  string
}

// Sources are the side effects Collect relies on. A nil func or executor skips its section.
type Sources struct {
	Host func() sysinfo.Info
	// CPUTimes, when set, replaces the host's short CPU sample with the average
	// since the previous refresh.
	CPUTimes func() (sysinfo.CPUTimes, bool)
	// CPUStat, when set, adds per-core usage split by user/nice/system/steal.
	CPUStat func() (sysinfo.CPUStat, []sysinfo.CPUStat, bool)
	// Tasks reports the thread count and runnable count.
	Tasks    func() (threads, running int, ok bool)
	Services func(ctx context.Context) ([]BaseService, error)
	Docker   func(ctx context.Context, args ...string) ([]byte, error)
	// Engine, when set, is preferred over Docker for exact container counters.
	Engine *Engine
	PG     nodeexec.Executor
	State  func(ctx context.Context) StateSection

	NoPostgres bool
	NoReplicas bool
}

// Collector produces snapshots and keeps the previous one to compute rates.
type Collector struct {
	Sources
	Timeout time.Duration
	Now     func() time.Time

	mu        sync.Mutex
	prev      *Snapshot
	prevCPU   *sysinfo.CPUTimes
	prevStat  *cpuReading
	FirstWait time.Duration // first per-core sample window (default 200ms)
}

type cpuReading struct {
	agg   sysinfo.CPUStat
	cores []sysinfo.CPUStat
}

func NewCollector(s Sources) *Collector {
	return &Collector{Sources: s, Timeout: DefaultSourceTimeout, Now: time.Now}
}

// Collect gathers every source concurrently and returns the merged snapshot.
func (c *Collector) Collect(ctx context.Context) Snapshot {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultSourceTimeout
	}
	c.mu.Lock()
	prev := c.prev
	c.mu.Unlock()

	snap := Snapshot{CollectedAt: now(), PostgresDisabled: c.NoPostgres, ReplicasDisabled: c.NoReplicas}
	var wg sync.WaitGroup
	run := func(fn func(ctx context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			fn(sctx)
		}()
	}

	if c.Host != nil {
		run(func(ctx context.Context) {
			snap.Host = collectHost(ctx, c.Host, prev)
			c.applyCPUAverage(&snap.Host.Info)
			c.applyCPUStat(&snap.Host)
			if c.Tasks != nil {
				if t, r, ok := c.Tasks(); ok {
					snap.Host.Threads, snap.Host.Running = t, r
				}
			}
		})
	}
	if c.Services != nil {
		run(func(ctx context.Context) {
			snap.Services = collectServices(ctx, c.Services, c.Docker, c.Engine, prev == nil)
		})
	}
	if c.PG != nil && !c.NoPostgres {
		run(func(ctx context.Context) { snap.Postgres = collectPostgres(ctx, c.PG) })
	}
	if c.PG != nil && !c.NoReplicas {
		run(func(ctx context.Context) { snap.Replicas = collectReplicas(ctx, c.PG) })
	}
	if c.State != nil {
		run(func(ctx context.Context) { snap.State = c.State(ctx) })
	}
	wg.Wait()

	if prev != nil {
		applyRates(prev, &snap)
	}
	snap.Findings = Evaluate(snap)

	c.mu.Lock()
	c.prev = &snap
	c.mu.Unlock()
	return snap
}

// collectHost runs the (context-unaware) host probe, falling back to the previous
// reading when it overruns the source timeout.
func collectHost(ctx context.Context, probe func() sysinfo.Info, prev *Snapshot) HostSection {
	done := make(chan sysinfo.Info, 1)
	go func() { done <- probe() }()
	select {
	case info := <-done:
		return HostSection{Info: info}
	case <-ctx.Done():
		h := HostSection{Err: "host probe timed out"}
		if prev != nil {
			h.Info = prev.Host.Info
		}
		return h
	}
}

func (c *Collector) applyCPUAverage(info *sysinfo.Info) {
	if c.CPUTimes == nil {
		return
	}
	cur, ok := c.CPUTimes()
	if !ok {
		return
	}
	c.mu.Lock()
	before := c.prevCPU
	c.prevCPU = &cur
	c.mu.Unlock()
	if before == nil {
		return
	}
	if pct, ok := sysinfo.CPUPercent(*before, cur); ok {
		info.CPU.UsagePercent = pct
	}
}

// applyCPUStat fills per-core usage from the counters since the previous refresh.
// The first refresh takes a short sample so the meters are populated immediately.
func (c *Collector) applyCPUStat(h *HostSection) {
	if c.CPUStat == nil {
		return
	}
	read := func() *cpuReading {
		agg, cores, ok := c.CPUStat()
		if !ok {
			return nil
		}
		return &cpuReading{agg: agg, cores: cores}
	}
	c.mu.Lock()
	before := c.prevStat
	c.mu.Unlock()
	if before == nil {
		if before = read(); before == nil {
			return
		}
		wait := c.FirstWait
		if wait <= 0 {
			wait = 200 * time.Millisecond
		}
		time.Sleep(wait)
	}
	cur := read()
	if cur == nil {
		return
	}
	c.mu.Lock()
	c.prevStat = cur
	c.mu.Unlock()

	agg := sysinfo.UsageBetween(before.agg, cur.agg)
	h.CPU = &agg
	h.Info.CPU.UsagePercent = agg.Total
	if len(before.cores) == len(cur.cores) {
		h.Cores = make([]sysinfo.CoreUsage, len(cur.cores))
		for i := range cur.cores {
			h.Cores[i] = sysinfo.UsageBetween(before.cores[i], cur.cores[i])
		}
	}
}

// applyRates fills per-second rates from the counter deltas since prev.
func applyRates(prev, cur *Snapshot) {
	dt := cur.CollectedAt.Sub(prev.CollectedAt).Seconds()
	if dt <= 0 {
		return
	}
	cur.IntervalSec = dt
	old := make(map[string]Service, len(prev.Services.Rows))
	for _, s := range prev.Services.Rows {
		old[s.Name] = s
	}
	for i := range cur.Services.Rows {
		s := &cur.Services.Rows[i]
		o, ok := old[s.Name]
		if !ok || !o.StartedAt.Equal(s.StartedAt) {
			continue
		}
		s.NetRxPerSec = rate(o.NetRxBytes, s.NetRxBytes, dt)
		s.NetTxPerSec = rate(o.NetTxBytes, s.NetTxBytes, dt)
		s.BlockRPerSec = rate(o.BlockRead, s.BlockRead, dt)
		s.BlockWPerSec = rate(o.BlockWrite, s.BlockWrite, dt)
		if o.systemCPU > 0 && s.systemCPU > 0 {
			s.CPUPercent = containerCPUPercent(o.cpuTotal, s.cpuTotal, o.systemCPU, s.systemCPU, s.onlineCPUs)
		}
	}
	if p, q := prev.Postgres, cur.Postgres; p != nil && q != nil && p.Err == "" && q.Err == "" {
		q.TPS = rate(p.Xacts, q.Xacts, dt)
	}
}

// rate is the per-second increase; counter resets (container or server restart) yield 0.
func rate(before, after uint64, dt float64) float64 {
	if after < before || dt <= 0 {
		return 0
	}
	return float64(after-before) / dt
}

// CachedProbe wraps a slow probe so it runs at most once per ttl.
func CachedProbe[T any](ttl time.Duration, fn func() T) func() T {
	var (
		mu   sync.Mutex
		at   time.Time
		last T
	)
	return func() T {
		mu.Lock()
		defer mu.Unlock()
		if at.IsZero() || time.Since(at) >= ttl {
			last = fn()
			at = time.Now()
		}
		return last
	}
}
