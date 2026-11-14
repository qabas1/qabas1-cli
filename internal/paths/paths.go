package paths

import (
	"os"
	"path/filepath"
)

const (
	EnvConfigDir = "QABAS_CONFIG_DIR"
	EnvStateDir  = "QABAS_STATE_DIR"
	EnvLogDir    = "QABAS_LOG_DIR"
	EnvLockPath  = "QABAS_LOCK_PATH"
	EnvDataDir   = "QABAS_DATA_DIR"
)

type Paths struct {
	ConfigDir string
	StateDir  string
	LogDir    string
	LockPath  string
}

func Resolve() Paths {
	p := defaults()
	if v := os.Getenv(EnvConfigDir); v != "" {
		p.ConfigDir = v
	}
	if v := os.Getenv(EnvStateDir); v != "" {
		p.StateDir = v
	}
	if v := os.Getenv(EnvLogDir); v != "" {
		p.LogDir = v
	}
	if v := os.Getenv(EnvLockPath); v != "" {
		p.LockPath = v
	}
	if v := os.Getenv(EnvDataDir); v != "" && os.Getenv(EnvStateDir) == "" {
		p.StateDir = v
	}
	return p
}

// Defaults returns the OS default layout, ignoring QABAS_* overrides.
func Defaults() Paths { return defaults() }

func (p Paths) ConfigFile() string  { return filepath.Join(p.ConfigDir, "config.yaml") }
func (p Paths) SecretsFile() string { return filepath.Join(p.ConfigDir, "secrets.env") }
func (p Paths) EnvAPI() string      { return filepath.Join(p.ConfigDir, ".env.api") }
func (p Paths) EnvMigrate() string  { return filepath.Join(p.ConfigDir, ".env.migrate") }
func (p Paths) EnvWorker() string   { return filepath.Join(p.ConfigDir, ".env.worker") }
func (p Paths) EnvApp() string      { return filepath.Join(p.ConfigDir, ".env.app") }
func (p Paths) EnvDash() string     { return filepath.Join(p.ConfigDir, ".env.dash") }
func (p Paths) EnvSiraj() string    { return filepath.Join(p.ConfigDir, ".env.siraj") }
func (p Paths) EnvCompose() string  { return filepath.Join(p.ConfigDir, ".env") }
func (p Paths) StateFile() string   { return filepath.Join(p.StateDir, "state.json") }
func (p Paths) HistoryFile() string { return filepath.Join(p.StateDir, "history.jsonl") }
func (p Paths) Deployments() string { return filepath.Join(p.StateDir, "deployments") }
func (p Paths) Releases() string    { return filepath.Join(p.StateDir, "releases") }
func (p Paths) Backups() string     { return filepath.Join(p.StateDir, "backups") }
func (p Paths) ComposeDir() string  { return filepath.Join(p.StateDir, "compose") }
func (p Paths) ComposeFile() string { return filepath.Join(p.ComposeDir(), "compose.yaml") }

// LogsUIDir holds materialized observability compose assets (Dozzle / Loki / Grafana).
func (p Paths) LogsUIDir() string { return filepath.Join(p.ComposeDir(), "logs-ui") }

// LogsUICredentialsFile stores generated localhost auth for Dozzle / Grafana (mode 0600).
func (p Paths) LogsUICredentialsFile() string {
	return filepath.Join(p.ConfigDir, "logs-ui.credentials")
}

// ComposeFileNext is the staging path for a compose bundle validated before SWITCH.
func (p Paths) ComposeFileNext() string {
	return filepath.Join(p.ComposeDir(), "compose.yaml.next")
}

// ComposeMigrateMemoryOverride raises migrate one-off RAM for seed (old stacks used 256m).
func (p Paths) ComposeMigrateMemoryOverride() string {
	return filepath.Join(p.ComposeDir(), "compose.migrate-memory.yaml")
}
func (p Paths) LogFile() string { return filepath.Join(p.LogDir, "qabas.log") }

func (p Paths) EnsureRuntime() error {
	dirs := []string{
		p.ConfigDir,
		p.StateDir,
		p.LogDir,
		filepath.Dir(p.LockPath),
		p.Deployments(),
		p.Releases(),
		p.Backups(),
		p.ComposeDir(),
	}
	for _, d := range dirs {
		if d == "" || d == "." {
			continue
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	normalizeRootOwnership(p)
	secureRuntime(p)
	return nil
}
