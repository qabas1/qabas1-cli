//go:build !windows

package execx_test

import (
	"testing"

	"github.com/qabasone/qabasone-cli/internal/execx"
)

func TestCommandSetsPgid(t *testing.T) {
	cmd := execx.Command("true")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("expected Setpgid on SysProcAttr, got %#v", cmd.SysProcAttr)
	}
}
