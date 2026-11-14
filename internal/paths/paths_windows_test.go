//go:build windows

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsProgramData(t *testing.T) {
	t.Setenv("ProgramData", `D:\PD`)
	p := defaults()
	want := Paths{
		ConfigDir: `D:\PD\QabasOne\etc`,
		StateDir:  `D:\PD\QabasOne\lib`,
		LogDir:    `D:\PD\QabasOne\log`,
		LockPath:  `D:\PD\QabasOne\lib\qabas.lock`,
	}
	if p != want {
		t.Fatalf("defaults = %+v, want %+v", p, want)
	}
}

func TestDefaultsProgramDataFallback(t *testing.T) {
	t.Setenv("ProgramData", "")
	if got := defaults().ConfigDir; got != `C:\ProgramData\QabasOne\etc` {
		t.Fatalf("ConfigDir = %s", got)
	}
}

func TestMigrateLegacy(t *testing.T) {
	tmp := t.TempDir()
	pd := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	t.Setenv("ProgramData", pd)
	for _, k := range []string{EnvConfigDir, EnvStateDir, EnvLogDir, EnvDataDir} {
		t.Setenv(k, "")
	}
	legacyEtc := filepath.Join(tmp, "qabasone", "etc")
	if err := os.MkdirAll(legacyEtc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyEtc, "github-token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msg := MigrateLegacy(); msg == "" {
		t.Fatal("expected migration notice")
	}
	b, err := os.ReadFile(filepath.Join(pd, "QabasOne", "etc", "github-token"))
	if err != nil || string(b) != "tok" {
		t.Fatalf("migrated token = %q, err=%v", b, err)
	}
	if msg := MigrateLegacy(); msg != "" {
		t.Fatalf("second run should be a no-op, got %q", msg)
	}
}
