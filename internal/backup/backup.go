package backup

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/interrupt"
	"github.com/qabas1/qabasone-cli/internal/manifest"
	"github.com/qabas1/qabasone-cli/internal/paths"
	"github.com/qabas1/qabasone-cli/internal/state"
	"gopkg.in/yaml.v3"
)

type Meta struct {
	ID              string    `yaml:"id" json:"id"`
	Name            string    `yaml:"name,omitempty" json:"name,omitempty"`
	QabasOneRelease string    `yaml:"qabasoneRelease" json:"qabasoneRelease"`
	CreatedAt       time.Time `yaml:"createdAt" json:"createdAt"`
	Database        Part      `yaml:"database" json:"database"`
	S3              Part      `yaml:"s3" json:"s3"`
	JSReport        Part      `yaml:"jsreport,omitempty" json:"jsreport,omitempty"`
	Verified        bool      `yaml:"verified" json:"verified"`
	Checksum        string    `yaml:"checksum,omitempty" json:"checksum,omitempty"`
}

// MaxNameLen is the maximum length of a backup display name.
const MaxNameLen = 128

// NormalizeName trims a backup display name. Empty input is allowed (no name).
// Rejects control characters, path separators, and names longer than MaxNameLen.
func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if len(name) > MaxNameLen {
		return "", exitcode.New(exitcode.InvalidInput, "backup name must be at most %d characters", MaxNameLen)
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == 0:
			return "", exitcode.New(exitcode.InvalidInput, "backup name must not contain path separators or null bytes")
		case r < 0x20 || r == 0x7f:
			return "", exitcode.New(exitcode.InvalidInput, "backup name must not contain control characters")
		}
	}
	return name, nil
}

// AutoName returns the default label for a user-initiated backup create when
// --name is omitted. Uses the same UTC stamp as NewID so it aligns with the id.
// Distinct from system labels like "pre-deploy" and "pre-restore".
func AutoName(t time.Time) string {
	return fmt.Sprintf("manual_%s", t.UTC().Format("20060102-150405"))
}

type Part struct {
	Included  bool   `yaml:"included" json:"included"`
	File      string `yaml:"file,omitempty" json:"file,omitempty"`
	Engine    string `yaml:"engine,omitempty" json:"engine,omitempty"`
	Container string `yaml:"container,omitempty" json:"container,omitempty"`
}

func NewID(t time.Time) string {
	return fmt.Sprintf("qb-%s", t.UTC().Format("20060102-150405"))
}

func Dir(p paths.Paths, id string) string {
	return filepath.Join(p.Backups(), id)
}

const (
	partialSuffix     = ".partial"
	partialMaxAge     = time.Hour
	partialSweepLabel = "BACKUP"
)

func stagingDir(p paths.Paths, id string) string {
	return Dir(p, id) + partialSuffix
}

// Create writes a local backup. When includeDB is set, PostgreSQL is backed up
// with the host vpg tool (full cluster). vpg may be nil (defaults to NewClient).
// name is an optional human-readable label stored in meta (not the directory id).
// When name is empty, AutoName is applied so every created backup has a label.
func Create(p paths.Paths, rt *compose.Runtime, st state.State, rel *manifest.Release, includeDB, includeS3 bool, name string, dryRun bool) (*Meta, error) {
	return CreateWith(p, rt, nil, st, rel, includeDB, includeS3, name, dryRun)
}

// CreateWith is Create with an injectable vpg client (nil = NewClient).
// Work is written under a *.partial staging directory and renamed only after
// meta.yaml is written. Ctrl+C is deferred for the duration of the create.
func CreateWith(p paths.Paths, rt *compose.Runtime, vpg *Client, st state.State, rel *manifest.Release, includeDB, includeS3 bool, name string, dryRun bool) (*Meta, error) {
	_ = SweepPartials(p, partialMaxAge)
	if !includeDB && !includeS3 {
		includeDB, includeS3 = true, true
	}
	name, err := NormalizeName(name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	id := NewID(now)
	if name == "" {
		name = AutoName(now)
	}
	meta := &Meta{
		ID:              id,
		Name:            name,
		QabasOneRelease: st.CurrentRelease,
		CreatedAt:       now,
		Database:        Part{Included: includeDB},
		S3:              Part{Included: includeS3},
	}
	meta.JSReport.Included = includeS3 && (releaseUsesJSReport(p, st.CurrentRelease, rel) || hasJSReportVolume(rt, compose.EnvForInstalledRelease(p, st.CurrentRelease)))
	if dryRun {
		if includeDB {
			if err := resolveClient(vpg).Preflight(); err != nil {
				return nil, err
			}
		}
		return meta, nil
	}

	var out *Meta
	err = interrupt.Protect(partialSweepLabel, func() error {
		var createErr error
		out, createErr = createStaged(p, rt, vpg, st, rel, meta, includeDB, includeS3)
		return createErr
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func createStaged(p paths.Paths, rt *compose.Runtime, vpg *Client, st state.State, rel *manifest.Release, meta *Meta, includeDB, includeS3 bool) (*Meta, error) {
	staging := stagingDir(p, meta.ID)
	final := Dir(p, meta.ID)
	// Mode 0700: backups may contain role password hashes and object storage.
	if err := ensureSecureDir(staging); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()

	env := compose.EnvForInstalledRelease(p, st.CurrentRelease)
	if rel != nil {
		env = compose.ImageEnv(rel, p)
	}

	if includeDB {
		vpg = resolveClient(vpg)
		if err := vpg.Preflight(); err != nil {
			return nil, err
		}
		// vpg needs a live postgres container; recreate/start it if the stack is down.
		if err := ensurePostgresForBackup(p, rt, env); err != nil {
			return nil, err
		}
		target, err := ResolvePostgresTarget(p, rt, env)
		if err != nil {
			return nil, err
		}
		pgDir := filepath.Join(staging, "postgres")
		if err := ensureSecureDir(pgDir); err != nil {
			return nil, err
		}
		if err := vpg.Backup(BackupOpts{
			OutputDir:            pgDir,
			Container:            target.Container,
			Username:             target.Username,
			Password:             target.Password,
			MaintenanceDB:        target.MaintenanceDB,
			Archive:              true,
			IncludeRolePasswords: true,
		}); err != nil {
			return nil, err
		}
		// vpg may write absolute paths into *.tar.gz.sha256 while output lives
		// under *.partial/; rewrite to basenames before the atomic rename.
		if err := relativizeVPGChecksumPaths(pgDir); err != nil {
			return nil, err
		}
		relFile, err := findVPGArtifact(staging, pgDir)
		if err != nil {
			return nil, err
		}
		meta.Database.File = relFile
		meta.Database.Engine = EngineVPG
		meta.Database.Container = target.Container
	}
	if includeS3 {
		s3dir := filepath.Join(staging, "s3")
		if err := os.MkdirAll(s3dir, 0o700); err != nil {
			return nil, err
		}
		_ = os.Chmod(s3dir, 0o700)
		// MinIO image has no tar; archive the named data volume via alpine.
		// Uses the Docker volume directly so this works when containers are down.
		s3file := filepath.Join(s3dir, "data.tar")
		if err := rt.TarVolumesFrom(compose.MinioContainer, compose.MinioDataDir, s3file, env); err != nil {
			return nil, exitcode.New(exitcode.BackupRestore, "minio archive: %v", err)
		}
		meta.S3.File = "s3/data.tar"
	}
	if meta.JSReport.Included {
		archive := filepath.Join(staging, "jsreport", "data.tar")
		if err := rt.TarVolumesFrom(compose.JSReportContainer, compose.JSReportDataDir, archive, env); err != nil {
			return nil, exitcode.New(exitcode.BackupRestore, "jsreport archive: %v", err)
		}
		meta.JSReport.File = "jsreport/data.tar"
	}

	sum, err := checksumDir(staging)
	if err != nil {
		return nil, err
	}
	meta.Checksum = sum
	meta.Verified = true
	if err := writeMeta(staging, meta); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, final); err != nil {
		return nil, exitcode.Wrap(exitcode.BackupRestore, err)
	}
	cleanup = false
	return meta, nil
}

// SweepPartials removes orphan *.partial backup directories older than maxAge.
// Best-effort; errors are ignored. Pass maxAge <= 0 to use the default (1h).
func SweepPartials(p paths.Paths, maxAge time.Duration) error {
	if maxAge <= 0 {
		maxAge = partialMaxAge
	}
	entries, err := os.ReadDir(p.Backups())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), partialSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(p.Backups(), e.Name()))
	}
	return nil
}

func List(p paths.Paths) ([]Meta, error) {
	_ = SweepPartials(p, partialMaxAge)
	entries, err := os.ReadDir(p.Backups())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), partialSuffix) {
			continue
		}
		m, err := Load(p, e.Name())
		if err != nil {
			continue
		}
		out = append(out, *m)
	}
	return out, nil
}

func Load(p paths.Paths, id string) (*Meta, error) {
	dir := Dir(p, id)
	if _, err := os.Stat(filepath.Join(dir, "meta.yaml")); err != nil {
		if os.IsNotExist(err) {
			return nil, exitcode.New(exitcode.BackupRestore, "backup %s not found", id)
		}
		return nil, exitcode.Wrap(exitcode.BackupRestore, err)
	}
	return LoadDir(dir)
}

// Delete removes a managed backup directory after validating meta.yaml exists.
// When dryRun is true, validation still runs but the directory is left intact.
func Delete(p paths.Paths, id string, dryRun bool) error {
	if _, err := Load(p, id); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	if err := os.RemoveAll(Dir(p, id)); err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	return nil
}

// LoadDir reads and validates meta.yaml from a Qabas backup wrapper directory.
// Rejects absolute / escaping relative paths in database and s3 file fields.
func LoadDir(dir string) (*Meta, error) {
	b, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, exitcode.New(exitcode.BackupRestore, "backup meta.yaml not found in %s", dir)
		}
		return nil, exitcode.Wrap(exitcode.BackupRestore, err)
	}
	var m Meta
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, exitcode.New(exitcode.BackupRestore, "invalid meta.yaml in %s: %v", dir, err)
	}
	if err := validateWrapperMeta(dir, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func validateWrapperMeta(dir string, m *Meta) error {
	if m == nil {
		return exitcode.New(exitcode.BackupRestore, "invalid backup metadata in %s", dir)
	}
	id := strings.TrimSpace(m.ID)
	if id == "" {
		return exitcode.New(exitcode.BackupRestore, "backup meta.yaml in %s is missing id", dir)
	}
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return exitcode.New(exitcode.BackupRestore, "backup meta.yaml in %s has invalid id %q", dir, id)
	}
	m.ID = id
	if !m.Database.Included && !m.S3.Included && !m.JSReport.Included {
		return exitcode.New(exitcode.BackupRestore, "backup %s includes no data", m.ID)
	}
	if m.Database.Included {
		if _, err := safeJoinUnder(dir, m.Database.File); err != nil {
			return exitcode.New(exitcode.BackupRestore,
				"backup %s has unsafe database.file: %v", m.ID, err)
		}
	}
	if m.S3.Included {
		if _, err := safeJoinUnder(dir, m.S3.File); err != nil {
			return exitcode.New(exitcode.BackupRestore,
				"backup %s has unsafe s3.file: %v", m.ID, err)
		}
	}
	if m.JSReport.Included {
		if _, err := safeJoinUnder(dir, m.JSReport.File); err != nil {
			return exitcode.New(exitcode.BackupRestore, "backup %s has unsafe jsreport.file: %v", m.ID, err)
		}
	}
	return nil
}

// safeJoinUnder joins root and a relative path, rejecting absolute paths,
// empty paths, and any result that escapes root (path traversal).
func safeJoinUnder(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("empty path")
	}
	rel = filepath.FromSlash(rel)
	if !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q is not a local relative path", rel)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(rootAbs, rel)
	joinedAbs, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	sep := string(filepath.Separator)
	if joinedAbs != rootAbs && !strings.HasPrefix(joinedAbs, rootAbs+sep) {
		return "", fmt.Errorf("path %q escapes backup directory", rel)
	}
	return joinedAbs, nil
}

func Verify(p paths.Paths, id string) error {
	return VerifyWith(p, id, nil)
}

// VerifyWith verifies the wrapper checksum and, for vpg backups, runs vpg verify.
func VerifyWith(p paths.Paths, id string, vpg *Client) error {
	dir := Dir(p, id)
	m, err := LoadDir(dir)
	if err != nil {
		return err
	}
	return verifyWrapper(dir, m, vpg)
}

// verifyWrapper checks checksum (when present) and vpg artifact integrity.
func verifyWrapper(dir string, m *Meta, vpg *Client) error {
	got, err := checksumDir(dir)
	if err != nil {
		return err
	}
	if m.Checksum != "" && m.Checksum != got {
		return exitcode.New(exitcode.BackupRestore, "backup %s checksum mismatch", m.ID)
	}
	if m.Database.Included && isVPGPart(m.Database) {
		path, err := safeJoinUnder(dir, m.Database.File)
		if err != nil {
			return exitcode.New(exitcode.BackupRestore, "backup %s database.file: %v", m.ID, err)
		}
		path = preferCompleteVPGDir(path)
		if err := resolveClient(vpg).Verify(path); err != nil {
			return err
		}
	}
	return nil
}

func Restore(p paths.Paths, rt *compose.Runtime, rel *manifest.Release, id string, dbOnly, s3Only bool, dryRun bool) error {
	return RestoreWith(p, rt, nil, rel, id, dbOnly, s3Only, dryRun, true)
}

// RestoreWith restores from a backup. force is passed to vpg restore (required for non-interactive use).
// Destructive restore work runs under interrupt.Protect so Ctrl+C cannot tear it down mid-flight.
func RestoreWith(p paths.Paths, rt *compose.Runtime, vpg *Client, rel *manifest.Release, id string, dbOnly, s3Only bool, dryRun, force bool) error {
	dir := Dir(p, id)
	m, err := LoadDir(dir)
	if err != nil {
		return err
	}
	return restoreWrapper(p, rt, vpg, rel, m, dir, dbOnly, s3Only, dryRun, force)
}

// restoreWrapper verifies then restores from an already-loaded wrapper directory.
func restoreWrapper(p paths.Paths, rt *compose.Runtime, vpg *Client, rel *manifest.Release, m *Meta, dir string, dbOnly, s3Only, dryRun, force bool) error {
	if err := verifyWrapper(dir, m, vpg); err != nil {
		return err
	}
	includeDB := m.Database.Included && !s3Only
	includeS3 := m.S3.Included && !dbOnly
	includeJSReport := m.JSReport.Included && !dbOnly
	if dryRun {
		if includeDB && isVPGPart(m.Database) {
			if err := resolveClient(vpg).Preflight(); err != nil {
				return err
			}
		}
		return nil
	}
	return interrupt.Protect("RESTORE", func() error {
		return restoreDestructive(p, rt, vpg, rel, m, dir, includeDB, includeS3, includeJSReport, force)
	})
}

func restoreDestructive(p paths.Paths, rt *compose.Runtime, vpg *Client, rel *manifest.Release, m *Meta, dir string, includeDB, includeS3, includeJSReport, force bool) error {
	env := compose.EnvForInstalledRelease(p, m.QabasOneRelease)
	if rel != nil {
		env = compose.ImageEnv(rel, p)
	}
	if includeDB {
		if isVPGPart(m.Database) {
			vpg = resolveClient(vpg)
			if err := vpg.Preflight(); err != nil {
				return err
			}
			container := m.Database.Container
			if container == "" {
				if name := postgresContainerName(rt, p, env); name != "" {
					container = name
				} else {
					container = DefaultContainer
				}
			}
			path, err := safeJoinUnder(dir, m.Database.File)
			if err != nil {
				return exitcode.New(exitcode.BackupRestore, "backup %s database.file: %v", m.ID, err)
			}
			path = preferCompleteVPGDir(path)
			if err := vpg.Restore(path, container, force); err != nil {
				return err
			}
		} else {
			dumpPath, err := safeJoinUnder(dir, m.Database.File)
			if err != nil {
				return exitcode.New(exitcode.BackupRestore, "backup %s database.file: %v", m.ID, err)
			}
			dump, err := os.Open(dumpPath)
			if err != nil {
				return exitcode.Wrap(exitcode.BackupRestore, err)
			}
			defer dump.Close()
			err = rt.ExecFromFile(p, env, "postgres", dump, "sh", "-c", "pg_restore -U \"$POSTGRES_USER\" -d \"$POSTGRES_DB\" --clean --if-exists")
			if err != nil {
				return exitcode.New(exitcode.BackupRestore, "pg_restore: %v", err)
			}
		}
	}
	if includeS3 {
		archive, err := safeJoinUnder(dir, m.S3.File)
		if err != nil {
			return exitcode.New(exitcode.BackupRestore, "backup %s s3.file: %v", m.ID, err)
		}
		if err := rt.UntarVolumesFrom(compose.MinioContainer, compose.MinioDataDir, archive, env); err != nil {
			return exitcode.New(exitcode.BackupRestore, "minio restore: %v", err)
		}
	}
	if includeJSReport {
		archive, err := safeJoinUnder(dir, m.JSReport.File)
		if err != nil {
			return exitcode.New(exitcode.BackupRestore, "backup %s jsreport.file: %v", m.ID, err)
		}
		if err := rt.UntarVolumesFrom(compose.JSReportContainer, compose.JSReportDataDir, archive, env); err != nil {
			return exitcode.New(exitcode.BackupRestore, "jsreport restore: %v", err)
		}
	}
	return nil
}

func releaseUsesJSReport(p paths.Paths, version string, rel *manifest.Release) bool {
	raw, err := os.ReadFile(filepath.Join(p.Releases(), version+".yaml"))
	if err == nil {
		installed, err := manifest.Parse(raw)
		return err == nil && installed.Infra.Jsreport != ""
	}
	return rel != nil && rel.Infra.Jsreport != ""
}

func hasJSReportVolume(rt *compose.Runtime, env []string) bool {
	if rt == nil {
		return false
	}
	_, err := rt.ResolveDataVolume(compose.JSReportContainer, compose.JSReportDataDir, env)
	return err == nil
}

// ensurePostgresForBackup recreates and starts postgres when containers were
// removed (compose down) but volumes remain. vpg requires a running container.
func ensurePostgresForBackup(p paths.Paths, rt *compose.Runtime, env []string) error {
	if rt == nil {
		return nil
	}
	// create is idempotent when the container already exists; start is a no-op
	// when already running. Covers: down (no container), stop (exited), running.
	if err := rt.EnsureServiceCreated(p, env, "postgres"); err != nil {
		return exitcode.New(exitcode.BackupRestore, "prepare postgres for backup: %v", err)
	}
	if err := rt.Start(p, env, "postgres"); err != nil {
		return exitcode.New(exitcode.BackupRestore, "start postgres for backup: %v", err)
	}
	return nil
}

func writeMeta(dir string, m *Meta) error {
	b, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o600)
}

func checksumDir(dir string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(path, "meta.yaml") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(h, f)
		return err
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", h.Sum(nil)), nil
}
