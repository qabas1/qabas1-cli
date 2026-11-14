package backup

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

// PostgresTarget is the live container + credentials used for vpg backup.
type PostgresTarget struct {
	Container     string
	Username      string
	Password      string
	MaintenanceDB string
}

// ResolvePostgresTarget loads credentials from Qabas env files and discovers the
// running postgres container name. Defaults match standard QabasOne installs.
func ResolvePostgresTarget(p paths.Paths, rt *compose.Runtime, imageEnv []string) (PostgresTarget, error) {
	t := PostgresTarget{
		Container:     DefaultContainer,
		Username:      DefaultUsername,
		MaintenanceDB: DefaultMaintenanceDB,
	}

	env := loadMergedEnv(
		p.SecretsFile(),
		p.EnvCompose(),
		p.EnvMigrate(),
		p.EnvAPI(),
		p.EnvWorker(),
	)

	if v := env["POSTGRES_USER"]; v != "" {
		t.Username = v
	}
	if v := env["POSTGRES_DB"]; v != "" {
		t.MaintenanceDB = v
	}
	if v := env["POSTGRES_PASSWORD"]; v != "" {
		t.Password = v
	}
	// Fall back to URI fields when explicit keys are absent.
	if uri := firstNonEmpty(env["POSTGRES_URI"], env["DATABASE_URL"]); uri != "" {
		if u, err := url.Parse(uri); err == nil && u.User != nil {
			if env["POSTGRES_USER"] == "" {
				if name := u.User.Username(); name != "" {
					t.Username = name
				}
			}
			if t.Password == "" {
				if pw, ok := u.User.Password(); ok {
					t.Password = pw
				}
			}
			if env["POSTGRES_DB"] == "" {
				if db := strings.TrimPrefix(u.Path, "/"); db != "" {
					t.MaintenanceDB = db
				}
			}
		}
	}

	if t.Password == "" {
		return t, exitcode.NewHint(exitcode.BackupRestore,
			"ensure POSTGRES_PASSWORD is set in "+p.SecretsFile(),
			"PostgreSQL password not found for vpg backup")
	}

	if name := postgresContainerName(rt, p, imageEnv); name != "" {
		t.Container = name
	}
	return t, nil
}

func postgresContainerName(rt *compose.Runtime, p paths.Paths, imageEnv []string) string {
	if rt == nil || rt.LookPath == nil {
		return ""
	}
	statuses, err := rt.ServiceStatus(p, imageEnv, "")
	if err != nil {
		return ""
	}
	for _, s := range statuses {
		if s.Service == "postgres" && s.Name != "" {
			return s.Name
		}
	}
	return ""
}

func loadMergedEnv(files ...string) map[string]string {
	out := map[string]string{}
	for _, path := range files {
		m, err := parseDotEnv(path)
		if err != nil {
			continue
		}
		for k, v := range m {
			if _, exists := out[k]; !exists && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

func parseDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		if k != "" {
			out[k] = v
		}
	}
	return out, sc.Err()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ScopeHelp documents what qabas backup includes and excludes.
const ScopeHelp = `Included:
  - PostgreSQL full cluster via vpg (all databases, roles; role password hashes)
  - MinIO / object-storage volume data (when S3 is included)
  - jsreport Studio templates, assets, fonts and scripts (with S3 when Studio is installed)

Not included:
  - Redis data
  - /etc/qabasone config and secrets (config.yaml, secrets.env, compose env)
  - Compose files, release metadata, container images, host OS state

Treat backup directories as secrets (they may contain role password hashes).`

// RestoreWarning is shown before destructive restore confirmation.
func RestoreWarning(includeDB, includeS3 bool) string {
	switch {
	case includeDB && includeS3:
		return fmt.Sprintf("DESTRUCTIVE: replace the FULL PostgreSQL cluster (all databases/roles via vpg), MinIO object storage, and jsreport Studio data when present in this backup. This is not a single-table or partial restore.\n\n%s", ScopeHelp)
	case includeDB:
		return "DESTRUCTIVE: replace the FULL PostgreSQL cluster (all databases and roles) via vpg. This is not a single-table or partial-database restore."
	case includeS3:
		return "DESTRUCTIVE: replace MinIO / object-storage volume data and jsreport Studio data when present in this backup."
	default:
		return "DESTRUCTIVE: replace PostgreSQL cluster and/or object storage from this backup."
	}
}
