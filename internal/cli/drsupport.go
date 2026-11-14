package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/config"
	"github.com/qabasone/qabasone-cli/internal/dr"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/history"
	"github.com/qabasone/qabasone-cli/internal/manifest"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/release"
	"github.com/qabasone/qabasone-cli/internal/replica"
	"github.com/qabasone/qabasone-cli/internal/sshx"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/qabasone/qabasone-cli/internal/version"
)

// EnvSSHIdentity overrides the SSH private key used to reach replicas.
const EnvSSHIdentity = "QABAS_SSH_IDENTITY"

func (e *env) cluster() (*cluster.Cluster, error) {
	c, err := cluster.Load(e.Paths)
	if errors.Is(err, cluster.ErrNotFound) {
		return nil, exitcode.NewHint(exitcode.InvalidInput,
			"create it on the writer with: sudo qabas replica init --name <node> --endpoint <public-host>:51820",
			"no cluster registry (%s)", cluster.File(e.Paths))
	}
	return c, err
}

// selfName is this machine's node name: dr/node.env, then QABAS_NODE, then the primary.
func (e *env) selfName(c *cluster.Cluster) string {
	l := nodestack.LocalLayout(e.Paths.ConfigDir, e.Paths.StateDir)
	if b, err := os.ReadFile(l.NodeEnvFile()); err == nil {
		if v := nodestack.ParseEnv(b)["QABAS_NODE"]; v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(os.Getenv("QABAS_NODE")); v != "" {
		return v
	}
	if p := c.Primary(); p != nil {
		return p.Name
	}
	return ""
}

// installedRelease loads the manifest of the release running on this node.
func (e *env) installedRelease() (*manifest.Release, state.State, error) {
	st, err := state.Load(e.Paths)
	if err != nil {
		return nil, st, err
	}
	if st.CurrentRelease == "" {
		return nil, st, exitcode.New(exitcode.InvalidInput, "QabasOne is not installed on this node")
	}
	b, err := os.ReadFile(filepath.Join(e.Paths.Releases(), st.CurrentRelease+".yaml"))
	if err != nil {
		return nil, st, exitcode.NewHint(exitcode.InvalidInput,
			"run `sudo qabas deploy "+st.CurrentRelease+"` to restore release metadata",
			"release metadata for %s is missing", st.CurrentRelease)
	}
	rel, err := manifest.Parse(b)
	return rel, st, err
}

type engineOpts struct {
	identity string
	// offline: never contact GitHub; compose files must exist locally.
	offline bool
}

// newReplicaEngine wires the replica engine for this node.
func (e *env) newReplicaEngine(c *cluster.Cluster, o engineOpts) (*replica.Engine, error) {
	self := e.selfName(c)
	if self == "" || c.Node(self) == nil {
		return nil, exitcode.NewHint(exitcode.InvalidInput,
			"set QABAS_NODE or run on a registered node",
			"cannot determine which registered node this machine is (got %q)", self)
	}
	identity := o.identity
	if identity == "" {
		identity = os.Getenv(EnvSSHIdentity)
	}
	images := c.Images
	if rel, _, err := e.installedRelease(); err == nil {
		images = compose.ImagePins(rel)
		c.Release = rel.Release.Version
		c.Images = images
	}
	var client *release.Client
	if !o.offline {
		if cfg, err := config.Load(e.Paths); err == nil {
			client = release.New(cfg.Deploy.Repo, cfg.Deploy.Ref)
		}
	}
	composeDir := e.Paths.ComposeDir()
	clusterKey := cluster.SSHKeyPath(e.Paths)
	eng := &replica.Engine{
		Paths:   e.Paths,
		Cluster: c,
		Self:    self,
		Connect: func(ctx context.Context, n cluster.Node) (nodeexec.Executor, error) {
			if n.Name == self {
				return &nodeexec.Local{Name: n.Name}, nil
			}
			dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return nodeexec.DialNode(dctx, n, identity, clusterKey)
		},
		KeyCheck: func(ctx context.Context, n cluster.Node) error {
			auth, err := sshx.KeyFileAuth(clusterKey)
			if err != nil {
				return err
			}
			r, err := nodeexec.DialNodeAuth(ctx, n, auth)
			if err != nil {
				return err
			}
			return r.Close()
		},
		GitHubToken: release.GitHubToken,
		Fetch: func(name string) ([]byte, error) {
			if client != nil {
				if b, err := client.FetchComposeFile(name); err == nil {
					return b, nil
				}
			}
			// Offline (disaster) path: every node carries the bundle it runs.
			b, err := os.ReadFile(filepath.Join(composeDir, name))
			if err != nil {
				return nil, fmt.Errorf("%s: not in %s and qabasone-deploy unreachable", name, composeDir)
			}
			return b, nil
		},
		Images:   images,
		CLI:      &replica.CLIBuild{Version: version.Version, Commit: version.Commit},
		Registry: func() (string, string) { return release.GHCRUsername(), release.GHCRToken() },
		Log:      func(format string, a ...any) { e.Out.Printf(format+"\n", a...) },
		ProbeTCP: func(ctx context.Context, addr string) bool {
			d := net.Dialer{Timeout: 3 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return false
			}
			_ = conn.Close()
			return true
		},
	}
	return eng, nil
}

func (e *env) newDREngine(c *cluster.Cluster, o engineOpts) (*dr.Engine, error) {
	re, err := e.newReplicaEngine(c, o)
	if err != nil {
		return nil, err
	}
	token, _ := cluster.ReadSecret(e.Paths, cluster.SharedScope, cluster.SecretWitnessToken)
	return &dr.Engine{Engine: re, Witnesses: dr.Witnesses(c, token)}, nil
}

// qabasCmd is how systemd units and remote scripts invoke the package-installed
// CLI on every node; systemd resolves bare names in ExecStart (v239+).
const qabasCmd = replica.QabasCmd

func audit(e *env, action, from, to string, err error, msg string) {
	status := "success"
	if err != nil {
		status = "failed"
		msg = err.Error()
	}
	_ = history.Append(e.Paths, history.Entry{Action: action, From: from, To: to, Status: status, Message: msg})
}

func drErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, dr.ErrRefused) || errors.Is(err, replica.ErrNotModified) {
		return exitcode.Wrap(exitcode.InvalidInput, err)
	}
	return wrapErr(err)
}
