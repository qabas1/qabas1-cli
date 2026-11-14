package replica

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/qabasone/qabasone-cli/internal/nodeexec"
)

// PathCheck tests the actual network namespace used by a writer service.
type PathCheck struct {
	Node        string `json:"node"`
	Service     string `json:"service"`
	Container   string `json:"container"`
	Source      string `json:"source,omitempty"`
	Destination string `json:"destination"`
	OK          bool   `json:"ok"`
	Detail      string `json:"detail"`
}

const PathGuidance = "check the writer's operator-owned inet fort forward chain for new traffic from this container bridge toward qabas0; persist a narrowly scoped rule in the host firewall configuration"

// CheckPaths checks PostgreSQL and MinIO egress from the writer. Both probes run in
// parallel and use the running containers' network namespaces (including their
// default routes), rather than the host's route or a one-off mc container's route.
func (e *Engine) CheckPaths(ctx context.Context, name string) []PathCheck {
	n := e.Cluster.Node(name)
	out := []PathCheck{{Node: name, Service: "postgres", Container: "qabasone-postgres"}, {Node: name, Service: "minio", Container: "qabasone-minio"}}
	if n == nil || net.ParseIP(n.WGIP) == nil {
		for i := range out {
			out[i].Detail = "peer is missing a valid WireGuard IP"
		}
		return out
	}
	w, err := e.writer()
	if err != nil {
		for i := range out {
			out[i].Detail = err.Error()
		}
		return out
	}
	x, err := e.Exec(ctx, w.Name)
	if err != nil {
		for i := range out {
			out[i].Detail = "writer executor: " + err.Error()
		}
		return out
	}
	probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, port := range []string{"5432", "9000"} {
		out[i].Destination = net.JoinHostPort(n.WGIP, port)
		wg.Add(1)
		go func(i int, port string) {
			defer wg.Done()
			out[i] = checkContainerPath(probeCtx, x, out[i], n.WGIP, port)
		}(i, port)
	}
	wg.Wait()
	return out
}

func checkContainerPath(ctx context.Context, x nodeexec.Executor, p PathCheck, ip, port string) PathCheck {
	// The IP and port originate from a validated registry IP and fixed service ports.
	// Python runs on the host under nsenter, so no Python dependency is added to images.
	py := `import socket,sys; s=socket.create_connection((sys.argv[1],int(sys.argv[2])),3); print("connected="+s.getsockname()[0]); s.close()`
	script := `pid=$(docker inspect -f '{{.State.Pid}}' ` + p.Container + `) || exit; ` +
		`test "$pid" -gt 1 || exit 1; ` +
		`nsenter -t "$pid" -n ip route get ` + nodeexec.Quote(ip) + ` | head -n 1; ` +
		`nsenter -t "$pid" -n python3 -c ` + nodeexec.Quote(py) + ` ` + nodeexec.Quote(ip) + ` ` + port
	res, err := x.Run(ctx, script, nil)
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		for j := 0; j+1 < len(fields); j++ {
			if fields[j] == "src" {
				p.Source = fields[j+1]
				break
			}
		}
	}
	if err == nil && res.ExitCode == 0 && strings.Contains(res.Stdout, "connected=") {
		p.OK, p.Detail = true, "TCP connection established"
		return p
	}
	why := "connection failed"
	if err != nil {
		why = err.Error()
	} else if s := strings.TrimSpace(res.Stderr); s != "" {
		why = s
	}
	if len(why) > 240 {
		why = why[:240]
	}
	p.Detail = fmt.Sprintf("%s; %s", why, PathGuidance)
	return p
}
