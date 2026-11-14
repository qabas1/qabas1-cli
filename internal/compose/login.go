package compose

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/execx"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
)

const ghcrRegistry = "ghcr.io"

// LoginGHCR authenticates Docker to GitHub Container Registry using the given
// GitHub username and PAT (needs read:packages). Password is passed on stdin.
func (r *Runtime) LoginGHCR(username, token string) error {
	username = strings.TrimSpace(username)
	token = strings.TrimSpace(token)
	if username == "" {
		return exitcode.NewHint(exitcode.InvalidInput,
			"sudo -E qabas auth login",
			"ghcr username is empty")
	}
	if token == "" {
		return exitcode.NewHint(exitcode.InvalidInput,
			"sudo -E qabas auth login",
			"ghcr token is empty")
	}
	if _, err := r.LookPath("docker"); err != nil {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	login := r.Login
	if login == nil {
		login = defaultDockerLogin
	}
	if err := login(ghcrRegistry, username, token); err != nil {
		return exitcode.New(exitcode.Network,
			"docker login %s failed: %v\n\n"+
				"Ensure the token has read:packages and is authorized for the qabasone org (SSO).\n"+
				"Reconfigure: sudo -E qabas auth login",
			ghcrRegistry, err)
	}
	return nil
}

func defaultDockerLogin(registry, username, password string) error {
	cmd := execx.Command("docker", "login", registry, "-u", username, "--password-stdin")
	cmd.Stdin = strings.NewReader(password + "\n")
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
