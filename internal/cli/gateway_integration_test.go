package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/config"
)

// CI runs this against real NGINX and Docker DNS; ordinary unit tests stay offline.
func TestGatewaySameClientRESTAndPollingIntegration(t *testing.T) {
	if os.Getenv("QABAS_GATEWAY_INTEGRATION") != "1" {
		t.Skip("set QABAS_GATEWAY_INTEGRATION=1 with Docker available")
	}
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	network := fmt.Sprintf("qabas-gateway-test-%d", time.Now().UnixNano())
	docker("network", "create", network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	image := "nginxinc/nginx-unprivileged:1.28-alpine"
	docker("pull", image)
	dir := t.TempDir()
	backend := filepath.Join(dir, "backend.conf")
	if err := os.WriteFile(backend, []byte("pid /tmp/nginx.pid; error_log /dev/stderr; events {} http { server { listen 3030; location / { return 200 '$hostname'; } } }"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := func(name, path string, publish bool) string {
		t.Helper()
		args := []string{"run", "-d", "--network", network, "--hostname", name, "--entrypoint", "nginx", "-v", path + ":/etc/nginx/test.conf:ro"}
		if publish {
			args = append(args, "-p", "127.0.0.1::8080")
		} else {
			args = append(args, "--network-alias", "api")
		}
		args = append(args, image, "-c", "/etc/nginx/test.conf", "-g", "daemon off;")
		id := docker(args...)
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
		return id
	}
	start("backend-a", backend, false)
	start("backend-b", backend, false)
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	rendered, err := gatewayConfig(cfg.EffectiveDomains())
	if err != nil {
		t.Fatal(err)
	}
	gatewayPath := filepath.Join(dir, "gateway.conf")
	if err := os.WriteFile(gatewayPath, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	id := start("gateway", gatewayPath, true)
	docker("exec", id, "nginx", "-t", "-c", "/etc/nginx/test.conf")
	address := "http://" + docker("port", id, "8080/tcp")
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	request := func(path string) (string, error) {
		req, err := http.NewRequest(http.MethodGet, address+path, nil)
		if err != nil {
			return "", err
		}
		req.Host = "api.qabas.one"
		req.Header.Set("X-Forwarded-For", "198.51.100.10")
		res, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 1024))
		if err != nil || res.StatusCode != http.StatusOK {
			return "", fmt.Errorf("gateway returned status %d: %v", res.StatusCode, err)
		}
		return string(body), nil
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := request("/ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test gateway did not become ready")
		}
		time.Sleep(200 * time.Millisecond)
	}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		body, err := request("/api/v1/test")
		if err != nil {
			t.Fatal(err)
		}
		seen[body] = true
	}
	if !seen["backend-a"] || !seen["backend-b"] || len(seen) != 2 {
		t.Fatalf("same-client REST requests did not reach both replicas: %v", seen)
	}
	var pinned string
	for i := 0; i < 12; i++ {
		if i == 6 {
			// Cross the resolver's 10-second refresh window with the peer set unchanged.
			time.Sleep(12 * time.Second)
		}
		path := "/socket.io/?EIO=4&transport=polling"
		if i%2 == 0 {
			path = "/socket.io?EIO=4&transport=polling"
		}
		body, err := request(path)
		if err != nil {
			t.Fatal(err)
		}
		if pinned != "" && body != pinned {
			t.Fatal("polling requests from the same client changed backend")
		}
		pinned = body
	}
}
