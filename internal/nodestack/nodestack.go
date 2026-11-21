// Package nodestack knows how a QabasOne node's compose stack is laid out per DR role:
// which qabas1-deploy overlays apply, which profiles are active, and where the
// DR/Postgres inputs live. Every node (local or remote) uses the same Linux layout.
package nodestack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Profile string

const (
	// ProfilePrimary is a writable primary feeding standbys.
	ProfilePrimary Profile = "primary"
	// ProfileReplicaRO is a physical hot standby (never runs writers or migrate).
	ProfileReplicaRO Profile = "replica-ro"
	// ProfileDRPrimary is a promoted standby acting as the writer.
	ProfileDRPrimary Profile = "dr-primary"
)

const (
	OverlayPrimary   = "compose.primary-replication.yaml"
	OverlayReplicaRO = "compose.replica-ro.yaml"
	OverlayDRPrimary = "compose.dr-primary.yaml"
	OverlayPITR      = "compose.pitr.yaml"
	OverlayScale     = "compose.scale.yaml"

	// OverlaysFile lists overlays/profiles/env-files the local qabas runtime must apply.
	OverlaysFile = "overlays"
)

// Infra images used when a release does not pin them; keep in sync with the defaults
// in qabas1-deploy compose/compose.yaml (vendored mirrors, no Docker Hub/quay auth).
const (
	DefaultPostgresImage = "ghcr.io/qabas1/vendor/postgres/postgres:16-alpine"
	DefaultMinioMcImage  = "ghcr.io/qabas1/vendor/quay.io/minio/mc:RELEASE.2025-04-16T18-13-26Z"
)

// Layout is the on-disk layout of a node.
type Layout struct {
	ConfigDir  string
	ComposeDir string
	DRDir      string
	PGConfDir  string
	RunDir     string
	AuditDir   string
}

// Remote is the standard layout on every provisioned node.
var Remote = Layout{
	ConfigDir:  "/etc/qabasone",
	ComposeDir: "/var/lib/qabasone/compose",
	DRDir:      "/etc/qabasone/dr",
	PGConfDir:  "/etc/qabasone/pg",
	RunDir:     "/run/qabas",
	AuditDir:   "/var/lib/qabasone/dr-audit",
}

func (l Layout) NodeEnvFile() string   { return l.DRDir + "/node.env" }
func (l Layout) ImagesEnvFile() string { return l.DRDir + "/images.env" }
func (l Layout) WriterFile() string    { return l.DRDir + "/writer.json" }
func (l Layout) RoleFile() string      { return l.DRDir + "/role" }
func (l Layout) HBAFile() string       { return l.PGConfDir + "/pg_hba.conf" }
func (l Layout) TLSDir() string        { return l.PGConfDir + "/tls" }

// Overlays returns the overlay files for a profile (order matters).
func Overlays(p Profile, pitr bool) []string {
	var out []string
	switch p {
	case ProfilePrimary:
		out = []string{OverlayPrimary}
	case ProfileReplicaRO:
		out = []string{OverlayReplicaRO}
	case ProfileDRPrimary:
		out = []string{OverlayPrimary, OverlayDRPrimary}
	}
	if pitr && p != ProfileReplicaRO {
		out = append(out, OverlayPITR)
	}
	return out
}

// Writers reports whether the `writers` compose profile is active.
func Writers(p Profile) bool { return p == ProfileDRPrimary }

// ComposeCommand renders a docker compose invocation for the node's role.
func ComposeCommand(l Layout, p Profile, pitr bool, args string) string {
	var b strings.Builder
	b.WriteString("set -a; . " + l.ImagesEnvFile() + "; set +a; ")
	b.WriteString("export QABAS_ENV_DIR=" + l.ConfigDir + " QABAS_DR_DIR=" + l.DRDir + " QABAS_PG_CONF_DIR=" + l.PGConfDir + " QABAS_DR_AUDIT_DIR=" + l.AuditDir + "; ")
	b.WriteString("docker compose --project-name qabasone -f " + l.ComposeDir + "/compose.yaml")
	for _, o := range Overlays(p, pitr) {
		b.WriteString(" -f " + l.ComposeDir + "/" + o)
	}
	b.WriteString(" --env-file " + l.ConfigDir + "/.env --env-file " + l.NodeEnvFile())
	if Writers(p) {
		b.WriteString(" --profile writers")
	}
	if args != "" {
		b.WriteString(" " + args)
	}
	return b.String()
}

// NodeEnv renders /etc/qabasone/dr/node.env: non-secret node identity for the API plus
// the host directories compose interpolates in the DR overlays.
func NodeEnv(l Layout, node, site, siteRole, wgIP, release string) string {
	return fmt.Sprintf("QABAS_NODE=%s\nQABAS_SITE=%s\nQABAS_SITE_ROLE=%s\nQABAS_WG_IP=%s\nQABAS_RELEASE=%s\n"+
		"QABAS_ENV_DIR=%s\nQABAS_DR_DIR=%s\nQABAS_PG_CONF_DIR=%s\nQABAS_DR_AUDIT_DIR=%s\n",
		node, site, siteRole, wgIP, release, l.ConfigDir, l.DRDir, l.PGConfDir, l.AuditDir)
}

// ParseEnv reads KEY=VALUE lines (no shell expansion).
func ParseEnv(b []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// SetEnv replaces (or appends) KEY=VALUE lines in an env file body, preserving the rest.
func SetEnv(body []byte, kv map[string]string) []byte {
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	done := map[string]bool{}
	for i, line := range lines {
		k, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		k = strings.TrimSpace(k)
		if v, hit := kv[k]; hit {
			lines[i] = k + "=" + v
			done[k] = true
		}
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !done[k] {
			lines = append(lines, k+"="+kv[k])
		}
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

// LocalLayout maps the control node's qabas paths onto a Layout.
func LocalLayout(configDir, stateDir string) Layout {
	return Layout{
		ConfigDir:  configDir,
		ComposeDir: filepath.Join(stateDir, "compose"),
		DRDir:      filepath.Join(configDir, "dr"),
		PGConfDir:  filepath.Join(configDir, "pg"),
		RunDir:     "/run/qabas",
		AuditDir:   filepath.Join(stateDir, "dr-audit"),
	}
}

// ImagesEnv renders pinned image refs (repo@sha256) as an env file.
func ImagesEnv(images map[string]string) string {
	keys := make([]string, 0, len(images))
	for k := range images {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, images[k])
	}
	return b.String()
}

// LocalOverlays is the content of the local overlays file for the qabas runtime.
func LocalOverlays(l Layout, p Profile, pitr bool) string {
	var b strings.Builder
	b.WriteString("# Managed by qabas dr / qabas replica. One directive per line.\n")
	for _, o := range Overlays(p, pitr) {
		b.WriteString("file:" + o + "\n")
	}
	b.WriteString("env-file:" + l.NodeEnvFile() + "\n")
	if Writers(p) {
		b.WriteString("profile:writers\n")
	}
	return b.String()
}

// AddDirective appends a directive line (e.g. "file:compose.pitr.yaml") to the local
// overlays file if it is not already present.
func AddDirective(composeDir, line string) error {
	fp := filepath.Join(composeDir, OverlaysFile)
	b, _ := os.ReadFile(fp)
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if len(b) == 0 {
		b = []byte("# Managed by qabas. One directive per line.\n")
	} else if b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, line+"\n"...)
	if err := os.MkdirAll(composeDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(fp, b, 0o644)
}

// Directives is the parsed overlays file.
type Directives struct {
	Files    []string
	EnvFiles []string
	Profiles []string
}

// ReadDirectives parses composeDir/overlays. Missing file = no directives.
func ReadDirectives(composeDir string) Directives {
	b, err := os.ReadFile(filepath.Join(composeDir, OverlaysFile))
	if err != nil {
		return Directives{}
	}
	return ParseDirectives(composeDir, string(b))
}

// ParseDirectives parses overlay directives from a supplied contents string.
// composeDir is used to resolve managed compose files for the selected node.
func ParseDirectives(composeDir, contents string) Directives {
	var d Directives
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		kind, val, ok := strings.Cut(line, ":")
		if !ok || val == "" || strings.ContainsAny(val, "\x00") {
			continue
		}
		switch kind {
		case "file":
			if filepath.Base(val) == val {
				d.Files = append(d.Files, filepath.Join(composeDir, val))
			}
		case "env-file":
			d.EnvFiles = append(d.EnvFiles, val)
		case "profile":
			d.Profiles = append(d.Profiles, val)
		}
	}
	return d
}
