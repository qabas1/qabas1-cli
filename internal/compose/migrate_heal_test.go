package compose

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/paths"
)

func TestMigrateWithHealResolvesAlreadyExistsThenRetries(t *testing.T) {
	p := paths.Paths{StateDir: t.TempDir(), ConfigDir: t.TempDir()}
	if err := p.EnsureRuntime(); err != nil {
		t.Fatal(err)
	}
	var calls []string
	deployCount := 0
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		RunIO: func(name string, args []string, env []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error {
			joined := strings.Join(args, " ")
			calls = append(calls, joined)
			switch {
			case strings.Contains(joined, "migrate resolve --applied 20260912040000_workday_open_per_party_unique"):
				return nil
			case strings.Contains(joined, "run --rm --remove-orphans migrate") && !strings.Contains(joined, "--entrypoint"):
				deployCount++
				if deployCount == 1 {
					msg := "Error: P3018\nMigration name: 20260912040000_workday_open_per_party_unique\nERROR: relation \"x\" already exists\n"
					_, _ = io.WriteString(stderr, msg)
					return errors.New("exit 1")
				}
				return nil
			default:
				t.Fatalf("unexpected docker args: %v", args)
				return nil
			}
		},
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			t.Fatalf("unexpected Run: %v", args)
			return nil
		},
	}

	if err := rt.MigrateWithHeal(p, nil); err != nil {
		t.Fatalf("MigrateWithHeal: %v", err)
	}
	if deployCount != 2 {
		t.Fatalf("deployCount=%d want 2; calls=%#v", deployCount, calls)
	}
}
