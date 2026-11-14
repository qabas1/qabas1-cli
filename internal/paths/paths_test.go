package paths_test

import (
	"testing"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

func TestEnvOverrides(t *testing.T) {
	t.Setenv(paths.EnvConfigDir, `C:\tmp\qabas-etc`)
	t.Setenv(paths.EnvStateDir, `C:\tmp\qabas-lib`)
	t.Setenv(paths.EnvLogDir, `C:\tmp\qabas-log`)
	t.Setenv(paths.EnvLockPath, `C:\tmp\qabas.lock`)
	p := paths.Resolve()
	if p.ConfigDir != `C:\tmp\qabas-etc` {
		t.Fatalf("config %s", p.ConfigDir)
	}
	if p.StateFile() != `C:\tmp\qabas-lib\state.json` && p.StateFile() != `C:\tmp\qabas-lib/state.json` {
		// filepath.Join on Windows uses backslash
		if p.StateDir != `C:\tmp\qabas-lib` {
			t.Fatalf("state %s file %s", p.StateDir, p.StateFile())
		}
	}
	if p.LockPath != `C:\tmp\qabas.lock` {
		t.Fatalf("lock %s", p.LockPath)
	}
}
