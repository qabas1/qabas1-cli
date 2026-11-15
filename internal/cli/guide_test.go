package cli_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/cli"
	"github.com/qabas1/qabas1-cli/internal/guide"
	"github.com/spf13/cobra"
)

func guideFixtures() []guide.Facts {
	base := guide.Facts{Detected: true, OS: "linux", Arch: "amd64", Hostname: "branch-01",
		IPs: []string{"10.20.0.5"}, HostKey: "SHA256:abc", Docker: true, Compose: true, Systemd: true}
	cluster := []guide.NodeFact{
		{Name: "hq", Role: "primary"},
		{Name: "dr1", Role: "standby", Promotable: true, Phase: "READY"},
		{Name: "kiosk", Role: "standby", Phase: "SEED", LastError: "timeout"},
	}
	fresh := base
	fresh.Role = guide.RoleFresh
	single := base
	single.Role, single.Installed = guide.RoleSingle, true
	writer := single
	writer.Role, writer.Cluster, writer.Writer, writer.Self, writer.Nodes = guide.RoleWriter, "qabasone", "hq", "hq", cluster
	replica := writer
	replica.Role, replica.Self = guide.RoleReplica, "dr1"
	return []guide.Facts{{}, fresh, single, writer, replica}
}

// shellSplit splits a command line into words, honouring single and double quotes.
func shellSplit(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	in := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

// qabasInvocations extracts every `qabas ...` invocation from a shell line.
func qabasInvocations(line string) [][]string {
	var out [][]string
	for _, seg := range strings.FieldsFunc(line, func(r rune) bool { return r == '|' || r == ';' }) {
		for _, part := range strings.Split(seg, "&&") {
			w := shellSplit(strings.TrimSpace(part))
			for len(w) > 0 && (w[0] == "sudo" || w[0] == "-E") {
				w = w[1:]
			}
			if len(w) > 0 && w[0] == "qabas" {
				out = append(out, w[1:])
			}
		}
	}
	return out
}

func lookupFlag(c *cobra.Command, tok string) bool {
	if strings.HasPrefix(tok, "--") {
		name := strings.SplitN(strings.TrimPrefix(tok, "--"), "=", 2)[0]
		if name == "help" {
			return true
		}
		return c.Flags().Lookup(name) != nil || c.InheritedFlags().Lookup(name) != nil
	}
	short := strings.TrimPrefix(tok, "-")[:1]
	return c.Flags().ShorthandLookup(short) != nil || c.InheritedFlags().ShorthandLookup(short) != nil
}

func checkInvocation(t *testing.T, where string, args []string) {
	t.Helper()
	root := cli.NewRoot()
	var path []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		path = append(path, a)
	}
	c, rest, err := root.Find(path)
	if err != nil {
		t.Errorf("%s: qabas %s: %v", where, strings.Join(args, " "), err)
		return
	}
	if err := c.ValidateArgs(rest); err != nil {
		t.Errorf("%s: qabas %s: %v", where, strings.Join(args, " "), err)
		return
	}
	if c.Name() == "guide" && len(rest) > 0 {
		if _, ok := guide.Find(rest[0]); !ok {
			t.Errorf("%s: qabas %s: unknown guide topic", where, strings.Join(args, " "))
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") && len(a) > 1 && !lookupFlag(c, a) {
			t.Errorf("%s: qabas %s: %q has no flag %s", where, strings.Join(args, " "), c.CommandPath(), a)
		}
	}
}

func TestGuideCommandsExist(t *testing.T) {
	n := 0
	for _, tp := range guide.Topics() {
		for _, f := range guideFixtures() {
			d := tp.Build(f, guide.NewVars(f, guide.Overrides{}))
			var lines []string
			for _, s := range d.Sections {
				for _, st := range s.Steps {
					lines = append(lines, st.Commands...)
				}
			}
			for _, q := range d.Troubleshooting {
				lines = append(lines, q.Commands...)
			}
			lines = append(lines, d.SeeAlso...)
			for _, l := range lines {
				for _, inv := range qabasInvocations(l) {
					checkInvocation(t, tp.ID+"/"+string(f.Role), inv)
					n++
				}
			}
		}
	}
	if n < 100 {
		t.Fatalf("only %d qabas invocations checked; extraction is broken", n)
	}
}

func runGuide(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(append([]string{"guide"}, args...))
	err := root.Execute()
	return buf.String(), err
}

func TestGuideIndexListsTopics(t *testing.T) {
	out, err := runGuide(t, "--no-detect")
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range guide.Topics() {
		if !strings.Contains(out, tp.ID) {
			t.Errorf("index missing topic %q:\n%s", tp.ID, out)
		}
	}
}

func TestGuideTopicNoDetect(t *testing.T) {
	out, err := runGuide(t, "replica", "--no-detect", "--name", "dr9", "--host", "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replica add dr9 --host 198.51.100.1") {
		t.Fatalf("overrides not applied:\n%s", out)
	}
}

func TestGuideUnknownTopic(t *testing.T) {
	_, err := runGuide(t, "replca", "--no-detect")
	if err == nil || !strings.Contains(err.Error(), "unknown guide topic") {
		t.Fatalf("want unknown topic error, got %v", err)
	}
}

func TestGuideJSON(t *testing.T) {
	root := cli.NewRoot()
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetArgs([]string{"guide", "backup", "--no-detect", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Topic    string `json:"topic"`
		Sections []any  `json:"sections"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if doc.Topic != "backup" || len(doc.Sections) == 0 {
		t.Fatalf("unexpected JSON: %s", buf.String())
	}
}

func TestShellSplitAndInvocations(t *testing.T) {
	got := qabasInvocations(`export GITHUB_TOKEN="$(gh auth token)" && sudo -E qabas auth login | cat`)
	if len(got) != 1 || strings.Join(got[0], " ") != "auth login" {
		t.Fatalf("got %v", got)
	}
	if w := shellSplit(`qabas backup ls --name '^pre deploy$'`); len(w) != 5 || w[4] != "^pre deploy$" {
		t.Fatalf("shellSplit = %q", w)
	}
}
