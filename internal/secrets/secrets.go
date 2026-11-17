package secrets

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/redact"
)

// TemplateFetcher loads an env template by filename from the deploy release
// (compose/templates/<name> in qabas1-deploy).
type TemplateFetcher func(name string) ([]byte, error)

type Bundle struct {
	PostgresPassword           string
	PostgresAppPassword        string
	RedisPassword              string
	MinioUser                  string
	MinioPassword              string
	JWTSecret                  string
	TwoFactorKey               string
	ServiceAccountAPIKeyPepper string
}

// ReadInstalled reads the host's current secrets without generating or changing them.
func ReadInstalled(p paths.Paths) (Bundle, error) {
	raw, err := os.ReadFile(p.SecretsFile())
	if err != nil {
		return Bundle{}, err
	}
	b := parse(string(raw))
	if b.PostgresPassword == "" || b.PostgresAppPassword == "" {
		return Bundle{}, fmt.Errorf("PostgreSQL credentials are missing in %s", p.SecretsFile())
	}
	return b, nil
}

func Generate() (Bundle, error) {
	var b Bundle
	var err error
	if b.PostgresPassword, err = randomHex(16); err != nil {
		return b, err
	}
	if b.PostgresAppPassword, err = randomHex(16); err != nil {
		return b, err
	}
	if b.RedisPassword, err = randomHex(16); err != nil {
		return b, err
	}
	b.MinioUser = "qabasone"
	if b.MinioPassword, err = randomHex(16); err != nil {
		return b, err
	}
	if b.JWTSecret, err = randomHex(32); err != nil {
		return b, err
	}
	if b.TwoFactorKey, err = randomHex(32); err != nil {
		return b, err
	}
	if b.ServiceAccountAPIKeyPepper, err = randomHex(32); err != nil {
		return b, err
	}
	return b, nil
}

// Ensure loads the secrets bundle. Existing credentials are preserved. An
// older installation receives a separate runtime database password once:
//   - existing secrets.env → read, append the runtime password if absent
//   - missing secrets.env but complete service env files → reconstruct secrets.env once
//   - no secrets at all → generate and write secrets.env once
func Ensure(p paths.Paths) (Bundle, error) {
	b, err := ensure(p)
	if err == nil && b.PostgresAppPassword == "" {
		// Existing installs need a distinct runtime credential without rotating the owner password.
		b.PostgresAppPassword, err = randomHex(16)
		if err == nil {
			var raw []byte
			raw, err = os.ReadFile(p.SecretsFile())
			if err == nil {
				err = writeAtomic(p.SecretsFile(), []byte(strings.TrimRight(string(raw), "\n")+"\nPOSTGRES_APP_PASSWORD="+b.PostgresAppPassword+"\n"))
			}
		}
	}
	b.Register()
	return b, err
}

// Register adds every credential value to the redactor so it is masked in
// logs, history, and error output.
func (b Bundle) Register() {
	redact.Register(b.PostgresPassword, b.PostgresAppPassword, b.RedisPassword, b.MinioPassword, b.JWTSecret, b.TwoFactorKey, b.ServiceAccountAPIKeyPepper)
}

// RegisterKnown registers secrets.env values with the redactor when the file
// is readable. It never creates or modifies anything.
func RegisterKnown(p paths.Paths) {
	if raw, err := os.ReadFile(p.SecretsFile()); err == nil {
		parse(string(raw)).Register()
	}
	if raw, err := os.ReadFile(p.EnvCompose()); err == nil {
		values := nodestack.ParseEnv(raw)
		redact.Register(values["JSREPORT_ADMIN_PASSWORD"], values["JSREPORT_COOKIE_SECRET"])
	}
}

func ensure(p paths.Paths) (Bundle, error) {
	if raw, err := os.ReadFile(p.SecretsFile()); err == nil {
		b := parse(string(raw))
		// Prefer pepper already present in service env if secrets.env omitted it,
		// but never rewrite secrets.env after first creation.
		if b.ServiceAccountAPIKeyPepper == "" {
			existing, _, _ := existingBundle(p)
			b.ServiceAccountAPIKeyPepper = existing.ServiceAccountAPIKeyPepper
		}
		return b, nil
	}
	if b, found, ok := existingBundle(p); found {
		if !ok {
			return b, fmt.Errorf("existing service env files found but secret values are incomplete; refusing to generate replacement secrets")
		}
		// Reconstruct secrets.env from existing env values only — do not invent
		// new credential material that is not already on disk.
		if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
			return b, err
		}
		if err := writeSecretsFile(p, b); err != nil {
			return b, err
		}
		return b, nil
	}
	b, err := Generate()
	if err != nil {
		return b, err
	}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		return b, err
	}
	if err := writeSecretsFile(p, b); err != nil {
		return b, err
	}
	return b, nil
}

func writeSecretsFile(p paths.Paths, b Bundle) error {
	if _, err := os.Stat(p.SecretsFile()); err == nil {
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	body := fmt.Sprintf(
		"POSTGRES_PASSWORD=%s\nPOSTGRES_APP_PASSWORD=%s\nREDIS_PASSWORD=%s\nMINIO_ROOT_USER=%s\nMINIO_ROOT_PASSWORD=%s\nJWT_SECRET=%s\nTWO_FACTOR_SECRET_ENCRYPTION_KEY=%s\nSERVICE_ACCOUNT_API_KEY_PEPPER=%s\n",
		b.PostgresPassword, b.PostgresAppPassword, b.RedisPassword, b.MinioUser, b.MinioPassword, b.JWTSecret, b.TwoFactorKey, b.ServiceAccountAPIKeyPepper,
	)
	return os.WriteFile(p.SecretsFile(), []byte(body), 0o600)
}

func existingBundle(p paths.Paths) (Bundle, bool, bool) {
	b := Bundle{}
	found := false
	for _, path := range []string{
		p.EnvCompose(),
		p.EnvAPI(),
		p.EnvWorker(),
		p.EnvMigrate(),
		p.EnvApp(),
		p.EnvDash(),
		p.EnvSiraj(),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		found = true
		mergeEnv(&b, string(raw))
	}
	return b, found, b.baseComplete()
}

func mergeEnv(b *Bundle, raw string) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "POSTGRES_PASSWORD":
			if b.PostgresPassword == "" {
				b.PostgresPassword = v
			}
		case "POSTGRES_APP_PASSWORD":
			if b.PostgresAppPassword == "" {
				b.PostgresAppPassword = v
			}
		case "POSTGRES_URI", "DATABASE_URL":
			if u, parseErr := url.Parse(v); parseErr == nil && u.User != nil && u.User.Username() == "qabasone_app" {
				if b.PostgresAppPassword == "" {
					b.PostgresAppPassword = postgresPassword(v)
				}
			} else if b.PostgresPassword == "" {
				b.PostgresPassword = postgresPassword(v)
			}
		case "REDIS_PASSWORD":
			if b.RedisPassword == "" {
				b.RedisPassword = v
			}
		case "REDIS_URL":
			if b.RedisPassword == "" {
				b.RedisPassword = redisPassword(v)
			}
		case "MINIO_ROOT_USER", "STORAGE_ACCESS_KEY_ID":
			if b.MinioUser == "" {
				b.MinioUser = v
			}
		case "MINIO_ROOT_PASSWORD", "STORAGE_SECRET_ACCESS_KEY":
			if b.MinioPassword == "" {
				b.MinioPassword = v
			}
		case "JWT_SECRET":
			if b.JWTSecret == "" {
				b.JWTSecret = v
			}
		case "TWO_FACTOR_SECRET_ENCRYPTION_KEY":
			if b.TwoFactorKey == "" {
				b.TwoFactorKey = v
			}
		case "SERVICE_ACCOUNT_API_KEY_PEPPER":
			if b.ServiceAccountAPIKeyPepper == "" {
				b.ServiceAccountAPIKeyPepper = v
			}
		}
	}
}

func postgresPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return ""
	}
	password, _ := u.User.Password()
	return password
}

func redisPassword(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return ""
	}
	password, _ := u.User.Password()
	return password
}

func (b Bundle) baseComplete() bool {
	return b.PostgresPassword != "" &&
		b.RedisPassword != "" &&
		b.MinioUser != "" &&
		b.MinioPassword != "" &&
		b.JWTSecret != "" &&
		b.TwoFactorKey != ""
}

// envTemplates maps deploy-release template filenames to host destinations.
// Source of truth: qabas1-deploy/compose/templates/*.example
var envTemplates = []struct {
	name string
	dest func(paths.Paths) string
}{
	{".env.api.example", paths.Paths.EnvAPI},
	{".env.migrate.example", paths.Paths.EnvMigrate},
	{".env.worker.example", paths.Paths.EnvWorker},
	{".env.app.example", paths.Paths.EnvApp},
	{".env.dash.example", paths.Paths.EnvDash},
	{".env.siraj.example", paths.Paths.EnvSiraj},
	{".env.compose.example", paths.Paths.EnvCompose},
}

// RequiredTemplates lists the release assets needed for missing host env files.
func RequiredTemplates(p paths.Paths) ([]string, error) {
	var names []string
	for _, tmpl := range envTemplates {
		_, err := os.Stat(tmpl.dest(p))
		if os.IsNotExist(err) {
			names = append(names, tmpl.name)
		} else if err != nil {
			return nil, err
		}
	}
	return names, nil
}

// WriteEnvFiles materializes missing env files and stages the runtime role
// password. Existing API/worker credentials change only after role setup.
func WriteEnvFiles(p paths.Paths, b Bundle, domains config.Domains, fetch TemplateFetcher) error {
	if fetch == nil {
		return fmt.Errorf("env template fetcher is required (templates come from the qabas1-deploy release)")
	}
	for _, tmpl := range envTemplates {
		dest := tmpl.dest(p)
		if _, err := os.Stat(dest); err == nil {
			// Existing credential/env files are operator-owned after first setup.
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		raw, err := fetch(tmpl.name)
		if err != nil {
			return fmt.Errorf("fetch env template %s from deploy release: %w", tmpl.name, err)
		}
		body := RenderTemplate(string(raw), b, domains, p.ConfigDir)
		if err := os.WriteFile(dest, []byte(body), 0o600); err != nil {
			return err
		}
	}
	if err := prepareRuntimeDBEnv(p, b); err != nil {
		return err
	}
	return ensureJSReportCredentials(p)
}

// ApplyReplicaDomains updates only domain-related values in an already configured
// replica's env files. Existing credentials and all unrelated settings are preserved.
func ApplyReplicaDomains(p paths.Paths, domains config.Domains) error {
	if !domains.Complete() {
		return fmt.Errorf("replica gateway requires API, app, dashboard, Siraj, S3, and S3 Console domains")
	}
	mainDomain := baseDomain(domains.App)
	composeEnv := map[string]string{
		"MAIN_DOMAIN":       mainDomain,
		"API_DOMAIN":        domains.API,
		"APP_DOMAIN":        domains.App,
		"DASH_DOMAIN":       domains.Dash,
		"SIRAJ_DOMAIN":      domains.Siraj,
		"S3_DOMAIN":         domains.S3,
		"S3_CONSOLE_DOMAIN": domains.S3Console,
	}
	files := []struct {
		path string
		env  map[string]string
	}{
		{p.EnvCompose(), composeEnv},
		{p.EnvAPI(), map[string]string{
			"CORS_ALLOWED_DOMAINS": strings.Join([]string{domains.App, domains.Dash, domains.S3, domains.S3Console}, ","),
			"API_PUBLIC_BASE_URL":  "https://" + domains.API,
			"WEBAUTHN_RP_ID":       domains.App,
		}},
		{p.EnvWorker(), map[string]string{"WEBAUTHN_RP_ID": domains.App}},
		{p.EnvApp(), map[string]string{"API_BASE_URL": "https://" + domains.API + "/api/v1"}},
		{p.EnvDash(), map[string]string{"API_BASE_URL": "https://" + domains.API + "/api/v1"}},
		{p.EnvSiraj(), map[string]string{
			"QABAS_API_BASE_URL": "https://" + domains.API + "/api/v1",
			"QABAS_API_ORIGIN":   "https://" + domains.Siraj,
		}},
	}
	for _, file := range files {
		raw, err := os.ReadFile(file.path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("read replica env file %s: %w", file.path, err)
		}
		updated := nodestack.SetEnv(raw, file.env)
		if string(updated) == string(raw) {
			continue
		}
		if err := writeAtomic(file.path, updated); err != nil {
			return fmt.Errorf("update replica domains in %s: %w", file.path, err)
		}
	}
	return nil
}

// Studio credentials are kept in the operator-owned Compose env file. Upgrade
// existing installs without rotating values already configured by the operator.
func ensureJSReportCredentials(p paths.Paths) error {
	raw, err := os.ReadFile(p.EnvCompose())
	if err != nil {
		return err
	}
	values := nodestack.ParseEnv(raw)
	update := map[string]string{}
	for _, key := range []string{"JSREPORT_ADMIN_PASSWORD", "JSREPORT_COOKIE_SECRET"} {
		value := values[key]
		if value == "" || strings.HasPrefix(value, "REPLACE_") {
			value, err = randomHex(32)
			if err != nil {
				return err
			}
			update[key] = value
		}
		redact.Register(value)
	}
	if len(update) == 0 {
		return nil
	}
	return writeAtomic(p.EnvCompose(), nodestack.SetEnv(raw, update))
}

// RenderTemplate applies secret placeholders and domain host mapping to a
// deploy-release env template. Placeholder contract matches
// qabas1-deploy/scripts/prepare-e2e-env.py.
func RenderTemplate(tmpl string, b Bundle, domains config.Domains, configDir string) string {
	text := tmpl

	// JWT and 2FA share REPLACE_WITH_OPENSSL_RAND_HEX_32 in templates; assign
	// JWT first, then rewrite the 2FA line so the keys differ.
	text = strings.ReplaceAll(text, "REPLACE_WITH_OPENSSL_RAND_HEX_32", b.JWTSecret)
	if b.TwoFactorKey != "" && strings.Contains(text, "TWO_FACTOR_SECRET_ENCRYPTION_KEY=") {
		text = strings.Replace(
			text,
			"TWO_FACTOR_SECRET_ENCRYPTION_KEY="+b.JWTSecret,
			"TWO_FACTOR_SECRET_ENCRYPTION_KEY="+b.TwoFactorKey,
			1,
		)
	}

	replacements := [][2]string{
		{"REPLACE_POSTGRES_PASSWORD", b.PostgresPassword},
		{"REPLACE_POSTGRES_APP_PASSWORD", b.PostgresAppPassword},
		{"REPLACE_REDIS_PASSWORD", b.RedisPassword},
		{"REPLACE_MINIO_ROOT_USER", b.MinioUser},
		{"REPLACE_MINIO_ROOT_PASSWORD", b.MinioPassword},
		{"REPLACE_MINIO_ACCESS_KEY", b.MinioUser},
		{"REPLACE_MINIO_SECRET_KEY", b.MinioPassword},
		{"REPLACE_SERVICE_ACCOUNT_API_KEY_PEPPER", b.ServiceAccountAPIKeyPepper},
	}
	for _, kv := range replacements {
		if kv[1] == "" {
			continue
		}
		text = strings.ReplaceAll(text, kv[0], kv[1])
	}

	text = applyDomainHosts(text, domains)
	if configDir != "" {
		text = strings.ReplaceAll(text, "QABAS_ENV_DIR=/etc/qabasone", "QABAS_ENV_DIR="+filepath.ToSlash(configDir))
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}

// applyDomainHosts rewrites the default qabas.one hosts shipped in deploy
// templates to the operator-configured domains (longest FQDN first).
func applyDomainHosts(text string, d config.Domains) string {
	main := baseDomain(d.App)
	if main == "" {
		main = "qabas.one"
	}
	ordered := [][2]string{
		{"s3c.qabas.one", d.S3Console},
		{"s3.qabas.one", d.S3},
		{"api.qabas.one", d.API},
		{"app.qabas.one", d.App},
		{"dash.qabas.one", d.Dash},
		{"siraj.qabas.one", d.Siraj},
		{"qabas.one", main},
	}
	for _, kv := range ordered {
		if kv[1] == "" || kv[1] == kv[0] {
			continue
		}
		text = strings.ReplaceAll(text, kv[0], kv[1])
	}
	return text
}

func baseDomain(host string) string {
	parts := strings.Split(host, ".")
	if len(parts) <= 2 {
		return host
	}
	return strings.Join(parts[1:], ".")
}

func parse(raw string) Bundle {
	b := Bundle{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "POSTGRES_PASSWORD":
			b.PostgresPassword = v
		case "POSTGRES_APP_PASSWORD":
			b.PostgresAppPassword = v
		case "REDIS_PASSWORD":
			b.RedisPassword = v
		case "MINIO_ROOT_USER":
			b.MinioUser = v
		case "MINIO_ROOT_PASSWORD":
			b.MinioPassword = v
		case "JWT_SECRET":
			b.JWTSecret = v
		case "TWO_FACTOR_SECRET_ENCRYPTION_KEY":
			b.TwoFactorKey = v
		case "SERVICE_ACCOUNT_API_KEY_PEPPER":
			b.ServiceAccountAPIKeyPepper = v
		}
	}
	return b
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
