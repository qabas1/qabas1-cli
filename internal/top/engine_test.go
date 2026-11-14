package top

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const statsCgroupV1 = `{
  "cpu_stats": {"cpu_usage": {"total_usage": 3000, "percpu_usage": [1500, 1500]}, "system_cpu_usage": 20000},
  "precpu_stats": {"cpu_usage": {"total_usage": 1000}, "system_cpu_usage": 10000},
  "memory_stats": {"usage": 1000, "limit": 4000, "stats": {"total_inactive_file": 200}},
  "networks": {"eth0": {"rx_bytes": 1234567, "tx_bytes": 89}, "eth1": {"rx_bytes": 3, "tx_bytes": 11}},
  "blkio_stats": {"io_service_bytes_recursive": [
    {"op": "Read", "value": 4096}, {"op": "Write", "value": 8192}, {"op": "Total", "value": 12288}
  ]}
}`

const statsCgroupV2 = `{
  "cpu_stats": {"cpu_usage": {"total_usage": 5000}, "system_cpu_usage": 90000, "online_cpus": 4},
  "memory_stats": {"usage": 1000, "limit": 2000, "stats": {"inactive_file": 400}},
  "blkio_stats": {"io_service_bytes_recursive": [{"op": "read", "value": 1}, {"op": "write", "value": 2}]}
}`

func decode(t *testing.T, raw string) ContainerStats {
	t.Helper()
	var s EngineStats
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s.Decode()
}

func TestEngineDecodeCgroupV1(t *testing.T) {
	s := decode(t, statsCgroupV1)
	if !s.Exact || s.MemBytes != 800 || s.MemLimitBytes != 4000 || s.MemPercent != 20 {
		t.Fatalf("memory: %+v", s)
	}
	if s.NetRx != 1234570 || s.NetTx != 100 {
		t.Fatalf("net must be exact and summed over interfaces: %+v", s)
	}
	if s.BlockRead != 4096 || s.BlockWrite != 8192 {
		t.Fatalf("blkio: %+v", s)
	}
	// (3000-1000)/(20000-10000) * 2 cpus (from percpu_usage) * 100
	if s.OnlineCPUs != 2 || s.CPUPercent != 40 {
		t.Fatalf("cpu from precpu: %+v", s)
	}
}

func TestEngineDecodeCgroupV2(t *testing.T) {
	s := decode(t, statsCgroupV2)
	if s.MemBytes != 600 || s.MemPercent != 30 {
		t.Fatalf("memory: %+v", s)
	}
	if s.BlockRead != 1 || s.BlockWrite != 2 {
		t.Fatalf("blkio op case: %+v", s)
	}
	if s.CPUPercent != 0 || s.CPUTotal != 5000 || s.SystemCPU != 90000 || s.OnlineCPUs != 4 {
		t.Fatalf("one-shot reads carry counters but no CPU%%: %+v", s)
	}
}

func TestContainerCPUPercent(t *testing.T) {
	if got := containerCPUPercent(5000, 7000, 90000, 98000, 4); got != 100 {
		t.Fatalf("two samples: %v", got)
	}
	if containerCPUPercent(7000, 5000, 90000, 98000, 4) != 0 || containerCPUPercent(1, 2, 5, 5, 4) != 0 {
		t.Fatal("resets and zero elapsed time must give 0")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fakeEngine(h http.Handler) *Engine {
	return &Engine{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Result(), nil
	})}}
}

func engineHandler(t *testing.T, statsQueries *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/stats"):
			if statsQueries != nil {
				*statsQueries = append(*statsQueries, r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, statsCgroupV1)
		case strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = io.WriteString(w, `{"RestartCount": 3, "State": {"StartedAt": "2026-10-01T10:00:00.5Z"}}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	})
}

func TestCollectServicesUsesEngine(t *testing.T) {
	var queries []string
	e := fakeEngine(engineHandler(t, &queries))
	list := func(context.Context) ([]BaseService, error) {
		return []BaseService{{Service: "api", Name: "qabasone-api", State: "running"}}, nil
	}
	docker := func(context.Context, ...string) ([]byte, error) {
		t.Fatal("CLI must not be used when the engine works")
		return nil, nil
	}
	sec := collectServices(context.Background(), list, docker, e, true)
	if sec.Source != SourceEngine || sec.Err != "" {
		t.Fatalf("got %+v", sec)
	}
	r := sec.Rows[0]
	if r.NetRxBytes != 1234570 || r.cpuTotal != 3000 || r.CPUPercent != 40 || r.Restarts != 3 || r.StartedAt.Hour() != 10 {
		t.Fatalf("row: %+v", r)
	}
	_ = collectServices(context.Background(), list, docker, e, false)
	if len(queries) != 2 || strings.Contains(queries[0], "one-shot") || !strings.Contains(queries[1], "one-shot=true") {
		t.Fatalf("first read must wait for precpu, later ones one-shot: %v", queries)
	}
}

func TestCollectorCPUFromTwoEngineSamples(t *testing.T) {
	samples := []string{
		`{"cpu_stats": {"cpu_usage": {"total_usage": 5000}, "system_cpu_usage": 90000, "online_cpus": 4}}`,
		`{"cpu_stats": {"cpu_usage": {"total_usage": 7000}, "system_cpu_usage": 98000, "online_cpus": 4}}`,
	}
	i := 0
	e := fakeEngine(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stats") {
			_, _ = io.WriteString(w, samples[i])
			return
		}
		_, _ = io.WriteString(w, `{"State": {"StartedAt": "2026-10-01T10:00:00Z"}}`)
	}))
	c := NewCollector(Sources{
		Services: func(context.Context) ([]BaseService, error) {
			return []BaseService{{Service: "api", Name: "qabasone-api", State: "running"}}, nil
		},
		Engine:     e,
		NoPostgres: true,
		NoReplicas: true,
	})
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return start.Add(time.Duration(i) * 2 * time.Second) }
	if got := c.Collect(context.Background()).Services.Rows[0].CPUPercent; got != 0 {
		t.Fatalf("no precpu on the first sample: %v", got)
	}
	i = 1
	if got := c.Collect(context.Background()).Services.Rows[0].CPUPercent; got != 100 {
		t.Fatalf("cpu from deltas: %v", got)
	}
}

func TestCollectServicesFallsBackToCLI(t *testing.T) {
	broken := &Engine{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("socket gone")
	})}}
	list := func(context.Context) ([]BaseService, error) {
		return []BaseService{{Service: "api", Name: "qabasone-api", State: "running"}}, nil
	}
	sec := collectServices(context.Background(), list, fakeDocker(statsNDJSON, "/qabasone-api|1|2026-10-01T10:00:00Z"), broken, true)
	if sec.Source != SourceCLI || sec.Err != "" || sec.Rows[0].CPUPercent != 12.5 {
		t.Fatalf("got %+v", sec)
	}
}

func TestNewEngineOverUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets in temp dirs are unreliable on Windows")
	}
	dir, err := os.MkdirTemp("", "qtop")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip(err)
	}
	srv := httptest.NewUnstartedServer(engineHandler(t, nil))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	e, err := NewEngine(context.Background(), sock)
	if err != nil {
		t.Fatal(err)
	}
	stats, info, err := e.Collect(context.Background(), []string{"qabasone-api"}, false)
	if err != nil || stats["qabasone-api"].MemBytes != 800 || info["qabasone-api"].Restarts != 3 {
		t.Fatalf("stats %+v info %+v err %v", stats, info, err)
	}
	if _, err := NewEngine(context.Background(), filepath.Join(dir, "missing.sock")); err == nil {
		t.Fatal("ping on a missing socket must fail")
	}
}
