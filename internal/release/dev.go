package release

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/hint"
	"github.com/qabas1/qabasone-cli/internal/httpx"
	"github.com/qabas1/qabasone-cli/internal/manifest"
)

var ghcrBase = "https://ghcr.io"
var githubAPIBase = "https://api.github.com"

var devPackages = map[string]string{
	"api":   "qabasone-api",
	"app":   "qabasone-app",
	"dash":  "qabasone-dash",
	"siraj": "qabasone-siraj_app",
}

// LatestDevFromGHCR builds an immutable dev release manifest from the last-pushed
// GHCR package version for each component (GitHub Packages updated_at).
func LatestDevFromGHCR(httpClient *http.Client, owner, username, token string) (*manifest.Release, error) {
	if httpClient == nil {
		httpClient = httpx.New(30 * time.Second)
	}
	if owner == "" {
		owner = "qabasone"
	}
	if username == "" || token == "" {
		return nil, exitcode.NewHint(exitcode.InvalidInput,
			"run `"+hint.CmdEnv("qabas auth login --username YOUR_GITHUB_USER")+"`",
			"GitHub credentials are required to discover last-pushed images")
	}

	api, err := latestDevComponent(httpClient, owner, devPackages["api"], username, token)
	if err != nil {
		return nil, err
	}
	app, err := latestDevComponent(httpClient, owner, devPackages["app"], username, token)
	if err != nil {
		return nil, err
	}
	dash, err := latestDevComponent(httpClient, owner, devPackages["dash"], username, token)
	if err != nil {
		return nil, err
	}
	siraj, err := latestDevComponent(httpClient, owner, devPackages["siraj"], username, token)
	if err != nil {
		return nil, err
	}
	releaseVersion := devReleaseVersion(api.Version, app.Version, dash.Version, siraj.Version)
	for _, c := range []*manifest.Component{&api, &app, &dash, &siraj} {
		if !isSemVer(c.Version) {
			c.Version = releaseVersion
		}
	}

	rel := &manifest.Release{
		SchemaVersion: 1,
		Release: manifest.Meta{
			Version:    releaseVersion,
			Channel:    "dev",
			ReleasedAt: time.Now().UTC().Format(time.RFC3339),
		},
		Components: manifest.Components{API: api, App: app, Dash: dash, Siraj: siraj},
		// Infra stays empty so compose.yaml defaults from qabasone-deploy@dev apply.
		Compatibility: manifest.Compatibility{
			App:   manifest.APIRange{API: ">=0.0.0-0"},
			Dash:  manifest.APIRange{API: ">=0.0.0-0"},
			Siraj: manifest.APIRange{API: ">=0.0.0-0"},
		},
		Requirements: manifest.Requirements{
			QabasCLI: ">=0.0.0",
			Docker:   ">=27.0.0",
		},
		Database: manifest.Database{MigrationStrategy: "expand-contract", DowngradeSupported: false},
	}
	if err := rel.Validate(); err != nil {
		return nil, err
	}
	return rel, nil
}

// LatestDevComponent resolves the last-pushed GHCR package for one product
// component (api, app, dash, or siraj).
func LatestDevComponent(httpClient *http.Client, owner, service, username, token string) (manifest.Component, error) {
	if httpClient == nil {
		httpClient = httpx.New(30 * time.Second)
	}
	if owner == "" {
		owner = "qabasone"
	}
	if username == "" || token == "" {
		return manifest.Component{}, exitcode.NewHint(exitcode.InvalidInput,
			"run `"+hint.CmdEnv("qabas auth login --username YOUR_GITHUB_USER")+"`",
			"GitHub credentials are required to discover last-pushed images")
	}
	pkg, ok := devPackages[strings.ToLower(strings.TrimSpace(service))]
	if !ok {
		return manifest.Component{}, exitcode.New(exitcode.InvalidInput,
			"unknown product service %q (want api, app, dash, or siraj)", service)
	}
	return latestDevComponent(httpClient, owner, pkg, username, token)
}

// ResolveComponent selects a published package by tag, version, or full/unique
// digest prefix. The returned image is always pinned to its full digest.
func ResolveComponent(httpClient *http.Client, owner, service, spec, username, token string) (manifest.Component, error) {
	if httpClient == nil {
		httpClient = httpx.New(30 * time.Second)
	}
	if owner == "" {
		owner = "qabasone"
	}
	if token == "" {
		return manifest.Component{}, exitcode.New(exitcode.InvalidInput, "GHCR credentials are required")
	}
	pkg, ok := devPackages[service]
	if !ok {
		return manifest.Component{}, exitcode.New(exitcode.InvalidInput, "unknown product service %q", service)
	}
	versions, err := listPackageVersions(httpClient, owner, pkg, token)
	if err != nil {
		return manifest.Component{}, err
	}
	want := strings.TrimPrefix(strings.TrimSpace(spec), "sha256:")
	var found *packageVersion
	var tag string
	for i := range versions {
		v := &versions[i]
		if !strings.HasPrefix(v.Name, "sha256:") {
			continue
		}
		match := false
		if len(want) >= 12 && strings.HasPrefix(strings.TrimPrefix(v.Name, "sha256:"), want) {
			match = true
		}
		for _, t := range v.Metadata.Container.Tags {
			if t == spec {
				match = true
				tag = t
				break
			}
		}
		if !match {
			continue
		}
		if found != nil && found.Name != v.Name {
			return manifest.Component{}, exitcode.New(exitcode.InvalidInput, "image selector %q is ambiguous", spec)
		}
		found = v
	}
	if found == nil {
		return manifest.Component{}, exitcode.New(exitcode.InvalidInput, "no published %s image matches %q", service, spec)
	}
	if tag == "" || tag == "latest" {
		tags := allImageTags(found.Metadata.Container.Tags)
		if len(tags) > 0 {
			sort.SliceStable(tags, func(i, j int) bool { return preferImageTag(tags[i], tags[j]) })
			tag = tags[0]
		}
	}
	if tag == "" || tag == "latest" {
		tag = "digest-" + strings.TrimPrefix(found.Name, "sha256:")[:12]
	}
	_ = username
	return manifest.Component{Version: tag, Image: fmt.Sprintf("ghcr.io/%s/%s:%s", owner, pkg, tag), Digest: found.Name}, nil
}

func latestDevComponent(httpClient *http.Client, owner, pkg, username, token string) (manifest.Component, error) {
	tag, digest, err := latestPublishedFromPackages(httpClient, owner, pkg, token)
	if err != nil {
		return manifest.Component{}, exitcode.New(exitcode.InvalidInput, "%s: %w", pkg, err)
	}
	_ = username
	return manifest.Component{
		Version: tag,
		Image:   fmt.Sprintf("ghcr.io/%s/%s:%s", owner, pkg, tag),
		Digest:  digest,
	}, nil
}

type packageVersion struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
	Metadata  struct {
		Container struct {
			Tags []string `json:"tags"`
		} `json:"container"`
	} `json:"metadata"`
}

func latestPublishedFromPackages(httpClient *http.Client, owner, pkg, token string) (string, string, error) {
	versions, err := listPackageVersions(httpClient, owner, pkg, token)
	if err != nil {
		return "", "", err
	}
	return pickLatestPublishedTag(versions)
}

func listPackageVersions(httpClient *http.Client, owner, pkg, token string) ([]packageVersion, error) {
	var out []packageVersion
	for page := 1; page <= 20; page++ {
		u := fmt.Sprintf("%s/orgs/%s/packages/container/%s/versions?per_page=100&page=%d",
			strings.TrimRight(githubAPIBase, "/"), owner, url.PathEscape(pkg), page)
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, exitcode.New(exitcode.Network, "list package versions for %s: %w", pkg, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, exitcode.New(exitcode.Network, "list package versions for %s: HTTP %d: %s", pkg, resp.StatusCode, truncate(body))
		}
		var batch []packageVersion
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, exitcode.New(exitcode.Network, "list package versions for %s: decode: %w", pkg, err)
		}
		if len(batch) == 0 {
			break
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

func pickLatestPublishedTag(versions []packageVersion) (string, string, error) {
	var bestTime string
	var bestTags []string
	var bestDigest string
	for _, version := range versions {
		if !strings.HasPrefix(version.Name, "sha256:") {
			continue
		}
		// Last-pushed wins by updated_at, including versions that only have
		// floating tags (dev/latest) or commit tags (sha-*). Deploy pulls by digest.
		tags := allImageTags(version.Metadata.Container.Tags)
		if len(tags) == 0 {
			continue
		}
		updated := version.UpdatedAt
		if updated == "" {
			updated = version.CreatedAt
		}
		if bestTime == "" || updated > bestTime {
			bestTime = updated
			bestTags = append([]string(nil), tags...)
			bestDigest = version.Name
		}
	}
	if len(bestTags) == 0 || bestDigest == "" {
		return "", "", fmt.Errorf("no published package versions with tags")
	}
	sort.SliceStable(bestTags, func(i, j int) bool {
		return preferImageTag(bestTags[i], bestTags[j])
	})
	return bestTags[0], bestDigest, nil
}

func allImageTags(tags []string) []string {
	var out []string
	for _, tag := range tags {
		raw := strings.TrimSpace(tag)
		if raw == "" {
			continue
		}
		out = append(out, raw)
	}
	return out
}

func isFloatingOrShaTag(tag string) bool {
	lower := strings.ToLower(strings.TrimSpace(tag))
	return lower == "latest" || lower == "dev" || strings.HasPrefix(lower, "sha-")
}

func concreteImageTags(tags []string) []string {
	var out []string
	for _, tag := range allImageTags(tags) {
		if isFloatingOrShaTag(tag) {
			continue
		}
		out = append(out, tag)
	}
	return out
}

// preferImageTag reports whether a should rank above b for the display/image tag
// on an already-chosen package version (concrete > SemVer height > name).
func preferImageTag(a, b string) bool {
	fa, fb := isFloatingOrShaTag(a), isFloatingOrShaTag(b)
	if fa != fb {
		return !fa
	}
	va, ea := semver.NewVersion(strings.TrimPrefix(a, "v"))
	vb, eb := semver.NewVersion(strings.TrimPrefix(b, "v"))
	if ea == nil && eb != nil {
		return true
	}
	if ea != nil && eb == nil {
		return false
	}
	if ea == nil && eb == nil {
		return va.GreaterThan(vb)
	}
	return a > b
}

// newestDevTag prefers SemVer among a flat tag list (legacy helper / tests).
func newestDevTag(tags []string) (string, error) {
	concrete := concreteImageTags(tags)
	if len(concrete) == 0 {
		return "", fmt.Errorf("no concrete image tags found")
	}
	sort.SliceStable(concrete, func(i, j int) bool {
		return preferImageTag(concrete[i], concrete[j])
	})
	return concrete[0], nil
}

func devReleaseVersion(versions ...string) string {
	var best *semver.Version
	var bestRaw string
	for _, version := range versions {
		v, err := semver.NewVersion(strings.TrimPrefix(version, "v"))
		if err != nil {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
			bestRaw = strings.TrimPrefix(version, "v")
		}
	}
	if best != nil {
		return bestRaw
	}
	return "0.0.0-dev." + fmt.Sprint(time.Now().UTC().Unix())
}

func isSemVer(version string) bool {
	_, err := semver.NewVersion(strings.TrimPrefix(version, "v"))
	return err == nil
}
