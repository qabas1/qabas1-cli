package paths

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Directory and file modes enforced on Unix. ConfigDir stays traversable
// (0711) because postgres bind-mounts pg/ from it as a non-root user;
// StateDir stays 0755 for the same reason (compose and logs-UI config mounts).
const (
	ModeConfigDir  os.FileMode = 0o711
	ModeBackupsDir os.FileMode = 0o700
	ModeLogDir     os.FileMode = 0o750
	ModeSecretFile os.FileMode = 0o600
	ModeLockFile   os.FileMode = 0o600
)

// IsSecretFile reports whether a ConfigDir entry holds credentials.
func IsSecretFile(name string) bool {
	switch {
	case name == "secrets.env", name == "secrets.meta.json":
		return true
	case strings.HasPrefix(name, ".env"):
		return true
	case strings.HasSuffix(name, ".credentials"), strings.HasSuffix(name, ".agekey"), strings.HasSuffix(name, ".key"):
		return true
	case strings.Contains(name, "token"), strings.Contains(name, "username"):
		return true
	}
	return false
}

// SecretFiles lists credential files: top-level ConfigDir secrets plus the
// per-node cluster secrets under nodes/. pg/ and dr/ are skipped: their
// ownership and modes are managed for the postgres container.
func SecretFiles(p Paths) []string {
	var out []string
	entries, _ := os.ReadDir(p.ConfigDir)
	for _, e := range entries {
		if e.Type().IsRegular() && IsSecretFile(e.Name()) {
			out = append(out, filepath.Join(p.ConfigDir, e.Name()))
		}
	}
	_ = filepath.WalkDir(filepath.Join(p.ConfigDir, "nodes"), func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// Harden enforces private permissions on QabasOne directories and secret
// files, returning every failure (it never stops at the first one).
func Harden(p Paths) error {
	var errs []error
	for _, err := range harden(p) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
