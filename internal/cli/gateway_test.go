package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestGatewayConfigRoutesDomainsAndScaledAPI(t *testing.T) {
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	got, err := gatewayConfig(cfg.EffectiveDomains())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"server api:3030 resolve max_fails=0", "server app:8080 resolve", "server dash:8080 resolve", "server siraj:8080 resolve", "server minio:9000 resolve", "server minio:9001 resolve", "hash $http_x_forwarded_for;", "server_name api.qabas.one", "server_name app.qabas.one", "server_name dash.qabas.one", "server_name siraj.qabas.one", "server_name s3.qabas.one", "server_name s3c.qabas.one", "proxy_pass http://qabas_app", "proxy_pass http://qabas_minio_console", "proxy_set_header Upgrade $http_upgrade", "location = /__gateway/health"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(got, "trade") {
		t.Fatal("gateway must not render a trade upstream or route")
	}
	for _, want := range []string{"log_format audit escape=json", "access_log /dev/stdout audit", "\"client_ip\"", "\"peer_ip\"", "$upstream_response_time", "$request_id", "\"path\":\"$uri\""} {
		if !strings.Contains(got, want) {
			t.Errorf("missing audit log field %q", want)
		}
	}
}

func TestGatewayCORPOverrideIsScopedToS3Route(t *testing.T) {
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	got, err := gatewayConfig(cfg.EffectiveDomains())
	if err != nil {
		t.Fatal(err)
	}
	servers := strings.Split(got, "  server { listen 8080; server_name ")
	var s3, api, console string
	for _, server := range servers {
		switch {
		case strings.HasPrefix(server, "s3.qabas.one\n"):
			s3 = server
		case strings.HasPrefix(server, "api.qabas.one\n"):
			api = server
		case strings.HasPrefix(server, "s3c.qabas.one;\n"):
			console = server
		}
	}
	for name, server := range map[string]string{"S3": s3, "API": api, "console": console} {
		if server == "" {
			t.Fatalf("generated %s server block not found", name)
		}
	}
	for _, directive := range []string{
		"proxy_hide_header Cross-Origin-Resource-Policy;",
		"add_header Cross-Origin-Resource-Policy cross-origin always;",
	} {
		if !strings.Contains(s3, directive) {
			t.Errorf("S3 route missing %q", directive)
		}
		if strings.Contains(api, directive) || strings.Contains(console, directive) {
			t.Errorf("%q must be limited to the S3 object route", directive)
		}
	}
}

func TestGatewaySeparatesRESTFromSocketAffinity(t *testing.T) {
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	got, err := gatewayConfig(cfg.EffectiveDomains())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"upstream qabas_api { zone qabas_api 64k; least_conn;",
		"upstream qabas_api_socket { zone qabas_api_socket 64k; hash $http_x_forwarded_for;",
		"location / {\n      proxy_pass http://qabas_api;",
		"location = /socket.io {\n      proxy_pass http://qabas_api_socket;",
		"location ^~ /socket.io/ {\n      proxy_pass http://qabas_api_socket;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing routing boundary %q", want)
		}
	}
	if strings.Contains(got, "upstream qabas_api { zone qabas_api 64k; hash") {
		t.Fatal("REST traffic must not use client-IP affinity")
	}
	if strings.Contains(got, "hash $http_x_forwarded_for consistent") {
		t.Fatal("consistent hash must not group all Docker-DNS addresses as one logical peer")
	}
	if strings.Count(got, "proxy_pass http://qabas_api_socket;") != 2 {
		t.Fatal("socket affinity must be limited to the exact and trailing-slash Socket.IO locations")
	}
}

func TestGatewayConfigRejectsDuplicateDomain(t *testing.T) {
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	d := cfg.EffectiveDomains()
	d.App = d.API
	if _, err := gatewayConfig(d); err == nil {
		t.Fatal("duplicate hostname accepted")
	}
}

func TestReplicaGatewayStackIsDetectedWithoutReleaseState(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir()}
	roleFile := filepath.Join(p.ConfigDir, "dr", "role")
	if err := os.MkdirAll(filepath.Dir(roleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(roleFile, []byte(nodestack.ProfileReplicaRO+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	role, err := localNodeRole(&env{Paths: p})
	if err != nil {
		t.Fatal(err)
	}
	if role != nodestack.ProfileReplicaRO {
		t.Fatalf("configured replica role = %q", role)
	}
}
