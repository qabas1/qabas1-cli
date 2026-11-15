package deploy

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/history"
	"github.com/qabas1/qabas1-cli/internal/interrupt"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/state"
)

func TestIncompletePhase(t *testing.T) {
	if IncompletePhase("") || IncompletePhase("committed") {
		t.Fatal("empty/committed should be complete")
	}
	if !IncompletePhase("BACKUP") || !IncompletePhase("SWITCH") {
		t.Fatal("mid-deploy phases should be incomplete")
	}
}

func TestIncompleteDeployWarning(t *testing.T) {
	if IncompleteDeployWarning(state.State{}) != "" {
		t.Fatal("not installed")
	}
	if IncompleteDeployWarning(state.State{CurrentRelease: "1.0.0", Phase: "committed"}) != "" {
		t.Fatal("committed")
	}
	w := IncompleteDeployWarning(state.State{CurrentRelease: "1.0.0", Phase: "SWITCH", TargetRelease: "1.0.1"})
	if !strings.Contains(w, "phase=SWITCH") || !strings.Contains(w, "target=1.0.1") {
		t.Fatalf("warn: %s", w)
	}
}

func TestAbortIfInterruptedRecordsHistory(t *testing.T) {
	interrupt.ResetForTest()
	t.Cleanup(interrupt.ResetForTest)
	interrupt.SetExitForTest(func(int) {})
	interrupt.SetStderrForTest(&bytes.Buffer{})

	dir := t.TempDir()
	p := paths.Paths{
		ConfigDir: filepath.Join(dir, "etc"),
		StateDir:  filepath.Join(dir, "lib"),
		LogDir:    filepath.Join(dir, "log"),
		LockPath:  filepath.Join(dir, "qabas.lock"),
	}
	if err := p.EnsureRuntime(); err != nil {
		t.Fatal(err)
	}
	st := state.State{CurrentRelease: "1.0.0", Phase: "BACKUP"}
	if err := state.Save(p, st); err != nil {
		t.Fatal(err)
	}
	e := New(p, config.Default(), st, output.New(false, true))
	plan := &Plan{Action: ActionUpgrade, From: "1.0.0", To: "1.0.1"}

	_ = interrupt.Protect("BACKUP", func() error {
		interrupt.SimulateSignalForTest()
		return nil
	})
	err := e.abortIfInterrupted(Options{Action: ActionUpgrade}, plan, "qb-test", "BACKUP")
	if err == nil {
		t.Fatal("expected abort")
	}
	loaded, err := state.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TargetRelease != "1.0.1" {
		t.Fatalf("target %q", loaded.TargetRelease)
	}
	if !strings.Contains(loaded.LastError, "aborted after BACKUP") {
		t.Fatalf("lastError %q", loaded.LastError)
	}
	entries, err := history.List(p)
	if err != nil || len(entries) == 0 {
		t.Fatalf("history: %v %d", err, len(entries))
	}
	last := entries[len(entries)-1]
	if last.Status != "aborted" || last.Backup != "qb-test" {
		t.Fatalf("history entry %#v", last)
	}
}

func TestComposeStagingPromote(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{StateDir: filepath.Join(dir, "lib")}
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	live := []byte("services: {old: {}}\n")
	next := []byte("services: {new: {}}\n")
	if err := os.WriteFile(p.ComposeFile(), live, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := compose.WriteBundleNext(p, next); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p.ComposeFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, live) {
		t.Fatal("live compose should stay unchanged until promote")
	}
	if err := compose.PromoteBundle(p); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(p.ComposeFile())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, next) {
		t.Fatalf("after promote got %q", got)
	}
	if _, err := os.Stat(p.ComposeFileNext()); !os.IsNotExist(err) {
		t.Fatal("next file should be gone after rename")
	}
}
