package observability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

// Options control how the logs UI stack is started.
type Options struct {
	Full bool // include Loki + Alloy + Grafana
	All  bool // show all Docker containers in Dozzle (no project filter)
	Port int  // Dozzle host port (0 = default / env)
	Wipe bool // on Down, also remove volumes
}

// URLs are the localhost endpoints for the running stack.
type URLs struct {
	Dozzle  string `json:"dozzle"`
	Grafana string `json:"grafana,omitempty"`
	Loki    string `json:"loki,omitempty"`
}

// Status describes the qabasone-logs Compose project.
type Status struct {
	Running  bool         `json:"running"`
	Full     bool         `json:"full"`
	Services []ServiceRow `json:"services,omitempty"`
	URLs     URLs         `json:"urls"`
}

// ServiceRow is one container from docker compose ps.
type ServiceRow struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Health  string `json:"health,omitempty"`
}

// Runtime drives the separate qabasone-logs Compose project.
type Runtime struct {
	Compose *compose.Runtime
}

func NewRuntime() *Runtime {
	return &Runtime{Compose: compose.NewRuntime()}
}

func (r *Runtime) docker() *compose.Runtime {
	if r == nil || r.Compose == nil {
		return compose.NewRuntime()
	}
	return r.Compose
}

// Up materializes assets and starts Dozzle (and optionally the full stack).
func (r *Runtime) Up(p paths.Paths, opt Options) (URLs, Credentials, error) {
	rt := r.docker()
	if !rt.Available() {
		return URLs{}, Credentials{}, exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	if err := Materialize(p); err != nil {
		return URLs{}, Credentials{}, err
	}
	cred, err := EnsureCredentials(p)
	if err != nil {
		return URLs{}, Credentials{}, err
	}
	env := append(stackEnv(opt), credentialsEnv(cred)...)
	args := []string{"up", "-d", "--remove-orphans"}
	if opt.Full {
		if err := r.fixAlloyVolume(env); err != nil {
			return URLs{}, cred, err
		}
		// Wait for Loki/Grafana/Alloy healthchecks.
		args = append(args, "--wait", "--wait-timeout", "120")
	}
	if err := r.runCompose(p, opt.Full, env, args...); err != nil {
		return URLs{}, cred, err
	}
	return ResolveURLs(opt), cred, nil
}

// Down stops the qabasone-logs project.
func (r *Runtime) Down(p paths.Paths, wipe bool) error {
	rt := r.docker()
	if !rt.Available() {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	// Materialize so -f paths exist even if never started on this host.
	if err := Materialize(p); err != nil {
		return err
	}
	args := []string{"down", "--remove-orphans"}
	if wipe {
		args = append(args, "-v")
	}
	// Always pass both files so a prior --full stack is fully torn down.
	if err := r.runCompose(p, true, os.Environ(), args...); err != nil {
		return err
	}
	if wipe {
		_ = os.Remove(p.LogsUICredentialsFile())
	}
	return nil
}

// Status reports whether the logs UI project is running.
func (r *Runtime) Status(p paths.Paths) (Status, error) {
	rt := r.docker()
	urls := ResolveURLs(Options{})
	st := Status{URLs: urls}
	if !rt.Available() {
		return st, nil
	}
	if _, err := os.Stat(DozzleComposeFile(p)); err != nil {
		// Not materialized yet — treat as stopped.
		return st, nil
	}
	var stdout, stderr bytes.Buffer
	env := os.Environ()
	if err := r.runComposeOut(p, true, env, []string{"ps", "--format", "json"}, &stdout, &stderr); err != nil {
		// Project may not exist yet.
		return st, nil
	}
	rows, full := parsePsJSON(stdout.String())
	st.Services = rows
	st.Full = full
	for _, row := range rows {
		if strings.EqualFold(row.State, "running") {
			st.Running = true
			break
		}
	}
	st.URLs = ResolveURLs(Options{Full: full})
	if !full {
		st.URLs.Grafana = ""
		st.URLs.Loki = ""
	}
	return st, nil
}

// OpenBrowser opens the Dozzle URL (best-effort).
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func ResolveURLs(opt Options) URLs {
	dozzlePort := portOrDefault(opt.Port, EnvLogsUIPort, DefaultLogsUIPort)
	u := URLs{
		Dozzle: fmt.Sprintf("http://127.0.0.1:%s", dozzlePort),
	}
	if opt.Full {
		u.Grafana = fmt.Sprintf("http://127.0.0.1:%s", envOr(EnvGrafanaPort, DefaultGrafanaPort))
		// Loki stays on the internal compose network (no host publish).
	}
	return u
}

func stackEnv(opt Options) []string {
	port := portOrDefault(opt.Port, EnvLogsUIPort, DefaultLogsUIPort)
	filter := DefaultDozzleFilter
	if opt.All {
		filter = ""
	} else if v := os.Getenv(EnvDozzleFilter); v != "" {
		filter = v
	}
	grafana := envOr(EnvGrafanaPort, DefaultGrafanaPort)

	env := withoutEnvKeys(os.Environ(),
		EnvLogsUIPort, EnvDozzleFilter, EnvGrafanaPort, EnvLokiPort, EnvLogsRetention,
		EnvGrafanaUser, EnvGrafanaPassword, EnvDozzleUser, EnvDozzlePassword, EnvLogsUIUser,
	)
	env = append(env,
		EnvLogsUIPort+"="+port,
		EnvDozzleFilter+"="+filter,
		EnvGrafanaPort+"="+grafana,
		EnvLogsRetention+"="+LokiRetention(),
	)
	return env
}

var retentionPattern = regexp.MustCompile(`^[1-9][0-9]*[hdw]$`)

// LokiRetention is QABAS_LOGS_RETENTION (e.g. 72h, 14d, 4w) or the 7d default.
// Invalid values fall back to the default so Loki always starts.
func LokiRetention() string {
	v := strings.TrimSpace(os.Getenv(EnvLogsRetention))
	if !retentionPattern.MatchString(v) {
		return DefaultLogsRetention
	}
	return v
}

func withoutEnvKeys(env []string, keys ...string) []string {
	drop := map[string]struct{}{}
	for _, k := range keys {
		drop[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if _, ok := drop[key]; ok {
			continue
		}
		out = append(out, e)
	}
	return out
}

func portOrDefault(flagPort int, envKey, fallback string) string {
	if flagPort > 0 {
		return strconv.Itoa(flagPort)
	}
	return envOr(envKey, fallback)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// AlloyChownArgs re-owns the Alloy volume with only the capabilities chown needs.
func AlloyChownArgs() []string {
	return []string{
		"run", "--rm", "--user", "0:0",
		"--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE",
		"--network", "none",
		"-v", AlloyVolume + ":/d",
		"--entrypoint", "chown",
		AlloyImage, "-R", AlloyUID, "/d",
	}
}

// fixAlloyVolume repairs an Alloy volume left root-owned by older stacks. A missing
// volume is left for compose to create, so it keeps its project labels.
func (r *Runtime) fixAlloyVolume(env []string) error {
	rt := r.docker()
	var out, errb bytes.Buffer
	if err := rt.Run("docker", []string{"volume", "inspect", AlloyVolume}, env, &out, &errb); err != nil {
		return nil
	}
	errb.Reset()
	if err := rt.Run("docker", AlloyChownArgs(), env, &out, &errb); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return exitcode.NewHint(exitcode.Generic,
			"run manually: docker "+strings.Join(AlloyChownArgs(), " "),
			"repair ownership of %s for Alloy (uid %s): %s", AlloyVolume, AlloyUID, msg)
	}
	return nil
}

func (r *Runtime) composeArgs(p paths.Paths, full bool, extra ...string) []string {
	args := []string{
		"compose",
		"--project-name", ProjectName,
		"-f", DozzleComposeFile(p),
	}
	if full {
		args = append(args, "-f", FullComposeFile(p))
	}
	return append(args, extra...)
}

func (r *Runtime) runCompose(p paths.Paths, full bool, env []string, extra ...string) error {
	return r.runComposeOut(p, full, env, extra, nil, nil)
}

func (r *Runtime) runComposeOut(p paths.Paths, full bool, env []string, extra []string, stdout, stderr *bytes.Buffer) error {
	rt := r.docker()
	if _, err := rt.LookPath("docker"); err != nil {
		return exitcode.New(exitcode.InvalidInput, "docker is not installed")
	}
	args := r.composeArgs(p, full, extra...)
	err := rt.Run("docker", args, env, stdout, stderr)
	if err != nil {
		msg := err.Error()
		if stderr != nil && stderr.Len() > 0 {
			msg = strings.TrimSpace(stderr.String())
		}
		return exitcode.New(exitcode.Network, "docker compose %s: %s", strings.Join(extra, " "), msg)
	}
	return nil
}

func parsePsJSON(raw string) ([]ServiceRow, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	var rows []ServiceRow
	full := false
	dec := json.NewDecoder(strings.NewReader(raw))
	for dec.More() {
		var row struct {
			Service string `json:"Service"`
			Name    string `json:"Name"`
			State   string `json:"State"`
			Health  string `json:"Health"`
		}
		if err := dec.Decode(&row); err != nil {
			// Compose sometimes emits a JSON array instead of NDJSON.
			var arr []struct {
				Service string `json:"Service"`
				Name    string `json:"Name"`
				State   string `json:"State"`
				Health  string `json:"Health"`
			}
			if err2 := json.Unmarshal([]byte(raw), &arr); err2 != nil {
				return rows, full
			}
			for _, a := range arr {
				rows = append(rows, ServiceRow{Service: a.Service, Name: a.Name, State: a.State, Health: a.Health})
				if a.Service == "loki" || a.Service == "grafana" || a.Service == "alloy" {
					full = true
				}
			}
			return rows, full
		}
		rows = append(rows, ServiceRow{Service: row.Service, Name: row.Name, State: row.State, Health: row.Health})
		if row.Service == "loki" || row.Service == "grafana" || row.Service == "alloy" {
			full = true
		}
	}
	return rows, full
}
