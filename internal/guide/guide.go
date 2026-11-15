package guide

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/hint"
)

// Where a section's commands are run.
const (
	WhereHere    = "this machine"
	WhereWriter  = "the writer"
	WhereReplica = "the replica"
	WhereWitness = "a witness host"
	WhereAny     = "any node"
)

// State of one step on this machine.
type State string

const (
	Unknown State = ""
	Done    State = "done"
	Todo    State = "todo"
	Blocked State = "blocked"
)

// Status is a step's detected progress.
type Status struct {
	State  State  `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Step is one action with the commands that perform it.
type Step struct {
	Text     string   `json:"text"`
	Notes    []string `json:"notes,omitempty"`
	Commands []string `json:"commands,omitempty"`
	Status   Status   `json:"status"`
}

// Section groups steps run in the same place.
type Section struct {
	Title string   `json:"title"`
	Where string   `json:"where,omitempty"`
	Intro []string `json:"intro,omitempty"`
	Steps []Step   `json:"steps"`
}

// QA is a troubleshooting entry: a symptom and its fix.
type QA struct {
	Problem  string   `json:"problem"`
	Fix      string   `json:"fix"`
	Commands []string `json:"commands,omitempty"`
}

// Doc is a rendered walkthrough for one topic on this machine.
type Doc struct {
	Intro           []string  `json:"intro,omitempty"`
	Sections        []Section `json:"sections"`
	Troubleshooting []QA      `json:"troubleshooting,omitempty"`
	SeeAlso         []string  `json:"seeAlso,omitempty"`
}

// Topic is one walkthrough.
type Topic struct {
	ID      string
	Aliases []string
	Title   string
	Summary string
	Build   func(f Facts, v Vars) Doc
}

// Topics returns every topic in display order.
func Topics() []Topic {
	return []Topic{
		installTopic(), replicaTopic(), backupTopic(), scheduleTopic(),
		pitrTopic(), drTopic(), maintenanceTopic(),
	}
}

// Find resolves a topic by id or alias.
func Find(name string) (Topic, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, t := range Topics() {
		if t.ID == name {
			return t, true
		}
		for _, a := range t.Aliases {
			if a == name {
				return t, true
			}
		}
	}
	return Topic{}, false
}

// Suggest returns the topic ids closest to an unknown name.
func Suggest(name string) []string {
	name = strings.ToLower(name)
	type cand struct {
		id   string
		dist int
	}
	var cands []cand
	for _, t := range Topics() {
		best := levenshtein(name, t.ID)
		for _, a := range t.Aliases {
			if d := levenshtein(name, a); d < best {
				best = d
			}
		}
		if (name != "" && strings.HasPrefix(t.ID, name)) || best <= min(2, len(name)/2) {
			cands = append(cands, cand{t.ID, best})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.id)
	}
	return out
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Recommendation points at the next useful topic.
type Recommendation struct {
	Topic string `json:"topic"`
	Why   string `json:"why"`
}

// Recommend suggests topics for this machine, most urgent first.
func Recommend(f Facts) []Recommendation {
	if !f.Detected {
		return nil
	}
	var out []Recommendation
	add := func(t, why string) { out = append(out, Recommendation{t, why}) }
	switch f.Role {
	case RoleFresh:
		if f.OS != "" && f.OS != "linux" {
			add("install", "this machine is "+f.OS+": product hosts and replicas must be Linux (the CLI works here)")
			add("replica", "commands to run on the writer to add a Linux replica")
			break
		}
		add("replica", "make this machine a read-only replica of an existing writer")
		add("install", "or install QabasOne here as a new writer")
	case RoleSingle:
		if f.Backups == 0 {
			add("backup", "no local backups exist yet")
		}
		if !f.ScheduleEnabled {
			add("schedule", "automatic daily backups are not enabled")
		}
		add("replica", "add a read-only replica (another site / disaster recovery)")
		if !f.PITREnabled {
			add("pitr", "point-in-time recovery is not enabled")
		}
	case RoleWriter:
		for _, n := range f.Standbys() {
			if n.LastError != "" || (n.Phase != "" && n.Phase != "READY") {
				add("replica", fmt.Sprintf("replica %s stopped during provisioning (%s)", n.Name, n.Phase))
				break
			}
		}
		if !f.ScheduleEnabled {
			add("schedule", "automatic daily backups are not enabled")
		} else if f.Offsite == "" && len(f.Standbys()) > 0 {
			add("schedule", "scheduled backups are not copied off-site to a replica")
		}
		if !f.PITREnabled {
			add("pitr", "point-in-time recovery is not enabled")
		}
		if len(f.Standbys()) == 0 {
			add("replica", "the cluster has no replica yet")
		}
		add("dr", "rehearse switchover / failover before you need it")
	case RoleReplica:
		add("replica", "check replication health of this replica")
		add("dr", "what to do if the writer goes down")
	}
	return out
}

// RenderIndex prints the machine summary, recommendations, and topic list.
func RenderIndex(w io.Writer, f Facts) {
	if f.Detected {
		RenderMachine(w, f)
		if recs := Recommend(f); len(recs) > 0 {
			fmt.Fprintln(w, "Suggested next:")
			for _, r := range recs {
				fmt.Fprintf(w, "  qabas guide %-12s %s\n", r.Topic, r.Why)
			}
			fmt.Fprintln(w)
		}
	}
	fmt.Fprintln(w, "Topics:")
	for _, t := range Topics() {
		fmt.Fprintf(w, "  %-12s %s\n", t.ID, t.Title)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run `qabas guide <topic>` for a step-by-step walkthrough with commands")
	fmt.Fprintln(w, "pre-filled for this machine (`--no-detect` prints the generic version).")
}

// RenderMachine prints the "This machine" block.
func RenderMachine(w io.Writer, f Facts) {
	fmt.Fprintln(w, "This machine")
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %-11s %s\n", k+":", v)
		}
	}
	row("hostname", f.Hostname)
	row("addresses", strings.Join(f.IPs, ", "))
	if f.OS != "" {
		row("platform", f.OS+"/"+f.Arch)
	}
	docker := "not installed"
	if f.Docker {
		docker = "installed " + f.DockerVersion
		if f.Compose {
			docker += ", compose v2 ok"
		} else {
			docker += ", compose v2 MISSING"
		}
	}
	row("docker", strings.TrimSpace(docker))
	row("systemd", yesNo(f.Systemd))
	if f.SSHD != nil {
		row("ssh server", yesNo(*f.SSHD))
	}
	if f.HostKey != "" {
		row("host key", f.HostKey+" ("+f.HostKeyType+")")
	}
	release := "not installed"
	if f.Installed {
		release = f.Release
	}
	row("qabasone", release)
	row("role", f.RoleText())
	if len(f.Nodes) > 0 {
		var ns []string
		for _, n := range f.Nodes {
			s := n.Name
			if n.Name == f.Writer {
				s += "*"
			}
			ns = append(ns, s)
		}
		row("nodes", strings.Join(ns, ", ")+"  (* writer)")
	}
	fmt.Fprintln(w)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// RenderTopic prints a full walkthrough.
func RenderTopic(w io.Writer, t Topic, f Facts, d Doc) {
	title := strings.ToUpper(t.ID) + ": " + t.Title
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, strings.Repeat("=", len(title)))
	writeWrapped(w, "", t.Summary)
	fmt.Fprintln(w)
	if f.Detected {
		fmt.Fprintf(w, "Detected: %s", f.RoleText())
		if f.Hostname != "" {
			fmt.Fprintf(w, " | host %s", f.Hostname)
		}
		if len(f.IPs) > 0 {
			fmt.Fprintf(w, " (%s)", f.IPs[0])
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w)
	}
	for _, p := range d.Intro {
		writeWrapped(w, "", p)
		fmt.Fprintln(w)
	}
	for i, s := range d.Sections {
		head := fmt.Sprintf("%d. %s", i+1, s.Title)
		if s.Where != "" {
			head += "   [run on: " + s.Where + "]"
		}
		fmt.Fprintln(w, head)
		fmt.Fprintln(w, strings.Repeat("-", min(len(head), 78)))
		for _, p := range s.Intro {
			writeWrapped(w, "   ", p)
		}
		local := strings.HasPrefix(s.Where, WhereHere) || s.Where == WhereAny || s.Where == ""
		for j, st := range s.Steps {
			writeWrappedFirst(w, fmt.Sprintf("   %d.%d %s", i+1, j+1, mark(st.Status)), "        ", st.Text)
			if st.Status.State != Unknown && st.Status.Reason != "" {
				writeWrapped(w, "        ", "detected: "+st.Status.Reason)
			}
			for _, n := range st.Notes {
				writeWrapped(w, "        ", n)
			}
			for _, c := range st.Commands {
				if local {
					c = hint.Localize(c)
				}
				fmt.Fprintf(w, "          $ %s\n", c)
			}
		}
		fmt.Fprintln(w)
	}
	if len(d.Troubleshooting) > 0 {
		fmt.Fprintln(w, "Troubleshooting")
		fmt.Fprintln(w, "---------------")
		for _, q := range d.Troubleshooting {
			writeWrappedFirst(w, "   problem: ", "            ", q.Problem)
			writeWrappedFirst(w, "   fix:     ", "            ", q.Fix)
			for _, c := range q.Commands {
				fmt.Fprintf(w, "            $ %s\n", c)
			}
			fmt.Fprintln(w)
		}
	}
	if len(d.SeeAlso) > 0 {
		fmt.Fprintln(w, "See also: "+strings.Join(d.SeeAlso, ", "))
	}
}

func mark(s Status) string {
	switch s.State {
	case Done:
		return "[done] "
	case Todo:
		return "[todo] "
	case Blocked:
		return "[!!] "
	}
	return ""
}

const wrapWidth = 80

func writeWrapped(w io.Writer, indent, text string) {
	writeWrappedFirst(w, indent, indent, text)
}

// writeWrappedFirst word-wraps text; the first line starts with first, the rest with indent.
func writeWrappedFirst(w io.Writer, first, indent, text string) {
	for pi, para := range strings.Split(text, "\n") {
		prefix := first
		if pi > 0 {
			prefix = indent
		}
		line := prefix
		empty := true
		for _, word := range strings.Fields(para) {
			if !empty && len(line)+1+len(word) > wrapWidth {
				fmt.Fprintln(w, line)
				line, empty = indent, true
			}
			if empty {
				line += word
			} else {
				line += " " + word
			}
			empty = false
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
}

// JSONDoc is the --json shape of a topic.
type JSONDoc struct {
	Topic   string `json:"topic"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Facts   Facts  `json:"facts"`
	Vars    Vars   `json:"vars"`
	Doc
}

// JSONIndex is the --json shape of `qabas guide` without a topic.
type JSONIndex struct {
	Facts           Facts            `json:"facts"`
	Recommendations []Recommendation `json:"recommendations,omitempty"`
	Topics          []JSONTopic      `json:"topics"`
}

// JSONTopic lists one topic in the index.
type JSONTopic struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
	Title   string   `json:"title"`
}

// Index returns the --json index.
func Index(f Facts) JSONIndex {
	idx := JSONIndex{Facts: f, Recommendations: Recommend(f)}
	for _, t := range Topics() {
		idx.Topics = append(idx.Topics, JSONTopic{ID: t.ID, Aliases: t.Aliases, Title: t.Title})
	}
	return idx
}
