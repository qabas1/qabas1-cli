//go:build unix

package paths

import "testing"

func TestDefaultsLinuxLayout(t *testing.T) {
	want := Paths{
		ConfigDir: "/etc/qabasone",
		StateDir:  "/var/lib/qabasone",
		LogDir:    "/var/log/qabasone",
		LockPath:  "/var/lib/qabasone/qabas.lock",
	}
	if got := defaults(); got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	if MigrateLegacy() != "" {
		t.Fatal("MigrateLegacy must be a no-op on unix")
	}
}
