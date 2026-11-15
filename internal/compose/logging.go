package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

// LoggingOverlay is the CLI-owned compose overlay that caps container log size.
const LoggingOverlay = "compose.qabas-logging.yaml"

const (
	LogMaxSize  = "50m"
	LogMaxFiles = "5"
)

// DaemonLoggingDriver is the Docker daemon's default log driver ("" if unknown).
// The overlay only applies on json-file daemons, so operators who chose
// journald, local, or a remote driver keep their setup.
func DaemonLoggingDriver(ctx context.Context) string {
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(c, "docker", "info", "--format", "{{.LoggingDriver}}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func LoggingOverlayPath(p paths.Paths) string {
	return filepath.Join(p.ComposeDir(), LoggingOverlay)
}

// LoggingOverlayEnabled reports whether log rotation has been turned on.
func LoggingOverlayEnabled(p paths.Paths) bool {
	_, err := os.Stat(LoggingOverlayPath(p))
	return err == nil
}

// SyncLoggingOverlay (re)writes the logging overlay for services defined in
// every given compose file (each merged with this node's other overlays), so
// the overlay never references a service missing from either the live or the
// staged bundle. Services that already declare logging are left alone.
func SyncLoggingOverlay(p paths.Paths, composeFiles ...string) (changed bool, err error) {
	var common map[string]bool
	for _, main := range composeFiles {
		if _, err := os.Stat(main); err != nil {
			continue
		}
		set, err := loggingCandidates(append([]string{main}, otherOverlays(p)...))
		if err != nil {
			return false, err
		}
		if common == nil {
			common = set
			continue
		}
		for s := range common {
			if !set[s] {
				delete(common, s)
			}
		}
	}
	if common == nil {
		return false, errors.New("no compose bundle to derive logging overlay from")
	}
	services := make([]string, 0, len(common))
	for s, ok := range common {
		if ok {
			services = append(services, s)
		}
	}
	sort.Strings(services)
	body := RenderLoggingOverlay(services)
	if old, err := os.ReadFile(LoggingOverlayPath(p)); err == nil && bytes.Equal(old, body) {
		return false, nodestack.AddDirective(p.ComposeDir(), "file:"+LoggingOverlay)
	}
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(LoggingOverlayPath(p), body, 0o644); err != nil {
		return false, err
	}
	return true, nodestack.AddDirective(p.ComposeDir(), "file:"+LoggingOverlay)
}

// DisableLoggingOverlay removes the overlay file; composeArgs skips missing files.
func DisableLoggingOverlay(p paths.Paths) error {
	err := os.Remove(LoggingOverlayPath(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func otherOverlays(p paths.Paths) []string { return upstreamOverlays(p) }

// loggingCandidates returns services defined across files, marking those that
// already set logging as false.
func loggingCandidates(files []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		var doc struct {
			Services map[string]map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(f), err)
		}
		for name, def := range doc.Services {
			_, hasLogging := def["logging"]
			if prev, seen := out[name]; seen && !prev {
				continue
			}
			out[name] = !hasLogging
		}
	}
	return out, nil
}

// RenderLoggingOverlay renders json-file rotation for each service.
func RenderLoggingOverlay(services []string) []byte {
	var b strings.Builder
	b.WriteString("# Managed by qabas autopilot: rotates container logs so they cannot fill the disk.\n")
	if len(services) == 0 {
		b.WriteString("services: {}\n")
		return []byte(b.String())
	}
	b.WriteString("services:\n")
	for _, s := range services {
		fmt.Fprintf(&b, "  %s:\n    logging:\n      driver: json-file\n      options:\n        max-size: %q\n        max-file: %q\n", s, LogMaxSize, LogMaxFiles)
	}
	return []byte(b.String())
}
