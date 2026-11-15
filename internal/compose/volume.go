package compose

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
)

// Compose volume keys (see qabas1-deploy compose.yaml). Docker names them
// "{project}_{key}" unless the volume declares an explicit name.
const (
	MinioVolumeKey    = "qabasone_minio_data"
	MinioContainer    = "qabas1-minio"
	MinioDataDir      = "/data"
	JSReportVolumeKey = "qabasone_jsreport_data"
	JSReportContainer = "qabas1-jsreport"
	JSReportDataDir   = "/app/data"
)

// TarVolumesFrom archives containerDir from a service's data volume into
// hostArchive using a helper image that provides tar (MinIO has no tar).
//
// Prefers mounting the named Docker volume directly so backup works when the
// stack is down (containers removed by compose down). Falls back to
// --volumes-from only when the volume cannot be resolved but the container exists.
func (r *Runtime) TarVolumesFrom(container, containerDir, hostArchive string, env []string) error {
	abs, err := filepath.Abs(hostArchive)
	if err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	hostDir := filepath.Dir(abs)
	base := filepath.Base(abs)
	if err := os.MkdirAll(hostDir, 0o750); err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}

	volume, volErr := r.ResolveDataVolume(container, containerDir, env)
	var args []string
	if volErr == nil && volume != "" {
		args = []string{
			"run", "--rm",
			"-v", volume + ":" + containerDir + ":ro",
			"-v", hostDir + ":/out",
			backupHelperImage,
			"tar", "--exclude=./.minio.sys/tmp", "-C", containerDir, "-cf", "/out/" + base, ".",
		}
	} else if r.containerExists(container, env) {
		args = []string{
			"run", "--rm",
			"--volumes-from", container,
			"-v", hostDir + ":/out",
			backupHelperImage,
			"tar", "--exclude=./.minio.sys/tmp", "-C", containerDir, "-cf", "/out/" + base, ".",
		}
	} else {
		msg := "object-storage data volume not found"
		if volErr != nil {
			msg = volErr.Error()
		}
		return exitcode.New(exitcode.BackupRestore, "volume archive: %s", msg)
	}

	var stderr bytes.Buffer
	if err := r.rawDocker(env, args, nil, &stderr); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return exitcode.New(exitcode.BackupRestore, "volume archive: %s", msg)
	}
	return nil
}

// UntarVolumesFrom extracts hostArchive into containerDir on the service data
// volume. Uses a named-volume mount so restore works when containers are absent.
func (r *Runtime) UntarVolumesFrom(container, containerDir, hostArchive string, env []string) error {
	abs, err := filepath.Abs(hostArchive)
	if err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	hostDir := filepath.Dir(abs)
	base := filepath.Base(abs)

	volume, volErr := r.ResolveDataVolume(container, containerDir, env)
	var args []string
	if volErr == nil && volume != "" {
		args = []string{
			"run", "--rm",
			"-v", volume + ":" + containerDir,
			"-v", hostDir + ":/in:ro",
			backupHelperImage,
			"tar", "-C", containerDir, "-xf", "/in/" + base,
		}
	} else if r.containerExists(container, env) {
		args = []string{
			"run", "--rm",
			"--volumes-from", container,
			"-v", hostDir + ":/in:ro",
			backupHelperImage,
			"tar", "-C", containerDir, "-xf", "/in/" + base,
		}
	} else {
		msg := "object-storage data volume not found"
		if volErr != nil {
			msg = volErr.Error()
		}
		return exitcode.New(exitcode.BackupRestore, "volume restore: %s", msg)
	}

	var stderr bytes.Buffer
	if err := r.rawDocker(env, args, nil, &stderr); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return exitcode.New(exitcode.BackupRestore, "volume restore: %s", msg)
	}
	return nil
}

const backupHelperImage = "alpine:3.20"

// ResolveDataVolume finds the Docker volume mounted at dest for container.
// When the container is gone (compose down), falls back to the compose project
// volume naming convention for known services (e.g. MinIO).
func (r *Runtime) ResolveDataVolume(container, dest string, env []string) (string, error) {
	if name := r.inspectMountName(container, dest, env); name != "" {
		return name, nil
	}
	key := volumeKeyForContainer(container)
	if key == "" {
		return "", exitcode.New(exitcode.BackupRestore,
			"no data volume mapping for container %s (is the stack installed?)", container)
	}
	candidates := []string{
		ProjectName + "_" + key, // compose default: project_volumekey
		key,                     // explicit volume name, if ever set
	}
	for _, name := range candidates {
		if r.volumeExists(name, env) {
			return name, nil
		}
	}
	return "", exitcode.New(exitcode.BackupRestore,
		"docker volume for %s not found (tried %s); start the stack once or check `docker volume ls`",
		container, strings.Join(candidates, ", "))
}

func volumeKeyForContainer(container string) string {
	switch {
	case container == MinioContainer || strings.HasSuffix(container, "-minio"):
		return MinioVolumeKey
	case container == JSReportContainer || strings.HasSuffix(container, "-jsreport"):
		return JSReportVolumeKey
	default:
		return ""
	}
}

func (r *Runtime) containerExists(name string, env []string) bool {
	var stdout, stderr bytes.Buffer
	err := r.rawDocker(env, []string{"inspect", "-f", "{{.Id}}", name}, &stdout, &stderr)
	return err == nil && strings.TrimSpace(stdout.String()) != ""
}

func (r *Runtime) volumeExists(name string, env []string) bool {
	var stdout, stderr bytes.Buffer
	err := r.rawDocker(env, []string{"volume", "inspect", "-f", "{{.Name}}", name}, &stdout, &stderr)
	return err == nil && strings.TrimSpace(stdout.String()) != ""
}

func (r *Runtime) inspectMountName(container, dest string, env []string) string {
	// Format walks Mounts and prints the volume Name when Destination matches.
	format := fmt.Sprintf(
		`{{range .Mounts}}{{if eq .Destination %q}}{{.Name}}{{end}}{{end}}`,
		dest,
	)
	var stdout, stderr bytes.Buffer
	if err := r.rawDocker(env, []string{"inspect", "-f", format, container}, &stdout, &stderr); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

func (r *Runtime) rawDocker(env []string, args []string, stdout, stderr *bytes.Buffer) error {
	if _, err := r.LookPath("docker"); err != nil {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	if len(env) == 0 {
		env = os.Environ()
	}
	return r.Run("docker", args, env, stdout, stderr)
}
