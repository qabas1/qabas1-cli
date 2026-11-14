package top

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultDockerSocket is where the Docker daemon listens on Linux.
const DefaultDockerSocket = "/var/run/docker.sock"

// Engine reads exact container counters from the Docker Engine API over its unix
// socket. docker stats rounds counters to 3 significant digits, which makes
// per-refresh rates mostly zero; the API reports raw bytes.
type Engine struct {
	client *http.Client
}

// EngineSocket returns the unix socket to use, or "" when Docker is reached some
// other way (tcp, npipe, no socket), in which case the CLI path is used.
func EngineSocket() string {
	if h := strings.TrimSpace(os.Getenv("DOCKER_HOST")); h != "" {
		if p, ok := strings.CutPrefix(h, "unix://"); ok {
			return p
		}
		return ""
	}
	if fi, err := os.Stat(DefaultDockerSocket); err == nil && fi.Mode()&os.ModeSocket != 0 {
		return DefaultDockerSocket
	}
	return ""
}

// NewEngine returns a client for socket after a successful ping.
func NewEngine(ctx context.Context, socket string) (*Engine, error) {
	if socket == "" {
		return nil, errors.New("no docker unix socket")
	}
	e := &Engine{client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     30 * time.Second,
	}}}
	if err := e.get(ctx, "/_ping", nil); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("docker api %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	if v == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Collect fetches stats and inspect data for every container concurrently. With
// first set it waits for the daemon's own CPU sample (precpu) so CPU% is known
// immediately; later refreshes use one-shot reads and CPU deltas between refreshes.
func (e *Engine) Collect(ctx context.Context, names []string, first bool) (map[string]ContainerStats, map[string]inspectRow, error) {
	stats := make(map[string]ContainerStats, len(names))
	info := make(map[string]inspectRow, len(names))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	q := "?stream=false&one-shot=true"
	if first {
		q = "?stream=false"
	}
	for _, name := range names {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			p := "/containers/" + url.PathEscape(name)
			var raw EngineStats
			serr := e.get(ctx, p+"/stats"+q, &raw)
			var ins engineInspect
			ierr := e.get(ctx, p+"/json", &ins)
			mu.Lock()
			defer mu.Unlock()
			if serr != nil {
				if firstErr == nil {
					firstErr = serr
				}
			} else {
				s := raw.Decode()
				s.Name = name
				stats[name] = s
			}
			if ierr == nil {
				t, _ := time.Parse(time.RFC3339Nano, ins.State.StartedAt)
				info[name] = inspectRow{Restarts: ins.RestartCount, StartedAt: t}
			}
		}(name)
	}
	wg.Wait()
	if len(stats) == 0 && firstErr != nil {
		return nil, nil, firstErr
	}
	return stats, info, nil
}

type engineInspect struct {
	RestartCount int
	State        struct {
		StartedAt string
	}
}

type engineCPU struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  uint32 `json:"online_cpus"`
}

// EngineStats is the subset of GET /containers/{id}/stats that top uses.
type EngineStats struct {
	CPU    engineCPU `json:"cpu_stats"`
	PreCPU engineCPU `json:"precpu_stats"`
	Memory struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	Blkio struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

// Decode converts raw API stats into exact counters, mirroring the docker CLI's
// memory figure (usage minus inactive page cache) and CPU% formula.
func (s EngineStats) Decode() ContainerStats {
	out := ContainerStats{
		MemLimitBytes: s.Memory.Limit,
		CPUTotal:      s.CPU.CPUUsage.TotalUsage,
		SystemCPU:     s.CPU.SystemUsage,
		OnlineCPUs:    s.CPU.OnlineCPUs,
		Exact:         true,
	}
	if out.OnlineCPUs == 0 {
		out.OnlineCPUs = uint32(len(s.CPU.CPUUsage.PercpuUsage))
	}
	used := s.Memory.Usage
	for _, k := range []string{"total_inactive_file", "inactive_file"} {
		if v, ok := s.Memory.Stats[k]; ok && v < used {
			used -= v
			break
		}
	}
	out.MemBytes = used
	if s.Memory.Limit > 0 {
		out.MemPercent = float64(used) / float64(s.Memory.Limit) * 100
	}
	for _, n := range s.Networks {
		out.NetRx += n.RxBytes
		out.NetTx += n.TxBytes
	}
	for _, b := range s.Blkio.IOServiceBytesRecursive {
		switch strings.ToLower(b.Op) {
		case "read":
			out.BlockRead += b.Value
		case "write":
			out.BlockWrite += b.Value
		}
	}
	if s.PreCPU.SystemUsage > 0 {
		out.CPUPercent = containerCPUPercent(s.PreCPU.CPUUsage.TotalUsage, s.CPU.CPUUsage.TotalUsage,
			s.PreCPU.SystemUsage, s.CPU.SystemUsage, out.OnlineCPUs)
	}
	return out
}

// containerCPUPercent is docker's formula: container CPU time over host CPU time,
// scaled to the number of CPUs (100% = one full core).
func containerCPUPercent(cpuBefore, cpuAfter, sysBefore, sysAfter uint64, online uint32) float64 {
	if cpuAfter < cpuBefore || sysAfter <= sysBefore || online == 0 {
		return 0
	}
	return float64(cpuAfter-cpuBefore) / float64(sysAfter-sysBefore) * float64(online) * 100
}
