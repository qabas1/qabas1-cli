package compose

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/manifest"
)

func TestVerifyAPIWorkerEntrypointsOK(t *testing.T) {
	t.Parallel()
	var calls [][]string
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	rel := &manifest.Release{
		Components: manifest.Components{
			API: manifest.Component{
				Image:  "ghcr.io/qabasone/qabasone-api:1.2.3",
				Digest: "sha256:abc",
			},
		},
	}
	if err := rt.VerifyAPIWorkerEntrypoints(rel, nil); err != nil {
		t.Fatalf("VerifyAPIWorkerEntrypoints: %v", err)
	}
	if len(calls) != len(RequiredAPIWorkerEntrypoints) {
		t.Fatalf("calls=%d, want %d", len(calls), len(RequiredAPIWorkerEntrypoints))
	}
	for i, path := range RequiredAPIWorkerEntrypoints {
		got := calls[i]
		wantTail := []string{"run", "--rm", "--entrypoint", "test", "ghcr.io/qabasone/qabasone-api@sha256:abc", "-f", path}
		if len(got) < 2 || got[0] != "docker" {
			t.Fatalf("call %d: %#v", i, got)
		}
		for j, w := range wantTail {
			if got[1+j] != w {
				t.Fatalf("call %d arg %d: got %#v want %#v", i, j, got[1:], wantTail)
			}
		}
	}
}

func TestVerifyAPIWorkerEntrypointsMissing(t *testing.T) {
	t.Parallel()
	rt := &Runtime{
		LookPath: func(string) (string, error) { return "docker", nil },
		Run: func(name string, args []string, env []string, stdout, stderr *bytes.Buffer) error {
			if stderr != nil {
				stderr.WriteString("file not found")
			}
			return errVerifyFail
		},
	}
	rel := &manifest.Release{
		Components: manifest.Components{
			API: manifest.Component{
				Image:  "ghcr.io/qabasone/qabasone-api:1.2.3",
				Digest: "sha256:abc",
			},
		},
	}
	err := rt.VerifyAPIWorkerEntrypoints(rel, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "operations.worker.cli.js") {
		t.Fatalf("error=%v", err)
	}
}

type verifyFail struct{}

func (verifyFail) Error() string { return "exit status 1" }

var errVerifyFail error = verifyFail{}
