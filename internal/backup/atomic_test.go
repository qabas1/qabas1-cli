package backup_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qabasone/qabasone-cli/internal/backup"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/paths"
	"github.com/qabasone/qabasone-cli/internal/state"
)

func TestCreateFailureRemovesStaging(t *testing.T) {
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

	vpg := &backup.Client{
		LookPath: func(string) (string, error) { return "/usr/bin/vpg", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			if len(args) > 0 && args[0] == "version" {
				stdout.WriteString("vpg 1.0.0\n")
				return nil
			}
			return nil
		},
		RunIO: func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
			return errors.New("vpg backup failed")
		},
	}
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, " create ") || strings.Contains(joined, " start ") {
				return nil
			}
			return os.ErrNotExist
		},
	}

	_, err := backup.CreateWith(p, rt, vpg, state.State{CurrentRelease: "1.0.0"}, nil, true, false, "", false)
	if err == nil {
		t.Fatal("expected error")
	}
	entries, err := os.ReadDir(p.Backups())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Fatalf("staging left behind: %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), "qb-") {
			t.Fatalf("final backup left behind: %s", e.Name())
		}
	}
}

func TestCreateSuccessPromotesOutOfPartial(t *testing.T) {
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

	vpg := fakeVPGClient(t, nil)
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, " create ") || strings.Contains(joined, " start ") {
				return nil
			}
			return os.ErrNotExist
		},
	}
	meta, err := backup.CreateWith(p, rt, vpg, state.State{CurrentRelease: "1.0.0"}, nil, true, false, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(meta.Name, "manual_") {
		t.Fatalf("expected auto name, got %q", meta.Name)
	}
	if _, err := os.Stat(backup.Dir(p, meta.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup.Dir(p, meta.ID) + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backup.Dir(p, meta.ID), "meta.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestSweepPartials(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(root, "lib")}
	if err := os.MkdirAll(p.Backups(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(p.Backups(), "qb-old.partial")
	fresh := filepath.Join(p.Backups(), "qb-fresh.partial")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	if err := backup.SweepPartials(p, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expected old partial removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("expected fresh partial kept")
	}
}
