package compose

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestRestartAPIWaitsForEachReplica(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir()}
	var calls []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		Run: func(name string, args []string, _ []string, stdout, _ *bytes.Buffer) error {
			calls = append(calls, strings.Join(args, " "))
			switch args[0] {
			case "compose":
				stdout.WriteString("first\nsecond\n")
			case "inspect":
				stdout.WriteString("healthy\n")
			}
			return nil
		},
	}
	if err := rt.Restart(p, nil, "api"); err != nil {
		t.Fatal(err)
	}
	want := []string{"restart first", "inspect --format {{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}} first", "restart second"}
	for i, call := range calls[1:] {
		if i < len(want) && call != want[i] {
			t.Fatalf("call %d = %q, want %q", i, call, want[i])
		}
	}
	if len(calls) != 5 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestParseComposePsJSON(t *testing.T) {
	raw := `[
	  {
	    "ID": "1234567890abcdef",
	    "Name": "qabas1-api",
	    "Service": "api",
	    "State": "running",
	    "Health": "healthy",
	    "Image": "ghcr.io/qabas1/qabas1-api@sha256:abc"
	  }
	]`

	services, err := parseComposePsJSON(raw, "1.0.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(services))
	}
	got := services[0]
	if got.Service != "api" {
		t.Fatalf("service = %q", got.Service)
	}
	if got.ContainerID != "1234567890ab" {
		t.Fatalf("container id = %q", got.ContainerID)
	}
	if got.Version != "1.0.3" {
		t.Fatalf("version = %q", got.Version)
	}
	if got.Memory != "-" {
		t.Fatalf("memory = %q", got.Memory)
	}
}

func TestEnvForInstalledReleaseUsesReplicaImagePins(t *testing.T) {
	p := paths.Paths{ConfigDir: t.TempDir()}
	imageEnv := filepath.Join(p.ConfigDir, "dr", "images.env")
	if err := os.MkdirAll(filepath.Dir(imageEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(imageEnv, []byte("QABASONE_API_IMAGE=registry.example/api@sha256:pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QABASONE_API_IMAGE", "registry.example/api:latest")

	env := EnvForInstalledRelease(p, "")
	var apiImages []string
	for _, entry := range env {
		if strings.HasPrefix(entry, "QABASONE_API_IMAGE=") {
			apiImages = append(apiImages, entry)
		}
	}
	if len(apiImages) != 1 || apiImages[0] != "QABASONE_API_IMAGE=registry.example/api@sha256:pinned" {
		t.Fatalf("QABASONE_API_IMAGE entries = %v", apiImages)
	}
}

func TestParseComposePsJSONLines(t *testing.T) {
	raw := `{"ID":"abcdef1234567890","Name":"qabas1-postgres","Service":"postgres","State":"running","Health":"healthy","Image":"postgres:16-alpine"}
{"ID":"fedcba0987654321","Name":"qabas1-redis","Service":"redis","State":"running","Image":"redis:7-alpine"}`

	services, err := parseComposePsJSON(raw, "1.0.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(services))
	}
	if services[1].Health != "-" {
		t.Fatalf("missing health should render as dash, got %q", services[1].Health)
	}
}

func TestUpServiceBuildsComposeArgs(t *testing.T) {
	var got []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			got = append([]string(nil), args...)
			return nil
		},
	}
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}

	if err := rt.UpService(p, nil, "api", true, true); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"compose", "--project-name", "qabasone", "-f", p.ComposeFile(),
		"up", "-d", "--build", "--remove-orphans", "--wait", "--wait-timeout", "180", "api",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compose args mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestUpServicesBuildsComposeArgs(t *testing.T) {
	var got []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			got = append([]string(nil), args...)
			return nil
		},
	}
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}

	if err := rt.UpServices(p, nil, []string{"api", "printables-worker"}, false, true); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"compose", "--project-name", "qabasone", "-f", p.ComposeFile(),
		"up", "-d", "--remove-orphans", "--wait", "--wait-timeout", "180", "api", "printables-worker",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compose args mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestPullServicesBuildsComposeArgs(t *testing.T) {
	var got []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			got = append([]string(nil), args...)
			return nil
		},
	}
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}

	if err := rt.PullServices(p, nil, "app"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"compose", "--project-name", "qabasone", "-f", p.ComposeFile(),
		"pull", "app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compose args mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestPullFileServicesBuildsComposeArgs(t *testing.T) {
	var got []string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			got = append([]string(nil), args...)
			return nil
		},
	}
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	next := p.ComposeFileNext()

	if err := rt.PullFileServices(p, next, nil, "api", "app"); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"compose", "--project-name", "qabasone", "-f", next,
		"pull", "api", "app",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("compose args mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestComposeFileOverrideForBackup(t *testing.T) {
	var calls [][]string
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	rt := &Runtime{
		ComposeFileOverride: p.ComposeFileNext(),
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			calls = append(calls, append([]string(nil), args...))
			return nil
		},
	}
	if err := rt.EnsureServiceCreated(p, nil, "postgres"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(p, nil, "postgres"); err != nil {
		t.Fatal(err)
	}
	for _, args := range calls {
		if len(args) < 5 || args[4] != p.ComposeFileNext() {
			t.Fatalf("backup must use staged compose: %v", args)
		}
	}
}

func TestDownServiceStopsAndRemovesService(t *testing.T) {
	var calls [][]string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			calls = append(calls, append([]string(nil), args...))
			return nil
		},
	}
	p := paths.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}

	if err := rt.DownService(p, nil, "api"); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"compose", "--project-name", "qabasone", "-f", p.ComposeFile(), "stop", "api"},
		{"compose", "--project-name", "qabasone", "-f", p.ComposeFile(), "rm", "-f", "api"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("compose calls mismatch\n got: %#v\nwant: %#v", calls, want)
	}
}

func TestSaveReleaseCopySerializesInMemoryRelease(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	rel := &manifest.Release{
		SchemaVersion: 1,
		Release: manifest.Meta{
			Version: "1.0.4-dev.1788323844",
			Channel: "dev",
		},
		Components: manifest.Components{
			API:   manifest.Component{Version: "1.0.4-dev.1788323844", Image: "ghcr.io/qabas1/qabas1-api:dev", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			App:   manifest.Component{Version: "1.0.4-dev.1788323844", Image: "ghcr.io/qabas1/qabas1-app:dev", Digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			Dash:  manifest.Component{Version: "1.0.4-dev.1788323844", Image: "ghcr.io/qabas1/qabas1-dash:dev", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
			Siraj: manifest.Component{Version: "1.0.4-dev.1788323844", Image: "ghcr.io/qabas1/qabas1-siraj_app:dev", Digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
		},
		Compatibility: manifest.Compatibility{
			App:   manifest.APIRange{API: ">=0.0.0-0"},
			Dash:  manifest.APIRange{API: ">=0.0.0-0"},
			Siraj: manifest.APIRange{API: ">=0.0.0-0"},
		},
		Requirements: manifest.Requirements{QabasCLI: ">=0.0.0"},
	}

	if err := SaveReleaseCopy(p, rel); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(p.Releases(), rel.Release.Version+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "qabas1-api") {
		t.Fatalf("release copy did not include image metadata:\n%s", raw)
	}
	parsed, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Components.API.DigestRef() == "" {
		t.Fatal("parsed release missing api digest ref")
	}
}

func TestOneOffMemoryArgsRaisesMigrateCeiling(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: dir}
	got, err := oneOffComposeOverrideArgs(p, "migrate")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := p.ComposeMigrateMemoryOverride()
	want := []string{"-f", wantPath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrate override args = %v, want %v", got, want)
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mem_limit: 1g") {
		t.Fatalf("override missing mem_limit:\n%s", raw)
	}
	if got, err := oneOffComposeOverrideArgs(p, "api"); err != nil || got != nil {
		t.Fatalf("api one-off should not force override, got %v err %v", got, err)
	}
}
