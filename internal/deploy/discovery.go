package deploy

import (
	"fmt"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/manifest"
)

// changedComposeServices resolves pull targets against the staged Compose
// document, including newly added services that share an existing image pin.
func changedComposeServices(raw []byte, current, target *manifest.Release) ([]string, error) {
	before, after := compose.ImagePins(current), compose.ImagePins(target)
	keys := []string{
		"QABASONE_API_IMAGE", "QABASONE_APP_IMAGE", "QABASONE_DASH_IMAGE", "QABASONE_SIRAJ_IMAGE",
		"POSTGRES_IMAGE", "REDIS_IMAGE", "MINIO_IMAGE", "MINIO_MC_IMAGE", "GOTENBERG_IMAGE", "JSREPORT_IMAGE",
	}
	seen := map[string]bool{}
	var out []string
	for _, key := range keys {
		if before[key] == after[key] {
			continue
		}
		services, err := compose.ServicesForImage(raw, key)
		if err != nil {
			if len(key) >= 9 && key[:9] == "QABASONE_" {
				return nil, fmt.Errorf("resolve %s: %w", key, err)
			}
			continue // Optional infrastructure may be absent from this Compose bundle.
		}
		for _, service := range services {
			if !seen[service] {
				seen[service] = true
				out = append(out, service)
			}
		}
	}
	return out, nil
}
