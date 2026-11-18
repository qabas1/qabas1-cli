package secrets

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestComposeTemplatesDiscoverNewServiceEnv(t *testing.T) {
	raw := []byte(`services:
  api:
    image: ${QABASONE_API_IMAGE}
    env_file:
      - ${QABAS_ENV_DIR:-.}/.env.api
  reports:
    image: ${QABASONE_API_IMAGE}
    env_file:
      - ${QABAS_ENV_DIR:-.}/.env.reports
`)
	names, err := ComposeTemplates(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env.api.example", ".env.compose.example", ".env.reports.example"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("templates = %v, want %v", names, want)
	}
	p := paths.Paths{ConfigDir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(p.ConfigDir, ".env.api"), []byte("EXISTING=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing, err := RequiredComposeTemplates(p, names)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".env.compose.example", ".env.reports.example"}; !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}
