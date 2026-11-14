package release

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

// Token file basenames under the config dir (and ~/.config/qabas/).
const (
	githubTokenFile  = "github-token"
	ghcrTokenFile    = "ghcr-token"
	ghcrUsernameFile = "ghcr-username"
)

// Kind identifies a stored credential.
type Kind string

const (
	KindGitHub Kind = "github"
	KindGHCR   Kind = "ghcr"
)

// SystemTokenPath is the canonical root-owned GitHub token location used by sudo qabas.
func SystemTokenPath() string {
	return filepath.Join(paths.Resolve().ConfigDir, githubTokenFile)
}

// SystemGHCRTokenPath is the canonical root-owned GHCR token location.
func SystemGHCRTokenPath() string {
	return filepath.Join(paths.Resolve().ConfigDir, ghcrTokenFile)
}

// SystemGHCRUsernamePath is the canonical root-owned GHCR username location.
func SystemGHCRUsernamePath() string {
	return filepath.Join(paths.Resolve().ConfigDir, ghcrUsernameFile)
}

// GitHubToken returns a PAT from (in order):
//  1. GITHUB_TOKEN / GH_TOKEN
//  2. QABAS_GITHUB_TOKEN_FILE
//  3. /etc/qabasone/github-token (or Windows config dir) — survives sudo
//  4. SUDO_USER ~/.config/qabas/github-token (when running as root via sudo)
//  5. current user ~/.config/qabas/github-token
//  6. `gh auth token` for the invoking user (best-effort)
func GitHubToken() string {
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := os.Getenv("GH_TOKEN"); v != "" {
		return strings.TrimSpace(v)
	}
	if file := os.Getenv("QABAS_GITHUB_TOKEN_FILE"); file != "" {
		if v := readTokenFile(file); v != "" {
			return v
		}
	}
	if v := readTokenFile(SystemTokenPath()); v != "" {
		return v
	}
	if v := readTokenFile(sudoUserConfigFile(githubTokenFile)); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		if v := readTokenFile(filepath.Join(home, ".config", "qabas", githubTokenFile)); v != "" {
			return v
		}
	}
	if v := ghAuthToken(); v != "" {
		return v
	}
	return ""
}

// GHCRToken returns a registry PAT from (in order):
//  1. GHCR_TOKEN / QABAS_GHCR_TOKEN
//  2. QABAS_GHCR_TOKEN_FILE
//  3. /etc/qabasone/ghcr-token
//  4. SUDO_USER ~/.config/qabas/ghcr-token
//  5. current user ~/.config/qabas/ghcr-token
//  6. GitHubToken() fallback (same PAT with read:packages)
func GHCRToken() string {
	if v := ghcrTokenDedicated(); v != "" {
		return v
	}
	return GitHubToken()
}

func ghcrTokenDedicated() string {
	if v := os.Getenv("GHCR_TOKEN"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := os.Getenv("QABAS_GHCR_TOKEN"); v != "" {
		return strings.TrimSpace(v)
	}
	if file := os.Getenv("QABAS_GHCR_TOKEN_FILE"); file != "" {
		if v := readTokenFile(file); v != "" {
			return v
		}
	}
	if v := readTokenFile(SystemGHCRTokenPath()); v != "" {
		return v
	}
	if v := readTokenFile(sudoUserConfigFile(ghcrTokenFile)); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		if v := readTokenFile(filepath.Join(home, ".config", "qabas", ghcrTokenFile)); v != "" {
			return v
		}
	}
	return ""
}

// GHCRUsername returns the docker login username for ghcr.io from (in order):
//  1. GHCR_USERNAME / GITHUB_USER
//  2. /etc/qabasone/ghcr-username
//  3. SUDO_USER ~/.config/qabas/ghcr-username
//  4. current user ~/.config/qabas/ghcr-username
//  5. `gh api user -q .login` (best-effort)
func GHCRUsername() string {
	if v := os.Getenv("GHCR_USERNAME"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := os.Getenv("GITHUB_USER"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := readTokenFile(SystemGHCRUsernamePath()); v != "" {
		return v
	}
	if v := readTokenFile(sudoUserConfigFile(ghcrUsernameFile)); v != "" {
		return v
	}
	if home, err := os.UserHomeDir(); err == nil {
		if v := readTokenFile(filepath.Join(home, ".config", "qabas", ghcrUsernameFile)); v != "" {
			return v
		}
	}
	if v := ghAPIUserLogin(); v != "" {
		return v
	}
	return ""
}

// TokenSource describes where GitHubToken would be loaded from (for qabas auth status).
func TokenSource() (pathOrEnv string, ok bool) {
	return GitHubTokenSource()
}

// GitHubTokenSource describes where GitHubToken would be loaded from.
func GitHubTokenSource() (pathOrEnv string, ok bool) {
	if os.Getenv("GITHUB_TOKEN") != "" {
		return "GITHUB_TOKEN", true
	}
	if os.Getenv("GH_TOKEN") != "" {
		return "GH_TOKEN", true
	}
	if file := os.Getenv("QABAS_GITHUB_TOKEN_FILE"); file != "" && readTokenFile(file) != "" {
		return file, true
	}
	if p := SystemTokenPath(); readTokenFile(p) != "" {
		return p, true
	}
	if p := sudoUserConfigFile(githubTokenFile); p != "" && readTokenFile(p) != "" {
		return p, true
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "qabas", githubTokenFile)
		if readTokenFile(p) != "" {
			return p, true
		}
	}
	if ghAuthToken() != "" {
		return "gh auth token", true
	}
	return "", false
}

// GHCRTokenSource describes where GHCRToken would be loaded from (includes github fallback).
func GHCRTokenSource() (pathOrEnv string, ok bool) {
	if os.Getenv("GHCR_TOKEN") != "" {
		return "GHCR_TOKEN", true
	}
	if os.Getenv("QABAS_GHCR_TOKEN") != "" {
		return "QABAS_GHCR_TOKEN", true
	}
	if file := os.Getenv("QABAS_GHCR_TOKEN_FILE"); file != "" && readTokenFile(file) != "" {
		return file, true
	}
	if p := SystemGHCRTokenPath(); readTokenFile(p) != "" {
		return p, true
	}
	if p := sudoUserConfigFile(ghcrTokenFile); p != "" && readTokenFile(p) != "" {
		return p, true
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "qabas", ghcrTokenFile)
		if readTokenFile(p) != "" {
			return p, true
		}
	}
	if src, ok := GitHubTokenSource(); ok {
		return "github token (" + src + ")", true
	}
	return "", false
}

// GHCRUsernameSource describes where GHCRUsername would be loaded from.
func GHCRUsernameSource() (pathOrEnv string, ok bool) {
	if os.Getenv("GHCR_USERNAME") != "" {
		return "GHCR_USERNAME", true
	}
	if os.Getenv("GITHUB_USER") != "" {
		return "GITHUB_USER", true
	}
	if p := SystemGHCRUsernamePath(); readTokenFile(p) != "" {
		return p, true
	}
	if p := sudoUserConfigFile(ghcrUsernameFile); p != "" && readTokenFile(p) != "" {
		return p, true
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".config", "qabas", ghcrUsernameFile)
		if readTokenFile(p) != "" {
			return p, true
		}
	}
	if ghAPIUserLogin() != "" {
		return "gh api user", true
	}
	return "", false
}
