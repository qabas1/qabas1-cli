package observability

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/paths"
	"golang.org/x/crypto/bcrypt"
)

func TestEnsureCredentialsCreatesFileAndDozzleUsers(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: dir, ConfigDir: filepath.Join(dir, "etc")}
	if err := Materialize(p); err != nil {
		t.Fatal(err)
	}
	cred, err := EnsureCredentials(p)
	if err != nil {
		t.Fatal(err)
	}
	if !cred.Created {
		t.Fatal("expected newly created credentials")
	}
	if cred.User != DefaultLogsUIUser {
		t.Fatalf("user=%q", cred.User)
	}
	if cred.DozzlePassword == "" || cred.GrafanaPassword == "" {
		t.Fatal("expected generated passwords")
	}
	if _, err := os.Stat(p.LogsUICredentialsFile()); err != nil {
		t.Fatal(err)
	}
	usersPath := filepath.Join(p.LogsUIDir(), "config", "dozzle", "users.yml")
	raw, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "password:") {
		t.Fatalf("users.yml missing password: %s", raw)
	}
	// Second call should reuse.
	cred2, err := EnsureCredentials(p)
	if err != nil {
		t.Fatal(err)
	}
	if cred2.Created {
		t.Fatal("second ensure should not recreate")
	}
	if cred2.DozzlePassword != cred.DozzlePassword {
		t.Fatal("password changed unexpectedly")
	}
}

func TestWriteDozzleUsersBcryptVerifies(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: dir, ConfigDir: filepath.Join(dir, "etc")}
	cred := Credentials{User: "admin", DozzlePassword: "s3cret-pass"}
	if err := writeDozzleUsers(p, cred); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(p.LogsUIDir(), "config", "dozzle", "users.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Extract hash line roughly.
	var hash string
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "password:") {
			hash = strings.TrimSpace(strings.TrimPrefix(line, "password:"))
			hash = strings.Trim(hash, `"'`)
			break
		}
	}
	if hash == "" {
		t.Fatalf("no hash in %s", raw)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("s3cret-pass")); err != nil {
		t.Fatal(err)
	}
}

func TestComposeHasHealthchecksAndSecureDefaults(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: dir}
	if err := Materialize(p); err != nil {
		t.Fatal(err)
	}
	dozzle, err := os.ReadFile(DozzleComposeFile(p))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dozzle), "DOZZLE_AUTH_PROVIDER: simple") {
		t.Fatal("dozzle must enable simple auth")
	}
	if !strings.Contains(string(dozzle), "no-new-privileges") {
		t.Fatal("dozzle missing security_opt")
	}
	if !strings.Contains(string(dozzle), "127.0.0.1") {
		t.Fatal("dozzle must bind localhost")
	}

	full, err := os.ReadFile(FullComposeFile(p))
	if err != nil {
		t.Fatal(err)
	}
	s := string(full)
	for _, want := range []string{
		"healthcheck:",
		"GF_AUTH_ANONYMOUS_ENABLED: \"false\"",
		"GF_USERS_ALLOW_SIGN_UP: \"false\"",
		"QABAS_GRAFANA_PASSWORD",
		"no-new-privileges",
		"condition: service_healthy",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("full compose missing %q", want)
		}
	}
	if strings.Contains(s, "GF_AUTH_ANONYMOUS_ENABLED: \"true\"") {
		t.Fatal("anonymous grafana must stay disabled")
	}
	// Loki must not publish a host port.
	if strings.Contains(s, "QABAS_LOKI_PORT") {
		t.Fatal("loki should not be published on the host")
	}
}
