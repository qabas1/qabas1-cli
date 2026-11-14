//go:build windows

package paths

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func programDataBase() string {
	root := os.Getenv("ProgramData")
	if root == "" {
		root = `C:\ProgramData`
	}
	return filepath.Join(root, "QabasOne")
}

func defaults() Paths {
	base := programDataBase()
	return Paths{
		ConfigDir: filepath.Join(base, "etc"),
		StateDir:  filepath.Join(base, "lib"),
		LogDir:    filepath.Join(base, "log"),
		LockPath:  filepath.Join(base, "lib", "qabas.lock"),
	}
}

func normalizeRootOwnership(_ Paths) {}

// secureRuntime limits config, state, logs and backups (secrets, tokens,
// dumps, audit logs) to SYSTEM, Administrators, and the current user.
// Best-effort here; doctor and autopilot report failures via Harden.
func secureRuntime(p Paths) { _ = Harden(p) }

func harden(p Paths) []error {
	var errs []error
	for _, d := range []string{p.ConfigDir, p.StateDir, p.LogDir, p.Backups()} {
		if d == "" || d == "." {
			continue
		}
		if _, err := os.Stat(d); err != nil {
			continue
		}
		if err := restrictDir(d); err != nil {
			errs = append(errs, fmt.Errorf("restrict %s: %w", d, err))
		}
	}
	return errs
}

// RestrictPrivate applies the private DACL to dir.
func RestrictPrivate(dir string) error { return restrictDir(dir) }

func restrictDir(dir string) error {
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if sid := currentUserSID(); sid != "" {
		sddl += "(A;OICI;FA;;;" + sid + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}

func currentUserSID() string {
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tu == nil || tu.User.Sid == nil {
		return ""
	}
	return tu.User.Sid.String()
}

// legacyBase is the pre-ProgramData layout (%TEMP%\qabasone).
func legacyBase() string { return filepath.Join(os.TempDir(), "qabasone") }

// MigrateLegacy copies %TEMP%\qabasone into %ProgramData%\QabasOne once, when
// the new layout is still empty and no QABAS_* overrides are set. Returns a
// notice for the user, or "" when nothing was migrated.
func MigrateLegacy() string {
	for _, k := range []string{EnvConfigDir, EnvStateDir, EnvLogDir, EnvDataDir} {
		if os.Getenv(k) != "" {
			return ""
		}
	}
	oldBase := legacyBase()
	newBase := programDataBase()
	if !hasEntries(filepath.Join(oldBase, "etc")) && !hasEntries(filepath.Join(oldBase, "lib")) {
		return ""
	}
	if hasEntries(filepath.Join(newBase, "etc")) || hasEntries(filepath.Join(newBase, "lib")) {
		return ""
	}
	for _, sub := range []string{"etc", "lib", "log"} {
		src := filepath.Join(oldBase, sub)
		if !hasEntries(src) {
			continue
		}
		if err := copyTree(src, filepath.Join(newBase, sub)); err != nil {
			return fmt.Sprintf("warning: could not migrate %s to %s: %v (run from an elevated PowerShell)", oldBase, newBase, err)
		}
	}
	return fmt.Sprintf("Migrated QabasOne data from %s to %s (old copy left in place).", oldBase, newBase)
}

func hasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if filepath.Base(path) == "qabas.lock" || !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
