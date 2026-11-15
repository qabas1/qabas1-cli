package dr

import (
	"fmt"
	"sort"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/cluster"
)

// RoutingOptions for a site's local entry point.
type RoutingOptions struct {
	Site string
	// HealthHost is the Host/SNI used for health checks (the API domain).
	HealthHost string
	Port       int
}

// RenderHAProxy renders a TLS-passthrough HAProxy config for a site: the writer is the
// active server (health: /health/ready); same-site standbys are `backup` servers, so
// local users fall back to read-only automatically when the writer is unreachable.
// Traffic to remote nodes rides WireGuard.
func RenderHAProxy(c *cluster.Cluster, opt RoutingOptions) (string, error) {
	if opt.Port == 0 {
		opt.Port = 443
	}
	if opt.HealthHost == "" {
		return "", fmt.Errorf("health host (API domain) is required")
	}
	w := c.Primary()
	if w == nil || w.WGIP == "" {
		return "", fmt.Errorf("registry has no writer with a WireGuard address")
	}
	var local []cluster.Node
	for _, n := range c.Standbys() {
		if n.Site == opt.Site && n.WGIP != "" {
			local = append(local, n)
		}
	}
	sort.Slice(local, func(i, j int) bool {
		if local[i].Priority != local[j].Priority {
			return local[i].Priority > local[j].Priority
		}
		return local[i].Name < local[j].Name
	})
	var b strings.Builder
	fmt.Fprintf(&b, `# Managed by qabas dr routing (site %s, writer %s, epoch %d). Re-render after every writer change.
global
  log stdout format raw local0

defaults
  mode tcp
  log global
  option tcplog
  timeout connect 3s
  timeout client 1h
  timeout server 1h

frontend qabas_https
  bind :%d
  default_backend qabas_nodes

backend qabas_nodes
  option httpchk
  http-check connect ssl sni %s
  http-check send meth GET uri /health/ready ver HTTP/1.1 hdr Host %s
  http-check expect status 200
  default-server inter 5s fall 3 rise 2 check-ssl verify none
  server %s %s:443 check
`, opt.Site, w.Name, c.Epoch, opt.Port, opt.HealthHost, opt.HealthHost, w.Name, w.WGIP)
	for _, n := range local {
		fmt.Fprintf(&b, "  server %s %s:443 check backup\n", n.Name, n.WGIP)
	}
	return b.String(), nil
}

// RoutingNotes explains DNS / tunnel steps after a writer change.
func RoutingNotes(c *cluster.Cluster, site cluster.Site) []string {
	w := c.Primary()
	notes := []string{
		fmt.Sprintf("Writer is %s (epoch %d).", w.Name, c.Epoch),
	}
	if site.LocalDomain != "" {
		notes = append(notes, fmt.Sprintf("Company DNS: point %s at this site's HAProxy (split-horizon); HAProxy follows the writer by health checks.", site.LocalDomain))
	}
	if w.PublicURL != "" {
		notes = append(notes, fmt.Sprintf("Public entry: %s must reach %s (Cloudflare Tunnel: run cloudflared on %s with the same tunnel token, or update the DNS record).", w.PublicURL, w.Name, w.Name))
	} else {
		notes = append(notes, "Public entry: if you use Cloudflare Tunnel, start cloudflared on the new writer with the existing tunnel token (`cloudflared service install <token>`) and stop it on the old one.")
	}
	return notes
}
