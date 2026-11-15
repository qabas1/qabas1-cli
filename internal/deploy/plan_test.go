package deploy_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/deploy"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/state"
)

func catalog(t *testing.T) *release.Client {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "release", "testdata", "releases")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	type ghFile struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	var files []ghFile
	raw := map[string][]byte{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, ghFile{Name: e.Name(), Type: "file"})
		raw[e.Name()] = b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/releases") {
			_ = json.NewEncoder(w).Encode(files)
			return
		}
		name := filepath.Base(r.URL.Path)
		b, ok := raw[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	c := release.New("qabas1/qabas1-deploy", "main")
	c.HTTP = srv.Client()
	c.APIBase = srv.URL
	c.RawBase = srv.URL
	return c
}

func engine(t *testing.T, current, previous string) *deploy.Engine {
	t.Helper()
	dir := t.TempDir()
	p := paths.Paths{
		ConfigDir: filepath.Join(dir, "etc"),
		StateDir:  filepath.Join(dir, "lib"),
		LogDir:    filepath.Join(dir, "log"),
		LockPath:  filepath.Join(dir, "qabas.lock"),
	}
	if err := p.EnsureRuntime(); err != nil {
		t.Fatal(err)
	}
	st := state.State{CurrentRelease: current, PreviousRelease: previous}
	if err := state.Save(p, st); err != nil {
		t.Fatal(err)
	}
	e := deploy.New(p, config.Default(), st, output.New(false, true))
	e.Client = catalog(t)
	return e
}

func TestIdempotentInstall(t *testing.T) {
	e := engine(t, "2.8.0", "")
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionInstall, Channel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0] != "idempotent-skip" {
		t.Fatalf("steps %v", plan.Steps)
	}
}

func TestUpgradeCheckPlan(t *testing.T) {
	e := engine(t, "2.8.0", "2.7.0")
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionUpgrade, Channel: "stable", CheckOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.To != "2.9.0" {
		t.Fatalf("to %s", plan.To)
	}
	text := deploy.FormatPlan(plan)
	if !strings.Contains(text, "4.13.0 → 4.15.0") {
		t.Fatalf("plan text:\n%s", text)
	}
	if !strings.Contains(text, "3.8.0 → 3.9.0") || !strings.Contains(text, "5.5.0 → 5.7.0") {
		t.Fatalf("plan text:\n%s", text)
	}
}

func TestDowngradeBlocked(t *testing.T) {
	e := engine(t, "2.8.0", "2.7.0")
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDowngrade, Spec: "2.7.0"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked == "" {
		t.Fatal("expected block")
	}
	if plan.LeavingDev {
		t.Fatal("stable downgrade must not set LeavingDev")
	}
	if !strings.Contains(plan.Blocked, "2.7.0") {
		t.Fatalf("blocked %s", plan.Blocked)
	}
	if !strings.Contains(plan.Blocked, "current 2.8.0 → target 2.7.0") {
		t.Fatalf("blocked should include current → target:\n%s", plan.Blocked)
	}
}

func TestDeployStableFromDevNotBlocked(t *testing.T) {
	e := engine(t, "2.8.1-dev.1", "")
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "2.7.0"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked != "" {
		t.Fatalf("leaving dev should not be blocked: %s", plan.Blocked)
	}
	if !plan.LeavingDev {
		t.Fatal("expected LeavingDev")
	}
	if plan.To != "2.7.0" {
		t.Fatalf("to %s", plan.To)
	}
	text := deploy.FormatPlan(plan)
	if !strings.Contains(text, "Leaving the dev channel") {
		t.Fatalf("plan text missing leaving-dev note:\n%s", text)
	}
}

func TestDeployOlderStableStillBlocked(t *testing.T) {
	e := engine(t, "2.8.0", "")
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "2.7.0"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked == "" {
		t.Fatal("expected block for stable → older stable")
	}
	if plan.LeavingDev {
		t.Fatal("stable deploy must not set LeavingDev")
	}
}

func TestRollbackTarget(t *testing.T) {
	e := engine(t, "2.9.0", "2.8.0")
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionRollback})
	if err != nil {
		t.Fatal(err)
	}
	if plan.To != "2.8.0" {
		t.Fatalf("rollback to %s", plan.To)
	}
}

func TestRollbackMissingPrevious(t *testing.T) {
	e := engine(t, "2.8.0", "")
	_, err := e.BuildPlan(deploy.Options{Action: deploy.ActionRollback})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDeployDevUsesGHCR(t *testing.T) {
	e := engine(t, "1.0.4", "")
	called := false
	e.DevResolver = func(httpClient *http.Client, owner, username, token string) (*manifest.Release, error) {
		called = true
		_ = httpClient
		_ = owner
		_ = username
		_ = token
		return &manifest.Release{
			SchemaVersion: 1,
			Release:       manifest.Meta{Version: "0.0.0-dev.1", Channel: "dev", ReleasedAt: "2026-01-01T00:00:00Z"},
			Components: manifest.Components{
				API:   manifest.Component{Version: "0.0.0-dev.1", Image: "ghcr.io/qabas1/qabas1-api:0.0.0-dev.1", Digest: "sha256:aaa"},
				App:   manifest.Component{Version: "0.0.0-dev.1", Image: "ghcr.io/qabas1/qabas1-app:0.0.0-dev.1", Digest: "sha256:bbb"},
				Dash:  manifest.Component{Version: "0.0.0-dev.1", Image: "ghcr.io/qabas1/qabas1-dash:0.0.0-dev.1", Digest: "sha256:ccc"},
				Siraj: manifest.Component{Version: "0.0.0-dev.1", Image: "ghcr.io/qabas1/qabas1-siraj_app:0.0.0-dev.1", Digest: "sha256:ddd"},
			},
			Infra: manifest.Infra{
				Postgres: "postgres:16-alpine", Redis: "redis:7-alpine",
				Minio: "minio/minio:x", MinioMc: "minio/mc:x", Gotenberg: "gotenberg/gotenberg:8",
			},
			Compatibility: manifest.Compatibility{
				App:   manifest.APIRange{API: ">=0.0.0-0"},
				Dash:  manifest.APIRange{API: ">=0.0.0-0"},
				Siraj: manifest.APIRange{API: ">=0.0.0-0"},
			},
			Requirements: manifest.Requirements{QabasCLI: ">=0.0.0", Docker: ">=27.0.0"},
			Database:     manifest.Database{MigrationStrategy: "expand-contract", DowngradeSupported: false},
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected DevResolver for GHCR image pins")
	}
	if e.Client == nil || e.Client.Ref != "dev" {
		t.Fatalf("expected client ref=dev, got %#v", e.Client)
	}
	if plan.Channel != "dev" {
		t.Fatalf("channel %q", plan.Channel)
	}
	// Current stable 1.0.4 is newer than 0.0.0-dev.1 → promoteDevTarget bumps product version.
	if !strings.HasPrefix(plan.To, "1.0.5-dev.") {
		t.Fatalf("to %q", plan.To)
	}
}

func TestDeployDevIgnoresCatalogPins(t *testing.T) {
	devYAML := []byte(`schemaVersion: 1
release:
  version: "0.0.0-dev"
  channel: dev
  releasedAt: "2026-09-01T00:00:00Z"
components:
  api:
    version: "1.0.0-dev.2"
    commit: "aaa1111"
    image: "ghcr.io/qabas1/qabas1-api:1.0.0-dev.2"
    digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  app:
    version: "1.0.0-dev.2"
    commit: "bbb2222"
    image: "ghcr.io/qabas1/qabas1-app:1.0.0-dev.2"
    digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  dash:
    version: "1.0.0-dev.2"
    commit: "ccc3333"
    image: "ghcr.io/qabas1/qabas1-dash:1.0.0-dev.2"
    digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  siraj:
    version: "1.0.0-dev.2"
    commit: "ddd4444"
    image: "ghcr.io/qabas1/qabas1-siraj_app:1.0.0-dev.2"
    digest: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
compatibility:
  app:
    api: ">=0.0.0-0"
  dash:
    api: ">=0.0.0-0"
  siraj:
    api: ">=0.0.0-0"
requirements:
  qabasCli: ">=0.0.0"
  docker: ">=27.0.0"
database:
  migrationStrategy: expand-contract
  downgradeSupported: false
`)
	files := []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}{{Name: "0.0.0-dev.yaml", Type: "file"}}
	catalogHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/releases") {
			catalogHits++
			_ = json.NewEncoder(w).Encode(files)
			return
		}
		if strings.HasSuffix(r.URL.Path, "0.0.0-dev.yaml") {
			catalogHits++
			_, _ = w.Write(devYAML)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	e := engine(t, "1.0.4", "")
	e.Client = release.New("qabasone/qabas1-deploy", "main")
	e.Client.HTTP = srv.Client()
	e.Client.APIBase = srv.URL
	e.Client.RawBase = srv.URL
	called := false
	e.DevResolver = func(httpClient *http.Client, owner, username, token string) (*manifest.Release, error) {
		called = true
		_ = httpClient
		_ = owner
		_ = username
		_ = token
		return &manifest.Release{
			SchemaVersion: 1,
			Release:       manifest.Meta{Version: "0.0.0-dev.9", Channel: "dev", ReleasedAt: "2026-01-01T00:00:00Z"},
			Components: manifest.Components{
				API:   manifest.Component{Version: "0.0.0-dev.9", Image: "ghcr.io/qabas1/qabas1-api:0.0.0-dev.9", Digest: "sha256:111"},
				App:   manifest.Component{Version: "0.0.0-dev.9", Image: "ghcr.io/qabas1/qabas1-app:0.0.0-dev.9", Digest: "sha256:222"},
				Dash:  manifest.Component{Version: "0.0.0-dev.9", Image: "ghcr.io/qabas1/qabas1-dash:0.0.0-dev.9", Digest: "sha256:333"},
				Siraj: manifest.Component{Version: "0.0.0-dev.9", Image: "ghcr.io/qabas1/qabas1-siraj_app:0.0.0-dev.9", Digest: "sha256:444"},
			},
			Infra: manifest.Infra{
				Postgres: "postgres:16-alpine", Redis: "redis:7-alpine",
				Minio: "minio/minio:x", MinioMc: "minio/mc:x", Gotenberg: "gotenberg/gotenberg:8",
			},
			Compatibility: manifest.Compatibility{
				App:   manifest.APIRange{API: ">=0.0.0-0"},
				Dash:  manifest.APIRange{API: ">=0.0.0-0"},
				Siraj: manifest.APIRange{API: ">=0.0.0-0"},
			},
			Requirements: manifest.Requirements{QabasCLI: ">=0.0.0", Docker: ">=27.0.0"},
			Database:     manifest.Database{MigrationStrategy: "expand-contract", DowngradeSupported: false},
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected GHCR resolver even when catalog exists")
	}
	if catalogHits != 0 {
		t.Fatalf("expected catalog YAML unused for image pins, got %d hits", catalogHits)
	}
	if e.Client.Ref != "dev" {
		t.Fatalf("ref %q", e.Client.Ref)
	}
	if plan.Channel != "dev" {
		t.Fatalf("channel %q", plan.Channel)
	}
	if plan.Target.Components.API.Digest != "sha256:111" {
		t.Fatalf("api digest %q (catalog pins must be ignored)", plan.Target.Components.API.Digest)
	}
	if plan.Target.Components.API.Image != "ghcr.io/qabas1/qabas1-api:0.0.0-dev.9" {
		t.Fatalf("api image %q (catalog pins must be ignored)", plan.Target.Components.API.Image)
	}
	if !strings.HasPrefix(plan.To, "1.0.5-dev.") {
		t.Fatalf("expected promote from 0.0.0-dev.9 under stable 1.0.4, got %q", plan.To)
	}
}

func TestDryRunDoesNotMutate(t *testing.T) {
	e := engine(t, "2.8.0", "")
	before := e.State.CurrentRelease
	if err := e.Run(deploy.Options{Action: deploy.ActionUpgrade, Channel: "stable", DryRun: true}, nil); err != nil {
		t.Fatal(err)
	}
	if e.State.CurrentRelease != before {
		t.Fatalf("mutated to %s", e.State.CurrentRelease)
	}
}

func writeInstalledRelease(t *testing.T, e *deploy.Engine, version string, digests [4]string) {
	t.Helper()
	body := []byte(`schemaVersion: 1
release:
  version: "` + version + `"
  channel: dev
  releasedAt: "2026-01-01T00:00:00Z"
components:
  api:
    version: "` + version + `"
    image: "ghcr.io/qabas1/qabas1-api:` + version + `"
    digest: "` + digests[0] + `"
  app:
    version: "` + version + `"
    image: "ghcr.io/qabas1/qabas1-app:` + version + `"
    digest: "` + digests[1] + `"
  dash:
    version: "` + version + `"
    image: "ghcr.io/qabas1/qabas1-dash:` + version + `"
    digest: "` + digests[2] + `"
  siraj:
    version: "` + version + `"
    image: "ghcr.io/qabas1/qabas1-siraj_app:` + version + `"
    digest: "` + digests[3] + `"
compatibility:
  app:
    api: ">=0.0.0-0"
  dash:
    api: ">=0.0.0-0"
  siraj:
    api: ">=0.0.0-0"
requirements:
  qabasCli: ">=0.0.0"
  docker: ">=27.0.0"
database:
  migrationStrategy: expand-contract
  downgradeSupported: false
`)
	if err := os.MkdirAll(e.Paths.Releases(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.Paths.Releases(), version+".yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScopedPinnedAPIDowngradeRequiresOverride(t *testing.T) {
	e := engine(t, "2.8.0", "")
	writeInstalledRelease(t, e, "2.8.0", [4]string{"sha256:oldapi", "sha256:oldapp", "sha256:olddash", "sha256:oldsiraj"})
	e.ComponentResolver = func(_ *http.Client, _, _, _, _, _ string) (manifest.Component, error) {
		return manifest.Component{Version: "2.7.0", Image: "ghcr.io/qabas1/qabas1-api:2.7.0", Digest: "sha256:newapi"}, nil
	}
	_, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Service: "api", Spec: "2.7.0"})
	if err == nil {
		t.Fatal("expected downgrade guard")
	}
	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Service: "api", Spec: "2.7.0", AllowDowngrade: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Target.Components.API.Digest != "sha256:newapi" {
		t.Fatalf("digest: %s", plan.Target.Components.API.Digest)
	}
}

func TestDeployDevSameVersionNewDigestsRedeploys(t *testing.T) {
	e := engine(t, "1.1", "")
	writeInstalledRelease(t, e, "1.1", [4]string{"sha256:oldapi", "sha256:oldapp", "sha256:olddash", "sha256:oldsiraj"})
	e.DevResolver = func(httpClient *http.Client, owner, username, token string) (*manifest.Release, error) {
		_ = httpClient
		_ = owner
		_ = username
		_ = token
		return &manifest.Release{
			SchemaVersion: 1,
			Release:       manifest.Meta{Version: "1.1", Channel: "dev", ReleasedAt: "2026-01-02T00:00:00Z"},
			Components: manifest.Components{
				API:   manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-api:1.1", Digest: "sha256:newapi"},
				App:   manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-app:1.1", Digest: "sha256:newapp"},
				Dash:  manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-dash:1.1", Digest: "sha256:newdash"},
				Siraj: manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-siraj_app:1.1", Digest: "sha256:newsiraj"},
			},
			Compatibility: manifest.Compatibility{
				App:   manifest.APIRange{API: ">=0.0.0-0"},
				Dash:  manifest.APIRange{API: ">=0.0.0-0"},
				Siraj: manifest.APIRange{API: ">=0.0.0-0"},
			},
			Requirements: manifest.Requirements{QabasCLI: ">=0.0.0", Docker: ">=27.0.0"},
			Database:     manifest.Database{MigrationStrategy: "expand-contract", DowngradeSupported: false},
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) == 0 || plan.Steps[0] == "already-at-target" {
		t.Fatalf("expected redeploy when digests moved under same version, steps=%v", plan.Steps)
	}
	if plan.Target.Components.API.Digest != "sha256:newapi" {
		t.Fatalf("api digest %q", plan.Target.Components.API.Digest)
	}
}

func TestDeployDevSameVersionSameDigestsSkips(t *testing.T) {
	e := engine(t, "1.1", "")
	digests := [4]string{"sha256:api", "sha256:app", "sha256:dash", "sha256:siraj"}
	writeInstalledRelease(t, e, "1.1", digests)
	e.DevResolver = func(httpClient *http.Client, owner, username, token string) (*manifest.Release, error) {
		_ = httpClient
		_ = owner
		_ = username
		_ = token
		return &manifest.Release{
			SchemaVersion: 1,
			Release:       manifest.Meta{Version: "1.1", Channel: "dev", ReleasedAt: "2026-01-02T00:00:00Z"},
			Components: manifest.Components{
				API:   manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-api:1.1", Digest: digests[0]},
				App:   manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-app:1.1", Digest: digests[1]},
				Dash:  manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-dash:1.1", Digest: digests[2]},
				Siraj: manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-siraj_app:1.1", Digest: digests[3]},
			},
			Compatibility: manifest.Compatibility{
				App:   manifest.APIRange{API: ">=0.0.0-0"},
				Dash:  manifest.APIRange{API: ">=0.0.0-0"},
				Siraj: manifest.APIRange{API: ">=0.0.0-0"},
			},
			Requirements: manifest.Requirements{QabasCLI: ">=0.0.0", Docker: ">=27.0.0"},
			Database:     manifest.Database{MigrationStrategy: "expand-contract", DowngradeSupported: false},
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0] != "already-at-target" {
		t.Fatalf("steps %v", plan.Steps)
	}
}

func TestDeployDevSelectivePullSkipsUnchangedInfra(t *testing.T) {
	e := engine(t, "1.1", "")
	digests := [4]string{"sha256:api", "sha256:app", "sha256:dash", "sha256:siraj"}
	writeInstalledRelease(t, e, "1.1", digests)
	e.DevResolver = func(httpClient *http.Client, owner, username, token string) (*manifest.Release, error) {
		_ = httpClient
		_ = owner
		_ = username
		_ = token
		return &manifest.Release{
			SchemaVersion: 1,
			Release:       manifest.Meta{Version: "1.1", Channel: "dev", ReleasedAt: "2026-01-02T00:00:00Z"},
			Components: manifest.Components{
				API:   manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-api:1.1", Digest: "sha256:newapi"},
				App:   manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-app:1.1", Digest: "sha256:newapp"},
				Dash:  manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-dash:1.1", Digest: "sha256:newdash"},
				Siraj: manifest.Component{Version: "1.1", Image: "ghcr.io/qabas1/qabas1-siraj_app:1.1", Digest: "sha256:newsiraj"},
			},
			Compatibility: manifest.Compatibility{
				App:   manifest.APIRange{API: ">=0.0.0-0"},
				Dash:  manifest.APIRange{API: ">=0.0.0-0"},
				Siraj: manifest.APIRange{API: ">=0.0.0-0"},
			},
			Requirements: manifest.Requirements{QabasCLI: ">=0.0.0", Docker: ">=27.0.0"},
			Database:     manifest.Database{MigrationStrategy: "expand-contract", DowngradeSupported: false},
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PullAll {
		t.Fatal("product-only digest change must not PullAll")
	}
	joined := strings.Join(plan.PullServices, ",")
	for _, infra := range []string{"minio", "postgres", "redis", "gotenberg"} {
		if strings.Contains(joined, infra) {
			t.Fatalf("unexpected infra service %q in PullServices %#v", infra, plan.PullServices)
		}
	}
	for _, product := range []string{"api", "app", "dash", "siraj"} {
		if !strings.Contains(joined, product) {
			t.Fatalf("missing product service %q in PullServices %#v", product, plan.PullServices)
		}
	}
}

func TestDeployScopedServiceMergesOneComponent(t *testing.T) {
	e := engine(t, "1.1", "")
	digests := [4]string{"sha256:api", "sha256:app", "sha256:dash", "sha256:siraj"}
	writeInstalledRelease(t, e, "1.1", digests)
	called := false
	e.DevComponentResolver = func(httpClient *http.Client, owner, service, username, token string) (manifest.Component, error) {
		called = true
		_ = httpClient
		_ = owner
		_ = username
		_ = token
		if service != "api" {
			t.Fatalf("service %q", service)
		}
		return manifest.Component{
			Version: "1.2",
			Image:   "ghcr.io/qabas1/qabas1-api:1.2",
			Digest:  "sha256:newapi",
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev", Service: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected DevComponentResolver")
	}
	if plan.Service != "api" {
		t.Fatalf("service %q", plan.Service)
	}
	if plan.Target.Components.API.Digest != "sha256:newapi" {
		t.Fatalf("api digest %q", plan.Target.Components.API.Digest)
	}
	if plan.Target.Components.App.Digest != digests[1] {
		t.Fatalf("app digest should stay %q, got %q", digests[1], plan.Target.Components.App.Digest)
	}
	if plan.Target.Components.Dash.Digest != digests[2] || plan.Target.Components.Siraj.Digest != digests[3] {
		t.Fatalf("unchanged digests mutated: dash=%q siraj=%q", plan.Target.Components.Dash.Digest, plan.Target.Components.Siraj.Digest)
	}
	if plan.Steps[0] == "already-at-target" {
		t.Fatalf("expected redeploy, steps=%v", plan.Steps)
	}
	human := deploy.FormatPlan(plan)
	if !strings.Contains(human, "Deploy api (dev)") {
		t.Fatalf("format missing scoped header:\n%s", human)
	}
}

func TestDeployScopedServiceSameDigestSkips(t *testing.T) {
	e := engine(t, "1.1", "")
	digests := [4]string{"sha256:api", "sha256:app", "sha256:dash", "sha256:siraj"}
	writeInstalledRelease(t, e, "1.1", digests)
	e.DevComponentResolver = func(httpClient *http.Client, owner, service, username, token string) (manifest.Component, error) {
		_ = httpClient
		_ = owner
		_ = service
		_ = username
		_ = token
		return manifest.Component{
			Version: "1.1",
			Image:   "ghcr.io/qabas1/qabas1-app:1.1",
			Digest:  digests[1],
		}, nil
	}

	plan, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev", Service: "app"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[0] != "already-at-target" {
		t.Fatalf("steps %v", plan.Steps)
	}
}

func TestDeployScopedServiceRequiresInstall(t *testing.T) {
	e := engine(t, "", "")
	e.DevComponentResolver = func(httpClient *http.Client, owner, service, username, token string) (manifest.Component, error) {
		t.Fatal("should not resolve before install check")
		return manifest.Component{}, nil
	}
	_, err := e.BuildPlan(deploy.Options{Action: deploy.ActionDeploy, Spec: "dev", Service: "api"})
	if err == nil {
		t.Fatal("expected not-installed error")
	}
	if !strings.Contains(err.Error(), "existing QabasOne installation") {
		t.Fatalf("error %q", err)
	}
}
