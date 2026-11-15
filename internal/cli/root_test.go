package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/cli"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
)

func TestRootHelp(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"qabas [OPTIONS] COMMAND",
		"Common Commands:",
		"Management Commands:",
		"Lifecycle Commands:",
		"System Commands:",
		"install",
		"upgrade",
		"ps",
		"up",
		"down",
		"restart",
		"release",
		"backup",
		"db",
		"doctor",
		"guide",
		"completion",
		"login",
		"-y, --yes",
		"-q, --quiet",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q:\n%s", want, out)
		}
	}
	installIdx := strings.Index(out, "\n  install")
	authIdx := strings.Index(out, "\n  auth")
	if installIdx < 0 || authIdx < 0 || installIdx > authIdx {
		t.Fatalf("expected install before auth in Common Commands:\n%s", out)
	}
	if strings.Contains(out, "Additional Commands:") {
		t.Fatalf("help should not use Additional Commands (help belongs in System):\n%s", out)
	}
	sysIdx := strings.Index(out, "System Commands:")
	helpIdx := strings.Index(out, "\n  help")
	if sysIdx < 0 || helpIdx < 0 || helpIdx < sysIdx {
		t.Fatalf("expected help under System Commands:\n%s", out)
	}
}

func TestParentCommandsShowHelp(t *testing.T) {
	for _, name := range []string{"backup", "release", "auth", "config", "db", "maintenance"} {
		root := cli.NewRoot()
		buf := &bytes.Buffer{}
		root.SetOut(buf)
		root.SetErr(buf)
		root.SetArgs([]string{name})
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(buf.String(), "Usage:") {
			t.Fatalf("%s without subcommand should show help:\n%s", name, buf.String())
		}
	}
}

func TestSuggestForUnknown(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"instal"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unknown command error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "install") {
		t.Fatalf("expected suggestion mentioning install, got %q", msg)
	}
}

func TestRootNoArgsShowsHelp(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Common Commands:") {
		t.Fatalf("expected help on no args:\n%s", buf.String())
	}
}

func TestDeployNoArgsShowsActionableError(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"deploy"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected missing version error")
	}
	if got := err.Error(); !strings.Contains(got, "deploy requires a product version") {
		t.Fatalf("expected actionable deploy error, got %q", got)
	}
	if got := exitcode.HintOf(err); !strings.Contains(got, "sudo qabas deploy <version>") {
		t.Fatalf("expected deploy usage hint, got %q", got)
	}
}

func TestDeployServiceWithoutDevErrors(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"deploy", "api"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected service-without-dev error")
	}
	if got := err.Error(); !strings.Contains(got, "product service") {
		t.Fatalf("expected product service error, got %q", got)
	}
	if got := exitcode.HintOf(err); !strings.Contains(got, "deploy api --dev") {
		t.Fatalf("expected scoped deploy hint, got %q", got)
	}
}

func TestDeployDevFlagWithoutServiceErrors(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"deploy", "2.8.0", "--dev"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected --dev with version error")
	}
	if got := err.Error(); !strings.Contains(got, "--dev requires a product service") {
		t.Fatalf("expected --dev service error, got %q", got)
	}
}

func TestAliasesResolve(t *testing.T) {
	root := cli.NewRoot()

	list, _, err := root.Find([]string{"backup", "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Name() != "list" {
		t.Fatalf("backup ls → %s", list.Name())
	}

	relList, _, err := root.Find([]string{"release", "ls"})
	if err != nil {
		t.Fatal(err)
	}
	if relList.Name() != "list" {
		t.Fatalf("release ls → %s", relList.Name())
	}

	rest, _, err := root.Find([]string{"backup", "restore"})
	if err != nil {
		t.Fatal(err)
	}
	if rest.Name() != "restore" {
		t.Fatalf("backup restore → %s", rest.Name())
	}

	del, _, err := root.Find([]string{"backup", "delete"})
	if err != nil {
		t.Fatal(err)
	}
	if del.Name() != "delete" {
		t.Fatalf("backup delete → %s", del.Name())
	}

	rm, _, err := root.Find([]string{"backup", "rm"})
	if err != nil {
		t.Fatal(err)
	}
	if rm.Name() != "delete" {
		t.Fatalf("backup rm → %s", rm.Name())
	}

	cp, _, err := root.Find([]string{"backup", "cp"})
	if err != nil {
		t.Fatal(err)
	}
	if cp.Name() != "cp" {
		t.Fatalf("backup cp → %s", cp.Name())
	}

	copyAlias, _, err := root.Find([]string{"backup", "copy"})
	if err != nil {
		t.Fatal(err)
	}
	if copyAlias.Name() != "cp" {
		t.Fatalf("backup copy → %s", copyAlias.Name())
	}

	login, _, err := root.Find([]string{"login"})
	if err != nil {
		t.Fatal(err)
	}
	if login.Name() != "login" {
		t.Fatalf("login → %s", login.Name())
	}
}

func TestRestoreAcceptsDBFlags(t *testing.T) {
	root := cli.NewRoot()
	cmd, _, err := root.Find([]string{"restore"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"db", "s3", "db-only", "s3-only"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("restore missing flag --%s", name)
		}
	}
}

func TestUninstallHasNoPurgeFlag(t *testing.T) {
	root := cli.NewRoot()
	cmd, _, err := root.Find([]string{"uninstall"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Flags().Lookup("purge") != nil {
		t.Fatal("uninstall must not expose destructive --purge")
	}
}

func TestCompletionCommandExists(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"completion", "bash"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "qabas") {
		n := 200
		if len(out) < n {
			n = len(out)
		}
		t.Fatalf("expected bash completion script, got:\n%s", out[:n])
	}
}
