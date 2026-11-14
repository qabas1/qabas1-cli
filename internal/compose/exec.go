package compose

import (
	"bytes"
	"io"
	"os"
	"path/filepath"

	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

func (r *Runtime) Logs(p paths.Paths, env []string, service string, follow bool) error {
	args := []string{"logs", "--tail", "200"}
	if follow {
		args = append(args, "-f")
	}
	if service != "" {
		args = append(args, service)
	}
	return r.docker(p, env, args...)
}

func (r *Runtime) Exec(p paths.Paths, env []string, service string, cmd ...string) (string, error) {
	args := append([]string{"exec", "-T", service}, cmd...)
	var stdout, stderr bytes.Buffer
	if err := r.dockerOut(p, env, args, &stdout, &stderr); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

func (r *Runtime) ExecToFile(p paths.Paths, env []string, service string, out io.Writer, cmd ...string) error {
	args := append([]string{"exec", "-T", service}, cmd...)
	return r.dockerIO(p, env, args, nil, out, nil)
}

func (r *Runtime) ExecFromFile(p paths.Paths, env []string, service string, in io.Reader, cmd ...string) error {
	args := append([]string{"exec", "-T", service}, cmd...)
	return r.dockerIO(p, env, args, in, nil, nil)
}

func (r *Runtime) RunOneOff(p paths.Paths, env []string, service string, command string, runEnv ...string) error {
	args := []string{}
	overrideArgs, err := oneOffComposeOverrideArgs(p, service)
	if err != nil {
		return err
	}
	args = append(args, overrideArgs...)
	args = append(args, "run", "--rm", "--remove-orphans")
	for _, e := range runEnv {
		args = append(args, "-e", e)
	}
	args = append(args, "--entrypoint", "sh", service, "-ec", command)
	return r.docker(p, env, args...)
}

// RunOneOffInteractive attaches a TTY so commands that prompt (e.g. db seed) can confirm.
func (r *Runtime) RunOneOffInteractive(p paths.Paths, env []string, service string, command string, runEnv ...string) error {
	args := []string{}
	overrideArgs, err := oneOffComposeOverrideArgs(p, service)
	if err != nil {
		return err
	}
	args = append(args, overrideArgs...)
	args = append(args, "run", "--rm", "--remove-orphans", "-it")
	for _, e := range runEnv {
		args = append(args, "-e", e)
	}
	args = append(args, "--entrypoint", "sh", service, "-ec", command)
	return r.dockerIO(p, env, args, os.Stdin, os.Stdout, os.Stderr)
}

// migrateMemoryOverrideYAML bumps migrate above the legacy 256m init limit so
// Node+Prisma seed does not get OOM-killed (exit 137). Applied via an extra
// `docker compose -f` merge — not via `run --memory`, which older Compose rejects.
const migrateMemoryOverrideYAML = `services:
  migrate:
    mem_limit: 1g
    cpus: "1.0"
    pids_limit: 256
`

func oneOffComposeOverrideArgs(p paths.Paths, service string) ([]string, error) {
	if service != "migrate" {
		return nil, nil
	}
	path := p.ComposeMigrateMemoryOverride()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, exitcode.Wrap(exitcode.Generic, err)
	}
	if err := os.WriteFile(path, []byte(migrateMemoryOverrideYAML), 0o644); err != nil {
		return nil, exitcode.Wrap(exitcode.Generic, err)
	}
	return []string{"-f", path}, nil
}

func (r *Runtime) RunService(p paths.Paths, env []string, service string) error {
	return r.docker(p, env, "run", "--rm", "--remove-orphans", service)
}
