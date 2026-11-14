package observability

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

func TestMaterializeWritesComposeAndConfig(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: dir}
	if err := Materialize(p); err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(p.LogsUIDir(), "compose.dozzle.yaml"),
		filepath.Join(p.LogsUIDir(), "compose.full.yaml"),
		filepath.Join(p.LogsUIDir(), "config", "alloy.alloy"),
		filepath.Join(p.LogsUIDir(), "config", "loki-config.yaml"),
		filepath.Join(p.LogsUIDir(), "config", "dozzle", "users.yml"),
		filepath.Join(p.LogsUIDir(), "config", "grafana", "provisioning", "datasources", "loki.yaml"),
		filepath.Join(p.LogsUIDir(), "config", "grafana", "provisioning", "dashboards", "json", "qabasone-logs.json"),
	}
	for _, f := range want {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("missing materialized file %s: %v", f, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(p.LogsUIDir(), "compose.dozzle.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "amir20/dozzle:") {
		t.Fatal("dozzle compose missing pinned image")
	}
	if !strings.Contains(string(raw), "127.0.0.1") {
		t.Fatal("dozzle must bind localhost only")
	}
}

func TestComposeArgsSeparateProject(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: dir}
	if err := Materialize(p); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Compose: compose.NewRuntime()}
	args := rt.composeArgs(p, false, "up", "-d")
	if got := args[0]; got != "compose" {
		t.Fatalf("args[0]=%q", got)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--project-name "+ProjectName) {
		t.Fatalf("missing project name in %s", joined)
	}
	if strings.Contains(joined, "--project-name qabasone ") {
		t.Fatalf("must not use product project name: %s", joined)
	}
	if !strings.Contains(joined, DozzleComposeFile(p)) {
		t.Fatalf("missing dozzle file in %s", joined)
	}
	if strings.Contains(joined, FullComposeFile(p)) {
		t.Fatalf("full compose should be absent without --full: %s", joined)
	}

	full := rt.composeArgs(p, true, "up", "-d")
	fullJoined := strings.Join(full, " ")
	if !strings.Contains(fullJoined, FullComposeFile(p)) {
		t.Fatalf("full compose missing: %s", fullJoined)
	}
}

func TestResolveURLs(t *testing.T) {
	t.Setenv(EnvLogsUIPort, "")
	t.Setenv(EnvGrafanaPort, "")
	u := ResolveURLs(Options{})
	if u.Dozzle != "http://127.0.0.1:9999" {
		t.Fatalf("dozzle url=%q", u.Dozzle)
	}
	if u.Grafana != "" {
		t.Fatalf("grafana should be empty without full: %q", u.Grafana)
	}
	u = ResolveURLs(Options{Full: true, Port: 8888})
	if u.Dozzle != "http://127.0.0.1:8888" {
		t.Fatalf("dozzle url=%q", u.Dozzle)
	}
	if u.Grafana != "http://127.0.0.1:3001" {
		t.Fatalf("grafana url=%q", u.Grafana)
	}
}

func TestParsePsJSONDetectsFull(t *testing.T) {
	raw := `{"Service":"dozzle","Name":"qabasone-dozzle","State":"running","Health":""}
{"Service":"loki","Name":"qabasone-loki","State":"running","Health":""}
`
	rows, full := parsePsJSON(raw)
	if !full {
		t.Fatal("expected full=true when loki present")
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d", len(rows))
	}
}

func TestParsePsJSONArray(t *testing.T) {
	raw := `[{"Service":"dozzle","Name":"qabasone-dozzle","State":"running","Health":""}]`
	rows, full := parsePsJSON(raw)
	if full {
		t.Fatal("expected full=false")
	}
	if len(rows) != 1 || rows[0].Service != "dozzle" {
		t.Fatalf("rows=%+v", rows)
	}
}

func TestLokiRetention(t *testing.T) {
	for in, want := range map[string]string{"": "7d", "14d": "14d", "72h": "72h", "4w": "4w", "0d": "7d", "forever": "7d"} {
		t.Setenv(EnvLogsRetention, in)
		if got := LokiRetention(); got != want {
			t.Errorf("LokiRetention(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlloyRedactsBeforeLoki(t *testing.T) {
	raw, err := assets.ReadFile("config/alloy.alloy")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "forward_to = [loki.process.redact.receiver]") {
		t.Fatal("docker source must forward through the redact stage")
	}
	exprs := regexp.MustCompile("expression = `([^`]+)`").FindAllStringSubmatch(body, -1)
	if len(exprs) < 4 {
		t.Fatalf("expected redaction rules, got %d", len(exprs))
	}
	line := `login ok password=hunter22 url=postgres://u:pw1234@db/x Authorization: Bearer abcdefgh1234`
	for _, m := range exprs {
		re, err := regexp.Compile(m[1])
		if err != nil {
			t.Fatalf("rule %q is not RE2: %v", m[1], err)
		}
		line = re.ReplaceAllStringFunc(line, func(s string) string {
			sub := re.FindStringSubmatchIndex(s)
			return s[:sub[2]] + "***" + s[sub[3]:]
		})
	}
	for _, leak := range []string{"hunter22", "pw1234", "abcdefgh1234"} {
		if strings.Contains(line, leak) {
			t.Fatalf("leaked %q: %s", leak, line)
		}
	}
}

func TestLogsStackCapsOwnLogs(t *testing.T) {
	for _, f := range []string{"compose.dozzle.yaml", "compose.full.yaml"} {
		raw, _ := assets.ReadFile(f)
		if !strings.Contains(string(raw), `max-size: "20m"`) {
			t.Fatalf("%s: logs UI containers must rotate their own logs", f)
		}
	}
	raw, _ := assets.ReadFile("compose.full.yaml")
	if !strings.Contains(string(raw), "-config.expand-env=true") {
		t.Fatal("loki must expand QABAS_LOGS_RETENTION")
	}
}

func TestLogsStackIsolation(t *testing.T) {
	type svc struct {
		User        string         `yaml:"user"`
		CapDrop     []string       `yaml:"cap_drop"`
		ReadOnly    bool           `yaml:"read_only"`
		Volumes     []string       `yaml:"volumes"`
		Ports       []string       `yaml:"ports"`
		Environment map[string]any `yaml:"environment"`
	}
	services := map[string]svc{}
	for _, f := range []string{"compose.dozzle.yaml", "compose.full.yaml"} {
		raw, _ := assets.ReadFile(f)
		var doc struct {
			Services map[string]svc `yaml:"services"`
		}
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for k, v := range doc.Services {
			services[k] = v
		}
	}
	for name, s := range services {
		for _, v := range s.Volumes {
			if strings.Contains(v, "docker.sock") && name != "socket-proxy" {
				t.Errorf("%s mounts the Docker socket; only socket-proxy may", name)
			}
		}
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Errorf("%s publishes %q beyond localhost", name, p)
			}
		}
		if name == "socket-proxy" {
			if s.Environment["POST"] != 0 || s.Environment["EXEC"] != 0 {
				t.Errorf("socket-proxy must deny POST/EXEC: %v", s.Environment)
			}
			continue
		}
		if len(s.CapDrop) == 0 || s.CapDrop[0] != "ALL" || !s.ReadOnly {
			t.Errorf("%s must drop all capabilities and use a read-only rootfs", name)
		}
	}
}

func TestAlloyConstantsMatchCompose(t *testing.T) {
	raw, _ := assets.ReadFile("compose.full.yaml")
	var doc struct {
		Services map[string]struct {
			Image   string   `yaml:"image"`
			User    string   `yaml:"user"`
			Volumes []string `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	a := doc.Services["alloy"]
	if a.Image != AlloyImage || a.User != AlloyUID {
		t.Fatalf("alloy service image=%q user=%q, constants image=%q uid=%q", a.Image, a.User, AlloyImage, AlloyUID)
	}
	want := strings.TrimPrefix(AlloyVolume, ProjectName+"_") + ":"
	found := false
	for _, v := range a.Volumes {
		found = found || strings.HasPrefix(v, want)
	}
	if !found {
		t.Fatalf("alloy volumes %v do not mount %s", a.Volumes, AlloyVolume)
	}
}

func fakeDocker(volumeExists bool, chownErr error) (*Runtime, *[][]string) {
	var calls [][]string
	rt := compose.NewRuntime()
	rt.LookPath = func(string) (string, error) { return "docker", nil }
	rt.Run = func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
		calls = append(calls, args)
		switch args[0] {
		case "volume":
			if !volumeExists {
				return errors.New("no such volume")
			}
		case "run":
			if chownErr != nil {
				stderr.WriteString("denied")
				return chownErr
			}
		}
		return nil
	}
	return &Runtime{Compose: rt}, &calls
}

func TestFixAlloyVolume(t *testing.T) {
	r, calls := fakeDocker(false, nil)
	if err := r.fixAlloyVolume(nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("absent volume must not be touched: %v", *calls)
	}

	r, calls = fakeDocker(true, nil)
	if err := r.fixAlloyVolume(nil); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %v", *calls)
	}
	got := strings.Join((*calls)[1], " ")
	for _, part := range []string{"--user 0:0", "--cap-drop ALL", "--cap-add CHOWN", "--network none", AlloyVolume + ":/d", "--entrypoint chown", AlloyImage + " -R " + AlloyUID + " /d"} {
		if !strings.Contains(got, part) {
			t.Errorf("chown args %q missing %q", got, part)
		}
	}

	r, _ = fakeDocker(true, errors.New("exit 1"))
	err := r.fixAlloyVolume(nil)
	if err == nil || !strings.Contains(err.Error(), "denied") || !strings.Contains(exitcode.HintOf(err), "chown") {
		t.Fatalf("failed repair must explain and hint the manual command: %v / %q", err, exitcode.HintOf(err))
	}
}

func TestStackEnvAllClearsFilter(t *testing.T) {
	t.Setenv(EnvDozzleFilter, "stale")
	env := stackEnv(Options{All: true, Port: 9999})
	found := false
	for _, e := range env {
		if strings.HasPrefix(e, EnvDozzleFilter+"=") {
			found = true
			if e != EnvDozzleFilter+"=" {
				t.Fatalf("expected empty filter, got %q", e)
			}
		}
	}
	if !found {
		t.Fatal("DOZZLE_FILTER not set")
	}
}
