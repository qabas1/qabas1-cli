package backup

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/exitcode"
)

// SelectOptions controls which backups Select returns for deletion.
//
// Include set is the union of exact IDs, --id regexes, --name regexes, and --all.
// When only Except / ExceptName are set (no include selectors), the include set is all metas.
// Except / ExceptName then remove matching backups from the include set.
type SelectOptions struct {
	IDs          []string // exact backup IDs
	IDPatterns   []string // Go RE2 regexes against ID
	NamePatterns []string // Go RE2 regexes against Name (empty names never match)
	Except       string   // Go RE2 regex against ID: keep matches, drop from selection
	ExceptName   string   // Go RE2 regex against Name: keep matches, drop from selection
	All          bool
}

// Select returns the backups matching opts, in the same relative order as metas.
func Select(metas []Meta, opts SelectOptions) ([]Meta, error) {
	hasInclude := opts.All || len(opts.IDs) > 0 || len(opts.IDPatterns) > 0 || len(opts.NamePatterns) > 0
	hasExcept := opts.Except != "" || opts.ExceptName != ""
	if !hasInclude && !hasExcept {
		return nil, exitcode.New(exitcode.InvalidInput,
			"specify backup IDs, --id, --name, --except/-v, --except-name, or --all")
	}

	idRes, err := compilePatterns("--id", opts.IDPatterns)
	if err != nil {
		return nil, err
	}
	nameRes, err := compilePatterns("--name", opts.NamePatterns)
	if err != nil {
		return nil, err
	}
	exceptRe, err := compileOptional("--except/-v", opts.Except)
	if err != nil {
		return nil, err
	}
	exceptNameRe, err := compileOptional("--except-name", opts.ExceptName)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]Meta, len(metas))
	for _, m := range metas {
		byID[m.ID] = m
	}

	selected := make(map[string]struct{})
	if hasInclude {
		for _, id := range opts.IDs {
			if _, ok := byID[id]; !ok {
				return nil, exitcode.New(exitcode.BackupRestore, "backup %s not found", id)
			}
			selected[id] = struct{}{}
		}
		if opts.All {
			for _, m := range metas {
				selected[m.ID] = struct{}{}
			}
		}
		for _, m := range metas {
			if matchAny(m.ID, idRes) {
				selected[m.ID] = struct{}{}
			}
			if m.Name != "" && matchAny(m.Name, nameRes) {
				selected[m.ID] = struct{}{}
			}
		}
	} else {
		// Only except filters: start from all managed backups.
		for _, m := range metas {
			selected[m.ID] = struct{}{}
		}
	}

	for id := range selected {
		m := byID[id]
		if exceptRe != nil && exceptRe.MatchString(m.ID) {
			delete(selected, id)
			continue
		}
		if exceptNameRe != nil && m.Name != "" && exceptNameRe.MatchString(m.Name) {
			delete(selected, id)
		}
	}

	out := make([]Meta, 0, len(selected))
	for _, m := range metas {
		if _, ok := selected[m.ID]; ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return nil, exitcode.New(exitcode.InvalidInput, "no backups matched the selection")
	}
	return out, nil
}

func compilePatterns(flag string, patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, exitcode.New(exitcode.InvalidInput, "invalid %s regex %q: %v", flag, p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func compileOptional(flag, pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, exitcode.New(exitcode.InvalidInput, "invalid %s regex %q: %v", flag, pattern, err)
	}
	return re, nil
}

func matchAny(s string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// ResolveOne picks exactly one managed backup for commands like backup cp.
//
// Resolution order:
//  1. Exact ID match
//  2. Exact Name match (empty names never match)
//  3. Selector as Go RE2 against ID or Name (empty names never match name)
//
// Zero matches → BackupRestore "not found".
// Multiple matches → InvalidInput listing candidates.
// Invalid regex → InvalidInput.
func ResolveOne(metas []Meta, selector string) (Meta, error) {
	if strings.TrimSpace(selector) == "" {
		return Meta{}, exitcode.New(exitcode.InvalidInput, "backup selector is required")
	}

	for _, m := range metas {
		if m.ID == selector {
			return m, nil
		}
	}
	for _, m := range metas {
		if m.Name != "" && m.Name == selector {
			return m, nil
		}
	}

	re, err := regexp.Compile(selector)
	if err != nil {
		return Meta{}, exitcode.New(exitcode.InvalidInput, "invalid selector regex %q: %v", selector, err)
	}

	var matched []Meta
	for _, m := range metas {
		if re.MatchString(m.ID) || (m.Name != "" && re.MatchString(m.Name)) {
			matched = append(matched, m)
		}
	}
	switch len(matched) {
	case 0:
		return Meta{}, exitcode.New(exitcode.BackupRestore, "backup %q not found", selector)
	case 1:
		return matched[0], nil
	default:
		refs := make([]string, len(matched))
		for i, m := range matched {
			if m.Name != "" {
				refs[i] = fmt.Sprintf("%s (%s)", m.ID, m.Name)
			} else {
				refs[i] = m.ID
			}
		}
		return Meta{}, exitcode.New(exitcode.InvalidInput,
			"selector %q matched %d backups; refine it to match exactly one: %s",
			selector, len(matched), strings.Join(refs, ", "))
	}
}

// Filter returns backups matching any of the given ID or name Go RE2 patterns (OR).
// When both pattern slices are empty, metas is returned unchanged.
// No matches yields an empty slice (not an error). Invalid regex is an error.
// Empty backup names never match name patterns.
func Filter(metas []Meta, idPatterns, namePatterns []string) ([]Meta, error) {
	if len(idPatterns) == 0 && len(namePatterns) == 0 {
		return metas, nil
	}
	idRes, err := compilePatterns("--id", idPatterns)
	if err != nil {
		return nil, err
	}
	nameRes, err := compilePatterns("--name", namePatterns)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(metas))
	for _, m := range metas {
		if matchAny(m.ID, idRes) {
			out = append(out, m)
			continue
		}
		if m.Name != "" && matchAny(m.Name, nameRes) {
			out = append(out, m)
		}
	}
	return out, nil
}
