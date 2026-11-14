package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

func TestScaleCountReadsSavedComposeEnv(t *testing.T) {
	dir := t.TempDir()
	e := &env{Paths: paths.Paths{ConfigDir: dir}}
	if got := scaleCount(e); got != 2 {
		t.Fatalf("default count = %d", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "api-replicas.env"), []byte("QABAS_API_REPLICAS=4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := scaleCount(e); got != 4 {
		t.Fatalf("saved count = %d", got)
	}
}

func TestAutoscaleTimerRunsEveryThirtySeconds(t *testing.T) {
	svc, timer := autoscaleUnits()
	if !strings.Contains(svc, "autoscale run") || !strings.Contains(timer, "OnUnitActiveSec=30s") {
		t.Fatalf("unexpected units: %q %q", svc, timer)
	}
}
