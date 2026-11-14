package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

func TestReconcileRestoredDatabaseUsesLocalSecrets(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{ConfigDir: root, StateDir: root}
	if err := os.WriteFile(p.SecretsFile(), []byte("POSTGRES_PASSWORD=local-owner\nPOSTGRES_APP_PASSWORD=local-app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p.ComposeFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ComposeFile(), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sql string
	var calls [][]string
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return filepath.Join(root, "docker"), nil },
		Run: func(_ string, args []string, _ []string, _, _ *bytes.Buffer) error {
			calls = append(calls, append([]string{}, args...))
			return nil
		},
		RunIO: func(_ string, args []string, _ []string, in io.Reader, _, _ io.Writer) error {
			calls = append(calls, append([]string{}, args...))
			b, err := io.ReadAll(in)
			sql = string(b)
			return err
		},
	}
	if err := reconcileRestoredDatabase(p, rt, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "ALTER ROLE CURRENT_USER WITH PASSWORD 'local-owner'") ||
		!strings.Contains(sql, "ALTER ROLE qabasone_app WITH PASSWORD 'local-app'") {
		t.Fatalf("unexpected reconciliation SQL")
	}
	if len(calls) != 2 || !strings.Contains(strings.Join(calls[1], " "), "restart") {
		t.Fatalf("expected database update then service restart, got %v", calls)
	}
}
