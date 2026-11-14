package top

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/sysinfo"
)

// Stats sources reported in ServicesSection.Source.
const (
	SourceEngine = "engine"
	SourceCLI    = "cli"
)

func collectServices(ctx context.Context, list func(context.Context) ([]BaseService, error),
	docker func(context.Context, ...string) ([]byte, error), engine *Engine, first bool) ServicesSection {
	base, err := list(ctx)
	if err != nil {
		return ServicesSection{Err: err.Error()}
	}
	rows := make([]Service, 0, len(base))
	var names []string
	for _, b := range base {
		rows = append(rows, Service{Service: b.Service, Name: b.Name, State: b.State, Health: b.Health})
		if b.Name != "" && strings.EqualFold(b.State, "running") {
			names = append(names, b.Name)
		}
	}
	sec := ServicesSection{Rows: rows}
	if len(names) == 0 {
		return sec
	}
	if engine != nil {
		stats, info, err := engine.Collect(ctx, names, first)
		if err == nil {
			sec.Source = SourceEngine
			mergeStats(sec.Rows, stats)
			mergeInspect(sec.Rows, info)
			return sec
		}
	}
	if docker == nil {
		return sec
	}
	sec.Source = SourceCLI
	raw, err := docker(ctx, append([]string{"stats", "--no-stream", "--format", "{{json .}}"}, names...)...)
	if err != nil {
		sec.Err = "docker stats: " + err.Error()
	} else {
		mergeStats(sec.Rows, ParseStats(raw))
	}
	raw, err = docker(ctx, append([]string{"inspect", "--format", "{{.Name}}|{{.RestartCount}}|{{.State.StartedAt}}"}, names...)...)
	if err == nil {
		mergeInspect(sec.Rows, parseInspect(raw))
	}
	return sec
}

// ContainerStats is one decoded `docker stats --format '{{json .}}'` row.
type ContainerStats struct {
	Name          string
	CPUPercent    float64
	MemBytes      uint64
	MemLimitBytes uint64
	MemPercent    float64
	NetRx, NetTx  uint64
	BlockRead     uint64
	BlockWrite    uint64
	// Raw CPU counters (Engine API only) for CPU% between refreshes.
	CPUTotal   uint64
	SystemCPU  uint64
	OnlineCPUs uint32
	// Exact is true when counters are raw bytes rather than rounded CLI sizes.
	Exact bool
}

// ParseStats decodes docker stats NDJSON (human sizes such as "12.3MiB / 1.5GiB").
func ParseStats(raw []byte) map[string]ContainerStats {
	out := map[string]ContainerStats{}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v struct {
			Name     string
			CPUPerc  string
			MemUsage string
			MemPerc  string
			NetIO    string
			BlockIO  string
		}
		if json.Unmarshal([]byte(line), &v) != nil || v.Name == "" {
			continue
		}
		s := ContainerStats{Name: v.Name, CPUPercent: parsePercent(v.CPUPerc), MemPercent: parsePercent(v.MemPerc)}
		s.MemBytes, s.MemLimitBytes = splitPair(v.MemUsage)
		s.NetRx, s.NetTx = splitPair(v.NetIO)
		s.BlockRead, s.BlockWrite = splitPair(v.BlockIO)
		out[v.Name] = s
	}
	return out
}

type inspectRow struct {
	Restarts  int
	StartedAt time.Time
}

// parseInspect decodes lines of "/name|restartCount|startedAt".
func parseInspect(raw []byte) map[string]inspectRow {
	out := map[string]inspectRow{}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 {
			continue
		}
		n, _ := strconv.Atoi(parts[1])
		t, _ := time.Parse(time.RFC3339Nano, parts[2])
		out[strings.TrimPrefix(parts[0], "/")] = inspectRow{Restarts: n, StartedAt: t}
	}
	return out
}

func mergeStats(rows []Service, stats map[string]ContainerStats) {
	for i := range rows {
		s, ok := stats[rows[i].Name]
		if !ok {
			continue
		}
		r := &rows[i]
		r.CPUPercent, r.MemPercent = s.CPUPercent, s.MemPercent
		r.MemBytes, r.MemLimitBytes = s.MemBytes, s.MemLimitBytes
		r.NetRxBytes, r.NetTxBytes = s.NetRx, s.NetTx
		r.BlockRead, r.BlockWrite = s.BlockRead, s.BlockWrite
		r.cpuTotal, r.systemCPU, r.onlineCPUs = s.CPUTotal, s.SystemCPU, s.OnlineCPUs
	}
}

func mergeInspect(rows []Service, info map[string]inspectRow) {
	for i := range rows {
		if in, ok := info[rows[i].Name]; ok {
			rows[i].Restarts, rows[i].StartedAt = in.Restarts, in.StartedAt
		}
	}
}

func parsePercent(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return f
}

func splitPair(s string) (uint64, uint64) {
	a, b, _ := strings.Cut(s, "/")
	return sysinfo.ParseSize(a), sysinfo.ParseSize(b)
}

// HumanRate renders a byte rate compactly ("1.2 MiB/s", "0 B/s").
func HumanRate(bps float64) string {
	if bps < 1 {
		return "0 B/s"
	}
	return sysinfo.HumanBytes(uint64(bps)) + "/s"
}

// ShortDuration renders an age such as "3d4h", "12m" or "45s".
func ShortDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}
