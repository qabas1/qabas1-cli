package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/backup"
	"github.com/qabasone/qabasone-cli/internal/compose"
	"github.com/qabasone/qabasone-cli/internal/deploy"
	"github.com/qabasone/qabasone-cli/internal/doctor"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/output"
	"github.com/qabasone/qabasone-cli/internal/state"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

func newStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show installed release and service health",
		Long:  "Show the installed QabasOne release, health summary, and key service checks.",
		Example: `  qabas status
  qabas status --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			cfg, st, err := e.cfgState()
			if err != nil {
				return err
			}
			_ = cfg
			rep := doctor.Run(e.Paths, cfg, st, newRuntime(), false)
			type status struct {
				Release       string                  `json:"release"`
				Status        string                  `json:"status"`
				Maintenance   bool                    `json:"maintenance"`
				LastBackup    string                  `json:"lastBackup,omitempty"`
				Healthy       bool                    `json:"healthy"`
				Phase         string                  `json:"phase,omitempty"`
				TargetRelease string                  `json:"targetRelease,omitempty"`
				LastError     string                  `json:"lastError,omitempty"`
				Services      []compose.ServiceStatus `json:"services,omitempty"`
				Warnings      []string                `json:"backupWarnings,omitempty"`
				Machine       machineSummary          `json:"machine"`
				WriterLease   *doctor.Check           `json:"writerLease,omitempty"`
			}
			leaseCk := localWriterLease(e, time.Now())
			hs := "Healthy"
			if !rep.OK {
				hs = "Unhealthy"
			} else if leaseCk != nil && !leaseCk.OK {
				hs = "Degraded (writer lease expired: API read-only)"
			}
			if !st.Installed() {
				hs = "Not installed"
			}
			if st.Maintenance {
				hs = "Maintenance"
			}
			last := ""
			if items, err := backup.List(e.Paths); err == nil && len(items) > 0 {
				last = items[len(items)-1].CreatedAt.Format("2006-01-02 15:04")
			}
			var services []compose.ServiceStatus
			if st.Installed() && newRuntime().Available() {
				env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
				if rows, err := newRuntime().ServiceStatus(e.Paths, env, st.CurrentRelease); err == nil {
					services = rows
				}
			}
			out := status{
				Release:     st.CurrentRelease,
				Status:      hs,
				Maintenance: st.Maintenance,
				LastBackup:  last,
				Healthy:     rep.OK && st.Installed() && (leaseCk == nil || leaseCk.OK),
				Services:    services,
				Warnings:    scheduleWarnings(e.Paths, time.Now()),
				Machine:     summarizeMachine(rep.Machine),
				WriterLease: leaseCk,
			}
			if deploy.IncompletePhase(st.Phase) {
				out.Phase = st.Phase
				out.TargetRelease = st.TargetRelease
				out.LastError = st.LastError
			}
			if f.JSON {
				return e.Out.Encode(out)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "QabasOne\n\n")
			summaryRows := [][]string{
				{"Release", dash(st.CurrentRelease)},
				{"Status", hs},
			}
			if last != "" {
				summaryRows = append(summaryRows, []string{"Last backup", last})
			}
			if deploy.IncompletePhase(st.Phase) {
				summaryRows = append(summaryRows, []string{"Phase", st.Phase})
				if st.TargetRelease != "" {
					summaryRows = append(summaryRows, []string{"Target", st.TargetRelease})
				}
				if st.LastError != "" {
					summaryRows = append(summaryRows, []string{"Last error", st.LastError})
				}
			}
			if err := output.Table(cmd.OutOrStdout(), []string{"SUMMARY", "VALUE"}, summaryRows); err != nil {
				return err
			}
			if warn := deploy.IncompleteDeployWarning(st); warn != "" {
				fmt.Fprintln(cmd.OutOrStdout())
				fmt.Fprintln(cmd.OutOrStdout(), warn)
			}
			if len(out.Warnings) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
				for _, w := range out.Warnings {
					fmt.Fprintln(cmd.OutOrStdout(), "WARNING: backups: "+w)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout())
			if err := output.Table(cmd.OutOrStdout(), []string{"MACHINE", "VALUE"}, out.Machine.rows()); err != nil {
				return err
			}
			checkRows := make([][]string, 0)
			for _, c := range rep.Checks {
				if strings.HasPrefix(c.Name, "docker") || c.Name == "containers" || c.Name == "release" {
					checkRows = append(checkRows, []string{c.Name, c.Detail})
				}
			}
			if leaseCk != nil {
				checkRows = append(checkRows, []string{leaseCk.Name, leaseCk.Detail})
			}
			if len(checkRows) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
				if err := output.Table(cmd.OutOrStdout(), []string{"CHECK", "DETAIL"}, checkRows); err != nil {
					return err
				}
			}
			if len(services) > 0 {
				serviceRows := make([][]string, 0, len(services))
				for _, svc := range services {
					serviceRows = append(serviceRows, []string{
						dash(svc.Service),
						dash(svc.Version),
						dash(svc.ContainerID),
						dash(svc.Memory),
						dash(svc.State),
						dash(svc.Health),
						shortImage(svc.Image),
					})
				}
				fmt.Fprintln(cmd.OutOrStdout())
				if err := output.Table(cmd.OutOrStdout(), []string{"SERVICE", "VERSION", "CONTAINER ID", "MEMORY", "STATE", "HEALTH", "IMAGE"}, serviceRows); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

type machineSummary struct {
	Hostname          string         `json:"hostname,omitempty"`
	CPUs              int            `json:"cpus"`
	MemoryTotalBytes  uint64         `json:"memoryTotalBytes,omitempty"`
	MemoryAvailBytes  uint64         `json:"memoryAvailableBytes,omitempty"`
	DiskFreeBytes     uint64         `json:"diskFreeBytes,omitempty"`
	DiskTotalBytes    uint64         `json:"diskTotalBytes,omitempty"`
	Disks             []sysinfo.Disk `json:"disks,omitempty"`
	UptimeSec         int64          `json:"uptimeSec,omitempty"`
	Health            string         `json:"health"`
	healthDetailLines []string
}

func summarizeMachine(info sysinfo.Info) machineSummary {
	m := machineSummary{
		Hostname:  info.Hostname,
		CPUs:      info.CPU.Cores,
		UptimeSec: info.UptimeSec,
		Health:    sysinfo.Worst(info.Findings),
	}
	if info.Memory != nil {
		m.MemoryTotalBytes = info.Memory.TotalBytes
		m.MemoryAvailBytes = info.Memory.AvailableBytes
	}
	for _, d := range info.Disks {
		m.Disks = append(m.Disks, d)
		if strings.Contains(","+d.Label+",", ",state,") {
			m.DiskFreeBytes, m.DiskTotalBytes = d.FreeBytes, d.TotalBytes
		}
	}
	for _, f := range info.Findings {
		if f.Level != sysinfo.LevelOK {
			m.healthDetailLines = append(m.healthDetailLines, f.Check+": "+f.Detail)
		}
	}
	return m
}

func (m machineSummary) rows() [][]string {
	rows := [][]string{{"CPU cores", fmt.Sprintf("%d", m.CPUs)}}
	if m.MemoryTotalBytes > 0 {
		rows = append(rows, []string{"Memory", sysinfo.HumanBytes(m.MemoryAvailBytes) + " available of " + sysinfo.HumanBytes(m.MemoryTotalBytes)})
	}
	for _, d := range m.Disks {
		if d.TotalBytes == 0 {
			continue
		}
		used := d.TotalBytes - min(d.FreeBytes, d.TotalBytes)
		rows = append(rows, []string{"Disk (" + d.Label + ")", fmt.Sprintf("%s used (%.1f%%), %s available of %s — %s", sysinfo.HumanBytes(used), float64(used)/float64(d.TotalBytes)*100, sysinfo.HumanBytes(d.FreeBytes), sysinfo.HumanBytes(d.TotalBytes), d.Path)})
	}
	if m.UptimeSec > 0 {
		rows = append(rows, []string{"Uptime", sysinfo.HumanDuration(time.Duration(m.UptimeSec) * time.Second)})
	}
	rows = append(rows, []string{"Resources", m.Health})
	for _, l := range m.healthDetailLines {
		rows = append(rows, []string{"", l})
	}
	return rows
}

func shortImage(image string) string {
	if image == "" {
		return "-"
	}
	if before, _, ok := strings.Cut(image, "@sha256:"); ok {
		return before
	}
	return image
}

func newPsCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:     "ps",
		Aliases: []string{"services"},
		Short:   "List QabasOne containers",
		Long:    "List containers managed by the QabasOne Docker Compose project only. External reverse proxies such as edge-nginx are not controlled.",
		Example: `  qabas ps`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := state.Load(e.Paths)
			if err != nil {
				return err
			}
			env := compose.EnvForInstalledRelease(e.Paths, st.CurrentRelease)
			out, err := newRuntime().Ps(e.Paths, env)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}
}
func newDoctorCmd(f *flags) *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose host, Docker, and installation health",
		Long:  "Run host, Docker, credential, and installation checks. Exits non-zero when problems are found.",
		Example: `  qabas doctor
  qabas doctor --verbose
  qabas doctor --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			cfg, st, err := e.cfgState()
			if err != nil {
				return err
			}
			rep := doctor.Run(e.Paths, cfg, st, newRuntime(), verbose)
			rep.Checks = append(rep.Checks, scheduleChecks(e.Paths, cfg, time.Now())...)
			rep.Checks = append(rep.Checks, autopilotChecks(cmdContext(cmd), e.Paths, st.Installed())...)
			rep.Checks = append(rep.Checks, securityChecks(cmdContext(cmd), e, st.Installed())...)
			if f.JSON {
				if err := e.Out.Encode(rep); err != nil {
					return err
				}
			} else {
				rows := make([][]string, 0, len(rep.Checks))
				for _, c := range rep.Checks {
					mark := "ok"
					if !c.OK {
						mark = "FAIL"
					}
					rows = append(rows, []string{mark, c.Name, c.Detail})
				}
				if err := output.Table(cmd.OutOrStdout(), []string{"STATUS", "CHECK", "DETAIL"}, rows); err != nil {
					return err
				}
			}
			if !rep.OK {
				return exitcode.NewHint(exitcode.Health,
					"fix failing checks, then re-run: qabas doctor",
					"doctor found problems")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&verbose, "verbose", false, "include extra diagnostics")
	cmd.AddCommand(newDoctorRedisCmd(f))
	return cmd
}
