//go:build unix

package paths

import (
	"os"
	"path/filepath"
)

func defaults() Paths {
	return Paths{
		ConfigDir: "/etc/qabasone",
		StateDir:  "/var/lib/qabasone",
		LogDir:    "/var/log/qabasone",
		LockPath:  "/var/lib/qabasone/qabas.lock",
	}
}

func secureRuntime(p Paths) {
	if os.Geteuid() == 0 {
		_ = Harden(p)
	}
}

// harden tightens modes that are looser than the policy; it never loosens.
func harden(p Paths) []error {
	var errs []error
	tighten := func(path string, want os.FileMode) {
		st, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				errs = append(errs, err)
			}
			return
		}
		if st.Mode().Perm()&^want != 0 {
			errs = append(errs, os.Chmod(path, st.Mode().Perm()&want))
		}
	}
	tighten(p.ConfigDir, ModeConfigDir)
	tighten(filepath.Join(p.ConfigDir, "secret-files"), 0o700)
	tighten(p.Backups(), ModeBackupsDir)
	tighten(p.LogDir, ModeLogDir)
	tighten(p.LockPath, ModeLockFile)
	tighten(p.LogFile(), 0o640)
	tighten(p.HistoryFile(), 0o640)
	for _, f := range SecretFiles(p) {
		tighten(f, ModeSecretFile)
	}
	return errs
}

// RestrictPrivate limits dir to its owner.
func RestrictPrivate(dir string) error { return os.Chmod(dir, 0o700) }

// MigrateLegacy is a no-op on Unix; the layout has always been /etc + /var/lib.
func MigrateLegacy() string { return "" }

// normalizeRootOwnership fixes mixed sudo/non-sudo runs (common on WSL/dev machines).
func normalizeRootOwnership(p Paths) {
	if os.Geteuid() != 0 {
		return
	}
	for _, d := range []string{p.ConfigDir, p.StateDir, p.LogDir, filepath.Dir(p.LockPath)} {
		if d == "" || d == "." {
			continue
		}
		_ = os.Chown(d, 0, 0)
	}
	if st, err := os.Stat(p.LockPath); err == nil && !st.IsDir() {
		_ = os.Chown(p.LockPath, 0, 0)
		_ = os.Chmod(p.LockPath, ModeLockFile)
	}
}
