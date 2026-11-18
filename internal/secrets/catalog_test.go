package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

func TestDeployCatalogAddsAndRotatesNewSecret(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir()}
	defs := append([]Secret{}, Catalog...)
	for i := range defs {
		defs[i].Bytes = valueBytes(defs[i].Key)
		defs[i].Services = []string{"api"}
		defs[i].Files = []string{".env", ".env.api", ".env.worker", ".env.migrate"}
	}
	defs = append(defs, Secret{Name: "reports", Key: "REPORTS_SECRET", Bytes: 24,
		Desc: "Reports signing key", Impact: "reports restart", EnvKeys: []string{"REPORTS_KEY"},
		Services: []string{"reports"}, Files: []string{".env.reports"}, Delivery: "file", Mount: "reports_secret"})
	defs = append(defs, Secret{Name: "jsreport-admin", Key: "JSREPORT_ADMIN_PASSWORD", Bytes: 32,
		Desc: "jsreport login", Impact: "jsreport restart", Services: []string{"jsreport"}, Files: []string{".env"}})
	raw, err := yaml.Marshal(catalogFile{SchemaVersion: 1, Secrets: defs})
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallCatalog(p, raw); err != nil {
		t.Fatal(err)
	}
	base, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSecretsFile(p, base); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.EnvCompose(), []byte("JSREPORT_ADMIN_PASSWORD=REPLACE_JSREPORT_ADMIN_PASSWORD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Ensure(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Value("REPORTS_SECRET")) != 48 {
		t.Fatalf("new secret not generated")
	}
	rendered := RenderCatalogTemplate("REPORTS_KEY=REPLACE_REPORTS_SECRET\n", b, defs, config.Domains{}, p.ConfigDir)
	if strings.Contains(rendered, "REPLACE_") || !strings.Contains(rendered, b.Value("REPORTS_SECRET")) {
		t.Fatal("template was not resolved")
	}
	extra := filepath.Join(p.ConfigDir, ".env.reports")
	if err := os.WriteFile(extra, []byte("KEEP=operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fetch := func(name string) ([]byte, error) {
		if name == ".env.reports.example" {
			return []byte("REPORTS_KEY=REPLACE_REPORTS_SECRET\n"), nil
		}
		return []byte("JSREPORT_ADMIN_PASSWORD=REPLACE_JSREPORT_ADMIN_PASSWORD\n"), nil
	}
	if err := WriteComposeEnvFiles(p, b, config.Domains{}, []string{".env.compose.example", ".env.reports.example"}, fetch); err != nil {
		t.Fatal(err)
	}
	merged, err := os.ReadFile(extra)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "KEEP=operator") || !strings.Contains(string(merged), b.Value("REPORTS_SECRET")) {
		t.Fatal("existing env file did not preserve operator value and add new secret")
	}
	composeEnv, err := os.ReadFile(p.EnvCompose())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(composeEnv), b.Value("JSREPORT_ADMIN_PASSWORD")) {
		t.Fatal("service secret placeholder not resolved")
	}
	old := b.Value("REPORTS_SECRET")
	unrelated := filepath.Join(p.ConfigDir, ".env.api")
	if err := os.WriteFile(unrelated, []byte("REPORTS_KEY=operator-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	names, err := ResolveNamesFor(p, []string{"reports"}, false)
	if err != nil || len(names) != 1 {
		t.Fatalf("resolve: %v %v", names, err)
	}
	if _, err := Rotate(p, names, RotateDeps{Generate: counter()}); err != nil {
		t.Fatal(err)
	}
	newEnv, err := os.ReadFile(extra)
	if err != nil {
		t.Fatal(err)
	}
	newSecrets, err := os.ReadFile(p.SecretsFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(newEnv), old) || strings.Contains(string(newSecrets), "REPORTS_SECRET="+old) {
		t.Fatal("old secret remains")
	}
	newFile, err := os.ReadFile(SecretValueFile(p, "REPORTS_SECRET"))
	if err != nil || string(newFile) == old {
		t.Fatal("mounted secret file was not rotated")
	}
	untouched, err := os.ReadFile(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	if string(untouched) != "REPORTS_KEY=operator-owned\n" {
		t.Fatal("rotation changed another service's env file")
	}
}

func TestSiblingDeployCatalogWhenAvailable(t *testing.T) {
	path := filepath.Join("..", "..", "..", "qabas1-deploy", "compose", "secrets.yaml")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Skip("deploy repository is not checked out beside CLI")
	}
	if err != nil {
		t.Fatal(err)
	}
	defs, err := ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) < len(Catalog) {
		t.Fatal("deploy catalog omits baseline secrets")
	}
	b, err := GenerateForCatalog(defs)
	if err != nil {
		t.Fatal(err)
	}
	if b.Value("JSREPORT_ADMIN_PASSWORD") == "" || b.Value("JSREPORT_COOKIE_SECRET") == "" {
		t.Fatal("service-specific jsreport secrets were not generated")
	}
	composeRaw, err := os.ReadFile(filepath.Join("..", "..", "..", "qabas1-deploy", "compose", "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	names, err := ComposeTemplates(composeRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCatalogCompose(composeRaw, defs); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		template, err := os.ReadFile(filepath.Join("..", "..", "..", "qabas1-deploy", "compose", "templates", name))
		if err != nil {
			t.Fatal(err)
		}
		rendered := RenderCatalogTemplate(string(template), b, defs, config.Domains{}, "/etc/qabasone")
		for _, line := range strings.Split(rendered, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, "REPLACE_") && !strings.HasPrefix(line, "JSREPORT_ADMIN_PASSWORD=") && !strings.HasPrefix(line, "JSREPORT_COOKIE_SECRET=") {
				t.Fatalf("unresolved placeholder in %s: %s", name, line)
			}
		}
	}
}

func TestCatalogServiceMappingFailsClosed(t *testing.T) {
	compose := []byte("services:\n  api:\n    env_file: [.env.api]\n")
	defs := []Secret{{Name: "private", Key: "PRIVATE_KEY", Services: []string{"missing"}, Files: []string{".env.api"}}}
	if err := ValidateCatalogCompose(compose, defs); err == nil {
		t.Fatal("missing service accepted")
	}
	defs[0].Services = []string{"api"}
	defs[0].Files = []string{".env.worker"}
	if err := ValidateCatalogCompose(compose, defs); err == nil {
		t.Fatal("unattached env file accepted")
	}
	shared := []byte("services:\n  api:\n    env_file: [.env.worker]\n  worker:\n    env_file: [.env.worker]\n")
	if err := ValidateCatalogCompose(shared, defs); err == nil {
		t.Fatal("undeclared consumer of shared env file accepted")
	}
}

func TestMountedSecretFilesAndEnvCleanup(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(root, "etc"), StateDir: filepath.Join(root, "state")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ComposeDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	defs := []Secret{{Name: "jwt", Key: "JWT_SECRET", Delivery: "file", Mount: "jwt_secret", Services: []string{"api"}, Files: []string{".env.api"}}}
	b := Bundle{JWTSecret: "original-secret-value"}
	if err := MaterializeFiles(p, defs, b); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(SecretValueFile(p, "JWT_SECRET"))
	if err != nil || string(value) != b.JWTSecret {
		t.Fatalf("mounted file: %q %v", value, err)
	}
	if err := ValidateMountedFiles(p, defs, b); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(SecretValueFile(p, "JWT_SECRET")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMountedFiles(p, defs, b); err == nil {
		t.Fatal("missing Compose secret source was accepted")
	}
	if err := MaterializeFiles(p, defs, b); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(filepath.Dir(SecretValueFile(p, "JWT_SECRET"))); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o700) {
		t.Fatalf("secret directory mode: %v %v", st, err)
	}
	compose := []byte("services:\n  api:\n    env_file: [.env.api]\n    secrets: [jwt_secret]\nsecrets:\n  jwt_secret:\n    file: ${QABAS_ENV_DIR:-.}/secret-files/JWT_SECRET\n")
	if err := os.WriteFile(p.ComposeFile(), compose, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.EnvAPI(), []byte("JWT_SECRET=legacy-copy\nKEEP=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StripMountedEnv(p, defs); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(p.EnvAPI())
	if err != nil {
		t.Fatal(err)
	}
	if string(env) != "KEEP=yes\n" {
		t.Fatalf("env cleanup = %q", env)
	}
}
