package backup

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/qabas1/qabasone-cli/internal/execx"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
)

const (
	EngineVPG = "vpg"

	DefaultContainer     = "qabasone-postgres"
	DefaultUsername      = "qabasone"
	DefaultMaintenanceDB = "qabasone"

	vpgInstallHint = "install vpg from https://github.com/elqabasy/voidtools (Debian package); qabas does not install it"
)

// BackupOpts configures a vpg backup invocation.
type BackupOpts struct {
	OutputDir            string
	Container            string
	Username             string
	Password             string
	MaintenanceDB        string
	Archive              bool
	IncludeRolePasswords bool
}

// Client runs the host-installed vpg CLI. Fields are injectable for tests.
type Client struct {
	LookPath func(string) (string, error)
	Run      func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error
	RunIO    func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error
}

func NewClient() *Client {
	return &Client{
		LookPath: exec.LookPath,
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			cmd := execx.Command(name, args...)
			cmd.Env = env
			if stdout != nil {
				cmd.Stdout = stdout
			} else {
				cmd.Stdout = os.Stdout
			}
			if stderr != nil {
				cmd.Stderr = stderr
			} else {
				cmd.Stderr = os.Stderr
			}
			return cmd.Run()
		},
		RunIO: func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
			cmd := execx.Command(name, args...)
			cmd.Env = env
			cmd.Stdin = stdin
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			return cmd.Run()
		},
	}
}

func (c *Client) available() bool {
	if c == nil {
		return false
	}
	_, err := c.LookPath("vpg")
	return err == nil
}

func (c *Client) require() (string, error) {
	if c == nil {
		c = NewClient()
	}
	path, err := c.LookPath("vpg")
	if err != nil {
		return "", exitcode.NewHint(exitcode.BackupRestore, vpgInstallHint, "vpg is not installed or not on PATH")
	}
	return path, nil
}

// Preflight ensures vpg is on PATH and responds to `vpg version`.
func (c *Client) Preflight() error {
	bin, err := c.require()
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	run := c.Run
	if run == nil {
		return exitcode.New(exitcode.BackupRestore, "vpg runtime is not configured")
	}
	if err := run(bin, []string{"version"}, os.Environ(), &stdout, &stderr); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return exitcode.NewHint(exitcode.BackupRestore, vpgInstallHint, "vpg is present but not usable: %s", msg)
	}
	return nil
}

func backupArgs(o BackupOpts) []string {
	container := o.Container
	if container == "" {
		container = DefaultContainer
	}
	username := o.Username
	if username == "" {
		username = DefaultUsername
	}
	maintenanceDB := o.MaintenanceDB
	if maintenanceDB == "" {
		maintenanceDB = DefaultMaintenanceDB
	}
	args := []string{
		"backup", o.OutputDir,
		"--container", container,
		"--username", username,
		"--maintenance-db", maintenanceDB,
		"--password-stdin",
	}
	if o.Archive {
		args = append(args, "--archive")
	}
	if o.IncludeRolePasswords {
		args = append(args, "--include-role-passwords")
	}
	return args
}

func restoreArgs(backupPath, container string, force bool) []string {
	if container == "" {
		container = DefaultContainer
	}
	args := []string{"restore", backupPath, "--container", container}
	if force {
		args = append(args, "--force")
	}
	return args
}

func verifyArgs(backupPath string) []string {
	return []string{"verify", backupPath}
}

func (c *Client) run(args []string) error {
	return c.runWithStdin(args, nil)
}

func (c *Client) runWithStdin(args []string, stdin io.Reader) error {
	bin, err := c.require()
	if err != nil {
		return err
	}
	var stdout, stderr bytes.Buffer
	var runErr error
	if stdin != nil {
		runIO := c.RunIO
		if runIO == nil {
			return exitcode.New(exitcode.BackupRestore, "vpg IO runtime is not configured")
		}
		runErr = runIO(bin, args, os.Environ(), stdin, &stdout, &stderr)
	} else {
		run := c.Run
		if run == nil {
			return exitcode.New(exitcode.BackupRestore, "vpg runtime is not configured")
		}
		runErr = run(bin, args, os.Environ(), &stdout, &stderr)
	}
	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = runErr.Error()
		}
		return exitcode.New(exitcode.BackupRestore, "vpg %s: %s", args[0], msg)
	}
	return nil
}

// Backup creates a full-cluster VPG backup. Password is passed on stdin (never argv).
func (c *Client) Backup(o BackupOpts) error {
	if o.Password == "" {
		return exitcode.New(exitcode.BackupRestore, "vpg backup requires a PostgreSQL password")
	}
	return c.runWithStdin(backupArgs(o), strings.NewReader(o.Password+"\n"))
}

// Restore replaces the target cluster from a VPG backup directory or archive.
func (c *Client) Restore(backupPath, container string, force bool) error {
	return c.run(restoreArgs(backupPath, container, force))
}

// Verify checks VPG structure, inventory, and checksums.
func (c *Client) Verify(backupPath string) error {
	return c.run(verifyArgs(backupPath))
}

func isVPGPart(p Part) bool {
	if p.Engine == EngineVPG {
		return true
	}
	if p.File == "" || p.File == "postgres.dump" {
		return false
	}
	return strings.HasPrefix(filepath.ToSlash(p.File), "postgres/")
}

// findVPGArtifact returns a path relative to the backup id directory for the
// newest complete VPG archive (preferred) or backup directory under postgresDir.
func findVPGArtifact(backupDir, postgresDir string) (string, error) {
	entries, err := os.ReadDir(postgresDir)
	if err != nil {
		return "", exitcode.Wrap(exitcode.BackupRestore, err)
	}

	type cand struct {
		abs  string
		mod  time.Time
		arch bool
	}
	var bestDir, bestArch *cand
	consider := func(abs string, mod time.Time, arch bool) {
		slot := &bestDir
		if arch {
			slot = &bestArch
		}
		if *slot == nil || mod.After((*slot).mod) {
			*slot = &cand{abs: abs, mod: mod, arch: arch}
		}
	}

	for _, e := range entries {
		name := e.Name()
		abs := filepath.Join(postgresDir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !e.IsDir() && strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tar.gz.sha256") {
			consider(abs, info.ModTime(), true)
			continue
		}
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(abs, "BACKUP_COMPLETE")); err != nil {
				continue
			}
			consider(abs, info.ModTime(), false)
		}
	}
	// Prefer a complete backup directory over a sibling .tar.gz: archive sidecars
	// may embed absolute staging paths (*.partial/) that break after rename.
	best := bestDir
	if best == nil {
		best = bestArch
	}
	if best == nil {
		return "", exitcode.New(exitcode.BackupRestore, "vpg backup produced no complete artifact in %s", postgresDir)
	}
	rel, err := filepath.Rel(backupDir, best.abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func resolveClient(c *Client) *Client {
	if c == nil {
		return NewClient()
	}
	return c
}

// VPGAvailable reports whether the vpg binary is on PATH.
func VPGAvailable() bool {
	return NewClient().available()
}

// Supported reports whether vpg-based backup/restore runs on this OS.
// vpg ships as a Debian package only, so Windows hosts skip database backups.
func Supported() bool { return supportedOS(runtime.GOOS) }

func supportedOS(goos string) bool { return goos != "windows" }

// RequireSupported fails on hosts where vpg backup/restore is unavailable.
func RequireSupported() error {
	if Supported() {
		return nil
	}
	return exitcode.NewHint(exitcode.BackupRestore,
		"run backup/restore from a Linux host (or WSL2) where vpg is installed",
		"database backup and restore are not supported on %s", runtime.GOOS)
}

// RequireVPG fails with an install hint when vpg is missing or unusable.
func RequireVPG() error {
	return NewClient().Preflight()
}

func ensureSecureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	return nil
}

// preferCompleteVPGDir returns a sibling postgres_cluster_* directory with
// BACKUP_COMPLETE when path is the matching .tar.gz archive. Directory verify
// avoids stale absolute paths in *.tar.gz.sha256 written under *.partial/.
func preferCompleteVPGDir(path string) string {
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, ".tar.gz") {
		return path
	}
	dirCandidate := path[:len(path)-len(".tar.gz")]
	if _, err := os.Stat(filepath.Join(dirCandidate, "BACKUP_COMPLETE")); err == nil {
		return dirCandidate
	}
	return path
}

// relativizeVPGChecksumPaths rewrites checksum files under dir so listed paths
// are basenames (portable after moving/renaming the backup tree).
func relativizeVPGChecksumPaths(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	for _, e := range entries {
		name := e.Name()
		abs := filepath.Join(dir, name)
		if e.IsDir() {
			if err := relativizeVPGChecksumPaths(abs); err != nil {
				return err
			}
			continue
		}
		if name == "SHA256SUMS" || strings.HasSuffix(name, ".sha256") {
			if err := relativizeChecksumFile(abs); err != nil {
				return err
			}
		}
	}
	return nil
}

func relativizeChecksumFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	lines := strings.Split(string(raw), "\n")
	changed := false
	for i, line := range lines {
		trim := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		hash, rest, ok := strings.Cut(trim, " ")
		if !ok || hash == "" {
			continue
		}
		rest = strings.TrimSpace(rest)
		binary := false
		if strings.HasPrefix(rest, "*") {
			binary = true
			rest = rest[1:]
		}
		slash := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rest)))
		if slash == "." || slash == ".." || slash == "" {
			continue
		}
		// Already a portable basename.
		if !strings.Contains(slash, "/") {
			continue
		}
		base := filepath.Base(filepath.FromSlash(slash))
		if binary {
			lines[i] = hash + " *" + base
		} else {
			lines[i] = hash + "  " + base
		}
		changed = true
	}
	if !changed {
		return nil
	}
	out := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return exitcode.Wrap(exitcode.BackupRestore, err)
	}
	return nil
}

// Exported helpers for tests (keep production API small).
func BackupArgsForTest(o BackupOpts) []string {
	return backupArgs(o)
}

func RestoreArgsForTest(backupPath, container string, force bool) []string {
	return restoreArgs(backupPath, container, force)
}

func VerifyArgsForTest(backupPath string) []string {
	return verifyArgs(backupPath)
}

func IsVPGPartForTest(p Part) bool {
	return isVPGPart(p)
}

func ChecksumDirForTest(dir string) (string, error) {
	return checksumDir(dir)
}

func ParseDotEnvForTest(path string) (map[string]string, error) {
	return parseDotEnv(path)
}

func PreferCompleteVPGDirForTest(path string) string {
	return preferCompleteVPGDir(path)
}

func RelativizeVPGChecksumPathsForTest(dir string) error {
	return relativizeVPGChecksumPaths(dir)
}
