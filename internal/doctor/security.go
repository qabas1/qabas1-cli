package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/clilog"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/history"
	"github.com/qabas1/qabas1-cli/internal/observability"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/secrets"
	"github.com/qabas1/qabas1-cli/internal/sysinfo"
)

// Thresholds for log-size warnings.
const (
	ContainerLogsWarnBytes = 5 << 30
	LokiVolumeWarnBytes    = 5 << 30
)

// publicPorts may listen on every interface (the edge reverse proxy).
var publicPorts = map[string]bool{"80": true, "443": true}

// SecurityProbes are the side effects SecurityChecks uses; nil Docker skips
// container checks.
type SecurityProbes struct {
	GOOS   string
	Now    func() time.Time
	Docker func(args ...string) (string, error)
	// LoggingDriver is the daemon's default log driver.
	LoggingDriver string
	// WireGuardConfig is checked for mode 0600 when it exists.
	WireGuardConfig string
	// ContainerLogGlob matches json-file container logs (Linux only).
	ContainerLogGlob string
}

func DefaultSecurityProbes(docker func(args ...string) (string, error), driver string) SecurityProbes {
	return SecurityProbes{
		GOOS: runtime.GOOS, Now: time.Now, Docker: docker, LoggingDriver: driver,
		WireGuardConfig:  "/etc/wireguard/qabas0.conf",
		ContainerLogGlob: "/var/lib/docker/containers/*/*-json.log",
	}
}

// SecurityChecks reports secret hygiene, network exposure, Docker socket
// mounts, log bounds, and resource limits. Every row is info or warn: they
// never fail doctor on their own.
func SecurityChecks(p paths.Paths, installed bool, pr SecurityProbes) []Check {
	if pr.Now == nil {
		pr.Now = time.Now
	}
	var out []Check
	add := func(name string, ok bool, detail string) {
		lvl := "info"
		if !ok {
			lvl = "warn"
		}
		out = append(out, Check{Name: name, OK: ok, Detail: detail, Level: lvl})
	}

	if pr.GOOS != "windows" {
		var loose []string
		for _, f := range paths.SecretFiles(p) {
			if st, err := os.Stat(f); err == nil && st.Mode().Perm()&0o077 != 0 {
				loose = append(loose, fmt.Sprintf("%s (%04o)", filepath.Base(f), st.Mode().Perm()))
			}
		}
		if st, err := os.Stat(p.ConfigDir); err == nil && st.Mode().Perm()&^paths.ModeConfigDir != 0 {
			loose = append(loose, fmt.Sprintf("%s (%04o)", p.ConfigDir, st.Mode().Perm()))
		}
		if st, err := os.Stat(p.LockPath); err == nil && st.Mode().Perm()&^paths.ModeLockFile != 0 {
			loose = append(loose, fmt.Sprintf("%s (%04o)", filepath.Base(p.LockPath), st.Mode().Perm()))
		}
		if len(loose) > 0 {
			add("secret-perms", false, "too open: "+strings.Join(loose, ", ")+" — fix: sudo qabas autopilot run")
		} else {
			add("secret-perms", true, "secret files are owner-only")
		}
		if pr.WireGuardConfig != "" {
			if st, err := os.Stat(pr.WireGuardConfig); err == nil {
				ok := st.Mode().Perm()&0o077 == 0
				d := fmt.Sprintf("%s %04o", pr.WireGuardConfig, st.Mode().Perm())
				if !ok {
					d += " — fix: sudo chmod 600 " + pr.WireGuardConfig
				}
				add("wireguard-key", ok, d)
			}
		}
	}

	if installed {
		meta := secrets.LoadMeta(p)
		var due []string
		for _, s := range secrets.Catalog {
			if stamp, ok := meta[s.Name]; ok && pr.Now().Sub(stamp.Since()) > secrets.MaxAge {
				due = append(due, fmt.Sprintf("%s (%dd)", s.Name, int(pr.Now().Sub(stamp.Since()).Hours()/24)))
			}
		}
		if len(due) > 0 {
			add("secret-age", false, "older than "+fmt.Sprint(int(secrets.MaxAge.Hours()/24))+" days: "+strings.Join(due, ", ")+" — sudo qabas secrets rotate all")
		} else if len(meta) > 0 {
			add("secret-age", true, "all secrets rotated within "+fmt.Sprint(int(secrets.MaxAge.Hours()/24))+" days")
		}
	}

	switch pr.LoggingDriver {
	case "":
	case "json-file":
		if compose.LoggingOverlayEnabled(p) {
			add("log-rotation", true, "container logs rotate at "+compose.LogMaxSize+" x "+compose.LogMaxFiles)
		} else if installed {
			add("log-rotation", false, "container logs are unbounded (json-file without rotation) — run: sudo qabas deploy, or sudo qabas autopilot run")
		}
	case "local", "journald":
		add("log-rotation", true, "Docker "+pr.LoggingDriver+" driver rotates logs itself")
	default:
		add("log-rotation", true, "Docker log driver "+pr.LoggingDriver+" (rotation managed outside QabasOne)")
	}

	own := clilog.Size(p.LogFile(), clilog.Keep) + history.Size(p)
	add("log-sizes", true, "qabas.log + history "+sysinfo.HumanBytes(uint64(max(own, 0)))+" (bounded)")
	if pr.GOOS == "linux" && pr.ContainerLogGlob != "" {
		if files, _ := filepath.Glob(pr.ContainerLogGlob); len(files) > 0 {
			var total int64
			for _, f := range files {
				if st, err := os.Stat(f); err == nil {
					total += st.Size()
				}
			}
			ok := total < ContainerLogsWarnBytes
			d := "container logs " + sysinfo.HumanBytes(uint64(total))
			if !ok {
				d += " — enable rotation: sudo qabas deploy"
			}
			add("container-logs", ok, d)
		}
	}

	if installed {
		switch {
		case compose.LimitsDisabled():
			add("resource-limits", false, "disabled (QABAS_LIMITS=off): one runaway service can exhaust the host")
		case compose.LimitsOverlayEnabled(p):
			add("resource-limits", true, "applied (qabas limits show)")
		default:
			add("resource-limits", false, "not applied yet — sudo qabas limits sync")
		}
	}

	if pr.Docker == nil {
		return out
	}
	if exposed := exposedPorts(pr.Docker); len(exposed) > 0 {
		add("exposure", false, "listening on all interfaces: "+strings.Join(exposed, ", ")+" — bind to 127.0.0.1 or firewall these ports")
	} else {
		add("exposure", true, "only the edge (80/443) is published publicly")
	}
	if mounts := socketMounts(pr.Docker); len(mounts) > 0 {
		add("docker-socket", false, "mounted by "+strings.Join(mounts, ", ")+" (root-equivalent access)")
	} else {
		add("docker-socket", true, "no QabasOne container mounts the Docker socket directly")
	}
	if n := LokiVolumeBytes(pr.Docker); n > 0 {
		ok := n < LokiVolumeWarnBytes
		d := "Loki volume " + sysinfo.HumanBytes(n) + ", retention " + observability.LokiRetention()
		if !ok {
			d += " — lower QABAS_LOGS_RETENTION and rerun: qabas logs ui up --full"
		}
		add("loki-volume", ok, d)
	}
	return out
}

// projectFilter limits docker ps to the product and logs UI projects.
var projectFilters = []string{"label=com.docker.compose.project=qabasone", "label=com.docker.compose.project=" + observability.ProjectName}

// exposedPorts lists container ports published on 0.0.0.0 / :: other than 80/443.
func exposedPorts(docker func(args ...string) (string, error)) []string {
	seen := map[string]bool{}
	for _, f := range projectFilters {
		out, err := docker("ps", "--filter", f, "--format", "{{.Names}}\t{{.Ports}}")
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			name, ports, ok := strings.Cut(strings.TrimSpace(line), "\t")
			if !ok {
				continue
			}
			for _, p := range ParsePublicPorts(ports) {
				seen[name+":"+p] = true
			}
		}
	}
	return sortedKeys(seen)
}

// ParsePublicPorts extracts host ports bound to every interface from a
// `docker ps` Ports column, e.g. "0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp".
func ParsePublicPorts(col string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(col, ",") {
		part = strings.TrimSpace(part)
		hostPart, _, ok := strings.Cut(part, "->")
		if !ok {
			continue
		}
		i := strings.LastIndex(hostPart, ":")
		if i < 0 {
			continue
		}
		addr, port := hostPart[:i], hostPart[i+1:]
		if addr != "0.0.0.0" && addr != "[::]" && addr != "::" && addr != "" {
			continue
		}
		if publicPorts[port] || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	return out
}

// socketMounts lists containers (other than the logs socket proxy) that bind the Docker socket.
func socketMounts(docker func(args ...string) (string, error)) []string {
	seen := map[string]bool{}
	for _, f := range projectFilters {
		ids, err := docker("ps", "-q", "--filter", f)
		if err != nil || strings.TrimSpace(ids) == "" {
			continue
		}
		args := append([]string{"inspect", "--format", "{{.Name}}{{range .Mounts}} {{.Source}}{{end}}"}, strings.Fields(ids)...)
		out, err := docker(args...)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name := strings.TrimPrefix(fields[0], "/")
			if name == "qabas1-socket-proxy" {
				continue
			}
			for _, src := range fields[1:] {
				if strings.HasSuffix(src, "docker.sock") {
					seen[name] = true
				}
			}
		}
	}
	return sortedKeys(seen)
}

// LokiVolumeBytes is the size of the logs UI Loki volume (0 when absent).
func LokiVolumeBytes(docker func(args ...string) (string, error)) uint64 {
	out, err := docker("system", "df", "-v", "--format", `{{range .Volumes}}{{.Name}} {{.Size}}{{"\n"}}{{end}}`)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
		name, size, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && name == observability.LokiVolume {
			return sysinfo.ParseSize(size)
		}
	}
	return 0
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
