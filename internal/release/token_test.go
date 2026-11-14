package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/release"
)

func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_TOKEN", "GH_TOKEN", "QABAS_GITHUB_TOKEN_FILE",
		"GHCR_TOKEN", "QABAS_GHCR_TOKEN", "QABAS_GHCR_TOKEN_FILE",
		"GHCR_USERNAME", "GITHUB_USER",
		"SUDO_USER",
	} {
		t.Setenv(k, "")
	}
	// Prevent best-effort `gh auth token` / `gh api user` from using the host login.
	t.Setenv("PATH", t.TempDir())
}

func TestGHCRSaveLoadUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", dir)
	clearAuthEnv(t)

	if _, err := release.SaveGHCRToken("ghcr-pat"); err != nil {
		t.Fatal(err)
	}
	if _, err := release.SaveGHCRUsername("alice"); err != nil {
		t.Fatal(err)
	}
	if got := release.GHCRToken(); got != "ghcr-pat" {
		t.Fatalf("token %q", got)
	}
	if got := release.GHCRUsername(); got != "alice" {
		t.Fatalf("user %q", got)
	}
	src, ok := release.GHCRTokenSource()
	want := filepath.Join(dir, "ghcr-token")
	if !ok || src != want {
		t.Fatalf("source %q ok=%v want %s", src, ok, want)
	}

	st := release.Status()
	if !st.GHCROK || st.GHCRUsername != "alice" {
		t.Fatalf("status %+v", st)
	}

	if err := release.UnsetGHCR(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ghcr-token")); !os.IsNotExist(err) {
		t.Fatalf("ghcr-token still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ghcr-username")); !os.IsNotExist(err) {
		t.Fatalf("ghcr-username still present: %v", err)
	}
}

func TestGHCRFallsBackToGitHub(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", dir)
	clearAuthEnv(t)

	if _, err := release.SaveGitHubToken("shared-pat"); err != nil {
		t.Fatal(err)
	}
	if got := release.GHCRToken(); got != "shared-pat" {
		t.Fatalf("fallback token %q", got)
	}
	src, ok := release.GHCRTokenSource()
	if !ok || !strings.Contains(src, "github token") {
		t.Fatalf("source %q ok=%v", src, ok)
	}

	t.Setenv("GHCR_USERNAME", "dana")
	if err := release.RequireAuth(); err != nil {
		t.Fatal(err)
	}
	st := release.Status()
	if !st.OK || !strings.Contains(st.GHCRSource, "github token") {
		t.Fatalf("status %+v", st)
	}
}

func TestRequireAuth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", dir)
	clearAuthEnv(t)

	err := release.RequireAuth()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing required credentials") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "qabas auth login") {
		t.Fatalf("expected login hint: %v", err)
	}

	if _, err := release.SaveGitHubToken("gh-pat"); err != nil {
		t.Fatal(err)
	}
	err = release.RequireAuth()
	if err == nil || !strings.Contains(err.Error(), "ghcr username") {
		t.Fatalf("expected ghcr username still missing: %v", err)
	}

	if _, err := release.SaveGHCRUsername("bob"); err != nil {
		t.Fatal(err)
	}
	if err := release.RequireAuth(); err != nil {
		t.Fatal(err)
	}
	st := release.Status()
	if !st.OK {
		t.Fatalf("status not ok: %+v", st)
	}
}

func TestGHCRTokenFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", dir)
	clearAuthEnv(t)
	t.Setenv("GHCR_TOKEN", "env-ghcr")
	t.Setenv("GHCR_USERNAME", "carol")
	if got := release.GHCRToken(); got != "env-ghcr" {
		t.Fatalf("token %q", got)
	}
	if got := release.GHCRUsername(); got != "carol" {
		t.Fatalf("user %q", got)
	}
	src, ok := release.GHCRTokenSource()
	if !ok || src != "GHCR_TOKEN" {
		t.Fatalf("source %q ok=%v", src, ok)
	}
}

func TestParseKind(t *testing.T) {
	k, err := release.ParseKind("github")
	if err != nil || k != release.KindGitHub {
		t.Fatalf("%v %v", k, err)
	}
	k, err = release.ParseKind("ghcr")
	if err != nil || k != release.KindGHCR {
		t.Fatalf("%v %v", k, err)
	}
	if _, err := release.ParseKind("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func TestUnsetKind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QABAS_CONFIG_DIR", dir)
	clearAuthEnv(t)
	if _, err := release.SaveGitHubToken("x"); err != nil {
		t.Fatal(err)
	}
	if err := release.UnsetKind(release.KindGitHub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "github-token")); !os.IsNotExist(err) {
		t.Fatalf("token file still present: %v", err)
	}
}
