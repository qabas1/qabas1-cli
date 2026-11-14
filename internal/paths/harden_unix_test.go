//go:build unix

package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHardenTightensLooseModes(t *testing.T) {
	d := t.TempDir()
	p := Paths{ConfigDir: filepath.Join(d, "etc"), StateDir: filepath.Join(d, "lib"), LogDir: filepath.Join(d, "log"), LockPath: filepath.Join(d, "lib", "qabas.lock")}
	for _, dir := range []string{p.ConfigDir, p.Backups(), p.LogDir, filepath.Join(p.ConfigDir, "pg"), filepath.Join(p.ConfigDir, "nodes", "shared")} {
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(dir, 0o777)
	}
	files := map[string]os.FileMode{
		p.SecretsFile(): 0o644,
		p.EnvAPI():      0o644,
		filepath.Join(p.ConfigDir, "config.yaml"):          0o644,
		filepath.Join(p.ConfigDir, "pg", "pg_hba.conf"):    0o644,
		filepath.Join(p.ConfigDir, "nodes", "shared", "x"): 0o644,
		p.LockPath: 0o644,
	}
	for f, m := range files {
		if err := os.WriteFile(f, []byte("x"), m); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(f, m)
	}
	if err := Harden(p); err != nil {
		t.Fatal(err)
	}
	want := map[string]os.FileMode{
		p.ConfigDir:     0o711,
		p.Backups():     0o700,
		p.LogDir:        0o750,
		p.SecretsFile(): 0o600,
		p.EnvAPI():      0o600,
		p.LockPath:      0o600,
		filepath.Join(p.ConfigDir, "nodes", "shared", "x"): 0o600,
		// Readable by non-root qabas status / the postgres container.
		filepath.Join(p.ConfigDir, "config.yaml"):       0o644,
		filepath.Join(p.ConfigDir, "pg", "pg_hba.conf"): 0o644,
	}
	for f, m := range want {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != m {
			t.Errorf("%s mode = %04o, want %04o", f, st.Mode().Perm(), m)
		}
	}
}

func TestHardenNeverLoosens(t *testing.T) {
	d := t.TempDir()
	p := Paths{ConfigDir: filepath.Join(d, "etc"), StateDir: filepath.Join(d, "lib"), LogDir: filepath.Join(d, "log"), LockPath: filepath.Join(d, "lock")}
	_ = os.MkdirAll(p.ConfigDir, 0o700)
	_ = os.Chmod(p.ConfigDir, 0o700)
	if err := Harden(p); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p.ConfigDir); st.Mode().Perm() != 0o700 {
		t.Fatalf("mode loosened to %04o", st.Mode().Perm())
	}
}
