package cli

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/qabas1/qabasone-cli/internal/cluster"
	"github.com/qabas1/qabasone-cli/internal/dr"
	"github.com/qabas1/qabasone-cli/internal/exitcode"
	"github.com/qabas1/qabasone-cli/internal/lease"
	"github.com/qabas1/qabasone-cli/internal/nodestack"
	"github.com/qabas1/qabasone-cli/internal/output"
	"github.com/spf13/cobra"
)

func newDRCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dr",
		Short: "Disaster recovery: writer status, promote, switchover, failback",
		Long: `Change or inspect which node is the writer. There is exactly one writer
(PostgreSQL primary) at any time, guarded by an epoch-numbered writer lease.

  status      facts, reachability matrix and a verdict (never acts)
  check       writer container paths to provisioned peers
  candidates  promotable standbys ranked by data (least loss first)
  switchover  planned move of the writer (requires strict durability proof)
  promote     break-glass: writer is DOWN; typed confirmation required
  rejoin      bring a former writer back as a standby (pg_rewind)
  failback    return the writer role to a former writer
  lease       writer lease (renewed by a timer on the writer)
  witness     lease witnesses (third-party votes against split brain)
  routing     site HAProxy config + DNS/tunnel notes`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	cmd.PersistentFlags().String("identity", "", "SSH private key for other nodes (default: ssh-agent, ~/.ssh/id_ed25519, $"+EnvSSHIdentity+")")
	cmd.AddCommand(
		newDRStatusCmd(f),
		newDRCheckCmd(f),
		newDRCandidatesCmd(f),
		newDRSwitchoverCmd(f),
		newDRPromoteCmd(f),
		newDRRejoinCmd(f),
		newDRFailbackCmd(f),
		newDRLeaseCmd(f),
		newDRWitnessCmd(f),
		newDRRoutingCmd(f),
	)
	return cmd
}

func drEngine(cmd *cobra.Command, e *env, offline bool) (*dr.Engine, *cluster.Cluster, error) {
	c, err := e.cluster()
	if err != nil {
		return nil, nil, err
	}
	de, err := e.newDREngine(c, engineOpts{identity: identityFlag(cmd), offline: offline})
	return de, c, err
}

func newDRStatusCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Writer, lease, replication and reachability facts with a verdict",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			de, _, err := drEngine(cmd, e, true)
			if err != nil {
				return err
			}
			defer de.Close()
			facts := de.Status(cmd.Context())
			if f.JSON {
				return e.Out.Encode(facts)
			}
			printFacts(cmd, e, facts)
			return nil
		},
	}
}

func newDRCheckCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use: "check", Short: "Check writer container paths to every provisioned peer",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			de, _, err := drEngine(cmd, e, true)
			if err != nil {
				return err
			}
			defer de.Close()
			if de.Self != de.Cluster.Writer {
				return fmt.Errorf("dr check must run on writer %s", de.Cluster.Writer)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 12*time.Second)
			defer cancel()
			report := de.Check(ctx)
			if f.JSON {
				if err := e.Out.Encode(report); err != nil {
					return err
				}
			} else {
				for _, p := range report.Paths {
					state := "FAIL"
					if p.OK {
						state = "ok"
					}
					e.Out.Printf("%s %s %s (%s) -> %s: %s\n", state, p.Node, p.Container, p.Source, p.Destination, p.Detail)
				}
				for _, w := range report.Warnings {
					e.Out.Warnf("%s\n", w)
				}
			}
			if !report.OK {
				return fmt.Errorf("DR container path check failed")
			}
			return nil
		},
	}
}

func printFacts(cmd *cobra.Command, e *env, fa dr.Facts) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Observed from %s at %s\n", fa.Self, fa.At.Format(time.RFC3339))
	fmt.Fprintf(out, "Writer: %s (epoch %d), PostgreSQL reachable as primary from %s: %v\n", fa.Writer, fa.Epoch, fa.Self, fa.WriterReachable)
	fmt.Fprintf(out, "Lease mode: %s\n", fa.LeaseMode)
	switch {
	case fa.LeaseLocalOnly:
		fmt.Fprintln(out, "Lease witnesses: none configured")
	default:
		fmt.Fprintf(out, "Lease: %s epoch %d until %s (valid at quorum: %v)\n", fa.Lease.WriterNode, fa.Lease.Epoch, fa.Lease.LeaseExpiresAt.Format(time.RFC3339), fa.LeaseValidQuorum)
	}
	fmt.Fprintln(out)
	rows := [][]string{}
	for _, n := range fa.Nodes {
		rows = append(rows, []string{n.Name, n.Role, n.Site, strconv.FormatBool(n.Reachable), n.Describe()})
	}
	_ = output.Table(out, []string{"NODE", "ROLE", "SITE", "REACHABLE", "DETAIL"}, rows)

	if len(fa.Reachability) > 0 {
		fmt.Fprintln(out, "\nReachability (WireGuard) - rows observe columns:")
		header := []string{"FROM \\ TO"}
		for _, n := range fa.Nodes {
			header = append(header, n.Name)
		}
		header = append(header, "INTERNET")
		mrows := [][]string{}
		for _, r := range fa.Reachability {
			row := []string{r.Observer}
			for _, n := range fa.Nodes {
				switch {
				case n.Name == r.Observer:
					row = append(row, "-")
				case r.Error != "":
					row = append(row, "?")
				case r.Targets[n.Name]:
					row = append(row, "yes")
				default:
					row = append(row, "NO")
				}
			}
			switch {
			case r.Internet == nil:
				row = append(row, "?")
			case *r.Internet:
				row = append(row, "yes")
			default:
				row = append(row, "NO")
			}
			mrows = append(mrows, row)
		}
		_ = output.Table(out, header, mrows)
	}
	fmt.Fprintf(out, "\nVerdict [%s]: %s\n", fa.VerdictCode, fa.Verdict)
	fmt.Fprintf(out, "DR readiness [%s]: %s\n", fa.ReadinessCode, fa.Readiness)
}

func newDRCandidatesCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "candidates",
		Short: "Promotable standbys ranked by replayed data, then priority",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			de, _, err := drEngine(cmd, e, true)
			if err != nil {
				return err
			}
			defer de.Close()
			facts := de.Status(cmd.Context())
			if f.JSON {
				return e.Out.Encode(facts.Candidates)
			}
			rows := [][]string{}
			for _, c := range facts.Candidates {
				rows = append(rows, []string{strconv.Itoa(c.Rank), c.Node, c.Site, c.ReplayLSN, c.LastReplay, fmt.Sprintf("%.1fs", c.LagSeconds), strconv.Itoa(c.Priority), strconv.Itoa(c.Timeline)})
			}
			if err := output.Table(cmd.OutOrStdout(), []string{"RANK", "NODE", "SITE", "REPLAY LSN", "LAST TXN", "LAG", "PRIORITY", "TIMELINE"}, rows); err != nil {
				return err
			}
			e.Out.Printf("\n%s\n", facts.Verdict)
			return nil
		},
	}
}

func printAction(e *env, verb string, res *dr.ActionResult) {
	if res == nil {
		return
	}
	if e.flags.JSON {
		_ = e.Out.Encode(res)
		return
	}
	e.Out.Printf("%s: writer is now %s (epoch %d, was %s)\n", verb, res.NewWriter, res.Epoch, res.OldWriter)
	for _, w := range res.Warnings {
		e.Out.Warnf("%s\n", w)
	}
	for _, s := range res.NextSteps {
		e.Out.Printf("  next: %s\n", s)
	}
}

func newDRSwitchoverCmd(f *flags) *cobra.Command {
	var maxLag float64
	cmd := &cobra.Command{
		Use:   "switchover <node>",
		Short: "Planned writer move with zero data loss (run on the current writer)",
		Long: `Fence the current writer (API read-only, sessions closed, lease released), wait
until the target replayed every byte, stop and mark the old writer DEMOTED, promote the
target under a new epoch, then rewind the old writer into a standby.`,
		Example:           "  sudo qabas dr switchover dr1",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				de, c, err := drEngine(cmd, e, false)
				if err != nil {
					return err
				}
				defer de.Close()
				if e.flags.DryRun {
					e.Out.Printf("would switch writer %s -> %s\n", c.Writer, args[0])
					return nil
				}
				from := c.Writer
				res, err := de.Switchover(cmd.Context(), args[0], dr.SwitchoverOptions{MaxLagSeconds: maxLag, Binary: qabasCmd, Confirm: e.confirm})
				audit(e, "dr-switchover", from, args[0], err, "")
				printAction(e, "switchover", res)
				return drErr(err)
			})
		},
	}
	cmd.Flags().Float64Var(&maxLag, "max-lag", 30, "refuse when the target lags more than this many seconds")
	return cmd
}

func newDRPromoteCmd(f *flags) *cobra.Command {
	var breakGlass bool
	var confirmName string
	cmd := &cobra.Command{
		Use:   "promote <node>",
		Short: "Break-glass: make a standby the writer while the writer is DOWN",
		Long: `Promote a standby when the writer is gone. Refused when:
  - the writer is reachable from any node (use switchover), or
  - a witness majority still grants the old writer a valid lease.
Writes committed on the old writer after the standby's last replayed transaction are
lost (see: qabas dr candidates). Run it on the target node itself when the writer's
site is down; it works offline.`,
		Example: `  sudo qabas dr promote dr1 --break-glass
  sudo qabas dr promote dr1 --break-glass --confirm dr1 --non-interactive`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			target := args[0]
			return e.withLock(func() error {
				de, c, err := drEngine(cmd, e, true)
				if err != nil {
					return err
				}
				defer de.Close()
				if !breakGlass {
					return exitcode.NewHint(exitcode.InvalidInput, "if the writer is really down: qabas dr promote "+target+" --break-glass; if it is up: qabas dr switchover "+target,
						"promote requires --break-glass")
				}
				if confirmName == "" {
					if e.flags.NonInteractive {
						return exitcode.New(exitcode.InvalidInput, "--confirm %s is required non-interactively", target)
					}
					fmt.Fprintf(os.Stderr, "Type the node name (%s) to promote it to writer: ", target)
					line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
					confirmName = strings.TrimSpace(line)
				}
				if e.flags.DryRun {
					facts := de.Status(cmd.Context())
					printFacts(cmd, e, facts)
					return nil
				}
				from := c.Writer
				res, err := de.Promote(cmd.Context(), target, dr.PromoteOptions{BreakGlass: breakGlass, ConfirmName: confirmName, Binary: qabasCmd})
				audit(e, "dr-promote", from, target, err, "break-glass")
				printAction(e, "promote", res)
				return drErr(err)
			})
		},
	}
	cmd.Flags().BoolVar(&breakGlass, "break-glass", false, "acknowledge the writer is down and data after the last replay may be lost")
	cmd.Flags().StringVar(&confirmName, "confirm", "", "typed confirmation: the target node name")
	return cmd
}

func newDRRejoinCmd(f *flags) *cobra.Command {
	var rebuild bool
	cmd := &cobra.Command{
		Use:               "rejoin <node>",
		Short:             "Turn a former writer into a standby of the current writer",
		Example:           "  sudo qabas dr rejoin hq\n  sudo qabas dr rejoin hq --rebuild -y",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				de, c, err := drEngine(cmd, e, false)
				if err != nil {
					return err
				}
				defer de.Close()
				prompt := fmt.Sprintf("Rewind %s into a standby of %s (writes it made after the promotion are discarded)?", args[0], c.Writer)
				if rebuild {
					prompt = fmt.Sprintf("Discard %s's database and re-seed it from %s?", args[0], c.Writer)
				}
				if err := e.confirm(prompt); err != nil {
					return err
				}
				if e.flags.DryRun {
					return nil
				}
				err = de.Rejoin(cmd.Context(), args[0], dr.RejoinOptions{Rebuild: rebuild})
				audit(e, "dr-rejoin", c.Writer, args[0], err, "")
				if err == nil {
					e.Out.Printf("%s is a streaming standby of %s\n", args[0], c.Writer)
				}
				return drErr(err)
			})
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "re-seed with pg_basebackup instead of pg_rewind")
	return cmd
}

func newDRFailbackCmd(f *flags) *cobra.Command {
	var rebuild bool
	var maxLag float64
	cmd := &cobra.Command{
		Use:               "failback <node>",
		Short:             "Return the writer role to a former writer (rejoin + switchover)",
		Example:           "  sudo qabas dr failback hq",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeReplicaNodes,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				de, c, err := drEngine(cmd, e, false)
				if err != nil {
					return err
				}
				defer de.Close()
				if e.flags.DryRun {
					e.Out.Printf("would fail back %s -> %s\n", c.Writer, args[0])
					return nil
				}
				from := c.Writer
				res, err := de.Failback(cmd.Context(), args[0],
					dr.SwitchoverOptions{MaxLagSeconds: maxLag, Binary: qabasCmd, Confirm: e.confirm},
					dr.RejoinOptions{Rebuild: rebuild})
				audit(e, "dr-failback", from, args[0], err, "")
				printAction(e, "failback", res)
				return drErr(err)
			})
		},
	}
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "re-seed the former writer instead of pg_rewind")
	cmd.Flags().Float64Var(&maxLag, "max-lag", 30, "refuse the switchover when the node lags more than this")
	return cmd
}

func newDRLeaseCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lease",
		Short: "Writer lease (renewed every 20s by qabas-dr-lease.timer on the writer)",
		Args:  argsWithSuggestions,
		RunE:  parentHelp(),
	}
	renew := &cobra.Command{
		Use:   "renew",
		Short: "Renew the writer lease once (the timer runs this)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			de, _, err := drEngine(cmd, e, true)
			if err != nil {
				return err
			}
			defer de.Close()
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			res, err := de.Renew(ctx)
			if err != nil {
				return wrapErr(fmt.Errorf("lease renewal on %s (%s): %w", de.Self, res.Reason, err))
			}
			if res.Fenced {
				audit(e, "dr-self-fence", de.Self, "", nil, res.Reason)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(e.Out.Err, "warning: %s\n", w)
			}
			if f.JSON {
				return e.Out.Encode(res)
			}
			if res.Renewed {
				e.Out.Printf("lease renewed: %s epoch %d at %s until %s (duration %s)\n", res.Record.WriterNode, res.Record.Epoch, res.Record.RenewedAt.Format(time.RFC3339), res.Record.LeaseExpiresAt.Format(time.RFC3339), res.Duration)
			} else {
				// stderr even with --quiet: this line is what the timer's journal shows.
				fmt.Fprintf(e.Out.Err, "lease not renewed after %s: %s\n", res.Duration, res.Reason)
				return fmt.Errorf("lease not renewed: %s", res.Reason)
			}
			return nil
		},
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Show writer.json and each witness's view",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			de, c, err := drEngine(cmd, e, true)
			if err != nil {
				return err
			}
			defer de.Close()
			l := nodestack.LocalLayout(e.Paths.ConfigDir, e.Paths.StateDir)
			local, lerr := lease.ReadFile(l.WriterFile())
			best, votes := lease.Observe(cmd.Context(), de.Witnesses, time.Now().UTC())
			view := map[string]any{"registryWriter": c.Writer, "registryEpoch": c.Epoch, "leaseMode": c.LeaseMode, "local": local, "witnessBest": best, "votes": votes}
			if lerr != nil {
				view["localError"] = lerr.Error()
			}
			if f.JSON {
				return e.Out.Encode(view)
			}
			e.Out.Printf("registry: writer %s epoch %d, lease mode %q\n", c.Writer, c.Epoch, c.LeaseMode)
			if lerr != nil {
				e.Out.Printf("local writer.json: %v\n", lerr)
			} else {
				e.Out.Printf("local writer.json: %s epoch %d until %s (valid %v)\n", local.WriterNode, local.Epoch, local.LeaseExpiresAt.Format(time.RFC3339), local.Valid(time.Now()))
			}
			for _, v := range votes {
				if v.Error != "" {
					e.Out.Printf("witness %s: ERROR %s\n", v.Witness, v.Error)
					continue
				}
				e.Out.Printf("witness %s: %s epoch %d until %s\n", v.Witness, v.Stored.WriterNode, v.Stored.Epoch, v.Stored.LeaseExpiresAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	install := &cobra.Command{
		Use:   "install",
		Short: "Install/enable the lease timer on this node (writer only renews)",
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			de, _, err := drEngine(cmd, e, true)
			if err != nil {
				return err
			}
			defer de.Close()
			if e.flags.DryRun {
				return nil
			}
			if de.Self != de.Cluster.Writer {
				return fmt.Errorf("lease timers must be installed on writer %s", de.Cluster.Writer)
			}
			if err := de.InstallTimer(cmd.Context(), de.Self, qabasCmd); err != nil {
				return wrapErr(err)
			}
			e.Out.Printf("%s enabled on %s\n", dr.LeaseTimer, de.Self)
			return nil
		},
	}
	mode := &cobra.Command{
		Use:   "mode <local-only|strict>",
		Short: "Explicitly select the writer lease safety policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				c, err := e.cluster()
				if err != nil {
					return err
				}
				if err := c.ValidateLeaseMode(args[0]); err != nil {
					return err
				}
				if args[0] == cluster.LeaseModeStrict {
					de, err := e.newDREngine(c, engineOpts{identity: identityFlag(cmd), offline: true})
					if err != nil {
						return err
					}
					defer de.Close()
					for _, w := range de.Witnesses {
						probe, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
						_, err := w.Get(probe)
						cancel()
						if err != nil {
							return fmt.Errorf("witness %s is not reachable: %w", w.Name(), err)
						}
					}
				}
				old := c.LeaseMode
				if old == args[0] {
					e.Out.Printf("lease mode already %s\n", old)
					return nil
				}
				if e.flags.DryRun {
					e.Out.Printf("would change lease mode from %q to %q\n", old, args[0])
					return nil
				}
				c.LeaseMode = args[0]
				if err := cluster.Save(e.Paths, c); err != nil {
					return err
				}
				audit(e, "dr-lease-mode", old, c.LeaseMode, nil, "explicit policy selection")
				e.Out.Printf("lease mode changed from %q to %q; sync the registry to every node before renewal\n", old, c.LeaseMode)
				if c.LeaseMode == cluster.LeaseModeLocalOnly {
					e.Out.Warnf("local-only has no independent quorum fencing and prohibits cross-node promotion\n")
				}
				return nil
			})
		},
	}
	cmd.AddCommand(renew, show, install, mode)
	return cmd
}

func newDRWitnessCmd(f *flags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "witness",
		Short: "Lease witnesses: independent votes that prevent two writers",
		Long: `Witnesses store the writer lease. A writer keeps writing only while a majority of
witnesses renews its lease; a new writer can be promoted only after that lease expired.
Run at least 3 witnesses in different failure domains (e.g. a small cloud VM, a second
office, a NAS). Each runs: qabas dr witness serve.`,
		Args: argsWithSuggestions,
		RunE: parentHelp(),
	}
	var url, path, failureDomain string
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Register a witness (http URL or local file path)",
		Example: `  sudo qabas dr witness add cloud1 --url https://witness1.example.com:7443
  sudo qabas dr witness add nas --path /mnt/nas/qabas-witness.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			if (url == "") == (path == "") {
				return exitcode.New(exitcode.InvalidInput, "pass exactly one of --url or --path")
			}
			return e.withLock(func() error {
				c, err := e.cluster()
				if err != nil {
					return err
				}
				w := cluster.Witness{Name: args[0], Kind: "http", URL: url, FailureDomain: failureDomain}
				if path != "" {
					w = cluster.Witness{Name: args[0], Kind: "file", Path: path, FailureDomain: failureDomain}
				}
				replaced := false
				for i := range c.Witnesses {
					if c.Witnesses[i].Name == w.Name {
						c.Witnesses[i], replaced = w, true
					}
				}
				if !replaced {
					c.Witnesses = append(c.Witnesses, w)
				}
				if e.flags.DryRun {
					return nil
				}
				if _, err := cluster.EnsureSecret(e.Paths, cluster.SharedScope, cluster.SecretWitnessToken); err != nil {
					return err
				}
				if err := cluster.Save(e.Paths, c); err != nil {
					return err
				}
				audit(e, "dr-witness-add", "", w.Name, nil, w.Kind)
				e.Out.Printf("witness %s registered (%d total, quorum %d)\n", w.Name, len(c.Witnesses), lease.Quorum(len(c.Witnesses)))
				if w.Kind == "http" {
					e.Out.Printf("copy %s to the witness host (0600) and run there:\n  qabas dr witness serve --listen :7443 --token-file <copied file> --tls-cert ... --tls-key ...\n",
						cluster.NodeSecretsDir(e.Paths, cluster.SharedScope)+"/"+cluster.SecretWitnessToken)
				}
				e.Out.Println("then push the registry to every node: sudo qabas replica sync --all")
				return nil
			})
		},
	}
	add.Flags().StringVar(&url, "url", "", "witness base URL (qabas dr witness serve)")
	add.Flags().StringVar(&path, "path", "", "witness file path (shared storage only this cluster writes)")
	add.Flags().StringVar(&failureDomain, "failure-domain", "", "independent host/site failure domain for strict mode")
	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a registered witness (strict renewal may fence)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			return e.withLock(func() error {
				c, err := e.cluster()
				if err != nil {
					return err
				}
				found := false
				remaining := make([]cluster.Witness, 0, len(c.Witnesses))
				for _, w := range c.Witnesses {
					if w.Name == args[0] {
						found = true
						continue
					}
					remaining = append(remaining, w)
				}
				if !found {
					return fmt.Errorf("witness %q is not registered", args[0])
				}
				if e.flags.DryRun {
					e.Out.Printf("would remove witness %s\n", args[0])
					return nil
				}
				c.Witnesses = remaining
				if err := cluster.Save(e.Paths, c); err != nil {
					return err
				}
				audit(e, "dr-witness-remove", args[0], "", nil, "explicit witness removal")
				e.Out.Printf("witness %s removed; sync the registry to every node\n", args[0])
				return nil
			})
		},
	}

	var listen, store, tokenFile, certFile, keyFile string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run a lease witness server (on a third host)",
		Example: `  qabas dr witness serve --listen :7443 --store /var/lib/qabas-witness/lease.json \
    --token-file /etc/qabas-witness/token --tls-cert cert.pem --tls-key key.pem`,
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(tokenFile)
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, fmt.Errorf("token file: %w", err))
			}
			token := strings.TrimSpace(string(raw))
			if len(token) < 16 {
				return exitcode.New(exitcode.InvalidInput, "token too short")
			}
			srv := &http.Server{
				Addr:              listen,
				Handler:           lease.Handler(&lease.FileWitness{Label: "local", Path: store}, token),
				ReadHeaderTimeout: 5 * time.Second,
			}
			go func() {
				ch := make(chan os.Signal, 1)
				signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
				<-ch
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Shutdown(ctx)
			}()
			fmt.Fprintf(os.Stderr, "qabas witness listening on %s (store %s)\n", listen, store)
			if certFile != "" {
				err = srv.ListenAndServeTLS(certFile, keyFile)
			} else {
				fmt.Fprintln(os.Stderr, "warning: serving without TLS; only expose it over WireGuard or a private network")
				err = srv.ListenAndServe()
			}
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		},
	}
	serve.Flags().StringVar(&listen, "listen", ":7443", "listen address")
	serve.Flags().StringVar(&store, "store", "/var/lib/qabas-witness/lease.json", "lease store file")
	serve.Flags().StringVar(&tokenFile, "token-file", "/etc/qabas-witness/token", "bearer token file (copied from the cluster)")
	serve.Flags().StringVar(&certFile, "tls-cert", "", "TLS certificate (PEM)")
	serve.Flags().StringVar(&keyFile, "tls-key", "", "TLS private key (PEM)")
	cmd.AddCommand(add, remove, serve)
	return cmd
}

func newDRRoutingCmd(f *flags) *cobra.Command {
	var site, apiDomain, outFile string
	var port int
	cmd := &cobra.Command{
		Use:   "routing",
		Short: "Render the site HAProxy config and DNS/tunnel notes for the current writer",
		Example: `  qabas dr routing --site branch --api-domain api.qabas.one > /etc/haproxy/haproxy.cfg
  sudo qabas dr routing --site branch --out /etc/haproxy/haproxy.cfg`,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			c, err := e.cluster()
			if err != nil {
				return err
			}
			if site == "" {
				if n := c.Node(e.selfName(c)); n != nil {
					site = n.Site
				}
			}
			if apiDomain == "" {
				if b, err := os.ReadFile(e.Paths.EnvCompose()); err == nil {
					apiDomain = nodestack.ParseEnv(b)["API_DOMAIN"]
				}
			}
			cfg, err := dr.RenderHAProxy(c, dr.RoutingOptions{Site: site, HealthHost: apiDomain, Port: port})
			if err != nil {
				return exitcode.Wrap(exitcode.InvalidInput, err)
			}
			var st cluster.Site
			for _, s := range c.Sites {
				if s.Name == site {
					st = s
				}
			}
			if outFile != "" && !e.flags.DryRun {
				if err := os.WriteFile(outFile, []byte(cfg), 0o644); err != nil {
					return err
				}
				e.Out.Printf("wrote %s (reload haproxy to apply)\n", outFile)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), cfg)
			}
			for _, n := range dr.RoutingNotes(c, st) {
				fmt.Fprintln(os.Stderr, "# "+n)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&site, "site", "", "site to render for (default: this node's site)")
	cmd.Flags().StringVar(&apiDomain, "api-domain", "", "API domain for health checks (default: API_DOMAIN from .env)")
	cmd.Flags().StringVar(&outFile, "out", "", "write the config to this file instead of stdout")
	cmd.Flags().IntVar(&port, "port", 443, "HAProxy listen port")
	return cmd
}
