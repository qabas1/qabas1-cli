package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/output"
	"github.com/qabasone/qabasone-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

func newSysinfoCmd(f *flags) *cobra.Command {
	var watch time.Duration
	cmd := &cobra.Command{
		Use:     "sysinfo",
		Aliases: []string{"machine", "hostinfo"},
		Short:   "Show machine resources (CPU, memory, disk, uptime, Docker)",
		Long: "Show this machine's CPU, memory, swap, disk space for QabasOne data, uptime, network addresses, " +
			"and Docker resource usage, graded against the thresholds used by doctor and autopilot.",
		Example: `  qabas sysinfo
  qabas sysinfo --json
  qabas sysinfo --watch
  qabas sysinfo --watch=10s`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			w := cmd.OutOrStdout()
			if watch <= 0 {
				info := sysinfo.Collect(sysinfo.Targets(e.Paths)...)
				if f.JSON {
					return e.Out.Encode(info)
				}
				return renderSysinfo(w, info)
			}
			if watch < time.Second {
				watch = time.Second
			}
			for {
				info := sysinfo.Collect(sysinfo.Targets(e.Paths)...)
				if f.JSON {
					if err := e.Out.Encode(info); err != nil {
						return err
					}
				} else {
					fmt.Fprint(w, "\033[H\033[2J")
					fmt.Fprintf(w, "Every %s — %s (Ctrl+C to stop)\n\n", watch, info.CollectedAt.Format("2006-01-02 15:04:05"))
					if err := renderSysinfo(w, info); err != nil {
						return err
					}
				}
				time.Sleep(watch)
			}
		},
	}
	cmd.Flags().DurationVar(&watch, "watch", 0, "refresh continuously at this interval (default 5s when given without a value)")
	cmd.Flags().Lookup("watch").NoOptDefVal = "5s"
	return cmd
}

func renderSysinfo(w io.Writer, info sysinfo.Info) error {
	osName := info.OSName
	if osName == "" {
		osName = info.OS
	}
	sys := [][]string{
		{"Hostname", orDash(info.Hostname)},
		{"OS", osName + " (" + info.OS + "/" + info.Arch + ")"},
		{"Kernel", orDash(info.Kernel)},
		{"Uptime", sysinfo.HumanDuration(time.Duration(info.UptimeSec) * time.Second)},
	}
	if !info.BootTime.IsZero() {
		sys = append(sys, []string{"Booted", info.BootTime.Format("2006-01-02 15:04")})
	}
	sys = append(sys, []string{"Timezone", orDash(info.Timezone)})
	if len(info.Addresses) > 0 {
		sys = append(sys, []string{"Addresses", strings.Join(info.Addresses, ", ")})
	}
	if err := output.Table(w, []string{"SYSTEM", "VALUE"}, sys); err != nil {
		return err
	}

	cpu := [][]string{
		{"Model", orDash(info.CPU.Model)},
		{"Cores", fmt.Sprintf("%d", info.CPU.Cores)},
	}
	if len(info.CPU.Load) == 3 {
		cpu = append(cpu, []string{"Load (1/5/15m)", fmt.Sprintf("%.2f / %.2f / %.2f", info.CPU.Load[0], info.CPU.Load[1], info.CPU.Load[2])})
	}
	if info.CPU.UsagePercent > 0 {
		cpu = append(cpu, []string{"Usage", fmt.Sprintf("%.1f%%", info.CPU.UsagePercent)})
	}
	fmt.Fprintln(w)
	if err := output.Table(w, []string{"CPU", "VALUE"}, cpu); err != nil {
		return err
	}

	if m := info.Memory; m != nil {
		mem := [][]string{
			{"Total", sysinfo.HumanBytes(m.TotalBytes)},
			{"Available", sysinfo.HumanBytes(m.AvailableBytes)},
			{"Used", fmt.Sprintf("%.1f%%", m.UsedPercent)},
		}
		if m.SwapTotalBytes > 0 {
			mem = append(mem, []string{"Swap", sysinfo.HumanBytes(m.SwapFreeBytes) + " free of " + sysinfo.HumanBytes(m.SwapTotalBytes)})
		} else {
			mem = append(mem, []string{"Swap", "none"})
		}
		fmt.Fprintln(w)
		if err := output.Table(w, []string{"MEMORY", "VALUE"}, mem); err != nil {
			return err
		}
	}

	if len(info.Disks) > 0 {
		rows := make([][]string, 0, len(info.Disks))
		for _, d := range info.Disks {
			rows = append(rows, []string{d.Label, d.Path, sysinfo.HumanBytes(d.TotalBytes), sysinfo.HumanBytes(d.FreeBytes), fmt.Sprintf("%.1f%%", d.UsedPercent)})
		}
		fmt.Fprintln(w)
		if err := output.Table(w, []string{"DISK", "PATH", "SIZE", "FREE", "USED"}, rows); err != nil {
			return err
		}
	}

	if dk := info.Docker; dk != nil {
		rows := [][]string{
			{"Engine", orDash(dk.Version) + " (" + orDash(dk.OSType) + ")"},
			{"Containers", fmt.Sprintf("%d running / %d total", dk.ContainersUp, dk.ContainersTotal)},
		}
		if dk.NCPU > 0 {
			rows = append(rows, []string{"Engine CPUs", fmt.Sprintf("%d", dk.NCPU)})
		}
		if dk.MemTotalBytes > 0 {
			rows = append(rows, []string{"Engine memory", sysinfo.HumanBytes(dk.MemTotalBytes)})
		}
		for _, u := range dk.Usage {
			rows = append(rows, []string{u.Type, fmt.Sprintf("%s (%d, %s reclaimable)", sysinfo.HumanBytes(u.SizeBytes), u.Count, sysinfo.HumanBytes(u.ReclaimableBytes))})
		}
		fmt.Fprintln(w)
		if err := output.Table(w, []string{"DOCKER", "VALUE"}, rows); err != nil {
			return err
		}
	}

	if len(info.Findings) > 0 {
		rows := make([][]string, 0, len(info.Findings))
		for _, fd := range info.Findings {
			rows = append(rows, []string{strings.ToUpper(fd.Level), fd.Check, fd.Detail})
		}
		fmt.Fprintln(w)
		if err := output.Table(w, []string{"STATUS", "CHECK", "DETAIL"}, rows); err != nil {
			return err
		}
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
