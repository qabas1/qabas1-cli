package cli

import (
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/observability"
	"github.com/qabas1/qabas1-cli/internal/state"
	"github.com/spf13/cobra"
)

func newLogsCmd(f *flags) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Show service logs or open the localhost logs UI",
		Long: `Show logs for all QabasOne services, or one service (api, app, dash, siraj, postgres, minio, …).

Use "qabas logs ui" to start a localhost web UI (Dozzle) for realtime viewing.
Optional --full adds Loki + Grafana for searchable history.

The logs UI runs as a separate Docker Compose project (qabas1-logs) and does
not affect qabas up/down of the product stack. It binds to 127.0.0.1 only and
reads container logs through a read-only Docker API proxy on an internal network.

Retention: "qabas logs retention" shows how every log is bounded.`,
		Example: `  qabas logs
  qabas logs api
  qabas logs -f dash
  qabas logs ui up
  qabas logs ui up --full
  qabas logs ui status
  qabas logs ui open
  qabas logs ui down`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeLogServices,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := state.Load(e.Paths)
			if err != nil {
				return err
			}
			svc := ""
			if len(args) == 1 {
				var err error
				svc, err = compose.ServiceAlias(args[0])
				if err != nil {
					return err
				}
			}
			env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
			return newRuntime().Logs(e.Paths, env, svc, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	cmd.AddCommand(
		newLogsUICmd(f),
		newLogsRetentionCmd(f),
	)
	return cmd
}

func newLogsUICmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Localhost realtime logs web UI",
		Long: `Manage the localhost logs web UI (Dozzle, optional Loki/Grafana).

Requires Docker. Binds to 127.0.0.1 only. Containers run without capabilities on a
read-only filesystem and reach Docker only through a GET-only socket proxy.
Loki keeps logs for QABAS_LOGS_RETENTION (default 7d).`,
		Args: cobra.NoArgs,
		RunE: parentHelp(),
	}
	cmd.AddCommand(
		newLogsUIUpCmd(f),
		newLogsUIDownCmd(f),
		newLogsUIStatusCmd(f),
		newLogsUIOpenCmd(f),
	)
	return cmd
}

func newLogsUIUpCmd(f *flags) *cobra.Command {
	var full, all bool
	var port int
	cmd := &cobra.Command{
		Use:   "up",
		Short: "Start the localhost logs UI",
		Long: `Start Dozzle on localhost for realtime QabasOne container logs.

With --full, also start Grafana Alloy, Loki, and Grafana for searchable history.
By default only containers from Compose project "qabasone" are shown; use --all
to include every local Docker container.

Auth is enabled by default (Dozzle simple auth + Grafana login). Passwords are
generated once and stored in /etc/qabasone/logs-ui.credentials (mode 0600).
Override with QABAS_DOZZLE_PASSWORD / QABAS_GRAFANA_PASSWORD if needed.
Loki is not published on the host (internal network only).`,
		Example: `  qabas logs ui up
  qabas logs ui up --port 9999
  qabas logs ui up --full
  qabas logs ui up --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			opt := observability.Options{Full: full, All: all, Port: port}
			if f.DryRun {
				mode := "dozzle"
				if full {
					mode = "dozzle+loki+grafana"
				}
				urls := observability.ResolveURLs(opt)
				e.Out.Printf("would start logs UI (%s) at %s\n", mode, urls.Dozzle)
				if full {
					e.Out.Printf("would start Grafana at %s\n", urls.Grafana)
				}
				return nil
			}
			rt := observability.NewRuntime()
			urls, cred, err := rt.Up(e.Paths, opt)
			if err != nil {
				return err
			}
			out := map[string]any{
				"dozzle":          urls.Dozzle,
				"full":            full,
				"all":             all,
				"grafana":         urls.Grafana,
				"user":            cred.User,
				"credentialsFile": cred.Path,
				"credentialsNew":  cred.Created,
			}
			if f.JSON {
				// Never print raw passwords in --json by default; point at the file.
				return e.Out.Encode(out)
			}
			e.Out.Printf("Logs UI running at %s\n", urls.Dozzle)
			e.Out.Printf("Login user: %s\n", cred.User)
			if full {
				e.Out.Printf("Grafana (searchable logs) at %s\n", urls.Grafana)
			}
			e.Out.Printf("Credentials file: %s\n", cred.Path)
			if cred.Created {
				e.Out.Println("Generated new local passwords (shown once):")
				e.Out.Printf("  Dozzle password:  %s\n", cred.DozzlePassword)
				if full {
					e.Out.Printf("  Grafana password: %s\n", cred.GrafanaPassword)
				} else {
					e.Out.Printf("  Grafana password: %s (used when you pass --full)\n", cred.GrafanaPassword)
				}
			} else {
				e.Out.Println("Passwords: see the credentials file (not printed again).")
			}
			e.Out.Println("Auth is required (anonymous access disabled).")
			e.Out.Println("Stop with: qabas logs ui down")
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "also start Loki + Alloy + Grafana for searchable history")
	cmd.Flags().BoolVar(&all, "all", false, "show all Docker containers (not only project qabasone)")
	cmd.Flags().IntVar(&port, "port", 0, "Dozzle host port (default 9999 or QABAS_LOGS_UI_PORT)")
	return cmd
}

func newLogsUIDownCmd(f *flags) *cobra.Command {
	var wipe bool
	cmd := &cobra.Command{
		Use:     "down",
		Short:   "Stop the localhost logs UI",
		Long:    "Stop and remove qabas1-logs containers. Does not affect the product stack (qabasone).",
		Example: "  qabas logs ui down\n  qabas logs ui down --wipe",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if f.DryRun {
				e.Out.Println("would stop logs UI project qabas1-logs")
				return nil
			}
			if err := observability.NewRuntime().Down(e.Paths, wipe); err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(map[string]any{"stopped": true, "wipe": wipe})
			}
			e.Out.Println("Logs UI stopped.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&wipe, "wipe", false, "also remove logs UI volumes and local credentials file")
	return cmd
}

func newLogsUIStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show logs UI status and URLs",
		Example: "  qabas logs ui status\n  qabas logs ui status --json",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := observability.NewRuntime().Status(e.Paths)
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(st)
			}
			state := "stopped"
			if st.Running {
				state = "running"
			}
			mode := "dozzle"
			if st.Full {
				mode = "full"
			}
			e.Out.Printf("Logs UI: %s (%s)\n", state, mode)
			e.Out.Printf("  Dozzle:  %s\n", st.URLs.Dozzle)
			if st.Full || st.URLs.Grafana != "" {
				e.Out.Printf("  Grafana: %s\n", st.URLs.Grafana)
			}
			if len(st.Services) > 0 {
				e.Out.Println("Services:")
				for _, svc := range st.Services {
					health := svc.Health
					if health != "" {
						health = " health=" + health
					}
					e.Out.Printf("  %-12s %-24s %s%s\n", svc.Service, svc.Name, svc.State, health)
				}
			}
			return nil
		},
	}
}

func newLogsUIOpenCmd(f *flags) *cobra.Command {
	var grafana bool
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Print (and try to open) the logs UI URL",
		Long:  "Print the Dozzle URL. With --grafana, open Grafana instead (full stack).",
		Example: `  qabas logs ui open
  qabas logs ui open --grafana`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := observability.NewRuntime().Status(e.Paths)
			if err != nil {
				return err
			}
			url := st.URLs.Dozzle
			kind := "dozzle"
			if grafana {
				if st.URLs.Grafana == "" {
					st.URLs = observability.ResolveURLs(observability.Options{Full: true})
				}
				url = st.URLs.Grafana
				kind = "grafana"
			}
			if f.JSON {
				return e.Out.Encode(map[string]any{
					"url":     url,
					"kind":    kind,
					"running": st.Running,
				})
			}
			e.Out.Println(url)
			if !st.Running {
				hint := "qabas logs ui up"
				if grafana {
					hint += " --full"
				}
				e.Out.Printf("hint: start with %s\n", hint)
			}
			_ = observability.OpenBrowser(url)
			return nil
		},
	}
	cmd.Flags().BoolVar(&grafana, "grafana", false, "open Grafana instead of Dozzle")
	return cmd
}
