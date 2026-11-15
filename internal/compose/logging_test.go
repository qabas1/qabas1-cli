package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/nodestack"
	"github.com/qabas1/qabas1-cli/internal/paths"
)

func TestSyncLoggingOverlay(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	dir := p.ComposeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("compose.yaml", "services:\n  api:\n    image: a\n  redis:\n    image: r\n  old:\n    image: o\n  custom:\n    image: c\n    logging:\n      driver: none\n")
	write("compose.yaml.next", "services:\n  api:\n    image: a\n  redis:\n    image: r\n  new:\n    image: n\n  custom:\n    image: c\n")
	write("compose.pitr.yaml", "services:\n  wal-archiver:\n    image: w\n")
	if err := nodestack.AddDirective(dir, "file:compose.pitr.yaml"); err != nil {
		t.Fatal(err)
	}

	changed, err := SyncLoggingOverlay(p, p.ComposeFile(), p.ComposeFileNext())
	if err != nil || !changed {
		t.Fatalf("sync: changed=%v err=%v", changed, err)
	}
	b, _ := os.ReadFile(LoggingOverlayPath(p))
	got := string(b)
	for _, want := range []string{"  api:\n", "  redis:\n", "  wal-archiver:\n", `max-size: "50m"`, `max-file: "5"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("overlay missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"  old:", "  new:", "  custom:"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("overlay must not include %q:\n%s", unwanted, got)
		}
	}
	files := nodestack.ReadDirectives(dir).Files
	if filepath.Base(files[len(files)-1]) != LoggingOverlay {
		t.Fatalf("directive not added: %v", files)
	}
	if changed, err := SyncLoggingOverlay(p, p.ComposeFile(), p.ComposeFileNext()); err != nil || changed {
		t.Fatalf("second sync should be a no-op: %v %v", changed, err)
	}

	if err := DisableLoggingOverlay(p); err != nil || LoggingOverlayEnabled(p) {
		t.Fatalf("disable: %v", err)
	}
	args := NewRuntime().composeArgs(p)
	if strings.Contains(strings.Join(args, " "), LoggingOverlay) {
		t.Fatalf("missing overlay must not be passed to compose: %v", args)
	}
}

func TestRefreshOverlaysSkipsLoggingOverlay(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir()}
	if err := os.MkdirAll(p.ComposeDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(LoggingOverlayPath(p), RenderLoggingOverlay([]string{"api"}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := nodestack.AddDirective(p.ComposeDir(), "file:"+LoggingOverlay); err != nil {
		t.Fatal(err)
	}
	err := RefreshOverlays(p, func(name string) ([]byte, error) {
		t.Fatalf("fetched %s from the deploy repo", name)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
