package cli

import (
	"os"
	"time"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/lock"
	"github.com/spf13/cobra"
)

func newLockCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lock",
		Short: "Inspect the qabas operations lock",
		Long: `Every state-changing qabas command holds one operations lock so two of them never
run at once.

When a command finds the lock held it resolves it automatically:
  - a stuck holder (stopped with Ctrl+Z, no heartbeat for ` + lock.HeartbeatStale.String() + `, or over its
    runtime limit) is stopped with SIGTERM, then SIGKILL, and the takeover is audited
  - a healthy holder is waited on with a progress line
  - a holder in a critical phase (RESTORE, SWITCH, COMMIT) is never interrupted

The runtime limit is 30m for autopilot and 6h for everything else
(override with ` + lock.EnvStuckAfter + `). Never delete the lock file: it does not
release a running holder and lets two operations run at once.`,
		Example: `  qabas lock status
  qabas lock status --json`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.AddCommand(newLockStatusCmd(f))
	return cmd
}

func newLockStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show who holds the operations lock and what auto-resolve will do",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			st, err := lock.Inspect(e.Paths.LockPath)
			if err != nil {
				if os.IsPermission(err) {
					return exitcode.NewHint(exitcode.Generic, "re-run with sudo: sudo qabas lock status", "read lock %s: %v", e.Paths.LockPath, err)
				}
				return wrapErr(err)
			}
			if f.JSON {
				return e.Out.Encode(st)
			}
			if !st.Held {
				e.Out.Println("Lock:      free")
				return nil
			}
			e.Out.Printf("Lock:      held (%s)\n", st.Path)
			if h := st.Holder; h != nil {
				e.Out.Printf("PID:       %d\n", h.PID)
				if h.Command != "" {
					e.Out.Printf("Command:   %s\n", h.Command)
				}
				if !h.Started.IsZero() {
					e.Out.Printf("Started:   %s (running %s, limit %s)\n", h.Started.Local().Format(time.DateTime), roundDur(st.Runtime), h.Limit())
				}
				if h.Legacy {
					e.Out.Println("Heartbeat: none (taken by an older qabas)")
				} else if !h.Heartbeat.IsZero() {
					e.Out.Printf("Heartbeat: %s ago\n", roundDur(st.HeartbeatAge))
				}
				if h.Phase != "" {
					e.Out.Printf("Phase:     %s\n", h.Phase)
				}
			}
			if st.State != "" {
				e.Out.Printf("State:     %s\n", st.State)
			}
			switch {
			case st.Critical:
				e.Out.Printf("Verdict:   critical (%s); it is never interrupted, other commands wait.\n", st.Reason)
			case st.Stuck && st.Signalable:
				e.Out.Printf("Verdict:   stuck (%s); the next qabas command or autopilot run will stop it and take over.\n", st.Reason)
			case st.Stuck:
				e.Out.Printf("Verdict:   stuck (%s).\n", st.Reason)
			default:
				e.Out.Printf("Verdict:   %s; other commands wait for it to finish.\n", st.Reason)
			}
			return nil
		},
	}
}

func roundDur(d time.Duration) time.Duration {
	if d < time.Minute {
		return d.Round(time.Second)
	}
	return d.Round(time.Minute)
}
