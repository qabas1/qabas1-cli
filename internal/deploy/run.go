package deploy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/history"
	"github.com/qabas1/qabas1-cli/internal/host"
	"github.com/qabas1/qabas1-cli/internal/interrupt"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/secrets"
	"github.com/qabas1/qabas1-cli/internal/state"
)

func (e *Engine) Run(opt Options, backupFn func() (string, error)) error {
	plan, err := e.BuildPlan(opt)
	if err != nil {
		return err
	}
	if plan.Blocked != "" {
		return exitcode.New(exitcode.Compatibility, "%s", plan.Blocked)
	}

	if opt.CheckOnly || opt.DryRun {
		return nil
	}

	if plan.Steps[0] == "idempotent-skip" || plan.Steps[0] == "already-at-target" {
		if err := e.Runtime.EnsureEdgeNetwork(compose.ImageEnv(plan.Target, e.Paths)); err != nil {
			return err
		}
		e.Out.Printf("QabasOne %s is already installed.\n", e.State.CurrentRelease)
		return nil
	}

	if warn := IncompleteDeployWarning(e.State); warn != "" {
		e.Out.Printf("%s\n", warn)
	}

	hr := host.Inspect()
	if err := hr.RequireReady(); err != nil {
		return err
	}

	if err := e.Paths.EnsureRuntime(); err != nil {
		return exitcode.Wrap(exitcode.Generic, err)
	}

	if err := release.RequireAuth(); err != nil {
		return err
	}
	// Check release assets before writing phase, config, or credentials. A sparse
	// deploy repository should fail with a useful error and leave the VPS intact.
	var bundle []byte
	if plan.Service == "" {
		bundle, err = e.Client.FetchCompose()
		if err != nil {
			return fmt.Errorf("deploy repository %s@%s is missing compose/compose.yaml: %w", e.Client.Repo, e.Client.Ref, err)
		}
	}
	names, err := secrets.RequiredTemplates(e.Paths)
	if err != nil {
		return err
	}
	templates := make(map[string][]byte, len(names))
	for _, name := range names {
		raw, fetchErr := e.Client.FetchTemplate(name)
		if fetchErr != nil {
			return fmt.Errorf("deploy repository %s@%s is missing compose/templates/%s: %w", e.Client.Repo, e.Client.Ref, name, fetchErr)
		}
		templates[name] = raw
	}
	fetchTemplate := func(name string) ([]byte, error) {
		if raw, ok := templates[name]; ok {
			return raw, nil
		}
		return e.Client.FetchTemplate(name)
	}

	e.setPhase("PREPARE", "")
	if !e.State.Installed() {
		if err := e.ensureFirstInstallConfig(opt); err != nil {
			return err
		}
	}
	if err := config.Save(e.Paths, e.Config); err != nil {
		return err
	}
	sec, err := secrets.Ensure(e.Paths)
	if err != nil {
		return err
	}
	// Materialize missing env files and stage the runtime password. Existing
	// API/worker URLs are upgraded after the database role is initialized.
	if err := secrets.WriteEnvFiles(e.Paths, sec, e.Config.EffectiveDomains(), fetchTemplate); err != nil {
		return err
	}
	if err := e.abortIfInterrupted(opt, plan, "", "PREPARE"); err != nil {
		return err
	}

	e.setPhase("VALIDATE", "")
	env := compose.ImageEnv(plan.Target, e.Paths)
	scoped := plan.Service != ""
	if scoped {
		// Keep live compose; only refresh image pins for the merged release.
		if err := e.Runtime.Config(e.Paths, env); err != nil {
			return err
		}
	} else {
		// Stage compose under compose.yaml.next; promote only at SWITCH so an
		// interrupt before SWITCH leaves live compose on the installed release.
		if err := compose.WriteBundleNext(e.Paths, bundle); err != nil {
			return err
		}
		if err := compose.RefreshOverlays(e.Paths, e.Client.FetchComposeFile); err != nil {
			return err
		}
		e.syncLogging(e.Paths.ComposeFile(), e.Paths.ComposeFileNext())
		e.syncLimits(e.Paths.ComposeFile(), e.Paths.ComposeFileNext())
		if err := e.Runtime.ConfigFile(e.Paths, e.Paths.ComposeFileNext(), env); err != nil {
			return err
		}
	}
	if err := e.abortIfInterrupted(opt, plan, "", "VALIDATE"); err != nil {
		return err
	}

	backupID := ""
	if plan.BackupRequired && backupFn != nil {
		e.setPhase("BACKUP", "")
		err = interrupt.Protect("BACKUP", func() error {
			var berr error
			backupID, berr = backupFn()
			return berr
		})
		if err != nil {
			return exitcode.Wrap(exitcode.BackupRestore, err)
		}
		if err := e.abortIfInterrupted(opt, plan, backupID, "BACKUP"); err != nil {
			return err
		}
	}

	e.setPhase("PULL", "")
	err = interrupt.Protect("PULL", func() error {
		if err := e.Runtime.LoginGHCR(release.GHCRUsername(), release.GHCRToken()); err != nil {
			return err
		}
		return retryPull(func() error {
			if scoped {
				services := plan.PullServices
				if len(services) == 0 {
					services = compose.DeployComposeServices(plan.Service)
				}
				return e.Runtime.PullServices(e.Paths, env, services...)
			}
			if plan.PullAll {
				return e.Runtime.PullFile(e.Paths, e.Paths.ComposeFileNext(), env)
			}
			if len(plan.PullServices) == 0 {
				return nil
			}
			return e.Runtime.PullFileServices(e.Paths, e.Paths.ComposeFileNext(), env, plan.PullServices...)
		}, func(attempt int, delay time.Duration) error {
			e.Out.Warnf("image pull failed; retrying (%d/3) in %s. Docker retains completed layers", attempt+1, delay)
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
			return interrupt.Check()
		})
	})
	if err != nil {
		return err
	}
	if !scoped || plan.Service == "api" {
		if err := e.Runtime.VerifyAPIWorkerEntrypoints(plan.Target, env); err != nil {
			return err
		}
	}
	if err := e.abortIfInterrupted(opt, plan, backupID, "PULL"); err != nil {
		return err
	}

	if e.BeforeSwitch != nil {
		if err := e.BeforeSwitch(); err != nil {
			return err
		}
	}
	if !scoped || plan.Service == "api" {
		e.setPhase("MIGRATE", "")
	}
	e.setPhase("SWITCH", "")
	err = interrupt.Protect("SWITCH", func() error {
		if !scoped {
			if err := compose.PromoteBundle(e.Paths); err != nil {
				return err
			}
			e.syncLogging(e.Paths.ComposeFile())
			e.syncLimits(e.Paths.ComposeFile())
		}
		if err := compose.SaveReleaseCopy(e.Paths, plan.Target); err != nil {
			return err
		}
		if !scoped || plan.Service == "api" {
			// Heal/apply migrations with the promoted API image before starting the stack.
			// Compose `up` still runs migrate via depends_on; this makes P3009 already-exists
			// failures recoverable without leaving api/workers down.
			if err := e.Runtime.MigrateWithHeal(e.Paths, env); err != nil {
				e.recordFailure(opt, plan, err)
				return exitcode.Wrap(exitcode.Health, err)
			}
		}
		// Establish the limited login before changing existing API/worker URLs.
		// Older installed Compose bundles have no role initializer and retain
		// their current credentials until a full stack upgrade.
		composeBody, readErr := os.ReadFile(e.Paths.ComposeFile())
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(composeBody), "\n  db-runtime-role:") {
			if err := e.Runtime.RunOneShot(e.Paths, env, "db-runtime-role"); err != nil {
				e.recordFailure(opt, plan, err)
				return exitcode.Wrap(exitcode.Health, err)
			}
			if err := secrets.UpgradeRuntimeDBEnv(e.Paths, sec); err != nil {
				e.recordFailure(opt, plan, err)
				return err
			}
		}
		if scoped {
			services := compose.DeployComposeServices(plan.Service)
			if err := e.Runtime.UpServices(e.Paths, env, services, false, true); err != nil {
				e.recordFailure(opt, plan, err)
				return exitcode.Wrap(exitcode.Health, err)
			}
		} else if err := e.Runtime.Up(e.Paths, env); err != nil {
			e.recordFailure(opt, plan, err)
			return exitcode.Wrap(exitcode.Health, err)
		}
		if err := e.Runtime.EnsureEdgeNetwork(env); err != nil {
			e.recordFailure(opt, plan, err)
			return exitcode.Wrap(exitcode.Health, err)
		}

		e.setPhase("VERIFY", "")
		ps, err := e.Runtime.Ps(e.Paths, env)
		if err != nil {
			e.recordFailure(opt, plan, err)
			return exitcode.Wrap(exitcode.Health, err)
		}
		if strings.Contains(strings.ToLower(ps), "unhealthy") {
			err := exitcode.New(exitcode.Health, "one or more services are unhealthy")
			e.recordFailure(opt, plan, err)
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	e.setPhase("COMMIT", "")
	return interrupt.Protect("COMMIT", func() error {
		prev := e.State.CurrentRelease
		e.State.PreviousRelease = prev
		e.State.CurrentRelease = plan.To
		e.State.DeployedAt = time.Now().UTC()
		e.State.TargetRelease = ""
		e.State.LastError = ""
		e.State.Phase = "committed"
		if err := state.Save(e.Paths, e.State); err != nil {
			return err
		}
		_ = history.Append(e.Paths, history.Entry{
			Action: string(opt.Action),
			From:   prev,
			To:     plan.To,
			Backup: backupID,
			Status: "success",
		})
		_ = os.WriteFile(filepath.Join(e.Paths.Deployments(), fmt.Sprintf("%s.json", plan.To)), []byte(fmt.Sprintf(`{"release":%q,"at":%q}`+"\n", plan.To, e.State.DeployedAt.Format(time.RFC3339))), 0o640)
		if plan.Service != "" {
			e.Out.Printf("QabasOne %s deployed (%s).\n", plan.To, plan.Service)
		} else {
			e.Out.Printf("QabasOne %s deployed.\n", plan.To)
		}
		return nil
	})
}

// Docker retains successfully downloaded layers between attempts. Retrying the
// same Compose pull recovers from transient registry and network resets.
func retryPull(pull func() error, wait func(int, time.Duration) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		err := pull()
		if err == nil {
			return nil
		}
		if attempt == 2 {
			return fmt.Errorf("image pull failed after 3 attempts: %w", err)
		}
		if err := wait(attempt, time.Duration(1<<attempt)*3*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) abortIfInterrupted(opt Options, plan *Plan, backupID, afterPhase string) error {
	if err := interrupt.Check(); err != nil {
		e.recordAborted(opt, plan, backupID, afterPhase, err)
		return err
	}
	return nil
}

func (e *Engine) ensureFirstInstallConfig(opt Options) error {
	domains := e.Config.EffectiveDomains()
	if domains.Complete() {
		return nil
	}
	if opt.NonInteractive {
		return exitcode.NewHint(exitcode.InvalidInput,
			"set domains first, for example: sudo qabas config set domain example.com",
			"first install requires api, app, dashboard, and siraj domains")
	}
	fmt.Fprintln(os.Stderr, "First install configuration")
	reader := bufio.NewReader(os.Stdin)
	baseDefault := e.Config.Domain
	if baseDefault == "" {
		baseDefault = "qabas.one"
	}
	base, err := promptValue(reader, "Base domain", baseDefault)
	if err != nil {
		return err
	}
	if e.Config.Domain == "" {
		e.Config.Domain = base
	}
	if domains.API == "" {
		domains.API = "api." + base
	}
	if domains.App == "" {
		domains.App = "app." + base
	}
	if domains.Dash == "" {
		domains.Dash = "dash." + base
	}
	if domains.Siraj == "" {
		domains.Siraj = "siraj." + base
	}
	if domains.S3 == "" {
		domains.S3 = "s3." + base
	}
	if domains.S3Console == "" {
		domains.S3Console = "s3c." + base
	}
	if e.Config.Domains.API == "" {
		if e.Config.Domains.API, err = promptValue(reader, "API domain", domains.API); err != nil {
			return err
		}
	}
	if e.Config.Domains.App == "" {
		if e.Config.Domains.App, err = promptValue(reader, "App domain", domains.App); err != nil {
			return err
		}
	}
	if e.Config.Domains.Dash == "" {
		if e.Config.Domains.Dash, err = promptValue(reader, "Dashboard domain", domains.Dash); err != nil {
			return err
		}
	}
	if e.Config.Domains.Siraj == "" {
		if e.Config.Domains.Siraj, err = promptValue(reader, "Siraj domain", domains.Siraj); err != nil {
			return err
		}
	}
	if e.Config.Domains.S3 == "" {
		if e.Config.Domains.S3, err = promptValue(reader, "Storage domain", domains.S3); err != nil {
			return err
		}
	}
	if e.Config.Domains.S3Console == "" {
		if e.Config.Domains.S3Console, err = promptValue(reader, "Storage console domain", domains.S3Console); err != nil {
			return err
		}
	}
	return e.Config.Validate()
}

func promptValue(reader *bufio.Reader, label, fallback string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s [%s]: ", label, fallback)
	raw, err := reader.ReadString('\n')
	if err != nil && len(raw) == 0 {
		return "", err
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (e *Engine) setPhase(phase, errMsg string) {
	e.State.Phase = phase
	e.State.LastError = errMsg
	_ = state.Save(e.Paths, e.State)
	e.Out.Printf("%s\n", phase)
}

func (e *Engine) recordFailure(opt Options, plan *Plan, err error) {
	e.State.TargetRelease = plan.To
	e.State.LastError = err.Error()
	_ = state.Save(e.Paths, e.State)
	_ = history.Append(e.Paths, history.Entry{
		Action:  string(opt.Action),
		From:    plan.From,
		To:      plan.To,
		Status:  "failure",
		Message: err.Error(),
	})
}

func (e *Engine) recordAborted(opt Options, plan *Plan, backupID, afterPhase string, err error) {
	msg := fmt.Sprintf("aborted after %s: %s", afterPhase, err.Error())
	if backupID != "" {
		msg = fmt.Sprintf("%s; backup %s ok", msg, backupID)
	}
	left := e.State.CurrentRelease
	if left == "" {
		left = "(not installed)"
	}
	e.Out.Printf("deploy aborted before SWITCH; system left on %s", left)
	if backupID != "" {
		e.Out.Printf("; backup %s ok", backupID)
	}
	e.Out.Printf("\n")

	// Drop staged compose so the next deploy starts clean; live compose is untouched.
	_ = os.Remove(e.Paths.ComposeFileNext())

	e.State.TargetRelease = plan.To
	e.State.LastError = msg
	_ = state.Save(e.Paths, e.State)
	_ = history.Append(e.Paths, history.Entry{
		Action:  string(opt.Action),
		From:    plan.From,
		To:      plan.To,
		Backup:  backupID,
		Status:  "aborted",
		Message: msg,
	})
}

// IncompletePhase reports whether phase indicates a deploy that did not COMMIT.
func IncompletePhase(phase string) bool {
	switch phase {
	case "", "committed":
		return false
	default:
		return true
	}
}

// IncompleteDeployWarning returns operator guidance when state shows a mid-deploy phase.
func IncompleteDeployWarning(st state.State) string {
	if !st.Installed() || !IncompletePhase(st.Phase) {
		return ""
	}
	hint := "check: qabas status; re-run the deploy/upgrade, or qabas rollback if SWITCH may have partially applied"
	if st.TargetRelease != "" {
		return fmt.Sprintf("warning: previous deploy incomplete (phase=%s target=%s). %s", st.Phase, st.TargetRelease, hint)
	}
	return fmt.Sprintf("warning: previous deploy incomplete (phase=%s). %s", st.Phase, hint)
}

// syncLogging keeps the log-rotation overlay in step with the bundle; on any
// error the overlay is dropped so it can never block a deploy.
// syncLimits keeps the resource-limits overlay in step with the bundle; like
// logging, any error drops the overlay instead of failing the deploy.
func (e *Engine) syncLimits(composeFiles ...string) {
	if e.ContainerRAM == nil && !compose.LimitsOverlayEnabled(e.Paths) {
		return
	}
	var ram uint64
	if e.ContainerRAM != nil {
		ram = e.ContainerRAM()
	}
	if _, _, err := compose.SyncLimitsOverlay(e.Paths, ram, composeFiles...); err != nil {
		_ = compose.DisableLimitsOverlay(e.Paths)
	}
}

func (e *Engine) syncLogging(composeFiles ...string) {
	if !compose.LoggingOverlayEnabled(e.Paths) {
		if e.LogRotation == nil || !e.LogRotation() {
			return
		}
	}
	if _, err := compose.SyncLoggingOverlay(e.Paths, composeFiles...); err != nil {
		_ = compose.DisableLoggingOverlay(e.Paths)
	}
}
