package replica

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
)

func TestInviteRegistersPullNodeAndRendersJoin(t *testing.T) {
	fx := newFixture(t, preflightOK)
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	fx.eng.Now = func() time.Time { return now }

	inv, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "branch1", Host: "ignored"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	n := fx.eng.Cluster.Node("branch1")
	if n == nil || n.Mode != cluster.ModePull || n.SSHVia != cluster.SSHViaWG || n.Host != "" || n.WGIP != inv.WGIP {
		t.Fatalf("node = %+v", n)
	}
	st, _ := LoadState(fx.p, "branch1")
	if st.Phase != PhaseInvited || !st.TempWGKey || st.InviteExpires == nil || !st.InviteExpires.Equal(now.Add(time.Hour)) {
		t.Fatalf("state = %+v", st)
	}
	info, err := os.Stat(inv.Path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("join script mode %v (holds a private key)", info.Mode().Perm())
	}
	for _, want := range []string{"hq.example.com:51820", inv.WGIP, `from="10.66.0.0/24",restrict ssh-ed25519 `, cluster.SSHKeyComment} {
		if !strings.Contains(inv.Script, want) {
			t.Errorf("join script lacks %q", want)
		}
	}
	if !fx.writer.Ran(n.WGPublicKey) {
		t.Fatal("writer WireGuard was not given the invited peer")
	}

	if _, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "branch1"}}); err == nil {
		t.Fatal("duplicate invite accepted")
	}

	if err := fx.eng.RevokeInvite(context.Background(), "branch1"); err != nil {
		t.Fatal(err)
	}
	if fx.eng.Cluster.Node("branch1") != nil {
		t.Fatal("revoked node still registered")
	}
	if _, err := os.Stat(inv.Path); !os.IsNotExist(err) {
		t.Fatal("join script left behind after revoke")
	}
	if st, _ := LoadState(fx.p, "branch1"); st != nil {
		t.Fatalf("state left behind: %+v", st)
	}
}

func TestPruneExpiredInvites(t *testing.T) {
	fx := newFixture(t, preflightOK)
	now := time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC)
	fx.eng.Now = func() time.Time { return now }
	if _, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "branch1"}, TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if errs := fx.eng.PruneInvites(context.Background()); len(errs) != 0 || fx.eng.Cluster.Node("branch1") == nil {
		t.Fatalf("live invite pruned (errs %v)", errs)
	}
	now = now.Add(2 * time.Hour)
	if errs := fx.eng.PruneInvites(context.Background()); len(errs) != 0 {
		t.Fatal(errs)
	}
	if fx.eng.Cluster.Node("branch1") != nil {
		t.Fatal("expired invite not revoked")
	}
	if err := fx.eng.RevokeInvite(context.Background(), "dr-none"); err == nil {
		t.Fatal("revoking an unknown invite must fail")
	}
}

func TestRevokeRefusesProvisioningNode(t *testing.T) {
	fx := newFixture(t, preflightOK)
	if _, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "branch1"}}); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(fx.p, "branch1")
	st.Completed = append(st.Completed, PhasePreflight)
	if err := SaveState(fx.p, st); err != nil {
		t.Fatal(err)
	}
	if err := fx.eng.RevokeInvite(context.Background(), "branch1"); err == nil || !strings.Contains(err.Error(), "replica rm") {
		t.Fatalf("revoke of a provisioning node: %v", err)
	}
}
