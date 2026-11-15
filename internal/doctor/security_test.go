package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/secrets"
)

func TestParsePublicPorts(t *testing.T) {
	got := ParsePublicPorts("0.0.0.0:80->80/tcp, [::]:80->80/tcp, 0.0.0.0:5432->5432/tcp, [::]:5432->5432/tcp, 127.0.0.1:9000->9000/tcp, 6379/tcp")
	if strings.Join(got, ",") != "5432" {
		t.Fatalf("got %v", got)
	}
}

func fakeDocker(t *testing.T) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "ps --filter label=com.docker.compose.project=qabasone --format"):
			return "qabas1-postgres\t0.0.0.0:5432->5432/tcp\nqabasone-edge\t0.0.0.0:443->443/tcp\n", nil
		case strings.HasPrefix(joined, "ps -q --filter label=com.docker.compose.project=qabas1-logs"):
			return "aaa\nbbb\n", nil
		case strings.HasPrefix(joined, "inspect"):
			return "/qabas1-socket-proxy /var/run/docker.sock\n/qabas1-dozzle /var/lib/docker/volumes/x\n", nil
		case strings.HasPrefix(joined, "ps -q"):
			return "ccc\n", nil
		case strings.HasPrefix(joined, "system df"):
			return "qabas1-logs_loki_data 6.5GB\nother 1GB\n", nil
		}
		return "", nil
	}
}

func TestSecurityChecksFlagsRisks(t *testing.T) {
	d := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(d, "etc"), StateDir: filepath.Join(d, "lib"), LogDir: filepath.Join(d, "log"), LockPath: filepath.Join(d, "lock")}
	_ = os.MkdirAll(p.ConfigDir, 0o700)
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	meta := secrets.Meta{"postgres": {CreatedAt: now.AddDate(-1, 0, 0)}, "redis": {CreatedAt: now.AddDate(0, -1, 0)}}
	if err := secrets.SaveMeta(p, meta); err != nil {
		t.Fatal(err)
	}
	checks := SecurityChecks(p, true, SecurityProbes{GOOS: "windows", Now: func() time.Time { return now }, Docker: fakeDocker(t), LoggingDriver: "json-file"})
	byName := map[string]Check{}
	for _, c := range checks {
		byName[c.Name] = c
		if c.Level == "error" {
			t.Errorf("%s: security checks must never be error level", c.Name)
		}
	}
	want := map[string]bool{"secret-age": false, "log-rotation": false, "resource-limits": false, "exposure": false, "docker-socket": true, "loki-volume": false}
	for name, ok := range want {
		c, found := byName[name]
		if !found {
			t.Errorf("missing check %s", name)
			continue
		}
		if c.OK != ok {
			t.Errorf("%s ok=%v want %v (%s)", name, c.OK, ok, c.Detail)
		}
	}
	if !strings.Contains(byName["secret-age"].Detail, "postgres") || strings.Contains(byName["secret-age"].Detail, "redis") {
		t.Errorf("secret-age detail = %q", byName["secret-age"].Detail)
	}
	if !strings.Contains(byName["exposure"].Detail, "qabas1-postgres:5432") || strings.Contains(byName["exposure"].Detail, "443") {
		t.Errorf("exposure detail = %q", byName["exposure"].Detail)
	}
}

func TestSecurityChecksWithoutDocker(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir(), LogDir: t.TempDir()}
	for _, c := range SecurityChecks(p, false, SecurityProbes{GOOS: "windows", LoggingDriver: "journald"}) {
		if !c.OK {
			t.Errorf("unexpected warning %s: %s", c.Name, c.Detail)
		}
		if c.Name == "exposure" || c.Name == "docker-socket" {
			t.Errorf("docker checks must be skipped without docker")
		}
	}
}
