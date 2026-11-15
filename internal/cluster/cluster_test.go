package cluster

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qabas1/qabasone-cli/internal/paths"
)

func sample() *Cluster {
	return &Cluster{
		Name:   "qabasone",
		Writer: "prod-01",
		Epoch:  1,
		Nodes: []Node{
			{Name: "prod-01", Role: RolePrimary, Host: "203.0.113.10", WGIP: "10.66.0.1"},
			{Name: "dr-cairo-office", Site: "cairo", Role: RoleStandby, Host: "192.168.1.20", WGIP: "10.66.0.2", Promotable: true, Priority: 10},
			{Name: "dr-alex-branch", Site: "alex", Role: RoleStandby, Host: "192.168.5.20", WGIP: "10.66.0.3", Upstream: "dr-cairo-office"},
		},
	}
}

func TestValidateRequiresExactlyOnePrimary(t *testing.T) {
	c := sample()
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("valid registry rejected: %v", err)
	}
	c.Nodes[1].Role = RolePrimary
	if err := c.Validate(); err == nil {
		t.Fatal("two primaries accepted")
	}
}

func TestLeaseModeRequiresExplicitTopology(t *testing.T) {
	c := sample()
	if err := c.ValidateLeaseMode(LeaseModeLocalOnly); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateLeaseMode(""); err == nil {
		t.Fatal("implicit lease mode accepted")
	}
	if err := c.ValidateLeaseMode(LeaseModeStrict); err == nil {
		t.Fatal("strict mode without quorum accepted")
	}
	c.Witnesses = []Witness{
		{Name: "a", Kind: "http", URL: "https://a.example", FailureDomain: "site-a"},
		{Name: "b", Kind: "http", URL: "https://b.example", FailureDomain: "site-b"},
		{Name: "c", Kind: "http", URL: "https://c.example", FailureDomain: "site-b"},
	}
	if err := c.ValidateLeaseMode(LeaseModeStrict); err == nil {
		t.Fatal("shared failure domain accepted")
	}
	c.Witnesses[2].FailureDomain = "site-c"
	if err := c.ValidateLeaseMode(LeaseModeStrict); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateLeaseMode(LeaseModeLocalOnly); err == nil {
		t.Fatal("local-only with witnesses accepted")
	}
}

func TestSlotAndAppNames(t *testing.T) {
	if got := SlotName("dr-cairo-office"); got != "qabas_replica_dr_cairo_office" {
		t.Fatalf("slot = %q", got)
	}
	if got := ApplicationName("dr-cairo-office"); got != "qabas_dr_cairo_office" {
		t.Fatalf("app = %q", got)
	}
}

func TestNextWGIPSkipsUsed(t *testing.T) {
	c := sample()
	c.applyDefaults()
	ip, err := c.NextWGIP()
	if err != nil || ip != "10.66.0.4" {
		t.Fatalf("NextWGIP = %q, %v", ip, err)
	}
	if c.HubIP() != "10.66.0.1" {
		t.Fatalf("hub = %q", c.HubIP())
	}
}

func TestWGAddressesDefaultBeforeApplyDefaults(t *testing.T) {
	c := &Cluster{}
	if got := c.HubIP(); got != "10.66.0.1" {
		t.Fatalf("hub on zero-value cluster = %q", got)
	}
	c.Nodes = []Node{{Name: "hq", Role: RolePrimary, WGIP: c.HubIP()}}
	if ip, err := c.NextWGIP(); err != nil || ip != "10.66.0.2" {
		t.Fatalf("NextWGIP on zero-value cluster = %q, %v", ip, err)
	}
}

func TestValidateRejectsInvalidWGIP(t *testing.T) {
	for _, ip := range []string{"1", "10.67.0.2", "fd00::2"} {
		c := sample()
		c.applyDefaults()
		c.Nodes[1].WGIP = ip
		if err := c.Validate(); err == nil {
			t.Fatalf("wgIP %q accepted", ip)
		}
	}
}

func TestLoadRepairsPrimaryWGIPOne(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: dir, StateDir: filepath.Join(dir, "state")}
	raw := "schemaVersion: 1\nname: qabasone\nwriter: hq\nepoch: 1\nnodes:\n" +
		"  - name: hq\n    role: primary\n    host: localhost\n    wgIP: \"1\"\n"
	if err := os.WriteFile(File(p), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load broken registry: %v", err)
	}
	if got := c.Node("hq").WGIP; got != "10.66.0.1" {
		t.Fatalf("primary wgIP = %q, want 10.66.0.1", got)
	}
}

func TestPromoteRepointsAndAdvancesEpoch(t *testing.T) {
	c := sample()
	c.applyDefaults()
	if err := c.Promote("dr-cairo-office", 1); err == nil {
		t.Fatal("non-advancing epoch accepted")
	}
	if err := c.Promote("dr-cairo-office", 2); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if c.Writer != "dr-cairo-office" || c.Epoch != 2 {
		t.Fatalf("writer=%s epoch=%d", c.Writer, c.Epoch)
	}
	if c.Node("prod-01").Role != RoleStandby {
		t.Fatal("old primary not demoted")
	}
	if c.Node("dr-alex-branch").Upstream != "" {
		t.Fatal("cascading standby not re-pointed to new writer")
	}
}

func TestRemoveRefusesPrimaryAndFeeders(t *testing.T) {
	c := sample()
	c.applyDefaults()
	if err := c.Remove("prod-01"); err == nil {
		t.Fatal("removed primary")
	}
	if err := c.Remove("dr-cairo-office"); err == nil {
		t.Fatal("removed node that feeds a cascading standby")
	}
	if err := c.Remove("dr-alex-branch"); err != nil {
		t.Fatalf("remove leaf: %v", err)
	}
}

func TestSaveLoadRoundTripAndSecretsPerms(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: dir, StateDir: filepath.Join(dir, "state")}
	if err := Save(p, sample()); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Nodes) != 3 || got.Nodes[1].SSHPort != 22 {
		t.Fatalf("round trip lost data: %+v", got.Nodes)
	}

	if err := WriteSecret(p, "dr-cairo-office", SecretReplicationPassword, "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	v, err := ReadSecret(p, "dr-cairo-office", SecretReplicationPassword)
	if err != nil || v != "correct-horse-battery" {
		t.Fatalf("ReadSecret = %q, %v", v, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(NodeSecretsDir(p, "dr-cairo-office"), SecretReplicationPassword))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("secret perms = %v", st.Mode().Perm())
		}
	}
	b, _ := os.ReadFile(File(p))
	if strings.Contains(string(b), "correct-horse-battery") {
		t.Fatal("secret leaked into registry")
	}
}
