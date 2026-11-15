package compose

import (
	"bytes"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/manifest"
)

// RequiredAPIWorkerEntrypoints are Node entry files that compose workers run from
// the shared qabasone-api image. Missing any of these causes crash-loops after deploy.
var RequiredAPIWorkerEntrypoints = []string{
	"dist/modules/driver-app/operations.worker.cli.js",
	"dist/modules/notifications/notifications.worker.cli.js",
	"dist/modules/printables/printables.worker.js",
	"dist/modules/audit/audit.cleanup.js",
}

// VerifyAPIWorkerEntrypoints checks that the pulled API image contains the worker
// CLI files referenced by qabasone-deploy compose services.
func (r *Runtime) VerifyAPIWorkerEntrypoints(rel *manifest.Release, env []string) error {
	if rel == nil {
		return exitcode.New(exitcode.InvalidInput, "release is required to verify API worker entrypoints")
	}
	image := strings.TrimSpace(rel.Components.API.DigestRef())
	if image == "" || !strings.Contains(image, "@") {
		return exitcode.New(exitcode.InvalidInput, "API image digest is required to verify worker entrypoints")
	}
	for _, path := range RequiredAPIWorkerEntrypoints {
		if err := r.verifyImageHasFile(image, path, env); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) verifyImageHasFile(image, path string, env []string) error {
	if _, err := r.LookPath("docker"); err != nil {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	var stderr bytes.Buffer
	args := []string{"run", "--rm", "--entrypoint", "test", image, "-f", path}
	err := r.Run("docker", args, env, nil, &stderr)
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return exitcode.NewHint(exitcode.Compatibility,
			"rebuild and push qabasone-api so the image includes worker CLIs, then redeploy",
			"API image %s is missing required worker entrypoint %s (%s)", image, path, detail)
	}
	return nil
}
