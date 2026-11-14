package replica

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
)

// hookExec records every script in the embedded Fake and lets a test change results
// over time (a CLI that appears after install.sh, a boot_id that changes on reboot).
type hookExec struct {
	*nodeexec.Fake
	mu   sync.Mutex
	hook func(script string) (nodeexec.Result, bool)
}

func (h *hookExec) Run(ctx context.Context, script string, stdin io.Reader) (nodeexec.Result, error) {
	res, err := h.Fake.Run(ctx, script, stdin)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hook != nil {
		if r, ok := h.hook(script); ok {
			return r, nil
		}
	}
	return res, err
}

const bareUbuntu = "os=Linux\narch=x86_64\nosid=ubuntu\nosversion=24.04\nmachineid=replica-mid\nreboot=no\nwg=no\ncluster=\ndocker=\ncompose=\nsystemd=yes\ndiskfree_kb=104857600\nstack=\npgvolume=\nqabas=\n"

func wroteFile(f *nodeexec.Fake, substr string) bool {
	for _, v := range f.Files {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}

func bootOpts() *BootstrapOptions {
	return &BootstrapOptions{InstallScript: []byte("#!/usr/bin/env bash\necho install\n")}
}

// withInstallingCLI makes `qabas version --json` answer only after install.sh ran.
func withInstallingCLI(fx *fixture) *hookExec {
	installed := false
	h := &hookExec{Fake: fx.replica}
	h.hook = func(script string) (nodeexec.Result, bool) {
		if strings.Contains(script, "bash "+installPath) {
			installed = true
		}
		if strings.Contains(script, "qabas version --json") {
			if installed {
				return nodeexec.Result{Stdout: cliJSON("1.5.0", "abc")}, true
			}
			return nodeexec.Result{}, true
		}
		return nodeexec.Result{}, false
	}
	w := fx.writer
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return h, nil
	}
	return h
}

func TestAddBootstrapsBareHost(t *testing.T) {
	fx := newFixture(t, bareUbuntu)
	withInstallingCLI(fx)
	fx.eng.GitHubToken = func() string { return "ghp_writerSecretToken" }
	fx.eng.Registry = func() (string, string) { return "qabas-bot", "ghcr_writerSecretToken" }
	fx.eng.CLI = &CLIBuild{Version: "1.5.0", Commit: "abc"}

	rep, err := fx.eng.Add(context.Background(), AddOptions{
		Node:      cluster.Node{Name: "dr1", Host: "203.0.113.10", SSHHostKey: "SHA256:x", WGEndpoint: "203.0.113.10:51820"},
		Confirm:   func(string) error { return nil },
		Lease:     func(context.Context) error { return nil },
		Bootstrap: bootOpts(),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !rep.OK {
		t.Fatalf("verify:\n%s", rep.Failures())
	}
	st, _ := LoadState(fx.p, "dr1")
	if !st.Done(PhaseBootstrap) || st.Phase != PhaseReady {
		t.Fatalf("state = %+v", st)
	}
	r := fx.replica
	for _, want := range []string{"cloud-init status --wait", "--force-confold", "upgrade --with-new-pkgs", "wireguard-tools", DockerKeyFingerprint, "VERSION='1.5.0' bash " + installPath + " --cli-only --force"} {
		if !r.Ran(want) {
			t.Errorf("bootstrap did not run %q", want)
		}
	}
	if r.Files["/etc/qabasone/github-token"] != "ghp_writerSecretToken\n" || r.Files["/etc/qabasone/ghcr-token"] != "ghcr_writerSecretToken\n" {
		t.Fatalf("credentials not stored: %v", r.Files)
	}
	if !strings.HasPrefix(r.Files[installPath], "#!/usr/bin/env bash") {
		t.Fatal("embedded installer not uploaded")
	}
	if !wroteFile(r, "restrict ssh-ed25519 ") || !wroteFile(r, " "+cluster.SSHKeyComment+"\n") {
		t.Fatal("cluster key not authorized with restrict")
	}
	for _, s := range r.Scripts {
		for _, secret := range []string{"ghp_writerSecretToken", "ghcr_writerSecretToken"} {
			if strings.Contains(s, secret) {
				t.Fatalf("token leaked into a script argument:\n%s", s)
			}
		}
	}
	if _, err := os.Stat(cluster.SSHKeyPath(fx.p)); err != nil {
		t.Fatalf("cluster key not created on the writer: %v", err)
	}
}

func TestPreflightNeedsBootstrapForBareHost(t *testing.T) {
	fx := newFixture(t, bareUbuntu)
	_, err := fx.eng.Add(context.Background(), AddOptions{Node: cluster.Node{Name: "dr1", Host: "203.0.113.10"}})
	if err == nil || !errors.Is(err, ErrNotModified) || !strings.Contains(err.Error(), "Docker Engine") {
		t.Fatalf("--no-bootstrap must keep the strict preflight: %v", err)
	}
}

func TestPreflightHardFailures(t *testing.T) {
	cases := []struct {
		name, preflight, writerMID, want string
	}{
		{"unsupported distro", strings.Replace(bareUbuntu, "osid=ubuntu", "osid=centos", 1), "", `distribution "centos" is not supported`},
		{"old ubuntu", strings.Replace(bareUbuntu, "osversion=24.04", "osversion=20.04", 1), "", "Ubuntu 20.04 is too old"},
		{"writer itself", bareUbuntu, "replica-mid", "is the writer itself"},
		{"other cluster", strings.Replace(bareUbuntu, "cluster=", "cluster=other", 1), "", `already belongs to cluster "other"`},
		{"no systemd", strings.Replace(bareUbuntu, "systemd=yes", "systemd=no", 1), "", "systemd is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newFixture(t, c.preflight)
			if c.writerMID != "" {
				fx.writer.OnOut("cat /etc/machine-id", c.writerMID)
			}
			_, err := fx.eng.Add(context.Background(), AddOptions{Node: cluster.Node{Name: "dr1", Host: "203.0.113.10"}, Bootstrap: bootOpts()})
			if err == nil || !errors.Is(err, ErrNotModified) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if len(fx.replica.Files) != 0 {
				t.Fatalf("preflight wrote files: %v", fx.replica.Files)
			}
			if fx.replica.Ran("apt-get") {
				t.Fatal("preflight failure must not run apt")
			}
		})
	}
}

func TestResumeOldStateRunsBootstrap(t *testing.T) {
	fx := newFixture(t, "osid=debian\nosversion=12\n"+preflightOK)
	fx.replica.OnOut("qabas version --json", cliJSON("1.5.0", "abc"))
	if err := SaveState(fx.p, &State{Node: "dr1", Phase: PhaseNetwork, Completed: []Phase{PhasePreflight, PhaseNetwork}}); err != nil {
		t.Fatal(err)
	}
	c := fx.eng.Cluster
	c.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, Host: "203.0.113.10", SSHHostKey: "SHA256:x", WGIP: "10.66.0.2", WGPublicKey: testPub, WGEndpoint: "203.0.113.10:51820", Mode: cluster.ModePush, SSHPort: 22, Site: "dr1"})
	if _, err := fx.eng.Add(context.Background(), AddOptions{
		Node: cluster.Node{Name: "dr1"}, Resume: true, Bootstrap: bootOpts(),
		Confirm: func(string) error { return nil }, Lease: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	st, _ := LoadState(fx.p, "dr1")
	if !st.Done(PhaseBootstrap) || st.Phase != PhaseReady {
		t.Fatalf("state = %+v", st)
	}
	if fx.replica.Ran("bash " + installPath) {
		t.Fatal("an installed CLI must not be reinstalled")
	}
}

func TestBootstrapSkipsAndRefusals(t *testing.T) {
	fx := newFixture(t, preflightOK)
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, Host: "h", WGIP: "10.66.0.2", Mode: cluster.ModePush})
	err := fx.eng.Bootstrap(context.Background(), "dr1", BootstrapOptions{SkipOSUpgrade: true, InstallScript: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "no GitHub token") {
		t.Fatalf("missing CLI without a token must fail clearly: %v", err)
	}
	if fx.replica.Ran("upgrade --with-new-pkgs") {
		t.Fatal("--no-os-upgrade ran apt upgrade")
	}
	if !fx.replica.Ran("wireguard-tools") {
		t.Fatal("base tools must still be installed")
	}
}

func TestRebootWaitsForNewBootID(t *testing.T) {
	defer func(s, p time.Duration) { rebootSettle, reconnectPoll = s, p }(rebootSettle, reconnectPoll)
	rebootSettle, reconnectPoll = 0, time.Millisecond

	fx := newFixture(t, preflightOK)
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, Host: "h", WGIP: "10.66.0.2", Mode: cluster.ModePush})
	rebooted := false
	h := &hookExec{Fake: fx.replica}
	h.hook = func(script string) (nodeexec.Result, bool) {
		switch {
		case strings.Contains(script, "systemctl reboot"):
			rebooted = true
		case script == bootIDScript:
			if rebooted {
				return nodeexec.Result{Stdout: "boot-2\n"}, true
			}
			return nodeexec.Result{Stdout: "boot-1\n"}, true
		case script == rebootRequiredScript:
			if rebooted {
				return nodeexec.Result{Stdout: "no\n"}, true
			}
			return nodeexec.Result{Stdout: "yes\n"}, true
		}
		return nodeexec.Result{}, false
	}
	dials := 0
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return fx.writer, nil
		}
		dials++
		return h, nil
	}
	if err := fx.eng.rebootIfRequired(context.Background(), "dr1", false); err != nil {
		t.Fatal(err)
	}
	if !rebooted || dials < 2 {
		t.Fatalf("rebooted=%v dials=%d (must reconnect after the reboot)", rebooted, dials)
	}

	rebooted = false
	fx.eng.Reconnect("dr1")
	if err := fx.eng.rebootIfRequired(context.Background(), "dr1", true); err != nil || rebooted {
		t.Fatalf("--no-reboot rebooted=%v err=%v", rebooted, err)
	}
}

func TestSwitchSSHToWG(t *testing.T) {
	fx := newFixture(t, preflightOK)
	fx.replica.OnOut("sshd -T", "2222")
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, Host: "198.51.100.4", SSHPort: 2200, WGIP: "10.66.0.2", Mode: cluster.ModePull})
	if err := fx.eng.SwitchSSHToWG(context.Background(), "dr1"); err != nil {
		t.Fatal(err)
	}
	c, _ := cluster.Load(fx.p)
	n := c.Node("dr1")
	if n.SSHVia != cluster.SSHViaWG || n.WGSSHPort != 2222 {
		t.Fatalf("node = %+v", n)
	}
	if host, port := n.SSHEndpoint(); host != "10.66.0.2" || port != 2222 {
		t.Fatalf("SSHEndpoint = %s:%d", host, port)
	}
	if !wroteFile(fx.replica, `from="10.66.0.0/24",restrict ssh-ed25519 `) {
		t.Fatal("cluster key must be limited to the WireGuard network")
	}
}

func TestEnsureSlotBound(t *testing.T) {
	fx := newFixture(t, preflightOK)
	fx.writer.OnOut("SHOW max_slot_wal_keep_size", "-1")
	var audited []string
	opt := AddOptions{Confirm: func(string) error { return nil }, Audit: func(a, d string) { audited = append(audited, a+": "+d) }}
	if err := fx.eng.EnsureSlotBound(context.Background(), "hq", true, opt); err != nil {
		t.Fatal(err)
	}
	if !fx.writer.Ran("ALTER SYSTEM SET max_slot_wal_keep_size = '10GB'") || !fx.writer.Ran("pg_reload_conf()") {
		t.Fatal("unbounded slot retention not limited with a reload")
	}
	if fx.writer.Ran("restart") {
		t.Fatal("slot bound must never restart the writer")
	}
	if len(audited) != 1 {
		t.Fatalf("audit = %v", audited)
	}

	fx2 := newFixture(t, preflightOK)
	fx2.writer.OnOut("SHOW max_slot_wal_keep_size", "-1")
	declined := AddOptions{Confirm: func(string) error { return errors.New("no") }}
	if err := fx2.eng.EnsureSlotBound(context.Background(), "hq", true, declined); err == nil || !strings.Contains(err.Error(), "unbounded") {
		t.Fatalf("declined bound must refuse: %v", err)
	}
	if fx2.writer.Ran("ALTER SYSTEM") {
		t.Fatal("declined bound changed the writer")
	}

	fx3 := newFixture(t, preflightOK)
	fx3.writer.OnOut("SHOW max_slot_wal_keep_size", "20GB")
	if err := fx3.eng.EnsureSlotBound(context.Background(), "hq", true, opt); err != nil || fx3.writer.Ran("ALTER SYSTEM") {
		t.Fatalf("bounded writer must be left alone (err %v)", err)
	}
}

func TestWriterGuardRefusesUnhealthyWriter(t *testing.T) {
	fx := newFixture(t, preflightOK)
	w := nodeexec.NewFake("hq").OnOut("{{if .State.Health}}", "unhealthy")
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) { return w, nil }
	defer func() { fx.eng.Connect = nil }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := fx.eng.WriterGuard(ctx); err == nil || !strings.Contains(err.Error(), "not healthy") && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}

	fx2 := newFixture(t, preflightOK)
	fx2.writer.OnOut("writer_diskfree_kb", "writer_diskfree_kb=1048576")
	if err := fx2.eng.WriterGuard(context.Background()); err == nil || !strings.Contains(err.Error(), "MiB free") {
		t.Fatalf("low disk must refuse: %v", err)
	}
}

func TestWriterGuardSupportsScaledAPIContainers(t *testing.T) {
	fx := newFixture(t, preflightOK)
	w := &hookExec{Fake: fx.writer}
	w.hook = func(script string) (nodeexec.Result, bool) {
		if strings.Contains(script, "docker ps -a --format '{{.Names}}'") {
			return nodeexec.Result{Stdout: "qabasone-api-1\nqabasone-api-2\n"}, true
		}
		return nodeexec.Result{}, false
	}
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return fx.replica, nil
	}
	if err := fx.eng.WriterGuard(context.Background()); err != nil {
		t.Fatalf("scaled healthy APIs should pass the writer guard: %v", err)
	}
	if !fx.writer.Ran("qabasone-api-1") || !fx.writer.Ran("qabasone-api-2") {
		t.Fatalf("writer guard did not inspect all API containers: %v", fx.writer.Log)
	}
}

func TestWaitHealthyReportsScaledAPIContainerFailure(t *testing.T) {
	fx := newFixture(t, preflightOK)
	w := &hookExec{Fake: fx.writer}
	w.hook = func(script string) (nodeexec.Result, bool) {
		if strings.Contains(script, "docker ps -a --format '{{.Names}}'") {
			return nodeexec.Result{Stdout: "qabasone-api-1\nqabasone-api-2\n"}, true
		}
		if strings.Contains(script, "docker inspect") && strings.Contains(script, "qabasone-api-2") {
			return nodeexec.Result{Stdout: "unhealthy"}, true
		}
		return nodeexec.Result{}, false
	}
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return w, nil
		}
		return fx.replica, nil
	}
	err := fx.eng.WaitHealthy(context.Background(), "hq", "qabasone-api", time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "qabasone-api-2=unhealthy") || !strings.Contains(err.Error(), "docker logs qabasone-api-2") {
		t.Fatalf("scaled API failure should identify the failing container: %v", err)
	}
}

func TestContainerProblems(t *testing.T) {
	out := "qabasone-api|Up 1 minute (healthy)\nqabasone-minio-init|Exited (0) 2 minutes ago\nqabasone-app|Restarting (1) 3 seconds ago\nqabasone-dash|Up 1 minute (unhealthy)\nqabasone-siraj|Exited (137) 1 minute ago\n"
	problems, seen := containerProblems(out)
	if seen != 5 || len(problems) != 3 {
		t.Fatalf("seen=%d problems=%v", seen, problems)
	}
}

func TestSupportedOS(t *testing.T) {
	for _, ok := range [][2]string{{"ubuntu", "22.04"}, {"ubuntu", "24.04"}, {"debian", "12"}, {"debian", "13"}} {
		if err := SupportedOS(ok[0], ok[1]); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range [][2]string{{"ubuntu", "20.04"}, {"debian", "11"}, {"rocky", "9"}, {"", ""}} {
		if SupportedOS(bad[0], bad[1]) == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestRotateWGKey(t *testing.T) {
	defer func(s, p time.Duration) { rotateSettle, reconnectPoll = s, p }(rotateSettle, reconnectPoll)
	rotateSettle, reconnectPoll = 0, time.Millisecond
	const newPub = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="

	fx := newFixture(t, preflightOK)
	r := nodeexec.NewFake("dr1").OnOut(".next", newPub).OnOut("wg pubkey < ", newPub)
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return fx.writer, nil
		}
		return r, nil
	}
	fx.eng.Cluster.Nodes[0].WGPublicKey = testPub
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, WGIP: "10.66.0.2", WGPublicKey: "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", Mode: cluster.ModePull, SSHVia: cluster.SSHViaWG})
	if err := fx.eng.RotateWGKey(context.Background(), "dr1"); err != nil {
		t.Fatal(err)
	}
	c, _ := cluster.Load(fx.p)
	if c.Node("dr1").WGPublicKey != newPub {
		t.Fatalf("registry pubkey = %s", c.Node("dr1").WGPublicKey)
	}
	if !r.Ran("systemd-run --quiet --collect --on-active=5") || !fx.writer.Ran(newPub) {
		t.Fatal("swap not scheduled on the node or writer peer not updated")
	}
}
