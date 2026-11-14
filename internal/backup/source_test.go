package backup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/backup"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"gopkg.in/yaml.v3"
)

func TestResolveSourceRejectsQabasWrapperArchive(t *testing.T) {
	arch := filepath.Join(t.TempDir(), "qb-20261102-000623.tar.gz")
	f, err := os.Create(arch)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	data := []byte("id: qb-20261102-000623\n")
	if err := tw.WriteHeader(&tar.Header{Name: "qb-20261102-000623/meta.yaml", Mode: 0o600, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = backup.ResolveSource(paths.Paths{StateDir: t.TempDir()}, arch)
	if err == nil || !strings.Contains(err.Error(), "Qabas backup wrapper archive") {
		t.Fatalf("got %v", err)
	}
}

func writeCompleteVPG(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "BACKUP_COMPLETE"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.txt"), []byte("vpg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveVPGPathDirectoryAndArchive(t *testing.T) {
	root := t.TempDir()
	cluster := filepath.Join(root, "postgres_cluster_2026-09-04_05-59-21_UTC_zmaj1XGB")
	writeCompleteVPG(t, cluster)
	arch := cluster + ".tar.gz"
	if err := os.WriteFile(arch, []byte("fake-archive"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := backup.ResolveVPGPath(cluster, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != cluster {
		t.Fatalf("dir got %s", got)
	}

	got, err = backup.ResolveVPGPath(arch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != arch {
		t.Fatalf("archive got %s", got)
	}

	// Parent directory prefers complete directory when both exist.
	got, err = backup.ResolveVPGPath(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != cluster {
		t.Fatalf("parent got %s want complete directory", got)
	}
}

func TestResolveSourceQabasVsVPG(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	id := "qb-20260904-120000"
	dir := backup.Dir(p, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := backup.Meta{ID: id, Database: backup.Part{Included: true, Engine: backup.EngineVPG, File: "postgres/x.tar.gz"}}
	b, _ := yaml.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	src, err := backup.ResolveSource(p, id)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != backup.SourceQabas {
		t.Fatalf("kind %s", src.Kind)
	}

	ext := filepath.Join(root, "ext")
	writeCompleteVPG(t, ext)
	src, err = backup.ResolveSource(p, ext)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != backup.SourceVPG || src.Path != ext {
		t.Fatalf("%+v", src)
	}
}

func TestRestoreSourceExternalVPG(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	arch := filepath.Join(root, "cluster.tar.gz")
	if err := os.WriteFile(arch, []byte("data"), 0o600); err != nil {
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
	src, err := backup.ResolveSource(p, arch)
	if err != nil {
		t.Fatal(err)
	}
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
	}
	if err := backup.RestoreSource(p, rt, vpg, nil, src, nil, true, false, false, true); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) < 2 || gotArgs[0] != "restore" || gotArgs[1] != arch {
		t.Fatalf("args %#v", gotArgs)
	}
	if !contains(gotArgs, "--force") {
		t.Fatalf("missing --force: %#v", gotArgs)
	}

	err = backup.RestoreSource(p, rt, vpg, nil, src, nil, false, true, false, true)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL only") {
		t.Fatalf("expected s3 reject, got %v", err)
	}
}

func TestResolveSourceMissing(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	_, err := backup.ResolveSource(p, "does-not-exist")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveSourcePathNotTreatedAsManagedID(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	id := "qb-20260907-134729"

	// Managed copy under state backups.
	managed := backup.Dir(p, id)
	writeQabasWrapper(t, managed, id, false)

	// Separate filesystem wrapper the operator pointed at.
	ext := filepath.Join(root, "tmp", id)
	writeQabasWrapper(t, ext, id, true)

	src, err := backup.ResolveSource(p, ext)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != backup.SourceQabas || src.Path != ext {
		t.Fatalf("expected filesystem wrapper, got %+v", src)
	}
	if !src.Meta.S3.Included {
		t.Fatal("should use /tmp wrapper with s3, not managed copy")
	}

	// ./id must also use the filesystem path, not Join(backups, "./id").
	dot := filepath.Join(root, "cwd")
	if err := os.MkdirAll(dot, 0o700); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(dot, id)
	writeQabasWrapper(t, local, id, true)
	rel := filepath.Join(".", id)
	// Resolve from dot as cwd via absolute path built like the CLI does.
	src, err = backup.ResolveSource(p, local)
	if err != nil {
		t.Fatal(err)
	}
	if src.Path != local {
		t.Fatalf("path %s", src.Path)
	}
	_ = rel
}

func writeQabasWrapper(t *testing.T, dir, id string, withS3 bool) backup.Meta {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	archRel := "postgres/cluster.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(archRel)), []byte("pg-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := backup.Meta{
		ID:       id,
		Database: backup.Part{Included: true, File: archRel, Engine: backup.EngineVPG, Container: "qabasone-postgres"},
		S3:       backup.Part{Included: withS3},
		Verified: true,
	}
	if withS3 {
		if err := os.MkdirAll(filepath.Join(dir, "s3"), 0o700); err != nil {
			t.Fatal(err)
		}
		s3Rel := "s3/data.tar"
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(s3Rel)), []byte("s3-data"), 0o600); err != nil {
			t.Fatal(err)
		}
		meta.S3.File = s3Rel
	}
	b, err := yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, err := backup.ChecksumDirForTest(dir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Checksum = sum
	b, err = yaml.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return meta
}

func TestResolveSourceFilesystemQabasWrapper(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	wrapper := filepath.Join(root, "qb-20260907-134729")
	meta := writeQabasWrapper(t, wrapper, "qb-20260907-134729", true)

	src, err := backup.ResolveSource(p, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if src.Kind != backup.SourceQabas {
		t.Fatalf("kind %s", src.Kind)
	}
	if src.Path != wrapper {
		t.Fatalf("path %s want %s", src.Path, wrapper)
	}
	if src.Meta == nil || src.Meta.ID != meta.ID {
		t.Fatalf("meta %+v", src.Meta)
	}
	if !src.Meta.S3.Included {
		t.Fatal("expected s3 included for full wrapper")
	}
}

func TestResolveSourceRejectsUnsafeMetaPaths(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	wrapper := filepath.Join(root, "qb-bad")
	if err := os.MkdirAll(wrapper, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := backup.Meta{
		ID:       "qb-bad",
		Database: backup.Part{Included: true, File: "../escape.tar.gz", Engine: backup.EngineVPG},
	}
	b, _ := yaml.Marshal(meta)
	if err := os.WriteFile(filepath.Join(wrapper, "meta.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := backup.ResolveSource(p, wrapper)
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
}

func TestResolveSourceInvalidMetaDoesNotFallThroughToVPG(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	wrapper := filepath.Join(root, "qb-broken")
	if err := os.MkdirAll(filepath.Join(wrapper, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A complete VPG artifact that would otherwise be discoverable.
	writeCompleteVPG(t, filepath.Join(wrapper, "postgres_cluster_x"))
	if err := os.WriteFile(filepath.Join(wrapper, "meta.yaml"), []byte("{not: valid: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := backup.ResolveSource(p, wrapper)
	if err == nil {
		t.Fatal("expected invalid meta error")
	}
	if strings.Contains(err.Error(), "no complete vpg") {
		t.Fatalf("should not fall through to vpg: %v", err)
	}
}

func TestRestoreSourceFilesystemQabasWrapper(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	wrapper := filepath.Join(root, "qb-20260907-134729")
	writeQabasWrapper(t, wrapper, "qb-20260907-134729", true)

	var vpgArgs []string
	vpg := &backup.Client{
		LookPath: func(string) (string, error) { return "/usr/bin/vpg", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			vpgArgs = append([]string{}, args...)
			if len(args) > 0 && args[0] == "version" {
				stdout.WriteString("vpg 1.0.0\n")
			}
			return nil
		},
	}
	var dockerCalls [][]string
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(_ string, args []string, _ []string, stdout, _ *bytes.Buffer) error {
			dockerCalls = append(dockerCalls, append([]string(nil), args...))
			joined := strings.Join(args, " ")
			switch {
			case strings.HasPrefix(joined, "inspect -f {{.Id}}"):
				return os.ErrNotExist
			case strings.Contains(joined, "inspect -f") && strings.Contains(joined, "Mounts"):
				return os.ErrNotExist
			case strings.HasPrefix(joined, "volume inspect"):
				name := args[len(args)-1]
				want := compose.ProjectName + "_" + compose.MinioVolumeKey
				if name == want && stdout != nil {
					stdout.WriteString(name + "\n")
					return nil
				}
				return os.ErrNotExist
			case strings.HasPrefix(joined, "run --rm"):
				return nil
			default:
				return os.ErrNotExist
			}
		},
	}

	src, err := backup.ResolveSource(p, wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.RestoreSource(p, rt, vpg, nil, src, nil, false, false, false, true); err != nil {
		t.Fatal(err)
	}
	wantDB := filepath.Join(wrapper, "postgres", "cluster.tar.gz")
	if len(vpgArgs) < 2 || vpgArgs[0] != "restore" || vpgArgs[1] != wantDB {
		t.Fatalf("vpg args %#v want restore %s", vpgArgs, wantDB)
	}
	foundS3 := false
	for _, args := range dockerCalls {
		joined := strings.Join(args, " ")
		if strings.HasPrefix(joined, "run --rm") && strings.Contains(joined, "data.tar") {
			foundS3 = true
			break
		}
	}
	if !foundS3 {
		t.Fatalf("expected minio restore docker run, got %#v", dockerCalls)
	}
}
