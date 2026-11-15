package compose

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func (r *Runtime) Config(p paths.Paths, env []string) error {
	var stderr bytes.Buffer
	return r.dockerOut(p, env, []string{"config", "--quiet"}, nil, &stderr)
}

// ConfigFile validates a compose file at an explicit path (e.g. staging next file).
func (r *Runtime) ConfigFile(p paths.Paths, composeFile string, env []string) error {
	var stderr bytes.Buffer
	return r.dockerOutFile(p, composeFile, env, []string{"config", "--quiet"}, nil, &stderr)
}

func (r *Runtime) Pull(p paths.Paths, env []string) error {
	return r.docker(p, env, "pull")
}

// PullFile pulls images using an explicit compose file (e.g. staged next).
func (r *Runtime) PullFile(p paths.Paths, composeFile string, env []string) error {
	return r.dockerOutFile(p, composeFile, env, []string{"pull"}, nil, nil)
}

// PullFileServices pulls images for the given compose services using an explicit
// compose file (e.g. staged next). With no services, pulls the full stack.
func (r *Runtime) PullFileServices(p paths.Paths, composeFile string, env []string, services ...string) error {
	args := []string{"pull"}
	args = append(args, services...)
	return r.dockerOutFile(p, composeFile, env, args, nil, nil)
}

// PullServices pulls images for the given compose services (live compose file).
// With no services, pulls the full stack.
func (r *Runtime) PullServices(p paths.Paths, env []string, services ...string) error {
	args := []string{"pull"}
	args = append(args, services...)
	return r.docker(p, env, args...)
}

func (r *Runtime) Up(p paths.Paths, env []string) error {
	return r.UpService(p, env, "", false, true)
}

// RunOneShot executes a Compose service's own command and checks its exit
// status. It is used after dependencies have already completed successfully.
func (r *Runtime) RunOneShot(p paths.Paths, env []string, service string) error {
	return r.docker(p, env, "run", "--rm", "--no-deps", service)
}

func (r *Runtime) UpService(p paths.Paths, env []string, service string, build bool, detach bool) error {
	services := []string(nil)
	if service != "" {
		services = []string{service}
	}
	return r.UpServices(p, env, services, build, detach)
}

// UpServices runs compose up for zero or more services (empty = full stack).
func (r *Runtime) UpServices(p paths.Paths, env []string, services []string, build bool, detach bool) error {
	args := []string{"up"}
	if detach {
		args = append(args, "-d")
	}
	if build {
		args = append(args, "--build")
	}
	args = append(args, "--remove-orphans", "--wait", "--wait-timeout", "180")
	args = append(args, services...)
	return r.docker(p, env, args...)
}

// ScaleAPI reconciles only the API service, preserving the rest of the stack.
func (r *Runtime) ScaleAPI(p paths.Paths, env []string, replicas int) error {
	return r.docker(p, env, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "180", "--scale", fmt.Sprintf("api=%d", replicas), "api")
}

func (r *Runtime) APIContainerIDs(p paths.Paths, env []string) ([]string, error) {
	var out, stderr bytes.Buffer
	if err := r.dockerOut(p, env, []string{"ps", "-q", "api"}, &out, &stderr); err != nil {
		return nil, err
	}
	return strings.Fields(out.String()), nil
}

func (r *Runtime) Start(p paths.Paths, env []string, service string) error {
	args := []string{"start"}
	if service != "" {
		args = append(args, service)
	}
	return r.docker(p, env, args...)
}

// CreateService creates containers for service without starting them.
// Idempotent when the container already exists.
func (r *Runtime) CreateService(p paths.Paths, env []string, service string) error {
	if service == "" {
		return exitcode.New(exitcode.InvalidInput, "create requires a service name")
	}
	return r.docker(p, env, "create", service)
}

// EnsureServiceCreated ensures the service container exists (compose create).
// Useful after compose down, when volumes remain but containers were removed.
func (r *Runtime) EnsureServiceCreated(p paths.Paths, env []string, service string) error {
	return r.CreateService(p, env, service)
}

func (r *Runtime) Stop(p paths.Paths, env []string, service string) error {
	args := []string{"stop"}
	if service != "" {
		args = append(args, service)
	}
	return r.docker(p, env, args...)
}

func (r *Runtime) Restart(p paths.Paths, env []string, service string) error {
	if service == "api" {
		ids, err := r.APIContainerIDs(p, env)
		if err != nil {
			return err
		}
		if len(ids) > 1 {
			for _, id := range ids {
				if err := r.restartAPIContainer(id); err != nil {
					return err
				}
			}
			return nil
		}
	}
	args := []string{"restart"}
	if service != "" {
		args = append(args, service)
	}
	return r.docker(p, env, args...)
}

// restartAPIContainer waits for readiness before touching the next replica.
func (r *Runtime) restartAPIContainer(id string) error {
	var out, stderr bytes.Buffer
	if err := r.Run("docker", []string{"restart", id}, os.Environ(), &out, &stderr); err != nil {
		return fmt.Errorf("restart API container %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
	}
	deadline := time.Now().Add(180 * time.Second)
	for time.Now().Before(deadline) {
		out.Reset()
		stderr.Reset()
		if err := r.Run("docker", []string{"inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}", id}, os.Environ(), &out, &stderr); err != nil {
			return fmt.Errorf("inspect API container %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
		}
		if strings.TrimSpace(out.String()) == "healthy" {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("API container %s did not become healthy within 180s; remaining replicas were not restarted", id)
}

func (r *Runtime) Down(p paths.Paths, env []string, volumes bool) error {
	args := []string{"down", "--remove-orphans"}
	if volumes {
		args = append(args, "--volumes")
	}
	return r.docker(p, env, args...)
}

func (r *Runtime) DownService(p paths.Paths, env []string, service string) error {
	if service == "" {
		return r.Down(p, env, false)
	}
	if err := r.Stop(p, env, service); err != nil {
		return err
	}
	return r.docker(p, env, "rm", "-f", service)
}
