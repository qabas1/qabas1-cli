package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/manifest"
	"github.com/qabas1/qabasone-cli/internal/nodestack"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

func ServiceAlias(name string) (string, error) {
	switch strings.ToLower(name) {
	case "", "all":
		return "", nil
	case "api":
		return "api", nil
	case "app":
		return "app", nil
	case "dash", "dashboard":
		return "dash", nil
	case "siraj":
		return "siraj", nil
	case "postgres", "db", "postgresql":
		return "postgres", nil
	case "redis":
		return "redis", nil
	case "minio", "s3":
		return "minio", nil
	case "gotenberg":
		return "gotenberg", nil
	case "jsreport":
		return "jsreport", nil
	case "printables", "rworker":
		return "printables-worker", nil
	case "documents", "documents-worker":
		return "documents-worker", nil
	case "operations", "operations-worker", "ops", "posting", "post", "posting-worker":
		// posting / posting-worker remain as legacy aliases after the rename.
		return "operations-worker", nil
	case "notifications", "notify":
		return "notifications-worker", nil
	case "audit", "audit-cleanup", "cleanup":
		return "audit-cleanup", nil
	default:
		return name, nil
	}
}

// ProductService reports whether name is a deployable product component
// (api, app, dash, siraj), normalizing aliases such as "dashboard" → "dash".
func ProductService(name string) (string, bool) {
	alias, err := ServiceAlias(name)
	if err != nil || alias == "" {
		return "", false
	}
	switch alias {
	case "api", "app", "dash", "siraj":
		return alias, true
	default:
		return "", false
	}
}

// APIImageServices are compose services that share QABASONE_API_IMAGE.
// Used when scoped-deploying api so workers pick up the new digest.
func APIImageServices() []string {
	return []string{
		"api",
		"printables-worker",
		"documents-worker",
		"operations-worker",
		"notifications-worker",
		"audit-cleanup",
	}
}

// DeployComposeServices returns the compose service names to pull/up for a
// scoped product-component deploy.
func DeployComposeServices(product string) []string {
	switch product {
	case "api":
		return APIImageServices()
	case "app", "dash", "siraj":
		return []string{product}
	default:
		return nil
	}
}

func DigestPins(rel *manifest.Release) []string {
	return []string{
		rel.Components.API.DigestRef(),
		rel.Components.App.DigestRef(),
		rel.Components.Dash.DigestRef(),
		rel.Components.Siraj.DigestRef(),
	}
}

func WriteBundle(p paths.Paths, composeYAML []byte) error {
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p.ComposeFile(), composeYAML, 0o644)
}

// WriteBundleNext writes compose YAML to the staging next path without replacing live compose.
func WriteBundleNext(p paths.Paths, composeYAML []byte) error {
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p.ComposeFileNext(), composeYAML, 0o644)
}

// RefreshOverlays re-fetches the DR overlays this node applies (compose dir "overlays"
// file) so they stay on the same release as compose.yaml. A fetch failure keeps the
// existing overlay; a missing overlay that cannot be fetched is an error.
func RefreshOverlays(p paths.Paths, fetch func(name string) ([]byte, error)) error {
	for _, f := range nodestack.ReadDirectives(p.ComposeDir()).Files {
		name := filepath.Base(f)
		if managedOverlays[name] {
			continue
		}
		raw, err := fetch(name)
		if err != nil {
			if _, statErr := os.Stat(f); statErr == nil {
				continue
			}
			return fmt.Errorf("fetch overlay %s: %w", name, err)
		}
		if err := os.WriteFile(f, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// PromoteBundle moves the staged compose.yaml.next into the live compose.yaml path.
func PromoteBundle(p paths.Paths) error {
	next := p.ComposeFileNext()
	if _, err := os.Stat(next); err != nil {
		return fmt.Errorf("staged compose missing: %w", err)
	}
	live := p.ComposeFile()
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		return err
	}
	return os.Rename(next, live)
}

func SaveReleaseCopy(p paths.Paths, rel *manifest.Release) error {
	if err := os.MkdirAll(p.Releases(), 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s.yaml", rel.Release.Version)
	raw := rel.Raw
	if len(raw) == 0 {
		var err error
		raw, err = yaml.Marshal(rel)
		if err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(p.Releases(), name), raw, 0o644)
}
