package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/clilog"
	"github.com/qabas1/qabas1-cli/internal/compose"
	"github.com/qabas1/qabas1-cli/internal/doctor"
	"github.com/qabas1/qabas1-cli/internal/history"
	"github.com/qabas1/qabas1-cli/internal/observability"
	"github.com/qabas1/qabas1-cli/internal/output"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/sysinfo"
	"github.com/spf13/cobra"
)

type logRetention struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	Bytes  int64  `json:"bytes"`
	Policy string `json:"policy"`
	Active bool   `json:"active"`
}

func logRetentionReport(p paths.Paths) []logRetention {
	return []logRetention{
		{Name: "qabas.log", Path: p.LogFile(), Bytes: clilog.Size(p.LogFile(), clilog.Keep), Active: true,
			Policy: sysinfo.HumanBytes(clilog.MaxBytes) + " x " + itoa(clilog.Keep) + " gzip archives"},
		{Name: "history.jsonl", Path: p.HistoryFile(), Bytes: history.Size(p), Active: true,
			Policy: sysinfo.HumanBytes(history.MaxBytes) + " or " + itoa(history.MaxEntries) + " entries, " + itoa(history.Keep) + " gzip archives"},
		{Name: "containers", Path: compose.LoggingOverlayPath(p), Active: compose.LoggingOverlayEnabled(p),
			Policy: "json-file " + compose.LogMaxSize + " x " + compose.LogMaxFiles + " per service"},
		{Name: "loki", Active: true, Policy: "retention " + observability.LokiRetention() + " (QABAS_LOGS_RETENTION)"},
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func lokiVolumeTask(docker func(args ...string) (string, error)) (string, uint64, error) {
	n := doctor.LokiVolumeBytes(docker)
	if n > doctor.LokiVolumeWarnBytes {
		return "", 0, fmt.Errorf("loki volume %s is %s (over %s); lower QABAS_LOGS_RETENTION (now %s) and rerun qabas logs ui up --full",
			observability.LokiVolume, sysinfo.HumanBytes(n), sysinfo.HumanBytes(doctor.LokiVolumeWarnBytes), observability.LokiRetention())
	}
	return "", 0, nil
}

// enforceLogRetention rotates qabas.log and history.jsonl when over their caps.
func enforceLogRetention(p paths.Paths) (string, uint64, error) {
	var did []string
	if st, err := os.Stat(p.LogFile()); err == nil && st.Size() >= clilog.MaxBytes {
		if err := clilog.Rotate(p.LogFile(), clilog.Keep); err != nil {
			return "", 0, err
		}
		did = append(did, "rotated qabas.log")
	}
	rotated, err := history.Enforce(p)
	if err != nil {
		return strings.Join(did, "; "), 0, err
	}
	if rotated {
		did = append(did, "rotated history.jsonl")
	}
	return strings.Join(did, "; "), 0, nil
}

func newLogsRetentionCmd(f *flags) *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Show log retention policies and sizes",
		Long: `Show how every QabasOne log is bounded: the qabas audit log, history.jsonl,
container logs (json-file rotation overlay) and Loki (logs UI --full).

Autopilot enforces these hourly; --apply rotates over-cap files now.`,
		Example: `  qabas logs retention
  sudo qabas logs retention --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if apply && !f.DryRun {
				detail, _, err := enforceLogRetention(e.Paths)
				if err != nil {
					return err
				}
				if detail != "" {
					e.Out.Println(detail)
				}
			}
			items := logRetentionReport(e.Paths)
			if f.JSON {
				return e.Out.Encode(items)
			}
			rows := make([][]string, 0, len(items))
			for _, it := range items {
				size := "-"
				if it.Bytes > 0 {
					size = sysinfo.HumanBytes(uint64(it.Bytes))
				}
				policy := it.Policy
				if !it.Active {
					policy += " (not active — run qabas deploy)"
				}
				rows = append(rows, []string{it.Name, size, policy})
			}
			return output.Table(cmd.OutOrStdout(), []string{"LOG", "SIZE", "POLICY"}, rows)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "rotate over-cap log files now")
	return cmd
}
