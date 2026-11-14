package replica

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
)

func TestPreflightRequiresQabasCLI(t *testing.T) {
	fx := newFixture(t, strings.Replace(preflightOK, "qabas=/usr/bin/qabas", "qabas=", 1))
	_, err := fx.eng.Add(context.Background(), AddOptions{Node: cluster.Node{Name: "dr1", Host: "203.0.113.10"}})
	if err == nil || !errors.Is(err, ErrNotModified) || !strings.Contains(err.Error(), "qabas CLI is not installed") || !strings.Contains(err.Error(), "install.sh --cli-only") {
		t.Fatalf("err = %v", err)
	}
	if len(fx.replica.Files) != 0 {
		t.Fatalf("preflight wrote files: %v", fx.replica.Files)
	}
}

func TestAddNeverCopiesTheWriterBinary(t *testing.T) {
	fx := newFixture(t, preflightOK)
	if _, err := fx.eng.Add(context.Background(), AddOptions{
		Node:    cluster.Node{Name: "dr1", Host: "203.0.113.10", SSHHostKey: "SHA256:x", WGEndpoint: "203.0.113.10:51820"},
		Confirm: func(string) error { return nil },
		Lease:   func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := fx.replica.Files["/usr/local/bin/qabas"]; ok {
		t.Fatal("writer binary copied to the replica")
	}
	if !fx.replica.Ran(CleanStaleBinaryScript) {
		t.Fatal("stale /usr/local/bin/qabas not repaired")
	}
}

func cliJSON(version, commit string) string {
	return fmt.Sprintf(`{"cli":%q,"commit":%q}`, version, commit)
}

func TestEnsureCLI(t *testing.T) {
	writer := CLIBuild{Version: "dev", Commit: "7c4a599d2ee9428643cfa35dab1256ae436871bb"}
	cases := []struct {
		name       string
		node       string
		wantUpdate string
		wantErr    string
		wantLog    string
	}{
		{name: "same build", node: cliJSON("dev", writer.Commit)},
		{name: "older dev build", node: cliJSON("dev", "4f22bb8000000000000000000000000000000000"), wantUpdate: "qabas update --dev --yes --non-interactive", wantLog: "still runs qabas dev (4f22bb8)"},
		{name: "pre-commit CLI", node: `{"cli":"dev"}`, wantUpdate: "qabas update --dev", wantLog: "differs from the writer's dev (7c4a599)"},
		{name: "missing", node: "", wantErr: "qabas CLI is not installed on dr1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := nodeexec.NewFake("dr1").OnOut("qabas version --json", c.node)
			var logs []string
			e := &Engine{CLI: &writer, Log: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
			err := e.ensureCLI(context.Background(), x)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.wantUpdate == "" && x.Ran("qabas update") {
				t.Fatal("updated a node already on the writer's build")
			}
			if c.wantUpdate != "" && !x.Ran(c.wantUpdate) {
				t.Fatalf("%q not run: %v", c.wantUpdate, x.Log)
			}
			if c.wantLog != "" && !strings.Contains(strings.Join(logs, "\n"), c.wantLog) {
				t.Fatalf("logs missing %q:\n%s", c.wantLog, strings.Join(logs, "\n"))
			}
		})
	}
}

func TestEnsureCLIStableChannel(t *testing.T) {
	x := nodeexec.NewFake("dr1").OnOut("qabas version --json", cliJSON("1.4.0", "abc"))
	e := &Engine{CLI: &CLIBuild{Version: "1.5.0", Commit: "def"}}
	if err := e.ensureCLI(context.Background(), x); err != nil {
		t.Fatal(err)
	}
	if !x.Ran("qabas update --yes --non-interactive") || x.Ran("--dev") {
		t.Fatalf("expected a stable-channel update: %v", x.Log)
	}
}

func TestCLIBuildMatches(t *testing.T) {
	if !(CLIBuild{Version: "dev", Commit: "a"}).Matches(CLIBuild{Version: "dev", Commit: "none"}) {
		t.Fatal("a writer without a commit compares by version only")
	}
	if (CLIBuild{Version: "1.0.0"}).Matches(CLIBuild{Version: "1.0.1"}) {
		t.Fatal("different versions must not match")
	}
}
