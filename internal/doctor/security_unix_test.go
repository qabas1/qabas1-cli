//go:build unix

package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/paths"
)

func TestSecurityChecksSecretPerms(t *testing.T) {
	d := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(d, "etc"), StateDir: filepath.Join(d, "lib"), LogDir: filepath.Join(d, "log"), LockPath: filepath.Join(d, "lock")}
	_ = os.MkdirAll(p.ConfigDir, 0o711)
	_ = os.WriteFile(p.SecretsFile(), []byte("X=1\n"), 0o644)
	_ = os.Chmod(p.SecretsFile(), 0o644)
	wg := filepath.Join(d, "qabas0.conf")
	_ = os.WriteFile(wg, []byte("[Interface]\n"), 0o644)
	_ = os.Chmod(wg, 0o644)
	got := map[string]Check{}
	for _, c := range SecurityChecks(p, false, SecurityProbes{GOOS: "linux", WireGuardConfig: wg}) {
		got[c.Name] = c
	}
	if c := got["secret-perms"]; c.OK || !strings.Contains(c.Detail, "secrets.env (0644)") {
		t.Fatalf("secret-perms = %+v", c)
	}
	if c := got["wireguard-key"]; c.OK {
		t.Fatalf("wireguard-key = %+v", c)
	}
	_ = os.Chmod(p.SecretsFile(), 0o600)
	_ = os.Chmod(wg, 0o600)
	for _, c := range SecurityChecks(p, false, SecurityProbes{GOOS: "linux", WireGuardConfig: wg}) {
		if (c.Name == "secret-perms" || c.Name == "wireguard-key") && !c.OK {
			t.Fatalf("%s still failing: %s", c.Name, c.Detail)
		}
	}
}
