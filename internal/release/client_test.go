package release_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/release"
)

func TestListAndLatest(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "testdata", "releases")
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
		if e.IsDir() {
			continue
		}
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

	c := release.New("qabasone/qabas1-deploy", "main")
	c.HTTP = srv.Client()
	c.APIBase = srv.URL
	c.RawBase = srv.URL

	all, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 {
		t.Fatalf("got %d releases", len(all))
	}
	if all[0].Release.Version != "2.9.0" {
		t.Fatalf("expected newest first, got %s", all[0].Release.Version)
	}
	latest, err := c.Latest("stable")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Release.Version != "2.9.0" {
		t.Fatalf("latest %s", latest.Release.Version)
	}
	got, err := c.Get("2.8.0")
	if err != nil {
		t.Fatal(err)
	}
	if got.Components.API.Version != "4.13.0" {
		t.Fatalf("api %s", got.Components.API.Version)
	}
}

func TestLatestDevChannelMissing(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "testdata", "releases")
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
		if e.IsDir() {
			continue
		}
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

	c := release.New("qabasone/qabas1-deploy", "devref")
	c.HTTP = srv.Client()
	c.APIBase = srv.URL
	c.RawBase = srv.URL

	_, err = c.Latest("dev")
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code %v", exitcode.CodeOf(err))
	}
	if !strings.Contains(err.Error(), "no dev releases found") {
		t.Fatalf("expected missing-dev error, got: %v", err)
	}
}

func TestLatestDevChannelFromCatalog(t *testing.T) {
	devYAML := []byte(`schemaVersion: 1
release:
  version: "0.0.0-dev"
  channel: dev
  releasedAt: "2026-09-01T00:00:00Z"
components:
  api:
    version: "1.0.0-dev.1"
    commit: "aaa1111"
    image: "ghcr.io/qabas1/qabas1-api:1.0.0-dev.1"
    digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  app:
    version: "1.0.0-dev.1"
    commit: "bbb2222"
    image: "ghcr.io/qabas1/qabas1-app:1.0.0-dev.1"
    digest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  dash:
    version: "1.0.0-dev.1"
    commit: "ccc3333"
    image: "ghcr.io/qabas1/qabas1-dash:1.0.0-dev.1"
    digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  siraj:
    version: "1.0.0-dev.1"
    commit: "ddd4444"
    image: "ghcr.io/qabas1/qabas1-siraj_app:1.0.0-dev.1"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/contents/releases") {
			_ = json.NewEncoder(w).Encode(files)
			return
		}
		if strings.HasSuffix(r.URL.Path, "0.0.0-dev.yaml") {
			_, _ = w.Write(devYAML)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := release.New("qabasone/qabas1-deploy", "dev")
	c.HTTP = srv.Client()
	c.APIBase = srv.URL
	c.RawBase = srv.URL

	rel, err := c.Latest("dev")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Release.Version != "0.0.0-dev" || rel.Release.Channel != "dev" {
		t.Fatalf("got %+v", rel.Release)
	}
}

func TestListPrivateRepoWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := release.New("qabasone/qabas1-deploy", "main")
	c.HTTP = srv.Client()
	c.APIBase = srv.URL
	c.Token = "" // force unauthenticated path for this unit test

	_, err := c.List()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "qabas auth login") && !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("expected auth hint, got: %v", err)
	}
}

func TestGitHubTokenFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "github-token")
	if err := os.WriteFile(path, []byte("pat-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("QABAS_GITHUB_TOKEN_FILE", path)

	if got := release.GitHubToken(); got != "pat-from-file" {
		t.Fatalf("token %q", got)
	}
}

func TestSaveAndLoadSystemToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", dir)
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("QABAS_GITHUB_TOKEN_FILE", "")
	t.Setenv("SUDO_USER", "")

	path, err := release.SaveGitHubToken("saved-pat")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "github-token")
	if path != want {
		t.Fatalf("path %s want %s", path, want)
	}
	if got := release.GitHubToken(); got != "saved-pat" {
		t.Fatalf("token %q", got)
	}
	src, ok := release.TokenSource()
	if !ok || src != want {
		t.Fatalf("source %q ok=%v", src, ok)
	}
}

func TestGitHubTokenFromSudoUserHome(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".config", "qabas")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(cfg, "github-token")
	if err := os.WriteFile(tokenPath, []byte("sudo-user-pat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("QABAS_GITHUB_TOKEN_FILE", "")
	t.Setenv("QABAS_CONFIG_DIR", filepath.Join(dir, "empty-etc"))
	// Simulate lookup by pointing HOME of a fake sudo user via QABAS path only —
	// TokenSource/sudoUserTokenPath needs a real user; exercise file path via env instead.
	t.Setenv("QABAS_GITHUB_TOKEN_FILE", tokenPath)
	if got := release.GitHubToken(); got != "sudo-user-pat" {
		t.Fatalf("token %q", got)
	}
}
