package deploy

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/version"
)

func (e *Engine) BuildPlan(opt Options) (*Plan, error) {
	channel := opt.Channel
	if channel == "" {
		channel = e.Config.Channel
	}
	if channel == "" {
		channel = "stable"
	}

	service := strings.ToLower(strings.TrimSpace(opt.Service))
	if service != "" {
		return e.buildScopedDevPlan(opt, channel, service)
	}

	current, _ := e.currentManifest()
	var target *manifest.Release
	var err error

	if opt.Action == ActionRollback {
		to := opt.Spec
		if to == "" {
			to = e.State.PreviousRelease
		}
		if to == "" {
			return nil, exitcode.New(exitcode.InvalidInput, "no previous release to roll back to")
		}
		target, err = e.loadTarget(to)
	} else if wantsDev(opt.Spec, channel) {
		// Compose/templates from qabas1-deploy@dev; images always from last-pushed GHCR.
		e.Client = withDevCatalogRef(e.Client, e.Config.Deploy.Repo)
		target, err = e.resolveDevTarget()
	} else {
		target, err = e.Client.Resolve(opt.Spec, channel)
	}
	if err != nil {
		return nil, err
	}
	if err := target.CheckCompatibility(); err != nil {
		return nil, err
	}
	if err := target.CheckCLI(version.Version); err != nil {
		return nil, err
	}

	plan := &Plan{
		Action:  opt.Action,
		From:    e.State.CurrentRelease,
		To:      target.Release.Version,
		Channel: target.Release.Channel,
		Target:  target,
		API:     Change{To: target.Components.API.Version},
		App:     Change{To: target.Components.App.Version},
		Dash:    Change{To: target.Components.Dash.Version},
		Siraj:   Change{To: target.Components.Siraj.Version},
		Steps: []string{
			"PREPARE", "VALIDATE", "BACKUP", "PULL", "MIGRATE", "SWITCH", "VERIFY", "COMMIT",
		},
	}
	if current != nil {
		plan.API.From = current.Components.API.Version
		plan.App.From = current.Components.App.Version
		plan.Dash.From = current.Components.Dash.Version
		plan.Siraj.From = current.Components.Siraj.Version
	}

	if opt.Action == ActionInstall && e.State.Installed() && !opt.Force {
		plan.Steps = []string{"idempotent-skip"}
		return plan, nil
	}

	// Stable releases are immutable: same version means already installed.
	// Dev pins last-pushed GHCR digests under often-reused tags (e.g. 1.1),
	// so only skip when digests match the currently installed release.
	if e.State.Installed() && !opt.Force {
		if wantsDev(opt.Spec, channel) {
			if current != nil && sameComponentDigests(current, target) {
				plan.To = plan.From
				plan.API.To = plan.API.From
				plan.App.To = plan.App.From
				plan.Dash.To = plan.Dash.From
				plan.Siraj.To = plan.Siraj.From
				plan.Steps = []string{"already-at-target"}
				return plan, nil
			}
		} else if plan.From == plan.To {
			plan.Steps = []string{"already-at-target"}
			return plan, nil
		}
	}

	leavingDev := isDevRelease(plan.From) && !isDevRelease(plan.To)
	if leavingDev {
		plan.LeavingDev = true
	}
	if opt.Action == ActionDowngrade || (e.State.Installed() && manifest.Compare(plan.To, plan.From) < 0) {
		if !leavingDev && !target.Database.DowngradeSupported {
			plan.Blocked = fmt.Sprintf("Downgrade blocked:\ncurrent %s → target %s\ndatabase schema is incompatible with QabasOne %s", plan.From, plan.To, target.Release.Version)
			return plan, nil
		}
	}

	plan.BackupRequired = e.State.Installed() && !opt.NoBackup && opt.Action != ActionInstall
	setPullTargets(plan, current, e.State.Installed(), opt.Force)
	return plan, nil
}

// buildScopedDevPlan merges one last-pushed GHCR component into the installed release.
func (e *Engine) buildScopedDevPlan(opt Options, channel, service string) (*Plan, error) {
	switch service {
	case "api", "app", "dash", "siraj":
	default:
		return nil, exitcode.New(exitcode.InvalidInput,
			"unknown product service %q (want api, app, dash, or siraj)", service)
	}
	if !e.State.Installed() {
		return nil, exitcode.NewHint(exitcode.InvalidInput,
			"run `sudo qabas deploy dev` or `sudo qabas install` first",
			"scoped deploy requires an existing QabasOne installation")
	}

	current, err := e.currentManifest()
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, exitcode.NewHint(exitcode.InvalidInput,
			"run `sudo qabas deploy dev` or `sudo qabas install` first",
			"scoped deploy requires installed release metadata")
	}

	var comp manifest.Component
	if opt.Spec == "dev" {
		comp, err = e.resolveDevComponent(service)
	} else {
		comp, err = e.resolveComponent(service, opt.Spec)
	}
	if err != nil {
		return nil, err
	}
	if service == "api" && opt.Spec != "dev" && !opt.AllowDowngrade && !sameComponentDigest(current, &manifest.Release{Components: manifest.Components{API: comp}}, service) {
		from, fromErr := semver.NewVersion(strings.TrimPrefix(current.Components.API.Version, "v"))
		to, toErr := semver.NewVersion(strings.TrimPrefix(comp.Version, "v"))
		if fromErr != nil || toErr != nil || !to.GreaterThan(from) {
			return nil, exitcode.NewHint(exitcode.InvalidInput,
				"verify the image and repeat with --allow-downgrade if intentional",
				"api image version cannot be proven newer than the installed version (%s -> %s)", current.Components.API.Version, comp.Version)
		}
	}
	now := time.Now().UTC()
	target := mergeDevComponent(current, service, comp, now)
	if err := target.Validate(); err != nil {
		return nil, err
	}
	if err := target.CheckCompatibility(); err != nil {
		return nil, err
	}
	if err := target.CheckCLI(version.Version); err != nil {
		return nil, err
	}

	plan := &Plan{
		Action:        opt.Action,
		From:          e.State.CurrentRelease,
		To:            target.Release.Version,
		Channel:       target.Release.Channel,
		Service:       service,
		ImageSelector: opt.Spec,
		Target:        target,
		API:           Change{From: current.Components.API.Version, To: target.Components.API.Version},
		App:           Change{From: current.Components.App.Version, To: target.Components.App.Version},
		Dash:          Change{From: current.Components.Dash.Version, To: target.Components.Dash.Version},
		Siraj:         Change{From: current.Components.Siraj.Version, To: target.Components.Siraj.Version},
		Steps: []string{
			"PREPARE", "VALIDATE", "BACKUP", "PULL", "MIGRATE", "SWITCH", "VERIFY", "COMMIT",
		},
	}
	if service != "api" {
		plan.Steps = []string{
			"PREPARE", "VALIDATE", "BACKUP", "PULL", "SWITCH", "VERIFY", "COMMIT",
		}
	}

	if !opt.Force && sameComponentDigest(current, target, service) {
		plan.To = plan.From
		plan.API.To = plan.API.From
		plan.App.To = plan.App.From
		plan.Dash.To = plan.Dash.From
		plan.Siraj.To = plan.Siraj.From
		plan.Steps = []string{"already-at-target"}
		return plan, nil
	}

	plan.BackupRequired = !opt.NoBackup && opt.Action != ActionInstall
	plan.PullServices = compose.DeployComposeServices(service)
	return plan, nil
}

// setPullTargets fills PullAll / PullServices for a full (non-scoped) deploy plan.
// First install, force, or missing current manifest → pull the full stack.
// Otherwise only services whose image refs changed vs current.
func setPullTargets(plan *Plan, current *manifest.Release, installed, force bool) {
	if plan == nil {
		return
	}
	if !installed || force || current == nil || plan.Target == nil {
		plan.PullAll = true
		plan.PullServices = nil
		return
	}
	plan.PullAll = false
	plan.PullServices = changedPullServices(current, plan.Target)
}

// changedPullServices returns compose service names whose image pins differ
// between current and target. Empty infra strings on both sides count as unchanged
// (compose defaults apply; typical for deploy dev).
func changedPullServices(current, target *manifest.Release) []string {
	if current == nil || target == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	add := func(services ...string) {
		for _, s := range services {
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}

	if digestChanged(current.Components.API.Digest, target.Components.API.Digest) {
		add(compose.DeployComposeServices("api")...)
	}
	if digestChanged(current.Components.App.Digest, target.Components.App.Digest) {
		add(compose.DeployComposeServices("app")...)
	}
	if digestChanged(current.Components.Dash.Digest, target.Components.Dash.Digest) {
		add(compose.DeployComposeServices("dash")...)
	}
	if digestChanged(current.Components.Siraj.Digest, target.Components.Siraj.Digest) {
		add(compose.DeployComposeServices("siraj")...)
	}

	ci, ti := current.Infra, target.Infra
	if ci.Postgres != ti.Postgres {
		add("postgres")
	}
	if ci.Redis != ti.Redis {
		add("redis")
	}
	if ci.Minio != ti.Minio || ci.MinioMc != ti.MinioMc {
		add("minio")
	}
	if ci.Gotenberg != ti.Gotenberg {
		add("gotenberg")
	}
	if ci.Jsreport != ti.Jsreport {
		add("jsreport")
	}
	return out
}

func digestChanged(from, to string) bool {
	return to != "" && from != to
}

func mergeDevComponent(current *manifest.Release, service string, comp manifest.Component, now time.Time) *manifest.Release {
	out := *current
	out.Raw = nil
	out.Components = current.Components
	switch service {
	case "api":
		out.Components.API = comp
	case "app":
		out.Components.App = comp
	case "dash":
		out.Components.Dash = comp
	case "siraj":
		out.Components.Siraj = comp
	}
	out.Release.Channel = "dev"
	out.Release.ReleasedAt = now.Format(time.RFC3339)
	if isSemVer(comp.Version) {
		out.Release.Version = strings.TrimPrefix(comp.Version, "v")
	} else {
		out.Release.Version = "0.0.0-dev." + fmt.Sprint(now.Unix())
	}
	promoteDevTarget(&out, current.Release.Version, now)
	if c := componentPtr(&out, service); c != nil && !isSemVer(c.Version) {
		c.Version = out.Release.Version
	}
	return &out
}

func componentPtr(rel *manifest.Release, service string) *manifest.Component {
	if rel == nil {
		return nil
	}
	switch service {
	case "api":
		return &rel.Components.API
	case "app":
		return &rel.Components.App
	case "dash":
		return &rel.Components.Dash
	case "siraj":
		return &rel.Components.Siraj
	default:
		return nil
	}
}

func sameComponentDigest(a, b *manifest.Release, service string) bool {
	ca, cb := componentPtr(a, service), componentPtr(b, service)
	if ca == nil || cb == nil {
		return false
	}
	return ca.Digest != "" && ca.Digest == cb.Digest
}

func isSemVer(version string) bool {
	_, err := semver.NewVersion(strings.TrimPrefix(version, "v"))
	return err == nil
}

// isDevRelease reports whether version is a QabasOne "dev" channel build
// (SemVer prerelease "dev" or "dev.*"), with a string fallback for non-SemVer.
func isDevRelease(version string) bool {
	version = strings.TrimSpace(version)
	if version == "" {
		return false
	}
	v, err := semver.NewVersion(strings.TrimPrefix(version, "v"))
	if err != nil {
		lower := strings.ToLower(version)
		return lower == "dev" || strings.Contains(lower, "-dev.") || strings.HasSuffix(lower, "-dev")
	}
	pre := v.Prerelease()
	return pre == "dev" || strings.HasPrefix(pre, "dev.")
}

func wantsDev(spec, channel string) bool {
	spec = strings.TrimSpace(spec)
	return spec == "dev" || (spec == "" && channel == "dev") || (spec == "latest" && channel == "dev")
}

func sameComponentDigests(a, b *manifest.Release) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Components.API.Digest == b.Components.API.Digest &&
		a.Components.App.Digest == b.Components.App.Digest &&
		a.Components.Dash.Digest == b.Components.Dash.Digest &&
		a.Components.Siraj.Digest == b.Components.Siraj.Digest &&
		a.Components.API.Digest != "" &&
		b.Components.API.Digest != ""
}

// withDevCatalogRef points the release client at the deploy `dev` branch unless
// the operator already overrode deploy.ref (e.g. QABAS_DEPLOY_REF).
func withDevCatalogRef(client *release.Client, repo string) *release.Client {
	if client == nil {
		return release.New(repo, "dev")
	}
	if client.Ref != "" && client.Ref != "main" {
		return client
	}
	out := *client
	out.Ref = "dev"
	return &out
}

func (e *Engine) resolveDevTarget() (*manifest.Release, error) {
	now := time.Now().UTC()
	resolve := e.DevResolver
	if resolve == nil {
		resolve = release.LatestDevFromGHCR
	}
	httpClient := (*http.Client)(nil)
	if e.Client != nil {
		httpClient = e.Client.HTTP
	}
	rel, err := resolve(httpClient, e.registryOwner(), release.GHCRUsername(), release.GHCRToken())
	if err != nil {
		return nil, err
	}
	promoteDevTarget(rel, e.State.CurrentRelease, now)
	return rel, nil
}

func (e *Engine) resolveDevComponent(service string) (manifest.Component, error) {
	resolve := e.DevComponentResolver
	if resolve == nil {
		resolve = release.LatestDevComponent
	}
	httpClient := (*http.Client)(nil)
	if e.Client != nil {
		httpClient = e.Client.HTTP
	}
	return resolve(httpClient, e.registryOwner(), service, release.GHCRUsername(), release.GHCRToken())
}

func (e *Engine) resolveComponent(service, spec string) (manifest.Component, error) {
	resolve := e.ComponentResolver
	if resolve == nil {
		resolve = release.ResolveComponent
	}
	var client *http.Client
	if e.Client != nil {
		client = e.Client.HTTP
	}
	return resolve(client, e.registryOwner(), service, spec, release.GHCRUsername(), release.GHCRToken())
}

func (e *Engine) registryOwner() string {
	if owner := strings.TrimSpace(os.Getenv("QABAS_GHCR_OWNER")); owner != "" {
		return owner
	}
	if e.Client != nil {
		if owner, _, ok := strings.Cut(e.Client.Repo, "/"); ok && owner != "" {
			return owner
		}
	}
	return "qabas1"
}

func promoteDevTarget(target *manifest.Release, current string, now time.Time) {
	if target == nil || target.Release.Channel != "dev" || current == "" {
		return
	}
	if manifest.Compare(target.Release.Version, current) >= 0 {
		return
	}
	next := nextDevVersion(current, now)
	old := target.Release.Version
	target.Release.Version = next
	if target.Components.API.Version == old {
		target.Components.API.Version = next
	}
	if target.Components.App.Version == old {
		target.Components.App.Version = next
	}
	if target.Components.Dash.Version == old {
		target.Components.Dash.Version = next
	}
	if target.Components.Siraj.Version == old {
		target.Components.Siraj.Version = next
	}
}

func nextDevVersion(current string, now time.Time) string {
	v, err := semver.NewVersion(strings.TrimPrefix(current, "v"))
	if err != nil {
		return "0.0.0-dev." + fmt.Sprint(now.Unix())
	}
	return fmt.Sprintf("%d.%d.%d-dev.%d", v.Major(), v.Minor(), v.Patch()+1, now.Unix())
}
func FormatPlan(plan *Plan) string {
	var b strings.Builder
	if plan.Blocked != "" {
		b.WriteString(plan.Blocked)
		b.WriteByte('\n')
		return b.String()
	}
	if plan.Service != "" {
		fmt.Fprintf(&b, "Deploy %s (%s)\n", plan.Service, plan.ImageSelector)
	}
	if plan.From == "" {
		fmt.Fprintf(&b, "Install QabasOne %s (%s)\n", plan.To, plan.Channel)
	} else {
		fmt.Fprintf(&b, "Current: %s\nTarget:  %s\n\n", plan.From, plan.To)
	}
	fmt.Fprintf(&b, "API:\n%s → %s\n\nApp:\n%s → %s\n\nDash:\n%s → %s\n\nSiraj:\n%s → %s\n",
		dash(plan.API.From), plan.API.To,
		dash(plan.App.From), plan.App.To,
		dash(plan.Dash.From), plan.Dash.To,
		dash(plan.Siraj.From), plan.Siraj.To,
	)
	if plan.LeavingDev {
		b.WriteString("\nLeaving the dev channel for a stable release.\nSchema from dev-only migrations may not match the target.\n")
	}
	if plan.BackupRequired {
		b.WriteString("\nA backup will be created before switching releases.\n")
	}
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func (e *Engine) currentManifest() (*manifest.Release, error) {
	if e.State.CurrentRelease == "" {
		return nil, nil
	}
	return e.loadTarget(e.State.CurrentRelease)
}

func (e *Engine) loadTarget(version string) (*manifest.Release, error) {
	local := filepath.Join(e.Paths.Releases(), version+".yaml")
	if b, err := os.ReadFile(local); err == nil {
		return manifest.Parse(b)
	}
	return e.Client.Get(version)
}
