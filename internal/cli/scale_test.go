package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestScalingRejectsLegacySessionUnsafeOverlay(t *testing.T) {
	for _, data := range []string{
		"services:\n  pgbouncer:\n    environment:\n      POOL_MODE: transaction\n  api:\n    environment:\n      PG_POOL_MAX: '10'\n",
		"services:\n  pgbouncer:\n    environment:\n      POOL_MODE: session\n",
		"not: [valid",
	} {
		if err := validateScaleOverlay([]byte(data)); err == nil {
			t.Fatal("unsafe or incomplete scale overlay accepted")
		}
	}
	safe := "services:\n  pgbouncer:\n    environment:\n      POOL_MODE: session\n  api:\n    container_name: !reset null\n    environment:\n      PG_POOL_MAX: ${QABAS_API_DB_CONNECTIONS:-10}\n"
	if err := validateScaleOverlay([]byte(safe)); err != nil {
		t.Fatal(err)
	}
}

func TestScalingRejectsLegacyRouting(t *testing.T) {
	old := "resolver 127.0.0.11; upstream qabas_api { hash $http_x_forwarded_for consistent; server api:3030 resolve; }"
	if err := validateAPIRouting(old); err == nil {
		t.Fatal("legacy all-REST client affinity accepted")
	}
	cfg := config.Default()
	cfg.Domain = "qabas.one"
	current, err := gatewayConfig(cfg.EffectiveDomains(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAPIRouting(current); err != nil {
		t.Fatal(err)
	}
}

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
