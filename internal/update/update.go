package update

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/hint"
	"github.com/qabasone/qabasone-cli/internal/httpx"
	"github.com/qabasone/qabasone-cli/internal/release"
	"github.com/qabasone/qabasone-cli/internal/version"
)

const repo = "qabasone/qabasone-cli"

type Info struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	Track   string `json:"track,omitempty"`
	Asset   string `json:"asset,omitempty"`
	URL     string `json:"url,omitempty"`
	SumsURL string `json:"sumsUrl,omitempty"`
	Private bool   `json:"private,omitempty"`
}

func Check(apiBase string) (*Info, error) {
	return CheckTrack(apiBase, "stable")
}

func CheckTrack(apiBase, track string) (*Info, error) {
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	track = normalizeTrack(track)
	u := strings.TrimRight(apiBase, "/") + "/repos/" + repo + "/releases/latest"
	if track == "dev" {
		u = strings.TrimRight(apiBase, "/") + "/repos/" + repo + "/releases/tags/dev-latest"
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "qabas-cli")
	req.Header.Set("Accept", "application/vnd.github+json")
	if token := release.GitHubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := httpx.New(20 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, exitcode.New(exitcode.Network, "check CLI update: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		info := &Info{Current: version.Version, Latest: version.Version, Track: track}
		if release.GitHubToken() == "" {
			info.Private = true
		}
		return info, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, exitcode.New(exitcode.Network, "check CLI update: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, err
	}
	latest := releaseVersion(rel.Tag)
	info := &Info{Current: version.Version, Latest: latest, Track: track}
	token := release.GitHubToken()
	for _, a := range rel.Assets {
		if a.Name == "SHA256SUMS" {
			info.SumsURL = assetURL(apiBase, a.ID, a.URL, token != "")
			break
		}
	}
	wants := wantedAssets(latest)
pick:
	for _, want := range wants {
		for _, a := range rel.Assets {
			if a.Name == want {
				info.Asset = a.Name
				info.URL = assetURL(apiBase, a.ID, a.URL, token != "")
				info.Private = token != "" && a.ID != 0
				break pick
			}
		}
	}
	if info.Asset == "" && len(wants) > 0 {
		info.Asset = wants[0]
	}
	return info, nil
}

func normalizeTrack(track string) string {
	switch strings.ToLower(strings.TrimSpace(track)) {
	case "", "stable":
		return "stable"
	case "dev", "dev-latest":
		return "dev"
	default:
		return track
	}
}

func releaseVersion(tag string) string {
	if tag == "dev-latest" {
		return "dev"
	}
	return strings.TrimPrefix(tag, "v")
}

func wantedAssets(ver string) []string {
	base := fmt.Sprintf("qabas_%s_%s_%s", ver, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("dpkg"); err == nil {
			return []string{base + ".deb"}
		}
	}
	if runtime.GOOS == "windows" {
		if installedViaMSI() {
			return []string{base + ".msi", base + ".exe"}
		}
		return []string{base + ".exe", base + ".msi"}
	}
	return []string{base, base + ".exe", "qabas-" + runtime.GOOS + "-" + runtime.GOARCH}
}

func missingAssetHint() string {
	if runtime.GOOS == "windows" {
		return "publish the .msi/.exe asset in the GitHub release, then rerun `qabas update`"
	}
	return "publish the .deb asset in the GitHub release, then rerun `sudo qabas update`"
}

// MissingAssetHint is the next step when a release has no package for this OS.
func MissingAssetHint() string { return missingAssetHint() }

func assetURL(apiBase string, id int, downloadURL string, useAPI bool) string {
	if useAPI && id != 0 {
		return fmt.Sprintf("%s/repos/%s/releases/assets/%d", strings.TrimRight(apiBase, "/"), repo, id)
	}
	return downloadURL
}

// Apply downloads, verifies, and installs the update. started is true when the
// install continues in a detached process (Windows MSI) after qabas exits.
func Apply(info *Info) (started bool, err error) {
	if info == nil || info.URL == "" || info.Asset == "" {
		asset := "this platform"
		if info != nil && info.Asset != "" {
			asset = info.Asset
		}
		return false, exitcode.NewHint(exitcode.Network, missingAssetHint(),
			"no CLI package published for %s", asset)
	}
	dir, err := os.MkdirTemp("", "qabas-update-*")
	if err != nil {
		return false, err
	}
	keepDir := false
	defer func() {
		if !keepDir {
			_ = os.RemoveAll(dir)
		}
	}()
	assetPath := filepath.Join(dir, info.Asset)
	if err := Download(info.URL, assetPath); err != nil {
		return false, err
	}
	if info.SumsURL != "" {
		sumsPath := filepath.Join(dir, "SHA256SUMS")
		if err := Download(info.SumsURL, sumsPath); err != nil {
			return false, err
		}
		if err := VerifyChecksum(sumsPath, assetPath, info.Asset); err != nil {
			return false, err
		}
	}
	switch {
	case strings.HasSuffix(info.Asset, ".deb"):
		if os.Geteuid() != 0 {
			return false, exitcode.NewHint(exitcode.InvalidInput,
				"run `sudo qabas update`",
				"installing the Debian package requires root")
		}
		cmd := exec.Command("dpkg", "-i", assetPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return false, exitcode.New(exitcode.Generic, "install %s with dpkg: %w", info.Asset, err)
		}
		switch linked, err := LinkStaleBinary(StaleBinary, PackageBinary); {
		case err != nil:
			fmt.Fprintf(os.Stderr, "warning: %s shadows %s on PATH and could not be replaced (%v); remove it with: sudo rm %s\n", StaleBinary, PackageBinary, err, StaleBinary)
		case linked:
			fmt.Printf("Replaced stale %s with a link to %s.\n", StaleBinary, PackageBinary)
		}
		return false, nil
	case strings.HasSuffix(info.Asset, ".msi"):
		// msiexec reads the package after qabas exits; leave the temp dir behind.
		if err := applyMSI(assetPath); err != nil {
			return false, err
		}
		keepDir = true
		return true, nil
	}
	return false, installBinary(assetPath)
}

const (
	// PackageBinary is where the Debian package installs qabas.
	PackageBinary = "/usr/bin/qabas"
	// StaleBinary is where older CLIs copied the writer's binary onto replicas;
	// it comes first on PATH and shadows PackageBinary.
	StaleBinary = "/usr/local/bin/qabas"
)

// LinkStaleBinary atomically replaces a regular file at stale with a symlink to
// pkg, so PATH lookups and units that still name stale run the packaged build.
// It reports false without changes when stale is absent or already a link, or
// pkg does not exist.
func LinkStaleBinary(stale, pkg string) (bool, error) {
	fi, err := os.Lstat(stale)
	if err != nil || !fi.Mode().IsRegular() {
		return false, nil
	}
	if _, err := os.Stat(pkg); err != nil {
		return false, nil
	}
	tmp := stale + ".qabas-link"
	_ = os.Remove(tmp)
	if err := os.Symlink(pkg, tmp); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, stale); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// CleanupStale removes the previous binary left behind by a Windows self-update
// (a running .exe can be renamed but not deleted).
func CleanupStale() {
	if runtime.GOOS != "windows" {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

func installBinary(assetPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return replaceBinary(runtime.GOOS, assetPath, exe)
}

func replaceBinary(goos, assetPath, exe string) error {
	next := exe + ".new"
	in, err := os.Open(assetPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(next, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if goos == "windows" {
		old := exe + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exe, old); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(next, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(next, exe)
}

func Download(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "qabas-cli")
	req.Header.Set("Accept", "application/octet-stream")
	if token := release.GitHubToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := httpx.New(60 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return exitcode.New(exitcode.Network, "download CLI update: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return exitcode.NewHint(exitcode.Network,
				hint.Run("qabas auth login")+" or set GITHUB_TOKEN/GH_TOKEN with repo access",
				"download CLI update: HTTP %d", resp.StatusCode)
		}
		return exitcode.New(exitcode.Network, "download CLI update: HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		return exitcode.New(exitcode.Network, "download CLI update: empty asset")
	}
	return nil
}

func VerifyChecksum(sumsPath, assetPath, assetName string) error {
	want := ""
	f, err := os.Open(sumsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[1] == assetName {
			want = fields[0]
			break
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if want == "" {
		return exitcode.New(exitcode.Network, "checksum for %s not found", assetName)
	}
	b, err := os.ReadFile(assetPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(b)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return exitcode.New(exitcode.Network, "checksum mismatch for %s", assetName)
	}
	return nil
}

func NeedsUpdate(current, latest string) bool {
	c, err1 := semver.NewVersion(strings.TrimPrefix(current, "v"))
	l, err2 := semver.NewVersion(strings.TrimPrefix(latest, "v"))
	if err1 != nil || err2 != nil {
		return current != latest && latest != ""
	}
	return l.GreaterThan(c)
}
