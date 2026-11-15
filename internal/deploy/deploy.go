package deploy

import (
	"net/http"

	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/manifest"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/qabas1/qabas1-cli/internal/state"
)

type Action string

const (
	ActionInstall   Action = "install"
	ActionDeploy    Action = "deploy"
	ActionUpgrade   Action = "upgrade"
	ActionDowngrade Action = "downgrade"
	ActionRollback  Action = "rollback"
)

type Options struct {
	Action         Action
	Spec           string
	Channel        string
	Service        string // product component for scoped deploy: api|app|dash|siraj
	AllowDowngrade bool
	Force          bool
	DryRun         bool
	CheckOnly      bool
	NoBackup       bool
	NonInteractive bool
}

type Plan struct {
	Action         Action            `json:"action"`
	From           string            `json:"from,omitempty"`
	To             string            `json:"to"`
	Channel        string            `json:"channel"`
	Service        string            `json:"service,omitempty"`
	ImageSelector  string            `json:"imageSelector,omitempty"`
	BackupRequired bool              `json:"backupRequired"`
	LeavingDev     bool              `json:"leavingDev,omitempty"`
	Steps          []string          `json:"steps"`
	API            Change            `json:"api"`
	App            Change            `json:"app"`
	Dash           Change            `json:"dash"`
	Siraj          Change            `json:"siraj"`
	Target         *manifest.Release `json:"target"`
	Blocked        string            `json:"blocked,omitempty"`
	// PullAll means PULL runs unfiltered compose pull (first install / force / no current manifest).
	PullAll bool `json:"pullAll,omitempty"`
	// PullServices lists compose services to pull when PullAll is false. Empty skips docker pull.
	PullServices []string `json:"pullServices,omitempty"`
}

type Change struct {
	From string `json:"from,omitempty"`
	To   string `json:"to"`
}

type Engine struct {
	Paths                paths.Paths
	Config               config.Config
	State                state.State
	Client               *release.Client
	Runtime              *compose.Runtime
	Out                  *output.Printer
	DevResolver          func(httpClient *http.Client, owner, username, token string) (*manifest.Release, error)
	DevComponentResolver func(httpClient *http.Client, owner, service, username, token string) (manifest.Component, error)
	ComponentResolver    func(httpClient *http.Client, owner, service, spec, username, token string) (manifest.Component, error)
	BeforeSwitch         func() error
	// LogRotation reports whether container log rotation should be turned on
	// when not yet enabled (nil: only keep an existing overlay in sync).
	LogRotation func() bool
	// ContainerRAM returns the RAM available to containers; when set, the
	// resource-limits overlay is generated (nil: only keep an existing one in sync).
	ContainerRAM func() uint64
}

func New(p paths.Paths, cfg config.Config, st state.State, out *output.Printer) *Engine {
	return &Engine{
		Paths:   p,
		Config:  cfg,
		State:   st,
		Client:  release.New(cfg.Deploy.Repo, cfg.Deploy.Ref),
		Runtime: compose.NewRuntime(),
		Out:     out,
	}
}
