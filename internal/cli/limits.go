package cli

import (
	"strconv"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/compose"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/qabas1/qabasone-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

func newLimitsCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "limits",
		Short: "Show or regenerate container resource limits",
		Long: `QabasOne caps each service so one runaway container cannot take the whole host down.
qabas writes compose.qabas-limits.yaml on every deploy:

  memory   share of the RAM available to containers, with a floor and ceiling
           (postgres 25%, api 15%, each worker 8%, redis 10%, minio 10%,
           gotenberg 8%, frontends 5%)
  pids     512 processes per container (postgres 1024)
  security no-new-privileges, nofile 65536, and drops NET_RAW, MKNOD,
           SYS_CHROOT, AUDIT_WRITE

Anything the release already sets for a service is left untouched.
Override one service with QABAS_LIMIT_<SERVICE>=4g (or =none); disable all with
QABAS_LIMITS=off. Changes apply on the next deploy, or now with "qabas limits sync".`,
		Example: `  qabas limits show
  sudo QABAS_LIMIT_POSTGRES=6g qabas limits sync`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newLimitsShowCmd(f), newLimitsSyncCmd(f))
	return cmd
}

type limitsReport struct {
	Active   bool                    `json:"active"`
	Disabled bool                    `json:"disabled"`
	RAMBytes uint64                  `json:"containerRamBytes"`
	Services []compose.ServiceLimits `json:"services"`
}

func newLimitsShowCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the limits each service gets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			rep := limitsReport{
				Active:   compose.LimitsOverlayEnabled(e.Paths),
				Disabled: compose.LimitsDisabled(),
				RAMBytes: sysinfo.DockerMemTotal(),
			}
			defs, err := compose.LiveServiceDefs(e.Paths)
			if err != nil {
				return exitcode.NewHint(exitcode.InvalidInput, "install first: qabas install", "%s", err)
			}
			rep.Services = compose.PlanLimits(defs, rep.RAMBytes)
			if f.JSON {
				return e.Out.Encode(rep)
			}
			w := cmd.OutOrStdout()
			state := "active"
			switch {
			case rep.Disabled:
				state = "disabled (QABAS_LIMITS=off)"
			case !rep.Active:
				state = "not applied yet (sudo qabas limits sync, or next deploy)"
			}
			e.Out.Printf("Limits: %s; container RAM %s\n\n", state, sysinfo.HumanBytes(rep.RAMBytes))
			rows := make([][]string, 0, len(rep.Services))
			for _, s := range rep.Services {
				mem := "-"
				if s.MemoryBytes > 0 {
					mem = sysinfo.HumanBytes(s.MemoryBytes) + " (" + s.MemorySrc + ")"
				}
				pids := "-"
				if s.PidsLimit > 0 {
					pids = strconv.Itoa(s.PidsLimit)
				}
				var hard []string
				if s.NoNewPrivs {
					hard = append(hard, "no-new-privileges")
				}
				if len(s.CapDrop) > 0 {
					hard = append(hard, "cap_drop")
				}
				rows = append(rows, []string{s.Service, mem, pids, orDash(strings.Join(hard, ",")), orDash(strings.Join(s.Upstream, ","))})
			}
			return output.Table(w, []string{"SERVICE", "MEMORY", "PIDS", "HARDENING", "SET BY RELEASE"}, rows)
		},
	}
}

func newLimitsSyncCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Regenerate the limits overlay and apply it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if f.DryRun {
				e.Out.Println("would regenerate " + compose.LimitsOverlayPath(e.Paths) + " and run qabas up")
				return nil
			}
			return e.withLock(func() error {
				changed, _, err := compose.SyncLimitsOverlay(e.Paths, sysinfo.DockerMemTotal(), e.Paths.ComposeFile())
				if err != nil {
					_ = compose.DisableLimitsOverlay(e.Paths)
					return wrapErr(err)
				}
				audit(e, "limits-sync", "", "", nil, "")
				if !changed {
					e.Out.Println("Limits unchanged.")
					return nil
				}
				_, st, err := e.cfgState()
				if err != nil {
					return err
				}
				if !st.Installed() {
					e.Out.Println("Limits written; they apply when the stack starts.")
					return nil
				}
				env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
				if err := newRuntime().Up(e.Paths, env); err != nil {
					return wrapErr(err)
				}
				e.Out.Println("Limits applied.")
				return nil
			})
		},
	}
}
