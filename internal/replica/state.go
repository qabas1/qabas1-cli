package replica

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

// Phase is one resumable provisioning step.
type Phase string

const (
	PhasePreflight Phase = "PREFLIGHT"
	PhaseBootstrap Phase = "BOOTSTRAP"
	PhaseNetwork   Phase = "NETWORK"
	PhaseUpstream  Phase = "UPSTREAM"
	PhaseStack     Phase = "STACK"
	PhaseSeed      Phase = "SEED"
	PhaseStorage   Phase = "STORAGE"
	PhaseStart     Phase = "START"
	PhaseVerify    Phase = "VERIFY"
	PhaseReady     Phase = "READY"

	// PhaseInvited marks a node registered by `qabas replica invite` whose join script
	// has not completed provisioning yet (not part of Phases).
	PhaseInvited Phase = "INVITED"
)

// Phases in execution order. PREFLIGHT is read-only on the replica; BOOTSTRAP only
// changes the replica (packages, Docker, qabas CLI, credentials).
var Phases = []Phase{PhasePreflight, PhaseBootstrap, PhaseNetwork, PhaseUpstream, PhaseStack, PhaseSeed, PhaseStorage, PhaseStart, PhaseVerify, PhaseReady}

// State is persisted after every phase so `--resume` continues where it stopped.
type State struct {
	Node      string    `json:"node"`
	Phase     Phase     `json:"phase"`
	Completed []Phase   `json:"completed"`
	StartedAt time.Time `json:"startedAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	LastError string    `json:"lastError,omitempty"`
	// SeedOwned: the data volume is ours to wipe (fresh node or --rebuild).
	SeedOwned bool `json:"seedOwned"`
	// InviteExpires is set while an invited node has not started provisioning.
	InviteExpires *time.Time `json:"inviteExpires,omitempty"`
	// TempWGKey: the node runs the writer-generated invite key; NETWORK rotates it.
	TempWGKey bool `json:"tempWGKey,omitempty"`
}

func (s *State) Done(p Phase) bool {
	for _, c := range s.Completed {
		if c == p {
			return true
		}
	}
	return false
}

func (s *State) mark(p Phase, now time.Time) {
	if !s.Done(p) {
		s.Completed = append(s.Completed, p)
	}
	s.Phase, s.UpdatedAt, s.LastError = p, now, ""
}

func StatePath(p paths.Paths, node string) string {
	return filepath.Join(p.StateDir, "replicas", node, "state.json")
}

func LoadState(p paths.Paths, node string) (*State, error) {
	b, err := os.ReadFile(StatePath(p, node))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	return &s, json.Unmarshal(b, &s)
}

func SaveState(p paths.Paths, s *State) error {
	fp := StatePath(p, s.Node)
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	tmp := fp + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, fp)
}

func RemoveState(p paths.Paths, node string) error {
	return os.RemoveAll(filepath.Dir(StatePath(p, node)))
}

func osMode(m uint32) os.FileMode { return os.FileMode(m) }
