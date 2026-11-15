package doctor

import (
	"fmt"
	"os"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/hint"
	"github.com/qabas1/qabas1-cli/internal/host"
	"github.com/qabas1/qabas1-cli/internal/observability"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/qabas1/qabas1-cli/internal/sysinfo"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Level  string `json:"level"` // error, warn, info
}

type Report struct {
	OK      bool         `json:"ok"`
	Checks  []Check      `json:"checks"`
	Machine sysinfo.Info `json:"-"`
}

func Run(p paths.Paths, cfg config.Config, st state.State, rt *compose.Runtime, verbose bool) Report {
	var checks []Check
	add := func(c Check) { checks = append(checks, c) }

	hr := host.Inspect()
	add(Check{Name: "os", OK: hr.Supported, Detail: hr.OS + "/" + hr.Arch, Level: level(hr.Supported)})
	add(Check{Name: "docker", OK: hr.Docker, Detail: hr.DockerVer, Level: level(hr.Docker)})
	if !hr.Linux && hr.Docker {
		add(Check{Name: "docker-engine", OK: hr.EngineOS == "linux", Detail: engineDetail(hr.EngineOS), Level: level(hr.EngineOS == "linux")})
	}
	add(Check{Name: "compose", OK: hr.Compose, Level: level(hr.Compose)})
	add(Check{Name: "cpu", OK: hr.CPUs >= 2, Detail: fmt.Sprintf("%d", hr.CPUs), Level: warnIf(hr.CPUs < 2)})
	machine := sysinfo.Collect(sysinfo.Targets(p)...)
	for _, c := range ResourceChecks(machine) {
		add(c)
	}

	if err := cfg.Validate(); err != nil {
		add(Check{Name: "config", OK: false, Detail: err.Error(), Level: "error"})
	} else {
		add(Check{Name: "config", OK: true, Detail: "channel=" + cfg.Channel, Level: "info"})
	}

	auth := release.Status()
	if auth.GitHubOK {
		add(Check{Name: "auth-github", OK: true, Detail: "from " + auth.GitHubSource, Level: "info"})
	} else {
		add(Check{Name: "auth-github", OK: false, Detail: "missing — " + hint.CmdEnv("qabas auth login"), Level: "warn"})
	}
	if auth.GHCROK {
		detail := "from " + auth.GHCRSource
		if auth.GHCRUsername != "" {
			detail += ", user=" + auth.GHCRUsername
		}
		add(Check{Name: "auth-ghcr", OK: true, Detail: detail, Level: "info"})
	} else {
		add(Check{Name: "auth-ghcr", OK: false, Detail: "missing — " + hint.CmdEnv("qabas auth login"), Level: "warn"})
	}

	_, err := os.Stat(p.ConfigDir)
	add(Check{Name: "config-dir", OK: err == nil, Detail: p.ConfigDir, Level: level(err == nil)})
	_, err = os.Stat(p.StateDir)
	add(Check{Name: "state-dir", OK: err == nil, Detail: p.StateDir, Level: level(err == nil)})

	if st.Installed() {
		add(Check{Name: "release", OK: true, Detail: st.CurrentRelease, Level: "info"})
		if backup.VPGAvailable() {
			add(Check{Name: "vpg", OK: true, Detail: "on PATH (required for PostgreSQL backup/restore)", Level: "info"})
		} else if backup.Supported() {
			add(Check{Name: "vpg", OK: false, Detail: "missing — install from https://github.com/elqabasy/voidtools (required for PostgreSQL backup/restore)", Level: "error"})
		} else {
			add(Check{Name: "vpg", OK: false, Detail: "PostgreSQL backup/restore is Linux-only; pre-deploy backups are skipped on " + hr.OS, Level: "warn"})
		}
		if rt != nil && rt.Available() && hr.LinuxEngine() {
			// compose.yaml requires QABASONE_*_IMAGE (and infra image) vars.
			// Deploy injects them via ImageEnv; status must do the same or
			// `docker compose ps` fails interpolation and looks "Unhealthy".
			env := compose.EnvForInstalledRelease(p, st.CurrentRelease)
			ps, psErr := rt.Ps(p, env)
			if psErr != nil {
				add(Check{Name: "containers", OK: false, Detail: psErr.Error(), Level: "error"})
			} else {
				ok := !strings.Contains(strings.ToLower(ps), "unhealthy")
				detail := "running"
				if verbose {
					detail = strings.TrimSpace(ps)
				}
				add(Check{Name: "containers", OK: ok, Detail: detail, Level: level(ok)})
			}
		}
	} else {
		add(Check{Name: "release", OK: false, Detail: "not installed", Level: "warn"})
	}

	if rt != nil && rt.Available() {
		logsRT := &observability.Runtime{Compose: rt}
		lst, err := logsRT.Status(p)
		if err != nil {
			add(Check{Name: "logs-ui", OK: false, Detail: err.Error(), Level: "warn"})
		} else if lst.Running {
			detail := "running at " + lst.URLs.Dozzle
			if lst.Full && lst.URLs.Grafana != "" {
				detail += "; grafana " + lst.URLs.Grafana
			}
			add(Check{Name: "logs-ui", OK: true, Detail: detail, Level: "info"})
		} else {
			add(Check{Name: "logs-ui", OK: true, Detail: "stopped — start with: qabas logs ui up", Level: "info"})
		}
	}

	ok := true
	for _, c := range checks {
		if !c.OK && c.Level == "error" {
			ok = false
		}
	}
	return Report{OK: ok, Checks: checks, Machine: machine}
}

// ResourceChecks converts machine findings into doctor checks: critical
// findings fail, warnings pass with a detail.
func ResourceChecks(info sysinfo.Info) []Check {
	checks := make([]Check, 0, len(info.Findings))
	for _, f := range info.Findings {
		c := Check{Name: f.Check, OK: f.Level != sysinfo.LevelCrit, Detail: f.Detail, Level: "info"}
		switch f.Level {
		case sysinfo.LevelCrit:
			c.Level = "error"
		case sysinfo.LevelWarn:
			c.Level = "warn"
		}
		if f.Check == "docker-disk" && f.Level != sysinfo.LevelOK {
			c.Detail += " — autopilot prunes it; or run: docker builder prune"
		}
		checks = append(checks, c)
	}
	return checks
}

func level(ok bool) string {
	if ok {
		return "info"
	}
	return "error"
}

func engineDetail(osType string) string {
	switch osType {
	case "linux":
		return "linux containers"
	case "":
		return "daemon not reachable — start Docker Desktop"
	default:
		return osType + " containers — switch Docker Desktop to Linux containers"
	}
}

func warnIf(cond bool) string {
	if cond {
		return "warn"
	}
	return "info"
}
