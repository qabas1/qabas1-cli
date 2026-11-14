package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
)

const edgeNetwork = "edge-network"

// EnsureEdgeNetwork restores the shared proxy network after Compose recreates
// containers. Docker's manual network connections are lost on recreation.
func (r *Runtime) EnsureEdgeNetwork(env []string) error {
	if err := r.rawDocker(env, []string{"network", "inspect", edgeNetwork}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		var stderr bytes.Buffer
		if createErr := r.rawDocker(env, []string{"network", "create", edgeNetwork}, &bytes.Buffer{}, &stderr); createErr != nil {
			return exitcode.New(exitcode.Network, "create %s: %s", edgeNetwork, dockerError(createErr, &stderr))
		}
	}
	for _, name := range []string{"qabasone-app", "qabasone-dash", "qabasone-api", "qabasone-minio", "qabasone-siraj", "edge-nginx"} {
		var stdout, stderr bytes.Buffer
		err := r.rawDocker(env, []string{"inspect", "--type", "container", "--format", "{{json .NetworkSettings.Networks}}", name}, &stdout, &stderr)
		if err != nil {
			// The external proxy and services omitted by older Compose bundles
			// need no connection on this host.
			if strings.Contains(stderr.String(), "No such object") || strings.Contains(stderr.String(), "No such container") {
				continue
			}
			return exitcode.New(exitcode.Network, "inspect %s networks: %s", name, dockerError(err, &stderr))
		}
		var networks map[string]json.RawMessage
		if err := json.Unmarshal(stdout.Bytes(), &networks); err != nil {
			return fmt.Errorf("decode %s networks: %w", name, err)
		}
		if _, attached := networks[edgeNetwork]; attached {
			continue
		}
		stderr.Reset()
		if err := r.rawDocker(env, []string{"network", "connect", edgeNetwork, name}, &bytes.Buffer{}, &stderr); err != nil {
			return exitcode.New(exitcode.Network, "connect %s to %s: %s", name, edgeNetwork, dockerError(err, &stderr))
		}
	}
	return nil
}

func dockerError(err error, stderr *bytes.Buffer) string {
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return msg
	}
	return err.Error()
}
