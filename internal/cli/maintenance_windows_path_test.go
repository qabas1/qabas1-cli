package cli

import (
	"errors"
	"testing"
)

func TestHostMountSource(t *testing.T) {
	tests := []struct {
		goos, in, want string
	}{
		{"windows", "/run/desktop/mnt/host/c/srv/edge/nginx/html", `C:\srv\edge\nginx\html`},
		{"windows", "/host_mnt/d/edge/includes", `D:\edge\includes`},
		{"windows", `C:\edge\conf.d`, `C:\edge\conf.d`},
		{"windows", "/var/lib/docker/volumes/x/_data", "/var/lib/docker/volumes/x/_data"},
		{"windows", "/run/desktop/mnt/host/wsl/x", "/run/desktop/mnt/host/wsl/x"},
		{"linux", "/run/desktop/mnt/host/c/x", "/run/desktop/mnt/host/c/x"},
	}
	for _, tt := range tests {
		if got := hostMountSource(tt.goos, tt.in); got != tt.want {
			t.Errorf("hostMountSource(%s, %q) = %q, want %q", tt.goos, tt.in, got, tt.want)
		}
	}
}

func TestResolveEdgeLayoutNotFoundIsDetectable(t *testing.T) {
	t.Setenv("QABAS_EDGE_NGINX_DIR", "")
	oldLook := edgeDockerLookPath
	edgeDockerLookPath = func(string) (string, error) { return "", errors.New("no docker") }
	t.Cleanup(func() { edgeDockerLookPath = oldLook })

	_, err := resolveEdgeLayout()
	if err == nil {
		t.Skip("default edge nginx dir exists on this host")
	}
	if !errors.Is(err, errEdgeLayoutNotFound) {
		t.Fatalf("expected errEdgeLayoutNotFound, got %v", err)
	}
}
