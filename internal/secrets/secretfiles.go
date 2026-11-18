package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/paths"
)

func secretFilesDir(p paths.Paths) string { return filepath.Join(p.ConfigDir, "secret-files") }

func SecretValueFile(p paths.Paths, key string) string {
	return filepath.Join(secretFilesDir(p), key)
}

// MaterializeFiles prepares one owner-only file per file-delivered secret.
// Compose grants each file only to the services listed in its secret mount.
func MaterializeFiles(p paths.Paths, catalog []Secret, b Bundle) error {
	needDir := false
	for _, s := range catalog {
		if s.Delivery == "file" {
			needDir = true
			break
		}
	}
	if !needDir {
		return nil
	}
	dir := secretFilesDir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("secret files directory is not a real directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	for _, s := range catalog {
		if s.Delivery != "file" {
			continue
		}
		value := b.Value(s.Key)
		if value == "" {
			return fmt.Errorf("file-delivered secret %s is missing", s.Name)
		}
		path := SecretValueFile(p, s.Key)
		if st, err := os.Lstat(path); err == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("mounted secret %s is not a regular file", s.Name)
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if current, err := os.ReadFile(path); err == nil && string(current) == value {
			if err := os.Chmod(path, 0o644); err != nil {
				return err
			}
			continue
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := writeAtomic(path, []byte(value)); err != nil {
			return err
		}
		// Compose bind mounts keep the source mode; the API image runs as uid 1000.
		// The containing host directory is 0700, so only root can traverse it.
		if err := os.Chmod(path, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// FileDeliveryActive reports whether the installed Compose file grants every
// file-delivered secret to its declared consumers.
func FileDeliveryActive(p paths.Paths, catalog []Secret) bool {
	raw, err := os.ReadFile(p.ComposeFile())
	if err != nil {
		return false
	}
	var files []Secret
	for _, s := range catalog {
		if s.Delivery == "file" {
			files = append(files, s)
		}
	}
	return len(files) > 0 && ValidateCatalogCompose(raw, files) == nil
}

// StripMountedEnv removes old env copies only after the new Compose stack has
// started and passed health checks. The canonical secrets.env remains intact.
func StripMountedEnv(p paths.Paths, catalog []Secret) error {
	if !FileDeliveryActive(p, catalog) {
		return nil
	}
	files := map[string]bool{}
	for _, s := range catalog {
		if s.Delivery != "file" {
			continue
		}
		for _, file := range s.Files {
			files[file] = true
		}
	}
	for file := range files {
		path := filepath.Join(p.ConfigDir, file)
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		updated := stripFileDeliveryBody(string(raw), catalog, file)
		if updated != string(raw) {
			if err := writeAtomic(path, []byte(updated)); err != nil {
				return err
			}
		}
	}
	return nil
}

func stripFileDeliveryBody(body string, catalog []Secret, file string) string {
	keys := map[string]bool{}
	for _, s := range catalog {
		if s.Delivery != "file" || !secretUsesFile(s, file) {
			continue
		}
		keys[s.Key] = true
		for _, alias := range s.EnvKeys {
			keys[alias] = true
		}
	}
	if len(keys) == 0 {
		return body
	}
	var kept []string
	for _, line := range strings.SplitAfter(body, "\n") {
		key, _, ok := strings.Cut(line, "=")
		if ok && keys[key] {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "")
}
