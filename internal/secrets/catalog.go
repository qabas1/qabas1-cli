package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

var envIdentifier = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
var secretName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

type catalogFile struct {
	SchemaVersion int      `yaml:"schemaVersion"`
	Secrets       []Secret `yaml:"secrets"`
}

func CatalogFile(p paths.Paths) string { return filepath.Join(p.ConfigDir, "secrets.catalog.yaml") }

func ParseCatalog(raw []byte) ([]Secret, error) {
	var file catalogFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse secret catalog: %w", err)
	}
	if file.SchemaVersion != 1 || len(file.Secrets) == 0 {
		return nil, fmt.Errorf("unsupported or empty secret catalog")
	}
	names, keys := map[string]bool{}, map[string]bool{}
	for i := range file.Secrets {
		s := &file.Secrets[i]
		if !secretName.MatchString(s.Name) || !envIdentifier.MatchString(s.Key) || s.Bytes < 16 || s.Bytes > 64 || s.Desc == "" || s.Impact == "" {
			return nil, fmt.Errorf("invalid secret definition %q", s.Name)
		}
		if names[s.Name] || keys[s.Key] {
			return nil, fmt.Errorf("duplicate secret name or key %q", s.Name)
		}
		names[s.Name], keys[s.Key] = true, true
		for _, key := range s.EnvKeys {
			if !envIdentifier.MatchString(key) {
				return nil, fmt.Errorf("invalid env key %q", key)
			}
		}
		for _, placeholder := range s.Placeholders {
			if !envIdentifier.MatchString(placeholder) {
				return nil, fmt.Errorf("invalid placeholder %q", placeholder)
			}
		}
		if len(s.Services) == 0 || len(s.Files) == 0 {
			return nil, fmt.Errorf("secret %s requires services and files", s.Name)
		}
		if s.Delivery != "" && s.Delivery != "file" {
			return nil, fmt.Errorf("invalid delivery for %s", s.Name)
		}
		if s.Delivery == "file" && !serviceName.MatchString(s.Mount) {
			return nil, fmt.Errorf("file-delivered secret %s requires a mount name", s.Name)
		}
		for _, service := range s.Services {
			if !serviceName.MatchString(service) {
				return nil, fmt.Errorf("invalid service %q for %s", service, s.Name)
			}
		}
		for _, file := range s.Files {
			if file != ".env" && (!strings.HasPrefix(file, ".env.") || strings.ContainsAny(file, "/\\$")) {
				return nil, fmt.Errorf("invalid env file %q for %s", file, s.Name)
			}
			if s.Delivery == "file" && file == ".env" {
				return nil, fmt.Errorf("file-delivered secret %s cannot use Compose interpolation .env", s.Name)
			}
		}
	}
	for _, core := range Catalog {
		if !keys[core.Key] {
			return nil, fmt.Errorf("secret catalog is missing required key %s", core.Key)
		}
	}
	return file.Secrets, nil
}

func secretUsesFile(s Secret, path string) bool {
	if len(s.Files) == 0 {
		return true
	} // legacy catalog
	name := filepath.Base(path)
	for _, file := range s.Files {
		if file == name {
			return true
		}
	}
	return false
}

func CatalogFor(p paths.Paths) ([]Secret, error) {
	raw, err := os.ReadFile(CatalogFile(p))
	if os.IsNotExist(err) {
		return Catalog, nil
	} // pre-catalog installations
	if err != nil {
		return nil, err
	}
	return ParseCatalog(raw)
}

func InstallCatalog(p paths.Paths, raw []byte) error {
	if _, err := ParseCatalog(raw); err != nil {
		return err
	}
	return writeAtomic(CatalogFile(p), raw)
}

func lookupIn(catalog []Secret, name string) (Secret, bool) {
	for _, s := range catalog {
		if s.Name == name {
			return s, true
		}
	}
	return Secret{}, false
}

func secretBytes(s Secret) int {
	if s.Bytes >= 16 {
		return s.Bytes
	}
	return valueBytes(s.Key) // legacy fallback definitions
}

func GenerateForCatalog(catalog []Secret) (Bundle, error) {
	b := Bundle{MinioUser: "qabasone"}
	for _, s := range catalog {
		value, err := randomHex(secretBytes(s))
		if err != nil {
			return Bundle{}, err
		}
		b = b.With(s.Key, value)
	}
	return b, nil
}

// existingCatalogValue adopts an operator-provided value when a newly declared
// secret is already present in a service env file.
func existingCatalogValue(p paths.Paths, s Secret) (string, error) {
	keys := map[string]bool{s.Key: true}
	for _, key := range s.EnvKeys {
		keys[key] = true
	}
	var found string
	for _, path := range EnvFiles(p) {
		if path == p.SecretsFile() {
			continue
		}
		if !secretUsesFile(s, path) {
			continue
		}
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if !ok || !keys[key] || value == "" || strings.HasPrefix(value, "REPLACE_") {
				continue
			}
			if found != "" && found != value {
				return "", fmt.Errorf("conflicting installed values for %s", s.Name)
			}
			found = value
		}
	}
	return found, nil
}
