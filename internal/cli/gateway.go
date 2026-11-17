package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/secrets"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/spf13/cobra"
)

func gatewayDir(e *env) string { return filepath.Join(e.Paths.ConfigDir, "gateway") }

func gatewayContainerID(e *env, serviceEnv []string) (string, error) {
	ids, err := newRuntime().ServiceContainerIDs(e.Paths, serviceEnv, "gateway")
	if err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("gateway: expected one running Compose container, found %d", len(ids))
	}
	return ids[0], nil
}

func installedGatewayContainerID(e *env) (string, error) {
	st, err := state.Load(e.Paths)
	if err != nil {
		return "", err
	}
	return gatewayContainerID(e, compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease))
}

func gatewayConfig(domains config.Domains, trade string) (string, error) {
	routes := []struct{ host, upstream string }{
		{domains.API, "http://api:3030"}, {domains.App, "http://app:8080"},
		{domains.Dash, "http://dash:8080"}, {domains.Siraj, "http://siraj:8080"},
		{domains.S3, "http://minio:9000"}, {domains.S3Console, "http://minio:9001"},
	}
	if trade != "" {
		routes = append(routes, struct{ host, upstream string }{trade, "http://trade:8080"})
	}
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("worker_processes auto;\npid /tmp/nginx.pid;\nerror_log /dev/stderr warn;\nevents { worker_connections 1024; }\nhttp {\n")
	b.WriteString("  map $http_x_real_ip $audit_client_ip { default $http_x_real_ip; '' $remote_addr; }\n")
	b.WriteString("  log_format audit escape=json '{\"time\":\"$time_iso8601\",\"request_id\":\"$request_id\",\"client_ip\":\"$audit_client_ip\",\"peer_ip\":\"$remote_addr\",\"forwarded_for\":\"$http_x_forwarded_for\",\"method\":\"$request_method\",\"host\":\"$host\",\"path\":\"$uri\",\"protocol\":\"$server_protocol\",\"status\":$status,\"bytes_sent\":$body_bytes_sent,\"request_bytes\":$request_length,\"duration\":$request_time,\"upstream\":\"$upstream_addr\",\"upstream_status\":\"$upstream_status\",\"upstream_duration\":\"$upstream_response_time\",\"referer\":\"$http_referer\",\"user_agent\":\"$http_user_agent\",\"scheme\":\"$http_x_forwarded_proto\"}';\n  access_log /dev/stdout audit;\n  client_body_temp_path /tmp/client_temp;\n  proxy_temp_path /tmp/proxy_temp;\n")
	b.WriteString("  resolver 127.0.0.11 valid=10s ipv6=off;\n  map $http_upgrade $connection_upgrade { default upgrade; '' close; }\n")
	// REST is balanced independently of the client address. Only Socket.IO polling
	// needs affinity; NAT must not pin an entire office's REST traffic to one replica.
	// Keep REST failure suppression short while Docker DNS exposes starting replicas.
	b.WriteString("  upstream qabas_api { zone qabas_api 64k; least_conn; server api:3030 resolve max_fails=1 fail_timeout=2s; }\n")
	// Plain hash selects a resolved peer. Consistent hash selects the logical server
	// name, then round-robins its addresses, breaking polling affinity for Docker DNS.
	b.WriteString("  upstream qabas_api_socket { zone qabas_api_socket 64k; hash $http_x_forwarded_for; server api:3030 resolve max_fails=0; }\n")
	for _, service := range []struct{ name, address string }{
		{"app", "app:8080"}, {"dash", "dash:8080"}, {"siraj", "siraj:8080"},
		{"minio", "minio:9000"}, {"minio_console", "minio:9001"}, {"trade", "trade:8080"},
	} {
		if service.name == "trade" && trade == "" {
			continue
		}
		fmt.Fprintf(&b, "  upstream qabas_%s { zone qabas_%s 64k; server %s resolve; }\n", service.name, service.name, service.address)
	}
	b.WriteString("  server { listen 8080 default_server; server_name _; location = /__gateway/health { return 200 'ok'; } location / { return 404; } }\n")
	for _, r := range routes {
		if r.host == "" || strings.ContainsAny(r.host, " \t\r\n/:;{}$") || seen[r.host] {
			return "", fmt.Errorf("invalid or duplicate gateway domain %q", r.host)
		}
		seen[r.host] = true
		upstream := r.upstream
		if r.host == domains.API {
			upstream = "http://qabas_api"
		} else {
			switch r.upstream {
			case "http://app:8080":
				upstream = "http://qabas_app"
			case "http://dash:8080":
				upstream = "http://qabas_dash"
			case "http://siraj:8080":
				upstream = "http://qabas_siraj"
			case "http://minio:9000":
				upstream = "http://qabas_minio"
			case "http://minio:9001":
				upstream = "http://qabas_minio_console"
			case "http://trade:8080":
				upstream = "http://qabas_trade"
			}
		}
		fmt.Fprintf(&b, "  server { listen 8080; server_name %s;\n", r.host)
		b.WriteString("    client_max_body_size 0;\n")
		writeGatewayLocation(&b, "/", upstream)
		if r.host == domains.API {
			writeGatewayLocation(&b, "= /socket.io", "http://qabas_api_socket")
			writeGatewayLocation(&b, "^~ /socket.io/", "http://qabas_api_socket")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func writeGatewayLocation(b *strings.Builder, location, upstream string) {
	fmt.Fprintf(b, "    location %s {\n      proxy_pass %s;\n", location, upstream)
	b.WriteString("      proxy_http_version 1.1;\n      proxy_set_header Host $host;\n      proxy_set_header X-Real-IP $audit_client_ip;\n      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n      proxy_set_header X-Forwarded-Proto $http_x_forwarded_proto;\n      proxy_set_header X-Request-ID $request_id;\n      proxy_set_header Upgrade $http_upgrade;\n      proxy_set_header Connection $connection_upgrade;\n      proxy_read_timeout 3600s;\n      proxy_request_buffering off;\n    }\n")
}

func ensureGatewayOverlay(e *env) error {
	path := filepath.Join(e.Paths.ComposeDir(), "compose.gateway.yaml")
	if _, err := os.Stat(path); err != nil {
		cfg, err := config.Load(e.Paths)
		if err != nil {
			return err
		}
		data, err := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref).FetchComposeFile("compose.gateway.yaml")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	return nodestack.AddDirective(e.Paths.ComposeDir(), "file:compose.gateway.yaml")
}

func localNodeRole(e *env) (nodestack.Profile, error) {
	layout := nodestack.LocalLayout(e.Paths.ConfigDir, e.Paths.StateDir)
	b, err := os.ReadFile(layout.RoleFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return nodestack.Profile(strings.TrimSpace(string(b))), nil
}

func isReplicaRole(role nodestack.Profile) bool {
	return role == nodestack.ProfileReplicaRO || role == nodestack.ProfileDRPrimary
}

func applyGateway(e *env) error {
	cfg, err := config.Load(e.Paths)
	if err != nil {
		return err
	}
	st, err := state.Load(e.Paths)
	if err != nil {
		return err
	}
	role, err := localNodeRole(e)
	if err != nil {
		return err
	}
	replicaStack := isReplicaRole(role)
	if !st.Installed() && !replicaStack {
		return fmt.Errorf("QabasOne is not installed")
	}
	domains := cfg.EffectiveDomains()
	trade := domains.Trade
	if trade == "" && cfg.Domain != "" {
		trade = "trade." + cfg.Domain
	}
	tradeEnabled := false
	for _, file := range nodestack.ReadDirectives(e.Paths.ComposeDir()).Files {
		if filepath.Base(file) == "compose.trade.yaml" {
			tradeEnabled = true
		}
	}
	if !tradeEnabled {
		trade = ""
	} else if replicaStack && trade == "" {
		return fmt.Errorf("set the replica-local domains.trade or base domain before applying the Trade gateway route")
	}
	if !replicaStack && cfg.Domains.Trade == "" {
		if b, err := os.ReadFile(e.Paths.EnvCompose()); err == nil {
			if override := nodestack.ParseEnv(b)["TRADE_DOMAIN"]; override != "" {
				trade = override
			}
		}
	}
	if replicaStack && !domains.Complete() {
		return fmt.Errorf("set a replica-local base domain or all service domains with `qabas config set` before applying the gateway")
	}
	conf, err := gatewayConfig(domains, trade)
	if err != nil {
		return err
	}
	if e.flags.DryRun {
		e.Out.Println("would render and start QabasOne gateway")
		return nil
	}
	if err := os.MkdirAll(gatewayDir(e), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gatewayDir(e), "nginx.conf"), []byte(conf), 0o644); err != nil {
		return err
	}
	if err := ensureGatewayOverlay(e); err != nil {
		return err
	}
	if replicaStack {
		if err := secrets.ApplyReplicaDomains(e.Paths, domains, trade); err != nil {
			return err
		}
	}
	if err := nodestack.AddDirective(e.Paths.ComposeDir(), "env-file:"+filepath.Join(e.Paths.ConfigDir, "gateway.env")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(e.Paths.ConfigDir, "gateway.env"), []byte("QABAS_GATEWAY_DIR="+filepath.ToSlash(gatewayDir(e))+"\n"), 0o644); err != nil {
		return err
	}
	rt := newRuntime()
	serviceEnv := scaleEnvironment(e, st.CurrentRelease, scaleCount(e))
	if err := rt.EnsureEdgeNetwork(serviceEnv); err != nil {
		return err
	}
	if replicaStack {
		if err := rt.UpServices(e.Paths, serviceEnv, []string{"api", "app", "dash", "siraj", "minio", "gateway"}, false, true); err != nil {
			return err
		}
	} else if err := rt.UpService(e.Paths, serviceEnv, "gateway", false, true); err != nil {
		return err
	}
	id, err := gatewayContainerID(e, serviceEnv)
	if err != nil {
		return err
	}
	out, err := exec.Command("docker", "exec", id, "nginx", "-t", "-c", "/etc/qabas-gateway/nginx.conf").CombinedOutput()
	if err != nil {
		return fmt.Errorf("gateway config: %w: %s", err, out)
	}
	_, err = exec.Command("docker", "exec", id, "nginx", "-c", "/etc/qabas-gateway/nginx.conf", "-s", "reload").CombinedOutput()
	return err
}

func newGatewayCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{Use: "gateway", Short: "Manage QabasOne's internal HTTP gateway", RunE: parentHelp()}
	cmd.AddCommand(&cobra.Command{
		Use:   "apply",
		Short: "Render routes and start the gateway",
		Long: `Render routes and start the local gateway. On a replica, configure its
domains locally first; this does not change the writer:

  qabas config set domain dr.example.com
  qabas gateway apply

DNS and external TLS routing to this host must be configured separately.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			e := load(f)
			return e.withLock(func() error { return applyGateway(e) })
		},
	})
	return cmd
}
