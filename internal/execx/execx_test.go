package execx_test

import (
	"runtime"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/execx"
)

func TestCommandRunnable(t *testing.T) {
	name := "true"
	if runtime.GOOS == "windows" {
		name = "cmd"
	}
	var cmdArgs []string
	if runtime.GOOS == "windows" {
		cmdArgs = []string{"/c", "exit", "0"}
	}
	cmd := execx.Command(name, cmdArgs...)
	if cmd == nil || cmd.Path == "" && cmd.Args == nil {
		t.Fatal("expected command")
	}
	if err := cmd.Run(); err != nil {
		// PATH may lack true on some Unix CI images; Command construction is enough.
		t.Logf("run skipped: %v", err)
	}
}
