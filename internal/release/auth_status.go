package release

import (
	"strings"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
)

// AuthStatus is a redacted snapshot of configured credentials.
type AuthStatus struct {
	OK                     bool   `json:"ok"`
	GitHubOK               bool   `json:"githubOk"`
	GitHubSource           string `json:"githubSource,omitempty"`
	GitHubSystemPath       string `json:"githubSystemPath"`
	GHCROK                 bool   `json:"ghcrOk"`
	GHCRSource             string `json:"ghcrSource,omitempty"`
	GHCRSystemPath         string `json:"ghcrSystemPath"`
	GHCRUsername           string `json:"ghcrUsername,omitempty"`
	GHCRUsernameSource     string `json:"ghcrUsernameSource,omitempty"`
	GHCRUsernameSystemPath string `json:"ghcrUsernameSystemPath"`
}

// Status returns whether both required credentials are available (never includes secrets).
func Status() AuthStatus {
	ghSrc, ghOK := GitHubTokenSource()
	regSrc, regOK := GHCRTokenSource()
	userSrc, userOK := GHCRUsernameSource()
	ghcrReady := regOK && userOK
	return AuthStatus{
		OK:                     ghOK && ghcrReady,
		GitHubOK:               ghOK,
		GitHubSource:           ghSrc,
		GitHubSystemPath:       SystemTokenPath(),
		GHCROK:                 ghcrReady,
		GHCRSource:             regSrc,
		GHCRSystemPath:         SystemGHCRTokenPath(),
		GHCRUsername:           GHCRUsername(),
		GHCRUsernameSource:     userSrc,
		GHCRUsernameSystemPath: SystemGHCRUsernamePath(),
	}
}

// RequireAuth fails with remediation hints when GitHub or GHCR credentials are missing.
func RequireAuth() error {
	st := Status()
	if st.OK {
		return nil
	}
	var missing []string
	if !st.GitHubOK {
		missing = append(missing, "github")
	}
	if !st.GHCROK {
		if st.GHCRSource == "" {
			missing = append(missing, "ghcr token")
		} else if st.GHCRUsername == "" {
			missing = append(missing, "ghcr username")
		} else {
			missing = append(missing, "ghcr")
		}
	}
	return exitcode.New(exitcode.InvalidInput,
		"missing required credentials: %s\n\n"+
			"Configure once:\n"+
			"  export GITHUB_TOKEN=\"$(gh auth token)\"\n"+
			"  sudo -E qabas auth login\n"+
			"  sudo qabas install\n\n"+
			"Check: sudo qabas auth status\n"+
			"(PAT needs repo read + read:packages; authorize org SSO if required.)",
		strings.Join(missing, ", "))
}
