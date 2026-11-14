package cli

import (
	"strings"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/exitcode"
	"github.com/qabasone/qabasone-cli/internal/guide"
	"github.com/spf13/cobra"
)

func newGuideCmd(f *flags) *cobra.Command {
	var o guide.Overrides
	var noDetect bool
	cmd := &cobra.Command{
		Use:   "guide [topic]",
		Short: "Step-by-step walkthroughs with commands pre-filled for this machine",
		Long: `Print an A-to-Z walkthrough for a task: where to run each step, the exact
commands, and what is already done on this machine.

Without a topic, shows what was detected about this machine, suggested next
topics, and the topic list. Detection is read-only; --no-detect prints the
generic version with <placeholders>.`,
		Example: `  qabas guide
  qabas guide replica                  # add this machine as a read-only replica
  qabas guide replica --name dr1 --host 203.0.113.10
  qabas guide backup
  qabas guide dr --no-detect
  qabas guide schedule --json`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeGuideTopics,
		RunE: func(cmd *cobra.Command, args []string) error {
			e := load(f)
			var facts guide.Facts
			if !noDetect {
				pr := guide.DefaultProbes()
				pr.SelfName = func(c *cluster.Cluster) string { return e.selfName(c) }
				facts = guide.Detect(e.Paths, pr)
			}
			out := cmd.OutOrStdout()
			e.Out.Out = out
			if len(args) == 0 {
				if f.JSON {
					return e.Out.Encode(guide.Index(facts))
				}
				guide.RenderIndex(out, facts)
				return nil
			}
			t, ok := guide.Find(args[0])
			if !ok {
				h := "list topics with: qabas guide"
				if s := guide.Suggest(args[0]); len(s) > 0 {
					h = "did you mean: qabas guide " + strings.Join(s, " | ")
				}
				return exitcode.NewHint(exitcode.InvalidInput, h, "unknown guide topic %q", args[0])
			}
			v := guide.NewVars(facts, o)
			d := t.Build(facts, v)
			if f.JSON {
				return e.Out.Encode(guide.JSONDoc{Topic: t.ID, Title: t.Title, Summary: t.Summary, Facts: facts, Vars: v, Doc: d})
			}
			guide.RenderTopic(out, t, facts, d)
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Name, "name", "", "node name to use in commands (default: from hostname / registry)")
	cmd.Flags().StringVar(&o.Host, "host", "", "replica address to use in commands (default: this machine's first IPv4)")
	cmd.Flags().StringVar(&o.Writer, "writer", "", "writer node name to use in commands (default: from the registry)")
	cmd.Flags().BoolVar(&noDetect, "no-detect", false, "do not inspect this machine; print generic placeholders")
	return cmd
}

func completeGuideTopics(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, t := range guide.Topics() {
		if strings.HasPrefix(t.ID, toComplete) {
			out = append(out, t.ID+"\t"+t.Title)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
