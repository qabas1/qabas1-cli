package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/nodestack"
)

func setupServiceControlNode(t *testing.T, role nodestack.Profile) {
	t.Helper()
	configDir := t.TempDir()
	stateDir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", configDir)
	t.Setenv("QABAS_STATE_DIR", stateDir)

	roleFile := filepath.Join(configDir, "dr", "role")
	if err := os.MkdirAll(filepath.Dir(roleFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(roleFile, []byte(role+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	composeDir := filepath.Join(stateDir, "compose")
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(composeDir, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunServiceControlAllowsReplicaWithoutInstalledRelease(t *testing.T) {
	setupServiceControlNode(t, nodestack.ProfileReplicaRO)

	err := runServiceControl(&flags{DryRun: true}, serviceControl{
		action:  "up",
		service: "api",
		detach:  true,
	})
	if err != nil {
		t.Fatalf("replica service control failed without installed-release state: %v", err)
	}
}

func TestValidateReplicaServiceControlRejectsStartingWorkers(t *testing.T) {
	err := validateReplicaServiceControl(nodestack.ProfileReplicaRO, serviceControl{
		action:  "up",
		service: "operations-worker",
	})
	if err == nil || !strings.Contains(err.Error(), "must run only on the writer") {
		t.Fatalf("expected read-only replica worker guard, got %v", err)
	}
}
