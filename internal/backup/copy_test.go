package backup_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/exitcode"
)

func writeBackupTree(t *testing.T, root, id string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "postgres"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.yaml"), []byte("id: "+id+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "postgres", "data.tar.gz"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertCopied(t *testing.T, dest string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dest, "meta.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty meta.yaml")
	}
	b, err = os.ReadFile(filepath.Join(dest, "postgres", "data.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "payload" {
		t.Fatalf("got %q", b)
	}
}

func TestCopyIntoExistingDir(t *testing.T) {
	root := t.TempDir()
	id := "qb-20260915-120000"
	src := writeBackupTree(t, root, id)
	destDir := filepath.Join(root, "exports")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	final, err := backup.Copy(src, destDir, false, false)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(destDir, id)
	if final != want {
		t.Fatalf("got %s want %s", final, want)
	}
	assertCopied(t, final)
	// Source intact
	assertCopied(t, src)
}

func TestCopyExactNewPath(t *testing.T) {
	root := t.TempDir()
	id := "qb-20260915-120000"
	src := writeBackupTree(t, root, id)
	dest := filepath.Join(root, "offsite", "my-copy")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}

	final, err := backup.Copy(src, dest, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if final != dest {
		t.Fatalf("got %s want %s", final, dest)
	}
	assertCopied(t, final)
}

func TestCopyRefuseOverwrite(t *testing.T) {
	root := t.TempDir()
	id := "qb-20260915-120000"
	src := writeBackupTree(t, root, id)
	destDir := filepath.Join(root, "exports")
	if err := os.MkdirAll(filepath.Join(destDir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destDir, id, "keep.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := backup.Copy(src, destDir, false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
	b, err := os.ReadFile(filepath.Join(destDir, id, "keep.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "old" {
		t.Fatalf("overwrite without force: %q", b)
	}
}

func TestCopyForceOverwrite(t *testing.T) {
	root := t.TempDir()
	id := "qb-20260915-120000"
	src := writeBackupTree(t, root, id)
	destDir := filepath.Join(root, "exports")
	target := filepath.Join(destDir, id)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	final, err := backup.Copy(src, destDir, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if final != target {
		t.Fatalf("got %s want %s", final, target)
	}
	assertCopied(t, final)
	if _, err := os.Stat(filepath.Join(final, "keep.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected old file removed, got %v", err)
	}
}

func TestCopyDryRunNoWrite(t *testing.T) {
	root := t.TempDir()
	id := "qb-20260915-120000"
	src := writeBackupTree(t, root, id)
	destDir := filepath.Join(root, "exports")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}

	final, err := backup.Copy(src, destDir, false, true)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(destDir, id)
	if final != want {
		t.Fatalf("got %s want %s", final, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote destination: %v", err)
	}
}

func TestCopyMissingParent(t *testing.T) {
	root := t.TempDir()
	src := writeBackupTree(t, root, "qb-20260915-120000")
	dest := filepath.Join(root, "missing", "child", "copy")

	_, err := backup.Copy(src, dest, false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.InvalidInput {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.InvalidInput)
	}
}

func TestCopySourceMissing(t *testing.T) {
	root := t.TempDir()
	_, err := backup.Copy(filepath.Join(root, "nope"), filepath.Join(root, "out"), false, false)
	if err == nil {
		t.Fatal("expected error")
	}
	if exitcode.CodeOf(err) != exitcode.BackupRestore {
		t.Fatalf("code=%d want %d", exitcode.CodeOf(err), exitcode.BackupRestore)
	}
}
