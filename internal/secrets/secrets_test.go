package secrets_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/config"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/secrets"
)

func testFetcher(templates map[string]string) secrets.TemplateFetcher {
	return func(name string) ([]byte, error) {
		body, ok := templates[name]
		if !ok {
			return nil, fmt.Errorf("missing template %s", name)
		}
		return []byte(body), nil
	}
}

func productionLikeTemplates() map[string]string {
	return map[string]string{
		".env.api.example": strings.Join([]string{
			"NODE_ENV=production",
			"POSTGRES_URI=postgresql://qabasone_app:REPLACE_POSTGRES_APP_PASSWORD@postgres:5432/qabasone",
			"REDIS_PASSWORD=REPLACE_REDIS_PASSWORD",
			"JWT_SECRET=REPLACE_WITH_OPENSSL_RAND_HEX_32",
			"TWO_FACTOR_SECRET_ENCRYPTION_KEY=REPLACE_WITH_OPENSSL_RAND_HEX_32",
			"SERVICE_ACCOUNT_API_KEY_PEPPER=REPLACE_SERVICE_ACCOUNT_API_KEY_PEPPER",
			"CORS_ALLOWED_DOMAINS=app.qabas.one,dash.qabas.one,s3.qabas.one,s3c.qabas.one",
			"API_PUBLIC_BASE_URL=https://api.qabas.one",
			"WEBAUTHN_RP_ID=app.qabas.one",
			"STORAGE_ACCESS_KEY_ID=REPLACE_MINIO_ACCESS_KEY",
			"STORAGE_SECRET_ACCESS_KEY=REPLACE_MINIO_SECRET_KEY",
			"",
		}, "\n"),
		".env.migrate.example": strings.Join([]string{
			"POSTGRES_URI=postgresql://qabasone:REPLACE_POSTGRES_PASSWORD@postgres:5432/qabasone",
			"DATABASE_URL=postgresql://qabasone:REPLACE_POSTGRES_PASSWORD@postgres:5432/qabasone",
			"",
		}, "\n"),
		".env.worker.example": strings.Join([]string{
			"POSTGRES_URI=postgresql://qabasone_app:REPLACE_POSTGRES_APP_PASSWORD@postgres:5432/qabasone",
			"JWT_SECRET=REPLACE_WITH_OPENSSL_RAND_HEX_32",
			"TWO_FACTOR_SECRET_ENCRYPTION_KEY=REPLACE_WITH_OPENSSL_RAND_HEX_32",
			"SERVICE_ACCOUNT_API_KEY_PEPPER=REPLACE_SERVICE_ACCOUNT_API_KEY_PEPPER",
			"WEBAUTHN_RP_ID=app.qabas.one",
			"STORAGE_ACCESS_KEY_ID=REPLACE_MINIO_ACCESS_KEY",
			"STORAGE_SECRET_ACCESS_KEY=REPLACE_MINIO_SECRET_KEY",
			"",
		}, "\n"),
		".env.app.example":   "API_BASE_URL=https://api.qabas.one/api/v1\nAPP_VERSION=prod\n",
		".env.dash.example":  "API_BASE_URL=https://api.qabas.one/api/v1\nAPP_VERSION=prod\n",
		".env.siraj.example": "QABAS_API_BASE_URL=https://api.qabas.one/api/v1\nQABAS_API_ORIGIN=https://siraj.qabas.one\n",
		".env.compose.example": strings.Join([]string{
			"POSTGRES_PASSWORD=REPLACE_POSTGRES_PASSWORD",
			"POSTGRES_APP_PASSWORD=REPLACE_POSTGRES_APP_PASSWORD",
			"REDIS_PASSWORD=REPLACE_REDIS_PASSWORD",
			"MINIO_ROOT_USER=REPLACE_MINIO_ROOT_USER",
			"MINIO_ROOT_PASSWORD=REPLACE_MINIO_ROOT_PASSWORD",
			"MAIN_DOMAIN=qabas.one",
			"API_DOMAIN=api.qabas.one",
			"APP_DOMAIN=app.qabas.one",
			"DASH_DOMAIN=dash.qabas.one",
			"SIRAJ_DOMAIN=siraj.qabas.one",
			"S3_DOMAIN=s3.qabas.one",
			"S3_CONSOLE_DOMAIN=s3c.qabas.one",
			"QABAS_ENV_DIR=/etc/qabasone",
			"",
		}, "\n"),
	}
}

func TestWriteEnvFilesGeneratesStableJSReportCredentials(t *testing.T) {
	p := paths.Paths{ConfigDir: filepath.Join(t.TempDir(), "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := secrets.Generate()
	if err != nil {
		t.Fatal(err)
	}
	templates := productionLikeTemplates()
	templates[".env.compose.example"] += "JSREPORT_ADMIN_PASSWORD=REPLACE_JSREPORT_ADMIN_PASSWORD\nJSREPORT_COOKIE_SECRET=REPLACE_JSREPORT_COOKIE_SECRET\n"
	fetch := testFetcher(templates)
	if err := secrets.WriteEnvFiles(p, b, config.Domains{}, fetch); err != nil {
		t.Fatal(err)
	}
	first := read(t, p.EnvCompose())
	if strings.Contains(first, "REPLACE_JSREPORT") || !strings.Contains(first, "JSREPORT_ADMIN_PASSWORD=") || !strings.Contains(first, "JSREPORT_COOKIE_SECRET=") {
		t.Fatalf("Studio credentials not materialized")
	}
	if err := secrets.WriteEnvFiles(p, b, config.Domains{}, fetch); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p.EnvCompose()); got != first {
		t.Fatal("Studio credentials rotated during repeat provisioning")
	}
}

func TestWriteEnvFilesUsesDomainMapping(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := secrets.Bundle{
		PostgresPassword:           "pg",
		PostgresAppPassword:        "app-pg",
		RedisPassword:              "redis",
		MinioUser:                  "minio",
		MinioPassword:              "miniopass",
		JWTSecret:                  "jwt",
		TwoFactorKey:               "twofactor",
		ServiceAccountAPIKeyPepper: "pepper",
	}
	domains := config.Domains{
		API:       "api.example.com",
		App:       "portal.example.com",
		Dash:      "admin.example.com",
		Siraj:     "siraj.example.com",
		S3:        "s3.example.com",
		S3Console: "s3c.example.com",
	}
	if err := secrets.WriteEnvFiles(p, b, domains, testFetcher(productionLikeTemplates())); err != nil {
		t.Fatal(err)
	}
	api := read(t, p.EnvAPI())
	if !strings.Contains(api, "CORS_ALLOWED_DOMAINS=portal.example.com,admin.example.com,s3.example.com,s3c.example.com") {
		t.Fatalf("api env missing CORS hosts:\n%s", api)
	}
	if !strings.Contains(api, "API_PUBLIC_BASE_URL=https://api.example.com") {
		t.Fatalf("api env missing api host:\n%s", api)
	}
	if !strings.Contains(api, "SERVICE_ACCOUNT_API_KEY_PEPPER=pepper") {
		t.Fatalf("api env missing service account pepper:\n%s", api)
	}
	if !strings.Contains(api, "JWT_SECRET=jwt\n") {
		t.Fatalf("api env missing jwt:\n%s", api)
	}
	if !strings.Contains(api, "TWO_FACTOR_SECRET_ENCRYPTION_KEY=twofactor\n") {
		t.Fatalf("api env missing distinct 2fa key:\n%s", api)
	}
	app := read(t, p.EnvApp())
	if !strings.Contains(app, "API_BASE_URL=https://api.example.com/api/v1") {
		t.Fatalf("app env missing api host:\n%s", app)
	}
	dash := read(t, p.EnvDash())
	if !strings.Contains(dash, "API_BASE_URL=https://api.example.com/api/v1") {
		t.Fatalf("dash env missing api host:\n%s", dash)
	}
	siraj := read(t, p.EnvSiraj())
	if !strings.Contains(siraj, "QABAS_API_BASE_URL=https://api.example.com/api/v1") {
		t.Fatalf("siraj env missing api host:\n%s", siraj)
	}
	if !strings.Contains(siraj, "QABAS_API_ORIGIN=https://siraj.example.com") {
		t.Fatalf("siraj env missing origin:\n%s", siraj)
	}
	compose := read(t, p.EnvCompose())
	for _, want := range []string{
		"API_DOMAIN=api.example.com",
		"APP_DOMAIN=portal.example.com",
		"DASH_DOMAIN=admin.example.com",
		"SIRAJ_DOMAIN=siraj.example.com",
		"S3_DOMAIN=s3.example.com",
		"S3_CONSOLE_DOMAIN=s3c.example.com",
		"QABAS_ENV_DIR=" + filepath.ToSlash(p.ConfigDir),
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("compose env missing %s:\n%s", want, compose)
		}
	}
	migrate := read(t, p.EnvMigrate())
	if !strings.Contains(migrate, "POSTGRES_URI=postgresql://qabasone:pg@postgres:5432/qabasone") {
		t.Fatalf("migrate env missing rendered postgres password:\n%s", migrate)
	}
}

func TestWriteEnvFilesLeavesExistingMigrateUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := "NODE_ENV=production\nPOSTGRES_URI=postgresql://qabasone:pg@postgres:5432/qabasone\nDATABASE_URL=postgresql://qabasone:pg@postgres:5432/qabasone\n"
	if err := os.WriteFile(p.EnvMigrate(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	b := secrets.Bundle{
		PostgresPassword:           "pg",
		PostgresAppPassword:        "app-pg",
		RedisPassword:              "redis",
		MinioUser:                  "minio",
		MinioPassword:              "miniopass",
		JWTSecret:                  "jwt-secret-value-at-least-32-bytes!!",
		TwoFactorKey:               "twofactor-key-value-at-least-32-bytes!",
		ServiceAccountAPIKeyPepper: "pepper-value-at-least-32-bytes-long!",
	}
	fetched := false
	fetch := func(name string) ([]byte, error) {
		if name == ".env.migrate.example" {
			fetched = true
		}
		return testFetcher(productionLikeTemplates())(name)
	}
	if err := secrets.WriteEnvFiles(p, b, config.Domains{}, fetch); err != nil {
		t.Fatal(err)
	}
	got := read(t, p.EnvMigrate())
	if got != legacy {
		t.Fatalf("existing migrate env must not be mutated:\ngot:\n%s\nwant:\n%s", got, legacy)
	}
	if fetched {
		t.Fatal("must not fetch migrate template when destination already exists")
	}
}

func TestEnsureUsesExistingServiceEnvWhenSecretsFileMissing(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := strings.Join([]string{
		"POSTGRES_PASSWORD=existing-pg",
		"REDIS_PASSWORD=existing-redis",
		"MINIO_ROOT_USER=existing-minio",
		"MINIO_ROOT_PASSWORD=existing-minio-pass",
		"JWT_SECRET=existing-jwt",
		"TWO_FACTOR_SECRET_ENCRYPTION_KEY=existing-twofactor",
		"SERVICE_ACCOUNT_API_KEY_PEPPER=existing-pepper",
		"",
	}, "\n")
	if err := os.WriteFile(p.EnvCompose(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	b, err := secrets.Ensure(p)
	if err != nil {
		t.Fatal(err)
	}

	if b.PostgresPassword != "existing-pg" ||
		b.RedisPassword != "existing-redis" ||
		b.MinioUser != "existing-minio" ||
		b.MinioPassword != "existing-minio-pass" ||
		b.JWTSecret != "existing-jwt" ||
		b.TwoFactorKey != "existing-twofactor" ||
		b.ServiceAccountAPIKeyPepper != "existing-pepper" {
		t.Fatalf("existing env was not preserved: %#v", b)
	}
	secretFile := read(t, p.SecretsFile())
	if !strings.Contains(secretFile, "POSTGRES_PASSWORD=existing-pg") {
		t.Fatalf("secrets file was not reconstructed from existing env:\n%s", secretFile)
	}
}

func TestEnsureUsesExistingServiceEnvURIPasswords(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	apiEnv := strings.Join([]string{
		"POSTGRES_URI=postgresql://qabasone:uri-pg@postgres:5432/qabasone",
		"REDIS_URL=redis://:uri-redis@redis:6379/0",
		"STORAGE_ACCESS_KEY_ID=uri-minio",
		"STORAGE_SECRET_ACCESS_KEY=uri-minio-pass",
		"JWT_SECRET=uri-jwt",
		"TWO_FACTOR_SECRET_ENCRYPTION_KEY=uri-twofactor",
		"SERVICE_ACCOUNT_API_KEY_PEPPER=uri-pepper",
		"",
	}, "\n")
	if err := os.WriteFile(p.EnvAPI(), []byte(apiEnv), 0o600); err != nil {
		t.Fatal(err)
	}

	b, err := secrets.Ensure(p)
	if err != nil {
		t.Fatal(err)
	}

	if b.PostgresPassword != "uri-pg" ||
		b.RedisPassword != "uri-redis" ||
		b.MinioUser != "uri-minio" ||
		b.MinioPassword != "uri-minio-pass" ||
		b.JWTSecret != "uri-jwt" ||
		b.TwoFactorKey != "uri-twofactor" ||
		b.ServiceAccountAPIKeyPepper != "uri-pepper" {
		t.Fatalf("existing URI env was not preserved: %#v", b)
	}
}

func TestEnsureReconstructsSecretsWithoutInventingPepper(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := strings.Join([]string{
		"POSTGRES_PASSWORD=legacy-pg",
		"REDIS_PASSWORD=legacy-redis",
		"MINIO_ROOT_USER=legacy-minio",
		"MINIO_ROOT_PASSWORD=legacy-minio-pass",
		"JWT_SECRET=legacy-jwt",
		"TWO_FACTOR_SECRET_ENCRYPTION_KEY=legacy-twofactor",
		"",
	}, "\n")
	if err := os.WriteFile(p.EnvWorker(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	b, err := secrets.Ensure(p)
	if err != nil {
		t.Fatal(err)
	}
	if b.PostgresPassword != "legacy-pg" {
		t.Fatalf("legacy env was not preserved: %#v", b)
	}
	if b.ServiceAccountAPIKeyPepper != "" {
		t.Fatalf("Ensure must not invent new pepper for existing installs: %#v", b)
	}
	secretFile := read(t, p.SecretsFile())
	if !strings.Contains(secretFile, "POSTGRES_PASSWORD=legacy-pg") {
		t.Fatalf("secrets file was not reconstructed from existing env:\n%s", secretFile)
	}
	if got := read(t, p.EnvWorker()); got != existing {
		t.Fatalf("existing worker env must not be mutated:\ngot:\n%s\nwant:\n%s", got, existing)
	}
}

func TestWriteEnvFilesLeavesExistingWorkerUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	worker := "JWT_SECRET=existing-jwt\n"
	if err := os.WriteFile(p.EnvWorker(), []byte(worker), 0o600); err != nil {
		t.Fatal(err)
	}
	b := secrets.Bundle{
		PostgresPassword:           "pg",
		PostgresAppPassword:        "app-pg",
		RedisPassword:              "redis",
		MinioUser:                  "minio",
		MinioPassword:              "miniopass",
		JWTSecret:                  "new-jwt",
		TwoFactorKey:               "twofactor",
		ServiceAccountAPIKeyPepper: "pepper",
	}

	if err := secrets.WriteEnvFiles(p, b, config.Domains{}, testFetcher(productionLikeTemplates())); err != nil {
		t.Fatal(err)
	}

	got := read(t, p.EnvWorker())
	if got != worker {
		t.Fatalf("existing worker env must not be mutated:\ngot:\n%s\nwant:\n%s", got, worker)
	}
}

func TestEnsureAddsOnlyRuntimeCredentialToExistingSecretsFile(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := strings.Join([]string{
		"POSTGRES_PASSWORD=pg",
		"REDIS_PASSWORD=redis",
		"MINIO_ROOT_USER=minio",
		"MINIO_ROOT_PASSWORD=miniopass",
		"JWT_SECRET=jwt",
		"TWO_FACTOR_SECRET_ENCRYPTION_KEY=twofactor",
		"",
	}, "\n")
	if err := os.WriteFile(p.SecretsFile(), []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	b, err := secrets.Ensure(p)
	if err != nil {
		t.Fatal(err)
	}
	if b.PostgresPassword != "pg" || b.JWTSecret != "jwt" {
		t.Fatalf("secrets not loaded: %#v", b)
	}
	if b.PostgresAppPassword == "" || b.PostgresAppPassword == b.PostgresPassword {
		t.Fatal("runtime credential must be independent")
	}
	if got := read(t, p.SecretsFile()); got != original+"POSTGRES_APP_PASSWORD="+b.PostgresAppPassword+"\n" {
		t.Fatal("existing credentials changed while adding runtime credential")
	}
	again, err := secrets.Ensure(p)
	if err != nil || again.PostgresAppPassword != b.PostgresAppPassword {
		t.Fatal("runtime credential changed on repeated ensure")
	}
}

func TestWriteEnvFilesIsIdempotentCreateOnce(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := secrets.Bundle{
		PostgresPassword:           "pg",
		PostgresAppPassword:        "app-pg",
		RedisPassword:              "redis",
		MinioUser:                  "minio",
		MinioPassword:              "miniopass",
		JWTSecret:                  "jwt",
		TwoFactorKey:               "twofactor",
		ServiceAccountAPIKeyPepper: "pepper",
	}
	domains := config.Domains{API: "api.example.com", App: "app.example.com"}
	fetch := testFetcher(productionLikeTemplates())
	if err := secrets.WriteEnvFiles(p, b, domains, fetch); err != nil {
		t.Fatal(err)
	}
	firstAPI := read(t, p.EnvAPI())
	b.JWTSecret = "mutated-jwt-must-not-land-on-disk"
	domains.API = "evil.example.com"
	if err := secrets.WriteEnvFiles(p, b, domains, fetch); err != nil {
		t.Fatal(err)
	}
	got := read(t, p.EnvAPI())
	if got != firstAPI {
		t.Fatalf("second WriteEnvFiles must leave existing files untouched:\ngot:\n%s\nwant:\n%s", got, firstAPI)
	}
	if strings.Contains(got, "mutated-jwt") || strings.Contains(got, "evil.example.com") {
		t.Fatalf("credentials/domains were overwritten:\n%s", got)
	}
}

func TestApplyReplicaDomainsRewritesOnlyReplicaEnv(t *testing.T) {
	dir := t.TempDir()
	replica := paths.Paths{ConfigDir: filepath.Join(dir, "replica")}
	writer := paths.Paths{ConfigDir: filepath.Join(dir, "writer")}
	for _, p := range []paths.Paths{replica, writer} {
		if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		replica.EnvCompose(): "MINIO_ROOT_PASSWORD=keep-me\nAPI_DOMAIN=api.primary.example\n",
		replica.EnvAPI():     "JWT_SECRET=keep-jwt\nCORS_ALLOWED_DOMAINS=primary.example\n",
		replica.EnvApp():     "API_BASE_URL=https://api.primary.example/api/v1\n",
		replica.EnvDash():    "API_BASE_URL=https://api.primary.example/api/v1\n",
		replica.EnvSiraj():   "QABAS_API_BASE_URL=https://api.primary.example/api/v1\nQABAS_API_ORIGIN=https://siraj.primary.example\n",
		replica.EnvWorker():  "WEBAUTHN_RP_ID=app.primary.example\n",
		writer.EnvAPI():      "CORS_ALLOWED_DOMAINS=primary.example\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	domains := config.Domains{
		API: "api.dr.example", App: "app.dr.example", Dash: "dash.dr.example",
		Siraj: "siraj.dr.example", S3: "s3.dr.example", S3Console: "s3c.dr.example",
		Trade: "trade.dr.example",
	}
	if err := secrets.ApplyReplicaDomains(replica, domains, domains.Trade); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]string{
		replica.EnvCompose(): {"MINIO_ROOT_PASSWORD=keep-me", "MAIN_DOMAIN=dr.example", "API_DOMAIN=api.dr.example", "TRADE_DOMAIN=trade.dr.example"},
		replica.EnvAPI():     {"JWT_SECRET=keep-jwt", "CORS_ALLOWED_DOMAINS=app.dr.example,dash.dr.example,s3.dr.example,s3c.dr.example", "API_PUBLIC_BASE_URL=https://api.dr.example", "WEBAUTHN_RP_ID=app.dr.example"},
		replica.EnvApp():     {"API_BASE_URL=https://api.dr.example/api/v1"},
		replica.EnvDash():    {"API_BASE_URL=https://api.dr.example/api/v1"},
		replica.EnvSiraj():   {"QABAS_API_BASE_URL=https://api.dr.example/api/v1", "QABAS_API_ORIGIN=https://siraj.dr.example"},
		replica.EnvWorker():  {"WEBAUTHN_RP_ID=app.dr.example"},
	} {
		got := read(t, path)
		for _, line := range want {
			if !strings.Contains(got, line) {
				t.Errorf("%s missing %q:\n%s", path, line, got)
			}
		}
	}
	if got := read(t, writer.EnvAPI()); got != files[writer.EnvAPI()] {
		t.Fatalf("replica domain update changed writer env: %q", got)
	}
}

func TestWriteEnvFilesUpgradesLegacyDatabaseCredentialsOnly(t *testing.T) {
	p := paths.Paths{ConfigDir: filepath.Join(t.TempDir(), "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := secrets.Bundle{PostgresPassword: "owner-pg", PostgresAppPassword: "app-pg"}
	legacyAPI := "POSTGRES_URI=postgresql://qabasone:owner-pg@postgres:5432/qabasone\nDATABASE_URL=postgresql://qabasone:owner-pg@postgres:5432/qabasone\nJWT_SECRET=unchanged\n"
	legacyWorker := "POSTGRES_URI=postgresql://qabasone:owner-pg@postgres:5432/qabasone\n"
	legacyMigrate := "POSTGRES_URI=postgresql://qabasone:owner-pg@postgres:5432/qabasone\n"
	files := map[string]string{
		p.EnvAPI(): legacyAPI, p.EnvWorker(): legacyWorker,
		p.EnvMigrate(): legacyMigrate, p.EnvCompose(): "POSTGRES_PASSWORD=owner-pg\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := secrets.WriteEnvFiles(p, b, config.Domains{}, testFetcher(productionLikeTemplates())); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p.EnvAPI()); got != legacyAPI {
		t.Fatal("API credentials changed before runtime role initialization")
	}
	if err := secrets.UpgradeRuntimeDBEnv(p, b); err != nil {
		t.Fatal(err)
	}
	api := read(t, p.EnvAPI())
	if strings.Contains(api, "qabasone:owner-pg") || strings.Count(api, "qabasone_app:app-pg") != 2 || !strings.Contains(api, "JWT_SECRET=unchanged") {
		t.Fatal("legacy API env was not narrowly upgraded")
	}
	if got := read(t, p.EnvWorker()); !strings.Contains(got, "qabasone_app:app-pg") {
		t.Fatal("worker remains on owner role")
	}
	if got := read(t, p.EnvMigrate()); got != legacyMigrate {
		t.Fatal("migration role changed")
	}
	if got := read(t, p.EnvCompose()); !strings.Contains(got, "POSTGRES_APP_PASSWORD=app-pg\n") {
		t.Fatal("compose runtime password missing")
	}
}

func TestWriteEnvFilesRejectsUnknownRoleWithoutPartialCutover(t *testing.T) {
	p := paths.Paths{ConfigDir: filepath.Join(t.TempDir(), "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := secrets.Bundle{PostgresPassword: "owner-pg", PostgresAppPassword: "app-pg"}
	compose := "POSTGRES_PASSWORD=owner-pg\n"
	api := "POSTGRES_URI=postgresql://qabasone:owner-pg@postgres:5432/qabasone\n"
	worker := "POSTGRES_URI=postgresql://custom:other-pg@postgres:5432/qabasone\n"
	for path, body := range map[string]string{p.EnvCompose(): compose, p.EnvAPI(): api, p.EnvWorker(): worker} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := secrets.WriteEnvFiles(p, b, config.Domains{}, testFetcher(productionLikeTemplates())); err != nil {
		t.Fatal(err)
	}
	prepared := read(t, p.EnvCompose())
	if err := secrets.UpgradeRuntimeDBEnv(p, b); err == nil {
		t.Fatal("expected unknown role rejection")
	}
	if got := read(t, p.EnvCompose()); got != prepared {
		t.Fatal("compose env changed during failed cutover")
	}
	if got := read(t, p.EnvAPI()); got != api {
		t.Fatal("API env changed before validation")
	}
	if got := read(t, p.EnvWorker()); got != worker {
		t.Fatal("worker env changed before validation")
	}
}

func TestWriteEnvFilesRequiresFetcher(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := secrets.WriteEnvFiles(p, secrets.Bundle{}, config.Domains{}, nil)
	if err == nil {
		t.Fatal("expected nil fetcher to fail")
	}
}

func TestEnsureRefusesToGenerateWhenExistingServiceEnvIsIncomplete(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(dir, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.EnvAPI(), []byte("JWT_SECRET=existing-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := secrets.Ensure(p)
	if err == nil {
		t.Fatal("expected incomplete existing env to fail")
	}
	if _, statErr := os.Stat(p.SecretsFile()); !os.IsNotExist(statErr) {
		t.Fatalf("secrets file should not be generated, stat err: %v", statErr)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
