package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

// RenderCatalogTemplate applies the release's declared secret bindings.
// Legacy shared JWT/2FA placeholders are distinguished by assignment key.
func RenderCatalogTemplate(raw string, b Bundle, catalog []Secret, domains config.Domains, configDir string) string {
	return RenderCatalogTemplateFile(raw, b, catalog, domains, configDir, "")
}

func RenderCatalogTemplateFile(raw string, b Bundle, catalog []Secret, domains config.Domains, configDir, file string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		key, _, assignment := strings.Cut(line, "=")
		if assignment && key == "TWO_FACTOR_SECRET_ENCRYPTION_KEY" && (file == "" || catalogKeyUsesFile(catalog, key, file)) {
			line = strings.ReplaceAll(line, "REPLACE_WITH_OPENSSL_RAND_HEX_32", b.TwoFactorKey)
		}
		lines = append(lines, line)
	}
	text := strings.Join(lines, "\n")
	for _, s := range catalog {
		if file != "" && !secretUsesFile(s, file) {
			continue
		}
		value := b.Value(s.Key)
		if value == "" {
			continue
		}
		text = strings.ReplaceAll(text, "REPLACE_"+s.Key, value)
		for _, placeholder := range s.Placeholders {
			text = strings.ReplaceAll(text, placeholder, value)
		}
	}
	text = strings.ReplaceAll(text, "REPLACE_MINIO_ROOT_USER", b.MinioUser)
	text = strings.ReplaceAll(text, "REPLACE_MINIO_ACCESS_KEY", b.MinioUser)
	text = applyDomainHosts(text, domains)
	if configDir != "" {
		text = strings.ReplaceAll(text, "QABAS_ENV_DIR=/etc/qabasone", "QABAS_ENV_DIR="+filepath.ToSlash(configDir))
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

func catalogKeyUsesFile(catalog []Secret, key, file string) bool {
	for _, s := range catalog {
		if s.Key == key {
			return secretUsesFile(s, file)
		}
	}
	return false
}

// ComposeTemplates discovers service env files from the release Compose file.
// The Compose interpolation env is always backed by .env.compose.example.
func ComposeTemplates(raw []byte) ([]string, error) {
	var doc struct {
		Services map[string]struct {
			EnvFile yaml.Node `yaml:"env_file"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse compose env files: %w", err)
	}
	if len(doc.Services) == 0 {
		return nil, fmt.Errorf("compose has no services")
	}
	seen := map[string]bool{".env.compose.example": true}
	for _, service := range doc.Services {
		var entries []string
		switch service.EnvFile.Kind {
		case 0:
		case yaml.ScalarNode:
			entries = append(entries, service.EnvFile.Value)
		case yaml.SequenceNode:
			for _, item := range service.EnvFile.Content {
				if item.Kind != yaml.ScalarNode {
					return nil, fmt.Errorf("unsupported compose env_file entry")
				}
				entries = append(entries, item.Value)
			}
		default:
			return nil, fmt.Errorf("unsupported compose env_file format")
		}
		for _, entry := range entries {
			name := filepath.Base(entry)
			if !strings.HasPrefix(name, ".env.") || name == ".env." || strings.ContainsAny(name, "{}$\\/") {
				return nil, fmt.Errorf("unsupported compose env_file %q", entry)
			}
			seen[name+".example"] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// ValidateCatalogCompose catches stale service names and env file bindings
// before a release modifies credentials or containers.
func ValidateCatalogCompose(raw []byte, catalog []Secret) error {
	var doc struct {
		Services map[string]struct {
			EnvFile yaml.Node `yaml:"env_file"`
			Secrets []string  `yaml:"secrets"`
		} `yaml:"services"`
		Secrets map[string]struct {
			File string `yaml:"file"`
		} `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	for _, secret := range catalog {
		if secret.Delivery == "file" {
			mount, ok := doc.Secrets[secret.Mount]
			if !ok || filepath.Base(mount.File) != secret.Key || !strings.Contains(mount.File, "/secret-files/") {
				return fmt.Errorf("secret %s has no matching Compose file mount", secret.Name)
			}
			for _, service := range secret.Services {
				mounted := false
				for _, name := range doc.Services[service].Secrets {
					if name == secret.Mount {
						mounted = true
						break
					}
				}
				if !mounted {
					return fmt.Errorf("service %s does not mount secret %s", service, secret.Name)
				}
			}
		}
		for _, service := range secret.Services {
			if _, ok := doc.Services[service]; !ok {
				return fmt.Errorf("secret %s refers to missing compose service %s", secret.Name, service)
			}
		}
		for _, file := range secret.Files {
			if file == ".env" {
				continue
			}
			found := false
			for serviceName, spec := range doc.Services {
				env := spec.EnvFile
				uses := env.Kind == yaml.ScalarNode && filepath.Base(env.Value) == file
				for _, item := range env.Content {
					if item.Kind == yaml.ScalarNode && filepath.Base(item.Value) == file {
						uses = true
					}
				}
				if !uses {
					continue
				}
				declared := false
				for _, owner := range secret.Services {
					if owner == serviceName {
						declared = true
						break
					}
				}
				if !declared {
					return fmt.Errorf("secret %s in %s also reaches undeclared service %s", secret.Name, file, serviceName)
				}
				found = true
			}
			if !found {
				return fmt.Errorf("secret %s file %s is not used by its declared services", secret.Name, file)
			}
		}
	}
	return nil
}

func templateDestination(p paths.Paths, name string) (string, error) {
	if !strings.HasPrefix(name, ".env.") || !strings.HasSuffix(name, ".example") || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid env template %q", name)
	}
	base := strings.TrimSuffix(name, ".example")
	if base == ".env.compose" {
		base = ".env"
	}
	return filepath.Join(p.ConfigDir, base), nil
}

func RequiredComposeTemplates(p paths.Paths, names []string) ([]string, error) {
	var missing []string
	for _, name := range names {
		dest, err := templateDestination(p, name)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			missing = append(missing, name)
		} else if err != nil {
			return nil, err
		}
	}
	return missing, nil
}
