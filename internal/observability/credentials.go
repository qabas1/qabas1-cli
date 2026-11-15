package observability

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const (
	EnvGrafanaUser     = "QABAS_GRAFANA_USER"
	EnvGrafanaPassword = "QABAS_GRAFANA_PASSWORD"
	EnvDozzleUser      = "QABAS_DOZZLE_USER"
	EnvDozzlePassword  = "QABAS_DOZZLE_PASSWORD"
	EnvLogsUIUser      = "QABAS_LOGS_UI_USER"

	DefaultLogsUIUser = "admin"
)

// Credentials are localhost auth secrets for Dozzle / Grafana.
type Credentials struct {
	User            string `json:"user"`
	DozzlePassword  string `json:"dozzlePassword,omitempty"`
	GrafanaPassword string `json:"grafanaPassword,omitempty"`
	Path            string `json:"path,omitempty"`
	Created         bool   `json:"created"`
}

type dozzleUsersFile struct {
	Users map[string]dozzleUser `yaml:"users"`
}

type dozzleUser struct {
	Email    string `yaml:"email"`
	Name     string `yaml:"name"`
	Password string `yaml:"password"`
}

// EnsureCredentials loads or creates auth secrets and writes Dozzle users.yml.
func EnsureCredentials(p paths.Paths) (Credentials, error) {
	path := p.LogsUICredentialsFile()
	cred, created, err := loadOrCreateCredentials(path)
	if err != nil {
		return Credentials{}, err
	}
	cred.Path = path
	cred.Created = created
	if err := writeDozzleUsers(p, cred); err != nil {
		return Credentials{}, err
	}
	return cred, nil
}

func loadOrCreateCredentials(path string) (Credentials, bool, error) {
	user := firstNonEmpty(os.Getenv(EnvLogsUIUser), os.Getenv(EnvDozzleUser), os.Getenv(EnvGrafanaUser), DefaultLogsUIUser)
	dozzlePass := strings.TrimSpace(os.Getenv(EnvDozzlePassword))
	grafanaPass := strings.TrimSpace(os.Getenv(EnvGrafanaPassword))

	if raw, err := os.ReadFile(path); err == nil {
		parsed := parseCredentialsEnv(string(raw))
		if parsed.User == "" {
			parsed.User = user
		}
		if dozzlePass != "" {
			parsed.DozzlePassword = dozzlePass
		}
		if grafanaPass != "" {
			parsed.GrafanaPassword = grafanaPass
		}
		if parsed.DozzlePassword == "" || parsed.GrafanaPassword == "" {
			return Credentials{}, false, exitcode.NewHint(exitcode.InvalidInput,
				"remove "+path+" or set QABAS_DOZZLE_PASSWORD / QABAS_GRAFANA_PASSWORD",
				"logs UI credentials file is incomplete")
		}
		return parsed, false, nil
	} else if !os.IsNotExist(err) {
		return Credentials{}, false, exitcode.New(exitcode.Generic, "read credentials: %w", err)
	}

	created := false
	if dozzlePass == "" {
		var err error
		dozzlePass, err = randomPassword(24)
		if err != nil {
			return Credentials{}, false, err
		}
		created = true
	}
	if grafanaPass == "" {
		var err error
		grafanaPass, err = randomPassword(24)
		if err != nil {
			return Credentials{}, false, err
		}
		created = true
	}
	cred := Credentials{
		User:            user,
		DozzlePassword:  dozzlePass,
		GrafanaPassword: grafanaPass,
	}
	if err := writeCredentialsFile(path, cred); err != nil {
		return Credentials{}, false, err
	}
	return cred, created, nil
}

// RotateCredentials writes new Dozzle/Grafana passwords and, when the logs UI
// is running, restarts it and resets the Grafana admin password (Grafana only
// reads GF_SECURITY_ADMIN_PASSWORD on first start).
func (r *Runtime) RotateCredentials(p paths.Paths) (Credentials, bool, error) {
	if os.Getenv(EnvDozzlePassword) != "" || os.Getenv(EnvGrafanaPassword) != "" {
		return Credentials{}, false, exitcode.New(exitcode.InvalidInput,
			"logs UI passwords come from %s/%s; change them there", EnvDozzlePassword, EnvGrafanaPassword)
	}
	old := parseCredentialsEnv(readFileString(p.LogsUICredentialsFile()))
	user := firstNonEmpty(old.User, os.Getenv(EnvLogsUIUser), DefaultLogsUIUser)
	dozzle, err := randomPassword(24)
	if err != nil {
		return Credentials{}, false, err
	}
	grafana, err := randomPassword(24)
	if err != nil {
		return Credentials{}, false, err
	}
	cred := Credentials{User: user, DozzlePassword: dozzle, GrafanaPassword: grafana, Path: p.LogsUICredentialsFile(), Created: true}
	if err := writeCredentialsFile(cred.Path, cred); err != nil {
		return cred, false, err
	}
	if err := writeDozzleUsers(p, cred); err != nil {
		return cred, false, err
	}
	st, err := r.Status(p)
	if err != nil || !st.Running {
		return cred, false, nil
	}
	if _, _, err := r.Up(p, Options{Full: st.Full}); err != nil {
		return cred, true, err
	}
	if st.Full {
		var stderr bytes.Buffer
		err := r.docker().RunIO("docker", []string{"exec", "-i", "qabasone-grafana", "grafana", "cli", "admin", "reset-admin-password", "--password-from-stdin"},
			os.Environ(), strings.NewReader(grafana+"\n"), io.Discard, &stderr)
		if err != nil {
			return cred, true, exitcode.New(exitcode.Generic, "reset Grafana admin password: %s", strings.TrimSpace(stderr.String()+" "+err.Error()))
		}
	}
	return cred, true, nil
}

func readFileString(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func writeCredentialsFile(path string, c Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return exitcode.New(exitcode.Generic, "create credentials dir: %w", err)
	}
	body := fmt.Sprintf(`# qabas logs UI credentials (mode 0600). Do not commit.
# Generated for localhost Dozzle / Grafana auth.
QABAS_LOGS_UI_USER=%s
QABAS_DOZZLE_PASSWORD=%s
QABAS_GRAFANA_PASSWORD=%s
`, c.User, c.DozzlePassword, c.GrafanaPassword)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return exitcode.New(exitcode.Generic, "write credentials: %w", err)
	}
	return nil
}

func parseCredentialsEnv(raw string) Credentials {
	c := Credentials{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case EnvLogsUIUser, EnvDozzleUser, EnvGrafanaUser:
			c.User = val
		case EnvDozzlePassword:
			c.DozzlePassword = val
		case EnvGrafanaPassword:
			c.GrafanaPassword = val
		}
	}
	return c
}

func writeDozzleUsers(p paths.Paths, c Credentials) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(c.DozzlePassword), bcrypt.DefaultCost)
	if err != nil {
		return exitcode.New(exitcode.Generic, "hash dozzle password: %w", err)
	}
	doc := dozzleUsersFile{
		Users: map[string]dozzleUser{
			c.User: {
				Email:    "logs-ui@localhost",
				Name:     "QabasOne Logs",
				Password: string(hash),
			},
		},
	}
	b, err := yaml.Marshal(&doc)
	if err != nil {
		return exitcode.New(exitcode.Generic, "marshal dozzle users: %w", err)
	}
	path := filepath.Join(p.LogsUIDir(), "config", "dozzle", "users.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return exitcode.New(exitcode.Generic, "create dozzle config dir: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return exitcode.New(exitcode.Generic, "write dozzle users: %w", err)
	}
	return nil
}

func randomPassword(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", exitcode.New(exitcode.Generic, "generate password: %w", err)
	}
	// URL-safe, no padding — easy to paste into login forms.
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func credentialsEnv(c Credentials) []string {
	return []string{
		EnvLogsUIUser + "=" + c.User,
		EnvDozzleUser + "=" + c.User,
		EnvDozzlePassword + "=" + c.DozzlePassword,
		EnvGrafanaUser + "=" + c.User,
		EnvGrafanaPassword + "=" + c.GrafanaPassword,
	}
}
