package replica

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
)

// QabasCmd is how systemd units and remote scripts invoke the package-installed CLI
// on every node; systemd resolves bare names in ExecStart (v239+).
const QabasCmd = "qabas"

// InstallHint is the next step when a node has no qabas CLI.
const InstallHint = "install the qabas CLI on the node first: VERSION=<channel> sudo -E bash install.sh --cli-only"

// CleanStaleBinaryScript turns a stale, non-package /usr/local/bin/qabas (older CLIs
// copied the writer's binary there) into a symlink to the package-installed
// /usr/bin/qabas, so PATH and units that still name the old path run the same build.
const CleanStaleBinaryScript = `if [ -f /usr/local/bin/qabas ] && [ ! -L /usr/local/bin/qabas ] && [ -x /usr/bin/qabas ] && ! dpkg -S /usr/local/bin/qabas >/dev/null 2>&1; then ln -sfn /usr/bin/qabas /usr/local/bin/qabas && echo linked; fi
`

const cliVersionScript = `if command -v qabas >/dev/null 2>&1; then qabas version --json 2>/dev/null || echo '{}'; fi
`

// CLIBuild identifies a qabas CLI build.
type CLIBuild struct {
	Version string `json:"cli"`
	Commit  string `json:"commit,omitempty"`
}

// Matches reports whether b is the same build as want. Builds without a commit
// (local builds report "none") compare by version only.
func (b CLIBuild) Matches(want CLIBuild) bool {
	if b.Version != want.Version {
		return false
	}
	if want.Commit == "" || want.Commit == "none" {
		return true
	}
	return b.Commit == want.Commit
}

func (b CLIBuild) String() string {
	if b.Commit == "" || b.Commit == "none" {
		return b.Version
	}
	c := b.Commit
	if len(c) > 7 {
		c = c[:7]
	}
	return b.Version + " (" + c + ")"
}

// nodeCLI returns the qabas build on a node; ok is false when qabas is not on PATH.
func nodeCLI(ctx context.Context, x nodeexec.Executor) (CLIBuild, bool, error) {
	out, err := run(ctx, x, cliVersionScript)
	if err != nil {
		return CLIBuild{}, false, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return CLIBuild{}, false, nil
	}
	var b CLIBuild
	_ = json.Unmarshal([]byte(out), &b)
	return b, true, nil
}

// ensureCLI repairs PATH shadowing on a node and, when e.CLI is set, updates the
// node's qabas to the writer's channel if its build differs. A build that still
// differs afterwards (the channel moved past the writer) only warns.
func (e *Engine) ensureCLI(ctx context.Context, x nodeexec.Executor) error {
	if out, err := run(ctx, x, CleanStaleBinaryScript); err != nil {
		return err
	} else if strings.TrimSpace(out) == "linked" {
		e.logf("  %s: replaced stale /usr/local/bin/qabas with a link to /usr/bin/qabas", x.Node())
	}
	if e.CLI == nil {
		return nil
	}
	have, ok, err := nodeCLI(ctx, x)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("qabas CLI is not installed on %s (%s)", x.Node(), InstallHint)
	}
	if have.Matches(*e.CLI) {
		return nil
	}
	e.logf("  %s: qabas %s differs from the writer's %s; updating", x.Node(), have, e.CLI)
	cmd := QabasCmd + " update --yes --non-interactive"
	if e.CLI.Version == "dev" {
		cmd = QabasCmd + " update --dev --yes --non-interactive"
	}
	if _, err := run(ctx, x, cmd+"\n"); err != nil {
		return fmt.Errorf("update qabas on %s: %w", x.Node(), err)
	}
	if _, err := run(ctx, x, CleanStaleBinaryScript); err != nil {
		return err
	}
	_, _ = run(ctx, x, RelayRestartScript)
	if have, _, err = nodeCLI(ctx, x); err != nil {
		return err
	}
	if !have.Matches(*e.CLI) {
		e.logf("  warning: %s still runs qabas %s (writer: %s); update the writer or the node to the same build", x.Node(), have, e.CLI)
	}
	return nil
}
