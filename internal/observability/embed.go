package observability

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

const (
	ProjectName = "qabasone-logs"

	EnvLogsUIPort   = "QABAS_LOGS_UI_PORT"
	EnvGrafanaPort  = "QABAS_GRAFANA_PORT"
	EnvLokiPort     = "QABAS_LOKI_PORT"
	EnvDozzleFilter = "DOZZLE_FILTER"
	// EnvLogsRetention bounds how long Loki keeps searchable logs.
	EnvLogsRetention = "QABAS_LOGS_RETENTION"

	DefaultLogsRetention = "7d"
	// LokiVolume is the compose volume holding Loki chunks.
	LokiVolume = ProjectName + "_loki_data"
	// AlloyVolume holds Alloy's positions; volumes created before Alloy ran as
	// AlloyUID are root-owned and must be re-owned before it starts.
	AlloyVolume = ProjectName + "_alloy_data"
	// AlloyImage and AlloyUID must match the alloy service in compose.full.yaml.
	AlloyImage = "ghcr.io/qabasone/vendor/grafana/alloy:v1.7.5"
	AlloyUID   = "473:473"

	DefaultLogsUIPort  = "9999"
	DefaultGrafanaPort = "3001"
	DefaultLokiPort    = "3100"

	DefaultDozzleFilter = "label=com.docker.compose.project=qabasone"

	composeDozzle = "compose.dozzle.yaml"
	composeFull   = "compose.full.yaml"
)

//go:embed compose.dozzle.yaml compose.full.yaml config
var assets embed.FS

// Materialize writes embedded compose/config files under Paths.LogsUIDir().
func Materialize(p paths.Paths) error {
	dir := p.LogsUIDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return exitcode.New(exitcode.Generic, "create logs UI dir: %w", err)
	}
	return fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := assets.ReadFile(path)
		if err != nil {
			return exitcode.New(exitcode.Generic, "read embedded %s: %w", path, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return exitcode.New(exitcode.Generic, "create %s: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, b, 0o644); err != nil {
			return exitcode.New(exitcode.Generic, "write %s: %w", target, err)
		}
		return nil
	})
}

func DozzleComposeFile(p paths.Paths) string {
	return filepath.Join(p.LogsUIDir(), composeDozzle)
}

func FullComposeFile(p paths.Paths) string {
	return filepath.Join(p.LogsUIDir(), composeFull)
}
