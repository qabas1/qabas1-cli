package guide

import (
	"bytes"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/hint"
	"github.com/qabas1/qabas1-cli/internal/host"
	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/state"
)

func fakeProbes(self string) Probes {
	yes := true
	return Probes{
		Host:        func() host.Report { return host.Report{OS: "linux", Arch: "amd64", Docker: true, Compose: true} },
		Hostname:    func() (string, error) { return "Branch-01.example.com", nil },
		IPs:         func() []string { return []string{"10.20.0.5"} },
		Systemd:     func() bool { return true },
		SSHD:        func() *bool { return &yes },
		HostKey:     func() (string, string) { return "SHA256:abc123", "ssh-ed25519" },
		Stack:       func() []string { return nil },
		DiskFreeMiB: func(string) int64 { return 50 * 1024 },
		SelfName:    func(*cluster.Cluster) string { return self },
	}
}

func tempPaths(t *testing.T) paths.Paths {
	d := t.TempDir()
	return paths.Paths{ConfigDir: d + "/etc", StateDir: d + "/lib", LogDir: d + "/log", LockPath: d + "/lock"}
}

func saveCluster(t *testing.T, p paths.Paths) {
	t.Helper()
	c := &cluster.Cluster{Name: "qabasone", Writer: "hq", Epoch: 3, Nodes: []cluster.Node{
		{Name: "hq", Role: cluster.RolePrimary, Host: "localhost", WGIP: "10.66.0.1", WGEndpoint: "hq.example.com:51820", Promotable: true},
		{Name: "dr1", Role: cluster.RoleStandby, Host: "203.0.113.10", Site: "branch", WGIP: "10.66.0.2", WGEndpoint: "203.0.113.10:51820", Mode: cluster.ModePush, SSHHostKey: "SHA256:x", Promotable: true},
	}}
	if err := cluster.Save(p, c); err != nil {
		t.Fatal(err)
	}
}

func TestDetectRoles(t *testing.T) {
	p := tempPaths(t)
	f := Detect(p, fakeProbes(""))
	if f.Role != RoleFresh || f.Installed {
		t.Fatalf("fresh: got role %q installed %v", f.Role, f.Installed)
	}
	if err := state.Save(p, state.State{CurrentRelease: "2.8.0"}); err != nil {
		t.Fatal(err)
	}
	if f = Detect(p, fakeProbes("")); f.Role != RoleSingle || f.Release != "2.8.0" {
		t.Fatalf("single: got role %q release %q", f.Role, f.Release)
	}
	saveCluster(t, p)
	if f = Detect(p, fakeProbes("hq")); f.Role != RoleWriter || len(f.Standbys()) != 1 {
		t.Fatalf("writer: got role %q standbys %v", f.Role, f.Standbys())
	}
	if f = Detect(p, fakeProbes("dr1")); f.Role != RoleReplica || f.Self != "dr1" || f.Writer != "hq" {
		t.Fatalf("replica: got %+v", f)
	}
}

func fixture(role Role) Facts {
	f := Facts{
		Detected: true, Hostname: "branch-01", OS: "linux", Arch: "amd64", IPs: []string{"10.20.0.5"},
		Docker: true, Compose: true, Systemd: true, HostKey: "SHA256:abc123", HostKeyType: "ssh-ed25519",
		Role: role,
	}
	switch role {
	case RoleSingle:
		f.Installed, f.Release = true, "2.8.0"
	case RoleWriter, RoleReplica:
		f.Installed, f.Release = true, "2.8.0"
		f.Cluster, f.Writer, f.Epoch = "qabasone", "hq", 3
		f.Nodes = []NodeFact{
			{Name: "hq", Role: "primary", Host: "localhost", Promotable: true},
			{Name: "dr1", Role: "standby", Host: "203.0.113.10", Site: "branch", Promotable: true, Phase: "READY"},
			{Name: "kiosk", Role: "standby", Host: "192.0.2.44", Phase: "SEED", LastError: "timeout"},
		}
		f.Self = "hq"
		if role == RoleReplica {
			f.Self = "dr1"
		}
	}
	return f
}

func render(t *testing.T, id string, f Facts, o Overrides) string {
	t.Helper()
	tp, ok := Find(id)
	if !ok {
		t.Fatalf("topic %q not found", id)
	}
	var b bytes.Buffer
	RenderTopic(&b, tp, f, tp.Build(f, NewVars(f, o)))
	return b.String()
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) && !strings.Contains(out, hint.Localize(w)) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func TestReplicaFreshPrefillsThisMachine(t *testing.T) {
	out := render(t, "replica", fixture(RoleFresh), Overrides{})
	mustContain(t, out,
		"sudo qabas replica add branch-01 --host 10.20.0.5 --host-key SHA256:abc123 --site branch-01 --promotable",
		"Do NOT run `qabas install` on this machine",
		"[run on: this machine]",
		"[run on: the writer]",
		"sudo qabas replica init --name <writer>",
		"[done] Docker Engine",
	)
}

func TestReplicaOverrides(t *testing.T) {
	out := render(t, "add-replica", fixture(RoleFresh), Overrides{Name: "jeddah", Host: "198.51.100.9", Writer: "hq"})
	mustContain(t, out, "replica add jeddah --host 198.51.100.9", "replica init --name hq")
}

func TestReplicaOnWriter(t *testing.T) {
	out := render(t, "replica", fixture(RoleWriter), Overrides{})
	mustContain(t, out,
		"This machine is the writer of cluster qabasone",
		"[done] dr1",
		"[todo] kiosk",
		"sudo qabas replica add kiosk --resume",
		"--host <replica-ip> --host-key SHA256:<fingerprint>",
		"[run on: this machine (writer)]",
	)
	if strings.Contains(out, "--host 10.20.0.5") {
		t.Fatalf("writer's own IP must not be used as the replica host:\n%s", out)
	}
}

func TestReplicaOnReplica(t *testing.T) {
	out := render(t, "replica", fixture(RoleReplica), Overrides{})
	mustContain(t, out, "already replica dr1 of writer hq", "sudo qabas replica verify dr1")
}

func TestReplicaOnSingleWarnsAboutRebuild(t *testing.T) {
	out := render(t, "replica", fixture(RoleSingle), Overrides{})
	mustContain(t, out, "--rebuild -y", "sudo qabas backup create --name pre-replica", "replica init --name branch-01")
}

func TestReplicaBlockedChecks(t *testing.T) {
	f := fixture(RoleFresh)
	f.OS, f.Systemd, f.Stack = "darwin", false, []string{"qabas1-api", "qabas1-app", "qabas1-dash", "qabas1-postgres"}
	out := render(t, "replica", f, Overrides{})
	mustContain(t, out, "[!!] Linux with systemd", "detected: os=darwin", "4 qabasone containers present")
}

func TestNoDetectUsesPlaceholders(t *testing.T) {
	out := render(t, "replica", Facts{}, Overrides{})
	mustContain(t, out, "replica add <name> --host <this-ip> --host-key SHA256:<fingerprint>")
	if strings.Contains(out, "[done]") || strings.Contains(out, "[!!]") || strings.Contains(out, "Detected:") {
		t.Fatalf("no-detect output must not show detected state:\n%s", out)
	}
}

func TestDRUsesClusterNames(t *testing.T) {
	mustContain(t, render(t, "dr", fixture(RoleWriter), Overrides{}),
		"sudo qabas dr switchover dr1", "sudo qabas dr failback hq", "sudo qabas dr rejoin hq")
	mustContain(t, render(t, "failover", fixture(RoleReplica), Overrides{}),
		"sudo qabas dr promote dr1 --break-glass")
}

func TestScheduleOffsiteStatus(t *testing.T) {
	mustContain(t, render(t, "schedule", fixture(RoleSingle), Overrides{}), "no replica registered")
	f := fixture(RoleWriter)
	f.ScheduleEnabled, f.ScheduleAt, f.ScheduleTZ, f.Offsite = true, "02:15", "Africa/Cairo", "dr1"
	mustContain(t, render(t, "schedule", f, Overrides{}), "detected: daily at 02:15 (Africa/Cairo)", "detected: to dr1", "--timezone UTC")
}

func TestEveryTopicRendersForEveryRole(t *testing.T) {
	facts := []Facts{{}, fixture(RoleFresh), fixture(RoleSingle), fixture(RoleWriter), fixture(RoleReplica)}
	for _, tp := range Topics() {
		for _, f := range facts {
			d := tp.Build(f, NewVars(f, Overrides{}))
			if len(d.Sections) == 0 {
				t.Errorf("%s/%s: no sections", tp.ID, f.Role)
			}
			var b bytes.Buffer
			RenderTopic(&b, tp, f, d)
			if !strings.Contains(b.String(), strings.ToUpper(tp.ID)+": ") {
				t.Errorf("%s/%s: missing title", tp.ID, f.Role)
			}
		}
	}
}

func TestFindAndSuggest(t *testing.T) {
	for _, alias := range []string{"replica", "standby", "restore", "offsite", "wal", "failover", "upgrade", "troubleshoot"} {
		if _, ok := Find(alias); !ok {
			t.Errorf("Find(%q) failed", alias)
		}
	}
	if s := Suggest("replca"); len(s) == 0 || s[0] != "replica" {
		t.Errorf("Suggest(replca) = %v", s)
	}
	if s := Suggest("nope"); len(s) != 0 {
		t.Errorf("Suggest(nope) = %v, want none", s)
	}
}

func TestRecommend(t *testing.T) {
	recs := Recommend(fixture(RoleFresh))
	if len(recs) == 0 || recs[0].Topic != "replica" {
		t.Fatalf("fresh linux: %v", recs)
	}
	win := fixture(RoleFresh)
	win.OS = "windows"
	if recs = Recommend(win); recs[0].Topic != "install" {
		t.Fatalf("fresh windows: %v", recs)
	}
	recs = Recommend(fixture(RoleWriter))
	if recs[0].Topic != "replica" || !strings.Contains(recs[0].Why, "kiosk") {
		t.Fatalf("writer with a failed replica: %v", recs)
	}
}

func TestNodeNameFromHostname(t *testing.T) {
	for in, want := range map[string]string{"Branch-01.example.com": "branch-01", "DESKTOP_LUV28JN": "desktop-luv28jn", "x": "", "": ""} {
		if got := NodeNameFromHostname(in); got != want {
			t.Errorf("NodeNameFromHostname(%q) = %q, want %q", in, got, want)
		}
	}
}
