package cli

import (
	"github.com/qabas1/qabasone-cli/internal/history"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/spf13/cobra"
)

func newHistoryCmd(f *flags) *cobra.Command {
	var format string
	var all bool
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show deployment and restore audit history",
		Long: `Show deployment and restore audit history.

history.jsonl is rotated automatically (5 MiB or 10,000 entries, 3 gzip archives kept);
--all includes the rotated archives.`,
		Example: `  qabas history
  qabas history --all
  qabas history --json
  qabas history --format '{{.Time}} {{.Action}} {{.Status}}'`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			list := history.List
			if all {
				list = history.ListAll
			}
			items, err := list(e.Paths)
			if err != nil {
				return err
			}
			if f.JSON {
				return e.Out.Encode(items)
			}
			rows := make([]historyRow, 0, len(items))
			for _, it := range items {
				change := ""
				if it.From != "" && it.To != "" {
					change = it.From + " → " + it.To
				} else if it.To != "" {
					change = it.To
				}
				rows = append(rows, historyRow{
					Time:   it.Time.Local().Format("2006-01-02 15:04"),
					Action: it.Action,
					Change: change,
					Backup: it.Backup,
					Status: it.Status,
				})
			}
			return output.FormatList(cmd.OutOrStdout(), format,
				"table {{.Time}}\t{{.Action}}\t{{.Change}}\t{{.Backup}}\t{{.Status}}", rows)
		},
	}
	cmd.Flags().StringVar(&format, "format", "", `Format output using a Go template ("table", "json", or a template)`)
	cmd.Flags().BoolVar(&all, "all", false, "include rotated history archives")
	return cmd
}

type historyRow struct {
	Time   string
	Action string
	Change string
	Backup string
	Status string
}
