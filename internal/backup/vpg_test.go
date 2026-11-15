package backup_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/state"
	"gopkg.in/yaml.v3"
)

func TestBackupArgs(t *testing.T) {
	args := backup.BackupArgsForTest(backup.BackupOpts{
		OutputDir:            "/srv/backups/postgres",
		Container:            "qabas1-postgres",
		Username:             "qabasone",
		MaintenanceDB:        "qabasone",
		Archive:              true,
		IncludeRolePasswords: true,
	})
	want := []string{
		"backup", "/srv/backups/postgres",
		"--container", "qabas1-postgres",
		"--username", "qabasone",
		"--maintenance-db", "qabasone",
		"--password-stdin",
		"--archive",
		"--include-role-passwords",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("got %#v want %#v", args, want)
	}
}

func TestRestoreArgsForce(t *testing.T) {
	args := backup.RestoreArgsForTest("./backup.tar.gz", "qabas1-postgres", true)
	if !contains(args, "--force") {
		t.Fatalf("expected --force in %#v", args)
	}
	argsNo := backup.RestoreArgsForTest("./backup.tar.gz", "qabas1-postgres", false)
	if contains(argsNo, "--force") {
		t.Fatalf("unexpected --force in %#v", argsNo)
	}
}

func TestVerifyArgs(t *testing.T) {
	args := backup.VerifyArgsForTest("/path/to/backup")
	if len(args) != 2 || args[0] != "verify" || args[1] != "/path/to/backup" {
		t.Fatalf("got %#v", args)
	}
}

func TestClientRequireMissing(t *testing.T) {
	c := &backup.Client{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	err := c.Backup(backup.BackupOpts{
		OutputDir: "/tmp/out",
		Container: "qabas1-postgres",
		Username:  "qabasone",
		Password:  "x",
		Archive:   true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.BackupRestore {
		t.Fatalf("code %d", exitcode.CodeOf(err))
	}
	if hint := exitcode.HintOf(err); hint == "" || !strings.Contains(hint, "voidtools") {
		t.Fatalf("hint %q", hint)
	}
}

func TestResolvePostgresTargetFromEnv(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(root, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.SecretsFile(), []byte("POSTGRES_PASSWORD=from-secrets\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.EnvCompose(), []byte("POSTGRES_USER=appuser\nPOSTGRES_DB=appdb\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := backup.ResolvePostgresTarget(p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "appuser" || got.MaintenanceDB != "appdb" || got.Password != "from-secrets" {
		t.Fatalf("got %+v", got)
	}
	if got.Container != backup.DefaultContainer {
		t.Fatalf("container %q", got.Container)
	}
}

func TestResolvePostgresTargetFromURI(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(root, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	uri := "POSTGRES_URI=postgresql://uriuser:uripass@postgres:5432/uridb\n"
	if err := os.WriteFile(p.EnvMigrate(), []byte(uri), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := backup.ResolvePostgresTarget(p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "uriuser" || got.Password != "uripass" || got.MaintenanceDB != "uridb" {
		t.Fatalf("got %+v", got)
	}
}

func TestResolvePostgresTargetMissingPassword(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{ConfigDir: filepath.Join(root, "etc")}
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := backup.ResolvePostgresTarget(p, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	body := "# comment\nexport POSTGRES_USER=qabasone\nPOSTGRES_DB=\"mydb\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := backup.ParseDotEnvForTest(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["POSTGRES_USER"] != "qabasone" || m["POSTGRES_DB"] != "mydb" {
		t.Fatalf("%v", m)
	}
}

func TestRestoreWarning(t *testing.T) {
	msg := backup.RestoreWarning(true, true)
	if !strings.Contains(msg, "DESTRUCTIVE") || !strings.Contains(msg, "FULL PostgreSQL") {
		t.Fatalf("%s", msg)
	}
	if !strings.Contains(backup.ScopeHelp, "Not included") {
		t.Fatal("scope help missing exclusions")
	}
}

func fakeVPGClient(t *testing.T, onBackup func(args []string, stdin string)) *backup.Client {
	t.Helper()
	return &backup.Client{
		LookPath: func(string) (string, error) { return "/usr/bin/vpg", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			// Preflight: vpg version
			if len(args) > 0 && args[0] == "version" {
				stdout.WriteString("vpg 1.0.0\n")
				return nil
			}
			if len(args) > 0 && args[0] == "verify" {
				return nil
			}
			return nil
		},
		RunIO: func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
			in := ""
			if stdin != nil {
				b, _ := io.ReadAll(stdin)
				in = string(b)
			}
			if onBackup != nil {
				onBackup(args, in)
			}
			if len(args) > 0 && args[0] == "backup" {
				outDir := args[1]
				id := "postgres_cluster_2026-09-03_12-00-00_UTC_abcd1234"
				cluster := filepath.Join(outDir, id)
				if err := os.MkdirAll(cluster, 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(cluster, "BACKUP_COMPLETE"), []byte("ok\n"), 0o600); err != nil {
					return err
				}
				arch := filepath.Join(outDir, id+".tar.gz")
				if err := os.WriteFile(arch, []byte("fake"), 0o600); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func TestCreateWithVPGWritesMeta(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{
		ConfigDir: filepath.Join(root, "etc"),
		StateDir:  filepath.Join(root, "lib"),
		LogDir:    filepath.Join(root, "log"),
		LockPath:  filepath.Join(root, "qabas.lock"),
	}
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.Backups(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.SecretsFile(), []byte("POSTGRES_PASSWORD=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.EnvCompose(), []byte("POSTGRES_USER=qabasone\nPOSTGRES_DB=qabasone\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var ran []string
	var stdin string
	vpg := fakeVPGClient(t, func(args []string, in string) {
		ran = args
		stdin = in
	})

	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			// Allow compose create/start used to prepare postgres when stack is down.
			if strings.Contains(joined, " create ") || strings.HasSuffix(joined, " create postgres") ||
				strings.Contains(joined, " start ") || strings.HasSuffix(joined, " start postgres") {
				return nil
			}
			// ServiceStatus / ps would fail; Create falls back to default container.
			return os.ErrNotExist
		},
	}
	st := state.State{CurrentRelease: "1.2.3"}
	meta, err := backup.CreateWith(p, rt, vpg, st, nil, true, false, "nightly", false)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Database.Engine != backup.EngineVPG {
		t.Fatalf("engine %q", meta.Database.Engine)
	}
	if !strings.HasPrefix(meta.Database.File, "postgres/") || strings.HasSuffix(meta.Database.File, ".tar.gz") {
		t.Fatalf("file %q (want complete cluster dir, not archive)", meta.Database.File)
	}
	if !contains(ran, "--archive") || !contains(ran, "--include-role-passwords") || !contains(ran, "--password-stdin") {
		t.Fatalf("vpg args %#v", ran)
	}
	if !strings.HasPrefix(stdin, "secret") {
		t.Fatalf("stdin password %q", stdin)
	}
	if meta.Database.Container != backup.DefaultContainer {
		t.Fatalf("container %q", meta.Database.Container)
	}
	if meta.Name != "nightly" {
		t.Fatalf("name %q", meta.Name)
	}

	info, err := os.Stat(backup.Dir(p, meta.ID))
	if err != nil {
		t.Fatal(err)
	}
	if testingOnUnix() && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("backup dir perms too open: %o", info.Mode().Perm())
	}
}

func testingOnUnix() bool {
	return os.PathSeparator == '/'
}

func TestMetaRoundTripEngine(t *testing.T) {
	dir := t.TempDir()
	m := backup.Meta{
		ID:              "qb-20260903-120000",
		QabasOneRelease: "1.0.0",
		CreatedAt:       time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
		Database:        backup.Part{Included: true, File: "postgres/x.tar.gz", Engine: backup.EngineVPG, Container: "qabas1-postgres"},
		S3:              backup.Part{Included: false},
		Verified:        true,
		Checksum:        "sha256:abc",
	}
	b, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o640); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var got backup.Meta
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Database.Engine != backup.EngineVPG || got.Database.File != "postgres/x.tar.gz" || got.Database.Container != "qabas1-postgres" {
		t.Fatalf("got %+v", got.Database)
	}

	legacy := backup.Part{Included: true, File: "postgres.dump"}
	if backup.IsVPGPartForTest(legacy) {
		t.Fatal("legacy dump should not be vpg")
	}
	if !backup.IsVPGPartForTest(got.Database) {
		t.Fatal("expected vpg part")
	}
}

func TestRestoreWithVPGCallsForce(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	id := "qb-20260903-120000"
	dir := backup.Dir(p, id)
	pg := filepath.Join(dir, "postgres")
	if err := os.MkdirAll(pg, 0o700); err != nil {
		t.Fatal(err)
	}
	archRel := "postgres/cluster.tar.gz"
	archAbs := filepath.Join(dir, filepath.FromSlash(archRel))
	if err := os.WriteFile(archAbs, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := backup.Meta{
		ID:       id,
		Database: backup.Part{Included: true, File: archRel, Engine: backup.EngineVPG, Container: "qabas1-postgres"},
		S3:       backup.Part{Included: false},
		Verified: true,
	}
	b, _ := yaml.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o640); err != nil {
		t.Fatal(err)
	}
	sum, err := backup.ChecksumDirForTest(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Checksum = sum
	b, _ = yaml.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o640); err != nil {
		t.Fatal(err)
	}

	var gotArgs []string
	vpg := &backup.Client{
		LookPath: func(string) (string, error) { return "/usr/bin/vpg", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			gotArgs = args
			if len(args) > 0 && args[0] == "version" {
				stdout.WriteString("vpg 1.0.0\n")
			}
			return nil
		},
	}
	rt := &compose.Runtime{}
	if err := backup.RestoreWith(p, rt, vpg, nil, id, false, false, false, true); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "restore" {
		t.Fatalf("args %#v", gotArgs)
	}
	if !contains(gotArgs, "--force") || !contains(gotArgs, "qabas1-postgres") {
		t.Fatalf("args %#v", gotArgs)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestPreferCompleteVPGDir(t *testing.T) {
	root := t.TempDir()
	cluster := filepath.Join(root, "postgres_cluster_x")
	if err := os.MkdirAll(cluster, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cluster, "BACKUP_COMPLETE"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arch := cluster + ".tar.gz"
	if err := os.WriteFile(arch, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := backup.PreferCompleteVPGDirForTest(arch); got != cluster {
		t.Fatalf("got %s want %s", got, cluster)
	}
	lonely := filepath.Join(root, "only.tar.gz")
	if err := os.WriteFile(lonely, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := backup.PreferCompleteVPGDirForTest(lonely); got != lonely {
		t.Fatalf("got %s", got)
	}
}

func TestRelativizeVPGChecksumPaths(t *testing.T) {
	root := t.TempDir()
	side := filepath.Join(root, "cluster.tar.gz.sha256")
	abs := filepath.Join(root, "qb-x.partial", "postgres", "cluster.tar.gz")
	content := "abc123  " + abs + "\n"
	if err := os.WriteFile(side, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.RelativizeVPGChecksumPathsForTest(root); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(side)
	if err != nil {
		t.Fatal(err)
	}
	if want := "abc123  cluster.tar.gz\n"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
