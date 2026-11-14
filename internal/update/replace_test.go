package update

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLinkStaleBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need extra privileges on Windows")
	}
	dir := t.TempDir()
	pkg := filepath.Join(dir, "usr-bin-qabas")
	stale := filepath.Join(dir, "usr-local-bin-qabas")
	if err := os.WriteFile(pkg, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if linked, err := LinkStaleBinary(stale, pkg); err != nil || linked {
		t.Fatalf("absent stale: linked=%v err=%v", linked, err)
	}

	if err := os.WriteFile(stale, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if linked, err := LinkStaleBinary(stale, filepath.Join(dir, "missing")); err != nil || linked {
		t.Fatalf("missing package binary must leave stale alone: linked=%v err=%v", linked, err)
	}
	if linked, err := LinkStaleBinary(stale, pkg); err != nil || !linked {
		t.Fatalf("linked=%v err=%v", linked, err)
	}
	if target, err := os.Readlink(stale); err != nil || target != pkg {
		t.Fatalf("stale -> %q (%v), want %q", target, err, pkg)
	}
	if b, _ := os.ReadFile(stale); string(b) != "new" {
		t.Fatalf("stale resolves to %q, want new", b)
	}
	if linked, err := LinkStaleBinary(stale, pkg); err != nil || linked {
		t.Fatalf("already linked: linked=%v err=%v", linked, err)
	}
}

func TestReplaceBinaryWindowsKeepsOldCopy(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "qabas.exe")
	asset := filepath.Join(dir, "asset.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary("windows", asset, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("exe = %q, want new", b)
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old" {
		t.Fatalf("exe.old = %q, want old", b)
	}
	if _, err := os.Stat(exe + ".new"); !os.IsNotExist(err) {
		t.Fatalf("expected .new to be consumed, stat err=%v", err)
	}
}
