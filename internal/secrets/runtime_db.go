package secrets

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

// prepareRuntimeDBEnv supplies the Compose role initializer without changing
// live API credentials before the role has been created.
func prepareRuntimeDBEnv(p paths.Paths, b Bundle) error {
	if b.PostgresAppPassword == "" {
		return fmt.Errorf("runtime DB credential is missing")
	}
	path := p.EnvCompose()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, line := range lines {
		key, _, ok := strings.Cut(line, "=")
		if ok && key == "POSTGRES_APP_PASSWORD" {
			lines[i] = key + "=" + b.PostgresAppPassword
			found = true
		}
	}
	if !found {
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		lines = append(lines, "POSTGRES_APP_PASSWORD="+b.PostgresAppPassword, "")
	}
	updated := strings.Join(lines, "\n")
	if updated != string(raw) {
		return writeAtomic(path, []byte(updated))
	}
	return nil
}

// UpgradeRuntimeDBEnv runs only after db-runtime-role has completed. It
// upgrades existing API and worker URLs while preserving other settings.
func UpgradeRuntimeDBEnv(p paths.Paths, b Bundle) error {
	if b.PostgresAppPassword == "" {
		return fmt.Errorf("runtime DB credential is missing")
	}
	type change struct {
		path              string
		original, updated []byte
	}
	var changes []change
	for _, path := range []string{p.EnvAPI(), p.EnvWorker()} {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			if key != "POSTGRES_URI" && key != "DATABASE_URL" {
				continue
			}
			u, err := url.Parse(value)
			if err != nil || u.User == nil {
				return fmt.Errorf("invalid %s in %s", key, path)
			}
			password, _ := u.User.Password()
			if u.User.Username() != "qabasone_app" && password != b.PostgresPassword {
				return fmt.Errorf("unrecognized database role in %s; runtime role cutover needs operator review", path)
			}
			u.User = url.UserPassword("qabasone_app", b.PostgresAppPassword)
			lines[i] = key + "=" + u.String()
		}
		updated := strings.Join(lines, "\n")
		if updated != string(raw) {
			changes = append(changes, change{path, raw, []byte(updated)})
		}
	}
	for i, c := range changes {
		if err := writeAtomic(c.path, c.updated); err != nil {
			for j := i - 1; j >= 0; j-- {
				_ = writeAtomic(changes[j].path, changes[j].original)
			}
			return fmt.Errorf("upgrade runtime DB credentials in %s: %w", c.path, err)
		}
	}
	return nil
}
