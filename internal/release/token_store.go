package release

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

// SaveGitHubToken writes the PAT to the system config path so sudo qabas works
// without re-exporting env. Falls back to the user config dir if system path
// is not writable.
func SaveGitHubToken(token string) (string, error) {
	return saveSecret(githubTokenFile, token, "github token")
}

// SaveGHCRToken writes the GHCR PAT to the system config path (user fallback).
func SaveGHCRToken(token string) (string, error) {
	return saveSecret(ghcrTokenFile, token, "ghcr token")
}

// SaveGHCRUsername writes the GHCR docker username to the system config path (user fallback).
func SaveGHCRUsername(username string) (string, error) {
	return saveSecret(ghcrUsernameFile, username, "ghcr username")
}

func saveSecret(basename, value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is empty", label)
	}
	sys := filepath.Join(paths.Resolve().ConfigDir, basename)
	if err := writeTokenFile(sys, value); err == nil {
		return sys, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("write %s: %w", sys, err)
	}
	userPath := filepath.Join(home, ".config", "qabas", basename)
	if err := writeTokenFile(userPath, value); err != nil {
		return "", err
	}
	// The token is already 0600; the directory DACL is defense in depth.
	_ = paths.RestrictPrivate(filepath.Dir(userPath))
	return userPath, nil
}

// UnsetGitHubToken removes the system GitHub token file.
func UnsetGitHubToken() error {
	return removeFile(SystemTokenPath())
}

// UnsetGHCR removes the system GHCR token and username files.
func UnsetGHCR() error {
	if err := removeFile(SystemGHCRTokenPath()); err != nil {
		return err
	}
	return removeFile(SystemGHCRUsernamePath())
}

// UnsetKind removes system files for the given credential kind.
func UnsetKind(kind Kind) error {
	switch kind {
	case KindGitHub:
		return UnsetGitHubToken()
	case KindGHCR:
		return UnsetGHCR()
	default:
		return fmt.Errorf("unknown auth kind %q (want github or ghcr)", kind)
	}
}

// ParseKind validates a CLI kind argument.
func ParseKind(s string) (Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "github", "gh":
		return KindGitHub, nil
	case "ghcr", "registry", "docker":
		return KindGHCR, nil
	default:
		return "", fmt.Errorf("unknown auth kind %q (want github or ghcr)", s)
	}
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func writeTokenFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
