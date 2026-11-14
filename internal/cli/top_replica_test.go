package cli

import (
	"context"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/paths"
)

func TestTopRecognizesReplicaWithoutInstalledRelease(t *testing.T) {
	setupServiceControlNode(t, nodestack.ProfileReplicaRO)
	e := &env{flags: &flags{}, Paths: paths.Resolve()}

	sources := topSources(e, false, false)
	if sources.PG == nil {
		t.Fatal("PostgreSQL probe is disabled for a replica without release state")
	}
	got := sources.State(context.Background())
	if !got.Probed || !got.Installed || got.Role != string(nodestack.ProfileReplicaRO) {
		t.Fatalf("replica state = %#v", got)
	}
	if got.NotInstalled() {
		t.Fatal("replica was reported as not installed")
	}
}
