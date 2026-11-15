package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/backup"
	"github.com/qabas1/qabas1-cli/internal/config"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/release"
	"github.com/spf13/cobra"
)

func completeBackupIDs(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	items, err := backup.List(paths.Resolve())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	used := make(map[string]struct{}, len(args))
	for _, a := range args {
		used[a] = struct{}{}
	}
	out := make([]string, 0, len(items))
	for _, m := range items {
		if _, ok := used[m.ID]; ok {
			continue
		}
		if toComplete == "" || strings.HasPrefix(m.ID, toComplete) {
			out = append(out, m.ID)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeBackupIDsOrFiles suggests Qabas backup ids and also allows filesystem paths
// for external vpg directories/archives.
func completeBackupIDsOrFiles(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	ids, _ := completeBackupIDs(cmd, args, toComplete)
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	// Prefer ids when the token looks like a qb- id; otherwise allow file completion too.
	if strings.HasPrefix(toComplete, "qb-") {
		return ids, cobra.ShellCompDirectiveNoFileComp
	}
	return ids, cobra.ShellCompDirectiveDefault
}

func completeReleaseVersions(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	p := paths.Resolve()
	cfg, err := config.Load(p)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	rels, err := release.New(cfg.Deploy.Repo, cfg.Deploy.Ref).List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(rels)+1)
	if toComplete == "" || strings.HasPrefix("dev", toComplete) {
		out = append(out, "dev")
	}
	for _, r := range rels {
		v := r.Release.Version
		if toComplete == "" || strings.HasPrefix(v, toComplete) {
			out = append(out, v)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeDeployArgs suggests release versions, "dev", and product services for scoped --dev.
func completeDeployArgs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	versions, directive := completeReleaseVersions(cmd, args, toComplete)
	if len(args) > 0 {
		return versions, directive
	}
	services := []string{"api", "app", "dash", "siraj"}
	out := append([]string(nil), versions...)
	seen := make(map[string]struct{}, len(out))
	for _, v := range out {
		seen[v] = struct{}{}
	}
	for _, s := range services {
		if _, ok := seen[s]; ok {
			continue
		}
		if toComplete == "" || strings.HasPrefix(s, toComplete) {
			out = append(out, s)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeLogServices(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	services := []string{
		"api", "app", "dash", "siraj", "postgres", "redis", "minio",
		"gotenberg", "jsreport", "printables", "documents", "operations", "notifications",
		"audit-cleanup",
	}
	out := make([]string, 0, len(services))
	for _, s := range services {
		if toComplete == "" || strings.HasPrefix(s, toComplete) {
			out = append(out, s)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeAuthKind(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	kinds := []string{"github", "ghcr"}
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		if toComplete == "" || strings.HasPrefix(k, toComplete) {
			out = append(out, k)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeChannel(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	channels := []string{"stable", "rc", "beta", "nightly", "dev"}
	out := make([]string, 0, len(channels))
	for _, c := range channels {
		if toComplete == "" || strings.HasPrefix(c, toComplete) {
			out = append(out, c)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// parentHelp makes management parents print help when invoked with no subcommand.
func parentHelp() func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	}
}

// argsWithSuggestions is like cobra.NoArgs but keeps typo suggestions (NoArgs omits them).
func argsWithSuggestions(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	msg := fmt.Sprintf("unknown command %q for %q", args[0], cmd.CommandPath())
	if suggestions := cmd.SuggestionsFor(args[0]); len(suggestions) > 0 {
		msg += "\n\nDid you mean this?\n"
		for _, s := range suggestions {
			msg += "\t" + s + "\n"
		}
	}
	return errors.New(msg)
}
