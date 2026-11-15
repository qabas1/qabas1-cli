package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/interrupt"
	"github.com/qabas1/qabasone-cli/internal/manifest"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

// SourceKind identifies how a restore/verify argument was resolved.
type SourceKind string

const (
	SourceQabas SourceKind = "qabas" // qb-* wrapper with meta.yaml
	SourceVPG   SourceKind = "vpg"   // raw vpg directory or archive
)

// Source is a resolved backup restore/verify target.
type Source struct {
	Kind    SourceKind
	Arg     string // original CLI argument
	Path    string // absolute wrapper dir (Qabas) or vpg dir/archive (VPG)
	Meta    *Meta  // set for Qabas wrappers
	Display string // human-readable label for messages
}

// ResolveSource classifies arg as a managed Qabas backup id, a filesystem
// Qabas wrapper directory (meta.yaml), or a VPG path.
//
// Accepts:
//   - qb-… id under the managed backups directory
//   - Qabas wrapper directory (contains valid meta.yaml; full DB+S3 restore)
//   - VPG backup directory, .tar.gz archive, or a parent directory that
//     contains a complete VPG artifact (PostgreSQL only)
func ResolveSource(p paths.Paths, arg string) (Source, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return Source{}, exitcode.New(exitcode.InvalidInput, "backup id or path is required")
	}

	// Prefer a bare managed backup id (not a path like ./qb-… or /tmp/qb-…).
	if looksLikeManagedBackupID(arg) {
		if m, err := Load(p, arg); err == nil {
			dir := Dir(p, m.ID)
			return Source{
				Kind:    SourceQabas,
				Arg:     arg,
				Path:    dir,
				Meta:    m,
				Display: m.ID,
			}, nil
		}
	}

	abs, err := filepath.Abs(arg)
	if err != nil {
		return Source{}, exitcode.Wrap(exitcode.InvalidInput, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return Source{}, exitcode.NewHint(exitcode.BackupRestore,
				"pass a Qabas backup id (qabas backup ls), a Qabas wrapper directory, or a vpg directory / .tar.gz",
				"backup %q not found as Qabas id or filesystem path", arg)
		}
		return Source{}, exitcode.Wrap(exitcode.BackupRestore, err)
	}

	// Filesystem Qabas wrapper: meta.yaml present means full wrapper semantics
	// (DB + S3). Do not fall through to VPG if meta.yaml exists but is invalid.
	if st.IsDir() {
		metaPath := filepath.Join(abs, "meta.yaml")
		if _, err := os.Stat(metaPath); err == nil {
			m, err := LoadDir(abs)
			if err != nil {
				return Source{}, err
			}
			return Source{
				Kind:    SourceQabas,
				Arg:     arg,
				Path:    abs,
				Meta:    m,
				Display: fmt.Sprintf("%s (%s)", m.ID, abs),
			}, nil
		}
	}
	if !st.IsDir() && strings.HasSuffix(strings.ToLower(abs), ".tar.gz") {
		isWrapper, err := archiveHasQabasMeta(abs)
		if err != nil {
			return Source{}, exitcode.New(exitcode.BackupRestore, "inspect backup archive %s: %v", abs, err)
		}
		if isWrapper {
			return Source{}, exitcode.NewHint(exitcode.BackupRestore,
				"extract the archive into a directory, then run qabas backup restore /path/to/extracted/qb-… --yes (the directory must contain meta.yaml)",
				"%s is a Qabas backup wrapper archive, not a raw vpg archive", abs)
		}
	}

	vpgPath, err := ResolveVPGPath(abs, st)
	if err != nil {
		return Source{}, err
	}
	return Source{
		Kind:    SourceVPG,
		Arg:     arg,
		Path:    vpgPath,
		Display: vpgPath,
	}, nil
}

func archiveHasQabasMeta(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return false, nil
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		name := strings.TrimPrefix(filepath.ToSlash(h.Name), "./")
		if name == "meta.yaml" || (strings.Count(name, "/") == 1 && strings.HasSuffix(name, "/meta.yaml")) {
			return true, nil
		}
	}
}

// looksLikeManagedBackupID reports whether arg is a bare backup id (qb-…) rather
// than a filesystem path. Paths like ./qb-… must not resolve via the managed
// backups directory (filepath.Join would swallow the "./").
func looksLikeManagedBackupID(arg string) bool {
	if arg == "" || strings.ContainsAny(arg, `/\:`) {
		return false
	}
	if arg == "." || arg == ".." || strings.HasPrefix(arg, ".") {
		return false
	}
	return strings.HasPrefix(arg, "qb-")
}

// ResolveVPGPath returns an absolute path to a VPG backup directory or archive.
func ResolveVPGPath(abs string, st os.FileInfo) (string, error) {
	if st == nil {
		var err error
		st, err = os.Stat(abs)
		if err != nil {
			return "", exitcode.Wrap(exitcode.BackupRestore, err)
		}
	}

	name := st.Name()
	if !st.IsDir() {
		if strings.HasSuffix(strings.ToLower(name), ".tar.gz") && !strings.HasSuffix(strings.ToLower(name), ".tar.gz.sha256") {
			return abs, nil
		}
		return "", exitcode.New(exitcode.InvalidInput,
			"%s is not a vpg backup directory or .tar.gz archive", abs)
	}

	// Complete VPG backup directory.
	if _, err := os.Stat(filepath.Join(abs, "BACKUP_COMPLETE")); err == nil {
		return abs, nil
	}

	// Parent directory from `vpg backup ./backup --archive` containing artifacts.
	rel, err := findVPGArtifact(abs, abs)
	if err != nil {
		return "", exitcode.NewHint(exitcode.BackupRestore,
			"point at a Qabas wrapper directory (meta.yaml), the postgres_cluster_* directory, or its .tar.gz archive",
			"no complete vpg backup found under %s", abs)
	}
	return filepath.Join(abs, filepath.FromSlash(rel)), nil
}

// IsVPGPath reports whether path looks like a VPG backup dir or archive (best-effort).
func IsVPGPath(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	_, err = ResolveVPGPath(path, st)
	return err == nil
}

// VerifySource verifies a Qabas wrapper or an external VPG path.
func VerifySource(p paths.Paths, src Source, vpg *Client) error {
	switch src.Kind {
	case SourceQabas:
		dir := src.Path
		if dir == "" {
			dir = Dir(p, src.Meta.ID)
		}
		m, err := LoadDir(dir)
		if err != nil {
			return err
		}
		return verifyWrapper(dir, m, vpg)
	case SourceVPG:
		vpg = resolveClient(vpg)
		if err := vpg.Preflight(); err != nil {
			return err
		}
		return vpg.Verify(preferCompleteVPGDir(src.Path))
	default:
		return exitcode.New(exitcode.InvalidInput, "unknown backup source kind %q", src.Kind)
	}
}

// RestoreSource restores from a Qabas wrapper or an external VPG path.
// Qabas wrappers (managed id or filesystem path with meta.yaml) restore
// PostgreSQL and/or MinIO according to flags and meta. External VPG sources
// restore PostgreSQL only. --s3 on an external VPG source is rejected.
// imageEnv is used to resolve the live postgres container name for VPG sources.
func RestoreSource(p paths.Paths, rt *compose.Runtime, vpg *Client, rel *manifest.Release, src Source, imageEnv []string, dbOnly, s3Only bool, dryRun, force bool) error {
	switch src.Kind {
	case SourceQabas:
		dir := src.Path
		if dir == "" {
			dir = Dir(p, src.Meta.ID)
		}
		// Re-load from disk so restore uses current meta and confined paths.
		m, err := LoadDir(dir)
		if err != nil {
			return err
		}
		return restoreWrapper(p, rt, vpg, rel, m, dir, dbOnly, s3Only, dryRun, force)
	case SourceVPG:
		if s3Only {
			return exitcode.New(exitcode.InvalidInput,
				"external vpg backups contain PostgreSQL only; cannot restore --s3 from %s", src.Display)
		}
		vpg = resolveClient(vpg)
		if err := vpg.Preflight(); err != nil {
			return err
		}
		if err := vpg.Verify(preferCompleteVPGDir(src.Path)); err != nil {
			return err
		}
		if dryRun {
			return nil
		}
		env := imageEnv
		if env == nil && rel != nil {
			env = compose.ImageEnv(rel, p)
		}
		container := DefaultContainer
		if name := postgresContainerName(rt, p, env); name != "" {
			container = name
		}
		return interrupt.Protect("RESTORE", func() error {
			return vpg.Restore(preferCompleteVPGDir(src.Path), container, force)
		})
	default:
		return exitcode.New(exitcode.InvalidInput, "unknown backup source kind %q", src.Kind)
	}
}
