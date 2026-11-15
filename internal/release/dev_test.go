package release

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/manifest"
)

func mustVersions(t *testing.T, raw string) []packageVersion {
	t.Helper()
	var versions []packageVersion
	if err := json.Unmarshal([]byte(raw), &versions); err != nil {
		t.Fatal(err)
	}
	return versions
}

func TestNewestDevTag(t *testing.T) {
	got, err := newestDevTag([]string{"latest", "dev-latest", "1.0.0-dev.1", "1.0.0-dev.3", "bad-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.0.0-dev.3" {
		t.Fatalf("tag %q", got)
	}
}

func TestNewestDevTagAllowsConcreteNonSemver(t *testing.T) {
	got, err := newestDevTag([]string{"latest", "dev", "feat-dev-free-port-before-start"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "feat-dev-free-port-before-start" {
		t.Fatalf("tag %q", got)
	}
}

func TestPickLatestPublishedTag(t *testing.T) {
	tag, digest, err := pickLatestPublishedTag(mustVersions(t, `[
		{"name":"sha256:`+strings.Repeat("a", 64)+`","updated_at":"2026-01-01T00:00:00Z","metadata":{"container":{"tags":["1.0.0","latest"]}}},
		{"name":"sha256:`+strings.Repeat("b", 64)+`","updated_at":"2026-09-11T12:00:00Z","metadata":{"container":{"tags":["feat-dev-free-port-before-start","latest"]}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if tag != "feat-dev-free-port-before-start" {
		t.Fatalf("tag %q", tag)
	}
	if digest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("digest %q", digest)
	}
}

func TestPickLatestPublishedTagIncludesShaOnlyNewest(t *testing.T) {
	// Newer CI pushes often only have sha-<commit> tags. Older PR builds may still
	// carry a concrete tag — last published must still win by updated_at.
	oldDigest := "sha256:" + strings.Repeat("f", 64)
	newDigest := "sha256:" + strings.Repeat("e", 64)
	tag, digest, err := pickLatestPublishedTag(mustVersions(t, `[
		{"name":"`+oldDigest+`","updated_at":"2026-09-09T00:00:00Z","metadata":{"container":{"tags":["pr-1076-renew-posting-worker-leader-lease","sha-b6d1fa05daa5"]}}},
		{"name":"`+newDigest+`","updated_at":"2026-09-12T01:00:00Z","metadata":{"container":{"tags":["sha-ee58e7abcdef"]}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if digest != newDigest {
		t.Fatalf("digest %q want %q", digest, newDigest)
	}
	if tag != "sha-ee58e7abcdef" {
		t.Fatalf("tag %q", tag)
	}
}

func TestPickLatestPublishedTagAllowsDevLatest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	tag, got, err := pickLatestPublishedTag(mustVersions(t, `[
		{"name":"sha256:`+strings.Repeat("a", 64)+`","updated_at":"2026-09-01T00:00:00Z","metadata":{"container":{"tags":["1.0.0-dev.1"]}}},
		{"name":"`+digest+`","updated_at":"2026-09-12T02:00:00Z","metadata":{"container":{"tags":["dev","latest"]}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if got != digest {
		t.Fatalf("digest %q", got)
	}
	if tag != "dev" && tag != "latest" {
		t.Fatalf("tag %q", tag)
	}
}

func TestPreferImageTagConcreteOverSha(t *testing.T) {
	if !preferImageTag("pr-1076-renew", "sha-ee58e7") {
		t.Fatal("expected concrete tag over sha-")
	}
	if preferImageTag("latest", "1.0.0-dev.2") {
		t.Fatal("expected SemVer over latest")
	}
}

func TestLatestDevFromGHCR(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	payload := `[{"name":"` + digest + `","updated_at":"2026-09-11T12:00:00Z","metadata":{"container":{"tags":["1.0.0-dev.2","latest"]}}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/packages/container/") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer pat" {
			t.Fatalf("auth %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	rel, err := LatestDevFromGHCR(srv.Client(), "qabasone", "alice", "pat")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Release.Channel != "dev" || rel.Release.Version != "1.0.0-dev.2" {
		t.Fatalf("release %#v", rel.Release)
	}
	if rel.Components.API.Digest != digest {
		t.Fatalf("api digest %q", rel.Components.API.Digest)
	}
	if !strings.HasSuffix(rel.Components.API.Image, ":1.0.0-dev.2") {
		t.Fatalf("api image %s", rel.Components.API.Image)
	}
	if rel.Infra != (manifest.Infra{}) {
		t.Fatalf("expected empty infra so compose defaults apply, got %#v", rel.Infra)
	}
}

func TestLatestDevFromGHCRNormalizesNonSemverComponentVersions(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	payload := `[{"name":"` + digest + `","updated_at":"2026-09-11T12:00:00Z","metadata":{"container":{"tags":["dev-latest"]}}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	rel, err := LatestDevFromGHCR(srv.Client(), "qabasone", "alice", "pat")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Release.Version == "dev-latest" || !strings.HasPrefix(rel.Release.Version, "0.0.0-dev.") {
		t.Fatalf("release version %q", rel.Release.Version)
	}
	if rel.Components.API.Version != rel.Release.Version {
		t.Fatalf("api version %q release %q", rel.Components.API.Version, rel.Release.Version)
	}
	if rel.Components.API.Image != "ghcr.io/qabasone/qabas1-api:dev-latest" {
		t.Fatalf("api image %q", rel.Components.API.Image)
	}
	if err := rel.CheckCompatibility(); err != nil {
		t.Fatal(err)
	}
}

func TestLatestDevComponent(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	payload := `[{"name":"` + digest + `","updated_at":"2026-09-11T12:00:00Z","metadata":{"container":{"tags":["1.2.0-dev.1","latest"]}}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/packages/container/qabas1-app/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })

	comp, err := LatestDevComponent(srv.Client(), "qabasone", "app", "alice", "pat")
	if err != nil {
		t.Fatal(err)
	}
	if comp.Digest != digest {
		t.Fatalf("digest %q", comp.Digest)
	}
	if !strings.HasSuffix(comp.Image, ":1.2.0-dev.1") {
		t.Fatalf("image %q", comp.Image)
	}

	_, err = LatestDevComponent(srv.Client(), "qabasone", "postgres", "alice", "pat")
	if err == nil {
		t.Fatal("expected unknown service error")
	}
}
