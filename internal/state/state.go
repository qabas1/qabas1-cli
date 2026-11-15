package state

import (
	"encoding/json"
	"os"
	"time"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/paths"
)

type State struct {
	CurrentRelease   string    `json:"currentRelease,omitempty"`
	PreviousRelease  string    `json:"previousRelease,omitempty"`
	DeployedAt       time.Time `json:"deployedAt,omitempty"`
	Maintenance      bool      `json:"maintenance"`
	MaintenanceUntil time.Time `json:"maintenanceUntil,omitempty"`
	TargetRelease    string    `json:"targetRelease,omitempty"`
	LastError        string    `json:"lastError,omitempty"`
	Phase            string    `json:"phase,omitempty"`
}

func Load(p paths.Paths) (State, error) {
	var s State
	b, err := os.ReadFile(p.StateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, exitcode.Wrap(exitcode.Generic, err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, exitcode.New(exitcode.Generic, "parse %s: %w", p.StateFile(), err)
	}
	return s, nil
}

func Save(p paths.Paths, s State) error {
	if err := os.MkdirAll(p.StateDir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p.StateFile() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, p.StateFile())
}

func (s State) Installed() bool {
	return s.CurrentRelease != ""
}
