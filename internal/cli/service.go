package cli

import (
	"os"
	"strings"

	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/nodestack"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/spf13/cobra"
)

func newUpCmd(f *flags) *cobra.Command {
	var detach, build bool
	cmd := &cobra.Command{
		Use:     "up [service]",
		Aliases: []string{"start-all"},
		Short:   "Start QabasOne services",
		Long:    "Create or start all QabasOne services, or one service, with Docker Compose. This uses project qabasone and does not manage external reverse proxy containers. On read-only replicas, only standby-safe services can be started.",
		Example: `  sudo qabas up
  sudo qabas up -d --build
  sudo qabas up api -d --build`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeLogServices,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := serviceArg(args)
			if err != nil {
				return err
			}
			return runServiceControl(f, serviceControl{action: "up", service: svc, detach: detach, build: build})
		},
	}
	cmd.Flags().BoolVarP(&detach, "detach", "d", true, "run containers in the background")
	cmd.Flags().BoolVar(&build, "build", false, "build images before starting containers")
	return cmd
}

func newDownCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "down [service]",
		Aliases: []string{"stop-all"},
		Short:   "Stop QabasOne services",
		Long:    "Stop and remove all QabasOne service containers, or one service container, while preserving data volumes. External containers such as edge-nginx are not touched.",
		Example: `  sudo qabas down
  sudo qabas down api`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeLogServices,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := serviceArg(args)
			if err != nil {
				return err
			}
			return runServiceControl(f, serviceControl{action: "down", service: svc})
		},
	}
}

func newStartCmd(f *flags) *cobra.Command {
	return serviceCommand(f, "start", "Start QabasOne services", "  sudo qabas start\n  sudo qabas start api")
}

func newStopCmd(f *flags) *cobra.Command {
	return serviceCommand(f, "stop", "Stop QabasOne services", "  sudo qabas stop\n  sudo qabas stop api")
}

func newRestartCmd(f *flags) *cobra.Command {
	return serviceCommand(f, "restart", "Restart QabasOne services", "  sudo qabas restart\n  sudo qabas restart api")
}

func serviceCommand(f *flags, action, short, example string) *cobra.Command {
	return &cobra.Command{
		Use:               action + " [service]",
		Short:             short,
		Long:              "Control all QabasOne services, or one service (api, app, dash, siraj, postgres, redis, minio, gotenberg, workers). External reverse proxies are not controlled. Read-only replicas cannot start writer-only workers.",
		Example:           example,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeLogServices,
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := serviceArg(args)
			if err != nil {
				return err
			}
			return runServiceControl(f, serviceControl{action: action, service: svc})
		},
	}
}

type serviceControl struct {
	action  string
	service string
	detach  bool
	build   bool
}

func serviceArg(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	return compose.ServiceAlias(args[0])
}

func runServiceControl(f *flags, opt serviceControl) error {
	e := load(f)
	return e.withLock(func() error {
		st, err := state.Load(e.Paths)
		if err != nil {
			return err
		}
		role, err := localNodeRole(e)
		if err != nil {
			return err
		}
		replicaStack := isReplicaRole(role)
		if !st.Installed() && !replicaStack {
			return exitcode.NewHint(exitcode.InvalidInput,
				"run `sudo qabas install` first",
				"QabasOne is not installed")
		}
		if _, err := os.Stat(e.Paths.ComposeFile()); err != nil {
			if replicaStack {
				return exitcode.NewHint(exitcode.InvalidInput,
					"run `sudo qabas replica add <name> --resume` to restore the replica stack",
					"replica Compose file is missing")
			}
			return exitcode.NewHint(exitcode.InvalidInput,
				"run `sudo qabas deploy "+st.CurrentRelease+"` to recreate compose files",
				"QabasOne compose file is missing")
		}
		if err := validateReplicaServiceControl(role, opt); err != nil {
			return err
		}
		if f.DryRun {
			target := "all services"
			if opt.service != "" {
				target = opt.service
			}
			extra := ""
			if opt.action == "up" {
				opts := []string{}
				if opt.detach {
					opts = append(opts, "-d")
				}
				if opt.build {
					opts = append(opts, "--build")
				}
				if len(opts) > 0 {
					extra = " with " + strings.Join(opts, " ")
				}
			}
			e.Out.Printf("would %s %s%s\n", opt.action, target, extra)
			return nil
		}
		env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
		rt := newRuntime()
		switch opt.action {
		case "up":
			if err := rt.UpService(e.Paths, env, opt.service, opt.build, opt.detach); err != nil {
				return err
			}
			if err := rt.EnsureEdgeNetwork(env); err != nil {
				return err
			}
		case "down":
			if err := rt.DownService(e.Paths, env, opt.service); err != nil {
				return err
			}
		case "start":
			if err := rt.Start(e.Paths, env, opt.service); err != nil {
				return err
			}
		case "stop":
			if err := rt.Stop(e.Paths, env, opt.service); err != nil {
				return err
			}
		case "restart":
			if err := rt.Restart(e.Paths, env, opt.service); err != nil {
				return err
			}
		default:
			return exitcode.New(exitcode.InvalidInput, "unknown service action %q", opt.action)
		}
		target := "services"
		if opt.service != "" {
			target = opt.service
		}
		e.Out.Printf("%s %s complete.\n", opt.action, target)
		return nil
	})
}

func validateReplicaServiceControl(role nodestack.Profile, opt serviceControl) error {
	if role != nodestack.ProfileReplicaRO || opt.service == "" {
		return nil
	}
	if opt.action != "up" && opt.action != "start" && opt.action != "restart" {
		return nil
	}
	switch opt.service {
	case "api", "app", "dash", "siraj", "postgres", "redis", "minio", "gotenberg", "jsreport", "gateway":
		return nil
	default:
		return exitcode.New(exitcode.InvalidInput,
			"cannot %s %s on a read-only replica; background workers and migration services must run only on the writer",
			opt.action, opt.service)
	}
}
