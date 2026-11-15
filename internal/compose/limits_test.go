package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/nodestack"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

func TestPlanLimitsBudgetsAndUpstream(t *testing.T) {
	t.Setenv(EnvLimitPrefix+"REDIS", "")
	defs := map[string]map[string]any{
		"postgres":   {"image": "pg"},
		"api":        {"image": "a", "mem_limit": "1g"},
		"worker-pdf": {"image": "w"},
		"gotenberg":  {"image": "g", "security_opt": []any{"seccomp:unconfined"}},
		"redis":      {"image": "r", "cap_drop": []any{"ALL"}},
		"migrate":    {"image": "m"},
		"mystery":    {"image": "x"},
	}
	got := map[string]ServiceLimits{}
	for _, l := range PlanLimits(defs, 8<<30) {
		got[l.Service] = l
	}
	if _, ok := got["migrate"]; ok {
		t.Fatal("one-off migrate service must be skipped")
	}
	if got["postgres"].MemoryBytes != 2048*mib || got["postgres"].PidsLimit != PostgresPidsLimit {
		t.Fatalf("postgres = %+v", got["postgres"])
	}
	if got["api"].MemoryBytes != 0 || !contains(got["api"].Upstream, "memory") {
		t.Fatalf("api upstream mem_limit overridden: %+v", got["api"])
	}
	if got["worker-pdf"].MemoryBytes != 655*mib {
		t.Fatalf("worker = %d MiB", got["worker-pdf"].MemoryBytes/mib)
	}
	if got["gotenberg"].NoNewPrivs || got["gotenberg"].MemoryBytes != 655*mib {
		t.Fatalf("gotenberg = %+v", got["gotenberg"])
	}
	if len(got["redis"].CapDrop) != 0 {
		t.Fatal("redis cap_drop set upstream must be left alone")
	}
	if got["mystery"].MemoryBytes != 0 || got["mystery"].PidsLimit != DefaultPidsLimit || !got["mystery"].NoNewPrivs {
		t.Fatalf("unknown service = %+v", got["mystery"])
	}
}

func TestPlanLimitsFloorsCeilingsAndOverrides(t *testing.T) {
	defs := map[string]map[string]any{"postgres": {}, "app": {}, "redis": {}}
	small := PlanLimits(defs, 1<<30)
	if small[0].Service != "app" || small[0].MemoryBytes != 128*mib {
		t.Fatalf("floor: %+v", small[0])
	}
	big := PlanLimits(defs, 512<<30)
	for _, l := range big {
		if l.Service == "postgres" && l.MemoryBytes != 16384*mib {
			t.Fatalf("ceiling: %+v", l)
		}
	}
	t.Setenv(EnvLimitPrefix+"POSTGRES", "6g")
	t.Setenv(EnvLimitPrefix+"REDIS", "none")
	for _, l := range PlanLimits(defs, 8<<30) {
		switch l.Service {
		case "postgres":
			if l.MemoryBytes != 6<<30 || l.MemorySrc != "QABAS_LIMIT_POSTGRES" {
				t.Fatalf("override: %+v", l)
			}
		case "redis":
			if l.MemoryBytes != 0 {
				t.Fatalf("none override: %+v", l)
			}
		}
	}
}

func TestSyncLimitsOverlayWritesValidYAML(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	dir := p.ComposeDir()
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services:\n  api:\n    image: a\n  postgres:\n    image: p\n  old:\n    image: o\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "compose.yaml.next"), []byte("services:\n  api:\n    image: a\n  postgres:\n    image: p\n"), 0o644)
	changed, limits, err := SyncLimitsOverlay(p, 4<<30, p.ComposeFile(), p.ComposeFileNext())
	if err != nil || !changed || len(limits) != 2 {
		t.Fatalf("changed=%v limits=%+v err=%v", changed, limits, err)
	}
	raw, _ := os.ReadFile(LimitsOverlayPath(p))
	var doc struct {
		Services map[string]struct {
			MemLimit    string   `yaml:"mem_limit"`
			PidsLimit   int      `yaml:"pids_limit"`
			SecurityOpt []string `yaml:"security_opt"`
			CapDrop     []string `yaml:"cap_drop"`
			Ulimits     struct {
				Nofile struct{ Soft, Hard int } `yaml:"nofile"`
			} `yaml:"ulimits"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid yaml: %v\n%s", err, raw)
	}
	pg := doc.Services["postgres"]
	if pg.MemLimit != "1024m" || pg.PidsLimit != 1024 || pg.SecurityOpt[0] != "no-new-privileges:true" || pg.Ulimits.Nofile.Hard != NofileLimit {
		t.Fatalf("postgres overlay = %+v", pg)
	}
	if _, ok := doc.Services["old"]; ok {
		t.Fatal("service missing from staged bundle must not be referenced")
	}
	found := false
	for _, f := range nodestack.ReadDirectives(dir).Files {
		found = found || filepath.Base(f) == LimitsOverlay
	}
	if !found {
		t.Fatal("overlay not registered")
	}
	if changed, _, _ := SyncLimitsOverlay(p, 4<<30, p.ComposeFile(), p.ComposeFileNext()); changed {
		t.Fatal("second sync must be a no-op")
	}
	t.Setenv(EnvLimits, "off")
	if _, _, err := SyncLimitsOverlay(p, 4<<30, p.ComposeFile()); err != nil || LimitsOverlayEnabled(p) {
		t.Fatal("QABAS_LIMITS=off must remove the overlay")
	}
	if strings.Contains(string(raw), "migrate") {
		t.Fatal("unexpected migrate entry")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
