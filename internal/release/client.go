package release

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/httpx"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

const defaultAPI = "https://api.github.com"
const defaultRaw = "https://raw.githubusercontent.com"

type Client struct {
	HTTP    *http.Client
	Repo    string
	Ref     string
	APIBase string
	RawBase string
	Token   string
}

func New(repo, ref string) *Client {
	if repo == "" {
		repo = "qabas1/qabas1-deploy"
	}
	if ref == "" {
		ref = "main"
	}
	return &Client{
		HTTP:    httpx.New(30 * time.Second),
		Repo:    repo,
		Ref:     ref,
		APIBase: envOr("QABAS_GITHUB_API", defaultAPI),
		RawBase: envOr("QABAS_GITHUB_RAW", defaultRaw),
		Token:   GitHubToken(),
	}
}

type Listing struct {
	Version string
	Name    string
}

func (c *Client) List() ([]*manifest.Release, error) {
	names, err := c.listFiles()
	if err != nil {
		return nil, err
	}
	var out []*manifest.Release
	for _, name := range names {
		if !isManifest(name) {
			continue
		}
		raw, err := c.fetchRaw(path.Join("releases", name))
		if err != nil {
			return nil, err
		}
		rel, err := manifest.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := c.verifyChecksum(name, raw); err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	sort.Slice(out, func(i, j int) bool {
		return manifest.Compare(out[i].Release.Version, out[j].Release.Version) > 0
	})
	return out, nil
}

func (c *Client) Get(version string) (*manifest.Release, error) {
	version = strings.TrimPrefix(version, "v")
	raw, err := c.fetchRaw("releases/" + version + ".yaml")
	if err != nil {
		return nil, err
	}
	rel, err := manifest.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := c.verifyChecksum(version+".yaml", raw); err != nil {
		return nil, err
	}
	return rel, nil
}

func (c *Client) Latest(channel string) (*manifest.Release, error) {
	if channel == "" {
		channel = "stable"
	}
	all, err := c.List()
	if err != nil {
		return nil, err
	}
	for _, rel := range all {
		if rel.Release.Channel == channel {
			return rel, nil
		}
	}
	return nil, exitcode.New(exitcode.InvalidInput, "no %s releases found in %s@%s", channel, c.Repo, c.Ref)
}

func (c *Client) Resolve(spec, channel string) (*manifest.Release, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "latest" || spec == channel {
		return c.Latest(channel)
	}
	switch spec {
	case "stable", "rc", "beta", "nightly", "dev":
		return c.Latest(spec)
	}
	return c.Get(strings.TrimPrefix(spec, "v"))
}

func (c *Client) FetchCompose() ([]byte, error) {
	return c.fetchRaw("compose/compose.yaml")
}

// FetchComposeFile fetches a compose overlay (e.g. compose.replica-ro.yaml) from qabas1-deploy.
func (c *Client) FetchComposeFile(name string) ([]byte, error) {
	return c.fetchRaw("compose/" + name)
}

// FetchOptionalComposeFile allows older deploy refs to omit a newly introduced
// Compose companion file. Other fetch failures still stop the deployment.
func (c *Client) FetchOptionalComposeFile(name string) ([]byte, error) {
	raw, err := c.FetchComposeFile(name)
	if err != nil && exitcode.CodeOf(err) == exitcode.InvalidInput && err.Error() == "not found: compose/"+name {
		return nil, nil
	}
	return raw, err
}

func (c *Client) FetchTemplate(name string) ([]byte, error) {
	return c.fetchRaw("compose/templates/" + name)
}

func (c *Client) listFiles() ([]string, error) {
	u := fmt.Sprintf("%s/repos/%s/contents/releases?ref=%s", strings.TrimRight(c.APIBase, "/"), c.Repo, url.QueryEscape(c.Ref))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, exitcode.New(exitcode.Network, "list releases: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, githubAPIError("list releases", c, resp.StatusCode, body)
	}
	var files []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &files); err != nil {
		return nil, exitcode.New(exitcode.Network, "list releases: decode: %w", err)
	}
	var names []string
	for _, f := range files {
		if f.Type == "file" {
			names = append(names, f.Name)
		}
	}
	return names, nil
}

func (c *Client) fetchRaw(relPath string) ([]byte, error) {
	u := fmt.Sprintf("%s/%s/%s/%s", strings.TrimRight(c.RawBase, "/"), c.Repo, c.Ref, strings.TrimPrefix(relPath, "/"))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, exitcode.New(exitcode.Network, "fetch %s: %w", relPath, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, exitcode.New(exitcode.Network, "fetch %s: %w", relPath, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, exitcode.New(exitcode.InvalidInput, "not found: %s", relPath)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, githubAPIError("fetch "+relPath, c, resp.StatusCode, body)
	}
	return body, nil
}

func (c *Client) verifyChecksum(name string, raw []byte) error {
	sumPath := "releases/" + name + ".sha256"
	sum, err := c.fetchRaw(sumPath)
	if err != nil {
		if exitcode.CodeOf(err) == exitcode.InvalidInput {
			return nil
		}
		return err
	}
	want := strings.Fields(strings.TrimSpace(string(sum)))
	if len(want) == 0 {
		return nil
	}
	got := strings.TrimPrefix(manifest.SHA256(raw), "sha256:")
	if !strings.EqualFold(want[0], got) {
		return exitcode.New(exitcode.Compatibility, "checksum mismatch for %s", name)
	}
	return nil
}

func (c *Client) auth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("User-Agent", "qabas-cli")
}

func isManifest(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

func githubAPIError(action string, c *Client, status int, body []byte) error {
	msg := fmt.Sprintf("%s: HTTP %d: %s", action, status, truncate(body))
	if status == http.StatusNotFound && c.Token == "" {
		return exitcode.New(exitcode.Network,
			"%s\n\n%s is private — GitHub auth is required once:\n\n"+
				"  export GITHUB_TOKEN=\"$(gh auth token)\"\n"+
				"  sudo -E qabas auth login\n"+
				"  sudo qabas install\n\n"+
				"Tokens are stored under %s so plain `sudo qabas` works afterward.",
			msg, c.Repo, paths.Resolve().ConfigDir)
	}
	if status == http.StatusNotFound {
		return exitcode.New(exitcode.Network,
			"%s\n\nNo releases/ directory in %s@%s. Check deploy.repo and deploy.ref in /etc/qabasone/config.yaml.",
			msg, c.Repo, c.Ref)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return exitcode.New(exitcode.Network,
			"%s\n\nGitHub rejected the token. Ensure it has read access to %s.",
			msg, c.Repo)
	}
	return exitcode.New(exitcode.Network, "%s", msg)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
