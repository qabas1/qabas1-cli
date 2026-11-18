package manifest

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"gopkg.in/yaml.v3"
)

type Release struct {
	SchemaVersion int           `yaml:"schemaVersion" json:"schemaVersion"`
	Release       Meta          `yaml:"release" json:"release"`
	Components    Components    `yaml:"components" json:"components"`
	Infra         Infra         `yaml:"infra,omitempty" json:"infra,omitempty"`
	Compatibility Compatibility `yaml:"compatibility" json:"compatibility"`
	Requirements  Requirements  `yaml:"requirements" json:"requirements"`
	Database      Database      `yaml:"database" json:"database"`
	Capabilities  Capabilities  `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Raw           []byte        `yaml:"-" json:"-"`
}

type Meta struct {
	Version    string `yaml:"version" json:"version"`
	Channel    string `yaml:"channel" json:"channel"`
	ReleasedAt string `yaml:"releasedAt" json:"releasedAt"`
}

type Components struct {
	API   Component `yaml:"api" json:"api"`
	App   Component `yaml:"app" json:"app"`
	Dash  Component `yaml:"dash" json:"dash"`
	Siraj Component `yaml:"siraj" json:"siraj"`
}

type Component struct {
	Version string `yaml:"version" json:"version"`
	Commit  string `yaml:"commit" json:"commit"`
	Image   string `yaml:"image" json:"image"`
	Digest  string `yaml:"digest" json:"digest"`
}

type Infra struct {
	Postgres  string `yaml:"postgres,omitempty" json:"postgres,omitempty"`
	Redis     string `yaml:"redis,omitempty" json:"redis,omitempty"`
	Minio     string `yaml:"minio,omitempty" json:"minio,omitempty"`
	MinioMc   string `yaml:"minioMc,omitempty" json:"minioMc,omitempty"`
	Gotenberg string `yaml:"gotenberg,omitempty" json:"gotenberg,omitempty"`
	Jsreport  string `yaml:"jsreport,omitempty" json:"jsreport,omitempty"`
}

type Compatibility struct {
	App   APIRange `yaml:"app" json:"app"`
	Dash  APIRange `yaml:"dash" json:"dash"`
	Siraj APIRange `yaml:"siraj" json:"siraj"`
}

type APIRange struct {
	API string `yaml:"api" json:"api"`
}

type Requirements struct {
	QabasCLI string `yaml:"qabasCli" json:"qabasCli"`
	Docker   string `yaml:"docker" json:"docker"`
}

type Database struct {
	MigrationStrategy  string `yaml:"migrationStrategy" json:"migrationStrategy"`
	DowngradeSupported bool   `yaml:"downgradeSupported" json:"downgradeSupported"`
}

type Capabilities struct {
	SecretFileConfig bool `yaml:"secretFileConfig,omitempty" json:"secretFileConfig,omitempty"`
}

func Parse(raw []byte) (*Release, error) {
	var rel Release
	if err := yaml.Unmarshal(raw, &rel); err != nil {
		return nil, exitcode.New(exitcode.InvalidInput, "parse release manifest: %w", err)
	}
	rel.Raw = append([]byte(nil), raw...)
	if err := rel.Validate(); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *Release) Validate() error {
	if r.SchemaVersion != 1 {
		return exitcode.New(exitcode.InvalidInput, "unsupported schemaVersion %d", r.SchemaVersion)
	}
	if r.Release.Version == "" {
		return exitcode.New(exitcode.InvalidInput, "release.version is required")
	}
	if _, err := semver.NewVersion(strings.TrimPrefix(r.Release.Version, "v")); err != nil {
		return exitcode.New(exitcode.InvalidInput, "release.version %q is not SemVer: %w", r.Release.Version, err)
	}
	switch r.Release.Channel {
	case "stable", "rc", "beta", "nightly", "dev":
	default:
		return exitcode.New(exitcode.InvalidInput, "invalid channel %q", r.Release.Channel)
	}
	for name, c := range map[string]Component{
		"api":   r.Components.API,
		"app":   r.Components.App,
		"dash":  r.Components.Dash,
		"siraj": r.Components.Siraj,
	} {
		if c.Version == "" || c.Image == "" || c.Digest == "" {
			return exitcode.New(exitcode.InvalidInput, "components.%s requires version, image, and digest", name)
		}
		if !strings.HasPrefix(c.Digest, "sha256:") {
			return exitcode.New(exitcode.InvalidInput, "components.%s.digest must be sha256:...", name)
		}
		if strings.HasSuffix(c.Image, ":latest") {
			return exitcode.New(exitcode.InvalidInput, "components.%s.image must not use :latest", name)
		}
	}
	if r.Compatibility.App.API == "" || r.Compatibility.Dash.API == "" || r.Compatibility.Siraj.API == "" {
		return exitcode.New(exitcode.InvalidInput, "compatibility.app.api, compatibility.dash.api, and compatibility.siraj.api are required")
	}
	if _, err := semver.NewConstraint(r.Compatibility.App.API); err != nil {
		return exitcode.New(exitcode.InvalidInput, "compatibility.app.api: %w", err)
	}
	if _, err := semver.NewConstraint(r.Compatibility.Dash.API); err != nil {
		return exitcode.New(exitcode.InvalidInput, "compatibility.dash.api: %w", err)
	}
	if _, err := semver.NewConstraint(r.Compatibility.Siraj.API); err != nil {
		return exitcode.New(exitcode.InvalidInput, "compatibility.siraj.api: %w", err)
	}
	if r.Requirements.QabasCLI == "" {
		return exitcode.New(exitcode.InvalidInput, "requirements.qabasCli is required")
	}
	return nil
}

func (c Component) DigestRef() string {
	image := c.Image
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	} else if i := strings.LastIndex(image, ":"); i >= 0 && !strings.Contains(image[i:], "/") {
		image = image[:i]
	}
	return image + "@" + c.Digest
}

func (r *Release) CheckCompatibility() error {
	api, err := semver.NewVersion(strings.TrimPrefix(r.Components.API.Version, "v"))
	if err != nil {
		return exitcode.New(exitcode.InvalidInput, "api version: %w", err)
	}
	if err := constraintOK(r.Compatibility.App.API, api); err != nil {
		return exitcode.New(exitcode.Compatibility, "app requires API %s; this release has %s", r.Compatibility.App.API, r.Components.API.Version)
	}
	if err := constraintOK(r.Compatibility.Dash.API, api); err != nil {
		return exitcode.New(exitcode.Compatibility, "dash requires API %s; this release has %s", r.Compatibility.Dash.API, r.Components.API.Version)
	}
	if err := constraintOK(r.Compatibility.Siraj.API, api); err != nil {
		return exitcode.New(exitcode.Compatibility, "siraj requires API %s; this release has %s", r.Compatibility.Siraj.API, r.Components.API.Version)
	}
	return nil
}

func (r *Release) CheckCLI(cliVersion string) error {
	v := strings.TrimPrefix(cliVersion, "v")
	if v == "" || v == "dev" {
		return nil
	}
	sv, err := semver.NewVersion(v)
	if err != nil {
		return nil
	}
	c, err := semver.NewConstraint(r.Requirements.QabasCLI)
	if err != nil {
		return exitcode.New(exitcode.InvalidInput, "requirements.qabasCli: %w", err)
	}
	if !c.Check(sv) {
		return exitcode.New(exitcode.Compatibility, "QabasOne %s requires qabas %s.\n\nRun:\nqabas update", r.Release.Version, r.Requirements.QabasCLI)
	}
	return nil
}

func Compare(a, b string) int {
	va, errA := semver.NewVersion(strings.TrimPrefix(a, "v"))
	vb, errB := semver.NewVersion(strings.TrimPrefix(b, "v"))
	if errA != nil || errB != nil {
		return strings.Compare(a, b)
	}
	return va.Compare(vb)
}

func SHA256(raw []byte) string {
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func constraintOK(expr string, v *semver.Version) error {
	c, err := semver.NewConstraint(expr)
	if err != nil {
		return err
	}
	if !c.Check(v) {
		return fmt.Errorf("does not satisfy %s", expr)
	}
	return nil
}
