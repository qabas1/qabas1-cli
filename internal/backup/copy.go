package backup

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
)

// Copy recursively copies a backup wrapper directory to dest.
//
// Destination layout (Unix cp -R style):
//   - If dest exists and is a directory → copy into dest/<base(srcDir)>/
//   - If dest does not exist → create the wrapper directory at dest (parent must exist)
//
// When the final target already exists, Copy fails unless force is true
// (then the existing target is removed and replaced).
// When dryRun is true, validation and path resolution still run but nothing is written.
func Copy(srcDir, dest string, force, dryRun bool) (string, error) {
	srcDir = filepath.Clean(srcDir)
	dest = filepath.Clean(dest)

	srcInfo, err := os.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", exitcode.New(exitcode.BackupRestore, "backup source %s not found", srcDir)
		}
		return "", exitcode.Wrap(exitcode.BackupRestore, err)
	}
	if !srcInfo.IsDir() {
		return "", exitcode.New(exitcode.InvalidInput, "backup source %s is not a directory", srcDir)
	}

	finalDest, err := resolveCopyDest(srcDir, dest)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(finalDest); err == nil {
		if !force {
			return "", exitcode.NewHint(exitcode.InvalidInput,
				"re-run with --force to replace",
				"destination %s already exists", finalDest)
		}
		if !dryRun {
			if err := os.RemoveAll(finalDest); err != nil {
				return "", exitcode.Wrap(exitcode.BackupRestore, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return "", exitcode.Wrap(exitcode.BackupRestore, err)
	}

	if dryRun {
		return finalDest, nil
	}

	if err := copyDir(srcDir, finalDest); err != nil {
		_ = os.RemoveAll(finalDest)
		return "", exitcode.Wrap(exitcode.BackupRestore, err)
	}
	return finalDest, nil
}

func resolveCopyDest(srcDir, dest string) (string, error) {
	info, err := os.Stat(dest)
	if err == nil {
		if !info.IsDir() {
			return "", exitcode.New(exitcode.InvalidInput,
				"destination %s exists and is not a directory", dest)
		}
		return filepath.Join(dest, filepath.Base(srcDir)), nil
	}
	if !os.IsNotExist(err) {
		return "", exitcode.Wrap(exitcode.BackupRestore, err)
	}

	parent := filepath.Dir(dest)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		if os.IsNotExist(err) {
			return "", exitcode.New(exitcode.InvalidInput,
				"destination parent directory %s does not exist", parent)
		}
		return "", exitcode.Wrap(exitcode.BackupRestore, err)
	}
	if !parentInfo.IsDir() {
		return "", exitcode.New(exitcode.InvalidInput,
			"destination parent %s is not a directory", parent)
	}
	return dest, nil
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()

		if d.IsDir() {
			return os.MkdirAll(target, mode.Perm())
		}
		if !mode.IsRegular() {
			return exitcode.New(exitcode.BackupRestore,
				"unsupported file type in backup: %s", path)
		}
		return copyFile(path, target, mode.Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
