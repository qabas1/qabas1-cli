package compose

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/execx"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

type Runtime struct {
	// ComposeFileOverride is used by pre-deploy backup when the installed
	// compose file is missing but a replacement has been validated and staged.
	ComposeFileOverride string
	LookPath func(string) (string, error)
	Run      func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error
	RunIO    func(name string, args []string, env []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error
	// Login authenticates Docker to a registry (defaults to docker login --password-stdin).
	Login func(registry, username, password string) error
}

func NewRuntime() *Runtime {
	return &Runtime{
		LookPath: exec.LookPath,
		Login:    defaultDockerLogin,
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			cmd := execx.Command(name, args...)
			cmd.Env = env
			if stdout != nil {
				cmd.Stdout = stdout
			} else {
				cmd.Stdout = os.Stdout
			}
			if stderr != nil {
				cmd.Stderr = stderr
			} else {
				cmd.Stderr = os.Stderr
			}
			return cmd.Run()
		},
		RunIO: func(name string, args []string, env []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
			cmd := execx.Command(name, args...)
			cmd.Env = env
			cmd.Stdin = stdin
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			return cmd.Run()
		},
	}
}

func (r *Runtime) Available() bool {
	_, err := r.LookPath("docker")
	return err == nil
}

func ImageEnv(rel *manifest.Release, p paths.Paths) []string {
	env := os.Environ()
	pins := ImagePins(rel)
	for _, k := range imagePinKeys {
		if v := pins[k]; v != "" {
			env = append(env, k+"="+v)
		}
	}
	env = append(env, "QABAS_ENV_DIR="+filepath.ToSlash(p.ConfigDir))
	return env
}

var imagePinKeys = []string{
	"QABASONE_API_IMAGE", "QABASONE_APP_IMAGE", "QABASONE_DASH_IMAGE", "QABASONE_SIRAJ_IMAGE",
	"POSTGRES_IMAGE", "REDIS_IMAGE", "MINIO_IMAGE", "MINIO_MC_IMAGE", "GOTENBERG_IMAGE", "JSREPORT_IMAGE",
}

// ImagePins returns the non-empty image references a release pins (replicas run the
// exact same digests as the writer).
func ImagePins(rel *manifest.Release) map[string]string {
	all := map[string]string{
		"QABASONE_API_IMAGE":   rel.Components.API.DigestRef(),
		"QABASONE_APP_IMAGE":   rel.Components.App.DigestRef(),
		"QABASONE_DASH_IMAGE":  rel.Components.Dash.DigestRef(),
		"QABASONE_SIRAJ_IMAGE": rel.Components.Siraj.DigestRef(),
		"POSTGRES_IMAGE":       rel.Infra.Postgres,
		"REDIS_IMAGE":          rel.Infra.Redis,
		"MINIO_IMAGE":          rel.Infra.Minio,
		"MINIO_MC_IMAGE":       rel.Infra.MinioMc,
		"GOTENBERG_IMAGE":      rel.Infra.Gotenberg,
		"JSREPORT_IMAGE":       rel.Infra.Jsreport,
	}
	out := map[string]string{}
	for k, v := range all {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// EnvForInstalledRelease loads /var/lib/qabasone/releases/<version>.yaml and
// returns ImageEnv. Used by status/doctor/logs so compose interpolation works
// without requiring operators to export QABASONE_*_IMAGE by hand.
func EnvForInstalledRelease(p paths.Paths, version string) []string {
	env, err := EnvForInstalledReleaseChecked(p, version)
	if err == nil {
		return env
	}
	b, err := os.ReadFile(filepath.Join(p.ConfigDir, "dr", "images.env"))
	if err == nil {
		pins := nodestack.ParseEnv(b)
		env := os.Environ()
		for _, k := range imagePinKeys {
			if v := pins[k]; v != "" {
				env = replaceEnv(env, k, v)
			}
		}
		env = append(env, "QABAS_ENV_DIR="+filepath.ToSlash(p.ConfigDir))
		return env
	}
	return os.Environ()
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func EnvForInstalledReleaseChecked(p paths.Paths, version string) ([]string, error) {
	if version == "" {
		return nil, exitcode.New(exitcode.InvalidInput, "QabasOne release is not set")
	}
	b, err := os.ReadFile(filepath.Join(p.Releases(), version+".yaml"))
	if err != nil {
		return nil, exitcode.NewHint(exitcode.InvalidInput,
			"run `sudo qabas deploy "+version+"` to restore release metadata",
			"release metadata for QabasOne %s is missing", version)
	}
	rel, err := manifest.Parse(b)
	if err != nil {
		return nil, exitcode.New(exitcode.InvalidInput, "parse release metadata for QabasOne %s: %w", version, err)
	}
	return ImageEnv(rel, p), nil
}

const ProjectName = "qabasone"

func (r *Runtime) composeArgs(p paths.Paths, extra ...string) []string {
	return r.composeArgsFile(p, r.composeFile(p), extra...)
}

func (r *Runtime) composeFile(p paths.Paths) string {
	if r.ComposeFileOverride != "" {
		return r.ComposeFileOverride
	}
	return p.ComposeFile()
}

func (r *Runtime) composeArgsFile(p paths.Paths, composeFile string, extra ...string) []string {
	args := []string{"compose", "--project-name", ProjectName, "-f", composeFile}
	// DR role overlays (qabas replica / qabas dr) must survive every deploy/up/restart.
	dir := nodestack.ReadDirectives(filepath.Dir(composeFile))
	for _, f := range dir.Files {
		if _, err := os.Stat(f); err == nil {
			args = append(args, "-f", f)
		}
	}
	if _, err := os.Stat(p.EnvCompose()); err == nil {
		args = append(args, "--env-file", p.EnvCompose())
	}
	for _, f := range dir.EnvFiles {
		if _, err := os.Stat(f); err == nil {
			args = append(args, "--env-file", f)
		}
	}
	for _, prof := range dir.Profiles {
		args = append(args, "--profile", prof)
	}
	return append(args, extra...)
}

func (r *Runtime) docker(p paths.Paths, env []string, extra ...string) error {
	return r.dockerOut(p, env, extra, nil, nil)
}

func (r *Runtime) dockerOut(p paths.Paths, env []string, extra []string, stdout, stderr *bytes.Buffer) error {
	return r.dockerOutFile(p, r.composeFile(p), env, extra, stdout, stderr)
}

func (r *Runtime) dockerOutFile(p paths.Paths, composeFile string, env []string, extra []string, stdout, stderr *bytes.Buffer) error {
	if _, err := r.LookPath("docker"); err != nil {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	err := r.Run("docker", r.composeArgsFile(p, composeFile, extra...), env, stdout, stderr)
	if err != nil {
		msg := err.Error()
		if stderr != nil && stderr.Len() > 0 {
			msg = strings.TrimSpace(stderr.String())
		}
		return exitcode.New(exitcode.Network, "docker compose %s: %s", summarizeArgs(extra), msg)
	}
	return nil
}

func (r *Runtime) dockerIO(p paths.Paths, env []string, extra []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
	if _, err := r.LookPath("docker"); err != nil {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	if r.RunIO == nil {
		return exitcode.New(exitcode.InvalidInput, "docker IO runtime is not configured")
	}
	var errBuf bytes.Buffer
	if stderr == nil {
		stderr = &errBuf
	}
	err := r.RunIO("docker", r.composeArgs(p, extra...), env, stdin, stdout, stderr)
	if err != nil {
		msg := err.Error()
		if errBuf.Len() > 0 {
			msg = strings.TrimSpace(errBuf.String())
		}
		return exitcode.New(exitcode.Network, "docker compose %s: %s", summarizeArgs(extra), msg)
	}
	return nil
}

func summarizeArgs(args []string) string {
	s := strings.Join(args, " ")
	if len(s) <= 180 {
		return s
	}
	return s[:180] + "..."
}
