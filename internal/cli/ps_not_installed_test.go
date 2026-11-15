package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/cli"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestPsWithoutFirstInstallCompose(t *testing.T) {
	t.Setenv(paths.EnvConfigDir, t.TempDir())
	t.Setenv(paths.EnvStateDir, t.TempDir())
	t.Setenv(paths.EnvLogDir, t.TempDir())
	root := cli.NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"ps"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not installed") {
		t.Fatalf("ps output = %q", out.String())
	}
}
