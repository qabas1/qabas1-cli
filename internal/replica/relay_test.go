package replica

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qabas1/qabas1-cli/internal/cluster"
	"github.com/qabas1/qabas1-cli/internal/nodeexec"
)

const (
	dr1Pub  = "DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD="
	homePub = "HHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHH="
)

// relayFixture is a fixture whose writer and replica have a relay-capable qabas.
func relayFixture(t *testing.T) *fixture {
	fx := newFixture(t, preflightOK)
	fx.writer.OnOut("relay connect --help", "ok")
	fx.replica.OnOut("relay connect --help", "ok")
	fx.eng.Cluster.Nodes[0].WGPublicKey = testPub
	return fx
}

// peerBlock returns the [Peer] section for a public key in a rendered config.
func peerBlock(conf, pub string) string {
	for _, b := range strings.Split(conf, "[Peer]") {
		if strings.Contains(b, "PublicKey = "+pub) {
			return b
		}
	}
	return ""
}

func TestRenderWGRelayTopology(t *testing.T) {
	fx := relayFixture(t)
	c := fx.eng.Cluster
	c.Upsert(cluster.Node{Name: "dr1", Role: cluster.RoleStandby, WGIP: "10.66.0.2", WGPublicKey: dr1Pub, WGEndpoint: "203.0.113.10:51820", Mode: cluster.ModePush})
	c.Upsert(cluster.Node{Name: "home", Role: cluster.RoleStandby, WGIP: "10.66.0.3", WGPublicKey: homePub, Mode: cluster.ModePull, SSHVia: cluster.SSHViaWG, Relay: true})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	home, _ := fx.eng.RenderWG("home")
	if strings.Contains(home, dr1Pub) {
		t.Fatal("relay node must not peer with other standbys")
	}
	if b := peerBlock(home, testPub); !strings.Contains(b, "Endpoint = 127.0.0.1:51821") || !strings.Contains(b, "PersistentKeepalive = 25") {
		t.Fatalf("relay node's writer peer:\n%s", b)
	}

	dr1, _ := fx.eng.RenderWG("dr1")
	if strings.Contains(dr1, homePub) {
		t.Fatal("standbys must not peer with relay nodes")
	}
	hq, _ := fx.eng.RenderWG("hq")
	b := peerBlock(hq, homePub)
	if b == "" || strings.Contains(b, "Endpoint") {
		t.Fatalf("writer must peer with the relay node without an endpoint:\n%s", b)
	}
	if !strings.Contains(peerBlock(hq, dr1Pub), "Endpoint = 203.0.113.10:51820") {
		t.Fatal("direct peers unchanged")
	}
}

func TestInviteEmbedsRelayAndRevokeCleansUp(t *testing.T) {
	fx := relayFixture(t)
	inv, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "home", Promotable: true}, Relay: true})
	if err != nil {
		t.Fatal(err)
	}
	c := fx.eng.Cluster
	if c.RelayCertSHA256 == "" || c.RelayPort != cluster.DefaultRelayPort {
		t.Fatalf("relay not pinned in the registry: %q %d", c.RelayCertSHA256, c.RelayPort)
	}
	n := c.Node("home")
	if !n.Relay || n.Promotable || n.Mode != cluster.ModePull {
		t.Fatalf("forced relay node = %+v", n)
	}
	token, err := cluster.ReadSecret(fx.p, "home", cluster.SecretRelayToken)
	if err != nil || token == "" {
		t.Fatalf("relay token: %v", err)
	}
	for _, want := range []string{"use_relay=1", "RELAY_SERVER=hq.example.com:8443", "RELAY_CERT_SHA256=" + c.RelayCertSHA256, "RELAY_TOKEN=" + token, "ExecStart=/usr/bin/env qabas relay connect", "endpoint 127.0.0.1:51821", testPub} {
		if !strings.Contains(inv.Script, want) {
			t.Errorf("join script lacks %q", want)
		}
	}
	if !fx.writer.Ran("ExecStart=/usr/bin/env qabas relay serve") {
		t.Fatal("relay server unit not installed on the writer")
	}
	for _, s := range fx.writer.Scripts {
		if strings.Contains(s, token) {
			t.Fatal("relay token leaked into a script argument")
		}
	}

	if err := fx.eng.RevokeInvite(context.Background(), "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := cluster.ReadSecret(fx.p, "home", cluster.SecretRelayToken); err == nil {
		t.Fatal("relay token survived the revoke")
	}
	if !fx.writer.Ran("systemctl disable --now " + RelayServerUnit) {
		t.Fatal("unused relay server not stopped")
	}
}

func TestInviteAutoFallbackScript(t *testing.T) {
	fx := relayFixture(t)
	inv, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "home"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := fx.eng.Cluster.Node("home"); n.Relay {
		t.Fatal("relay must not be assumed before the node joins")
	}
	for _, want := range []string{"use_relay=0", "wait_handshake 20", "switching to the TLS relay hq.example.com:8443"} {
		if !strings.Contains(inv.Script, want) {
			t.Errorf("join script lacks %q", want)
		}
	}

	fx2 := newFixture(t, preflightOK) // writer qabas without relay support
	inv2, err := fx2.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "home"}})
	if err != nil {
		t.Fatalf("a missing relay must not block a direct invite: %v", err)
	}
	if !strings.Contains(inv2.Script, `elif [ -n "" ]; then`) {
		t.Fatal("script without a relay must not offer the fallback")
	}
	if _, err := fx2.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "kiosk"}, Relay: true}); err == nil {
		t.Fatal("--relay without a working relay must fail")
	}
}

func TestWaitInviteDetectsRelay(t *testing.T) {
	defer func(p time.Duration) { reconnectPoll = p }(reconnectPoll)
	reconnectPoll = time.Millisecond
	fx := relayFixture(t)
	if _, err := fx.eng.Invite(context.Background(), InviteOptions{Node: cluster.Node{Name: "home", Promotable: true}}); err != nil {
		t.Fatal(err)
	}
	pub := fx.eng.Cluster.Node("home").WGPublicKey
	fx.writer.OnOut("latest-handshakes", pub+"\t1790000000").OnOut("endpoints", pub+"\t127.0.0.1:40122")
	if err := fx.eng.WaitInvite(context.Background(), "home", time.Minute); err != nil {
		t.Fatal(err)
	}
	c, _ := cluster.Load(fx.p)
	if n := c.Node("home"); !n.Relay || n.Promotable {
		t.Fatalf("node joined through the relay but registry says %+v", n)
	}
}

func TestProvisionNetworkFallsBackToRelay(t *testing.T) {
	defer func(t1, p time.Duration) { peerTimeout, peerPoll = t1, p }(peerTimeout, peerPoll)
	peerTimeout, peerPoll = 50*time.Millisecond, time.Millisecond

	fx := relayFixture(t)
	relayUp := false
	h := &hookExec{Fake: fx.replica}
	h.hook = func(script string) (nodeexec.Result, bool) {
		if strings.Contains(script, "/etc/systemd/system/"+RelayClientUnit) && strings.Contains(script, "cmp -s") {
			relayUp = true
		}
		if strings.HasPrefix(script, "ping -c 1") && !relayUp {
			return nodeexec.Result{ExitCode: 1}, true
		}
		return nodeexec.Result{}, false
	}
	fx.eng.Connect = func(_ context.Context, n cluster.Node) (nodeexec.Executor, error) {
		if n.Name == "hq" {
			return fx.writer, nil
		}
		return h, nil
	}
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr2", Role: cluster.RoleStandby, Host: "198.51.100.9", SSHPort: 22, WGIP: "10.66.0.4",
		WGEndpoint: "198.51.100.9:51820", Mode: cluster.ModePush, Promotable: true})

	if err := fx.eng.provisionNetwork(context.Background(), "dr2", &State{Node: "dr2"}, false); err != nil {
		t.Fatalf("NETWORK with relay fallback: %v", err)
	}
	c, _ := cluster.Load(fx.p)
	n := c.Node("dr2")
	if !n.Relay || n.Promotable || n.Mode != cluster.ModePull || n.SSHVia != cluster.SSHViaWG {
		t.Fatalf("node after fallback = %+v", n)
	}
	env := fx.replica.Files[relayNodeDir+"/client.env"]
	token, _ := cluster.ReadSecret(fx.p, "dr2", cluster.SecretRelayToken)
	if !strings.Contains(env, "RELAY_TOKEN="+token) || !strings.Contains(env, "RELAY_CERT_SHA256="+c.RelayCertSHA256) {
		t.Fatalf("client.env = %q", env)
	}
	for _, s := range fx.replica.Scripts {
		if strings.Contains(s, token) {
			t.Fatal("relay token leaked into a script argument")
		}
	}
}

func TestProvisionNetworkNoFallbackForWGReachedNode(t *testing.T) {
	defer func(t1, p time.Duration) { peerTimeout, peerPoll = t1, p }(peerTimeout, peerPoll)
	peerTimeout, peerPoll = 20*time.Millisecond, time.Millisecond
	fx := relayFixture(t)
	fx.replica.On("ping -c 1", nodeexec.Result{ExitCode: 1})
	fx.eng.Cluster.Upsert(cluster.Node{Name: "dr2", Role: cluster.RoleStandby, WGIP: "10.66.0.4", Mode: cluster.ModePull, SSHVia: cluster.SSHViaWG, WGPublicKey: dr1Pub})
	if err := fx.eng.provisionNetwork(context.Background(), "dr2", &State{Node: "dr2"}, false); err == nil {
		t.Fatal("expected the WireGuard error")
	}
	if fx.eng.Cluster.Node("dr2").Relay {
		t.Fatal("a node reached over WireGuard cannot be switched to the relay from the writer")
	}
}

func TestRemoveRelayNodeTearsDown(t *testing.T) {
	fx := relayFixture(t)
	if err := fx.eng.EnsureRelayServer(context.Background()); err != nil {
		t.Fatal(err)
	}
	fx.eng.Cluster.Upsert(cluster.Node{Name: "home", Role: cluster.RoleStandby, WGIP: "10.66.0.3", WGPublicKey: homePub, Mode: cluster.ModePull, SSHVia: cluster.SSHViaWG, Relay: true})
	if _, err := fx.eng.ensureRelayToken("home"); err != nil {
		t.Fatal(err)
	}
	for _, w := range fx.eng.Remove(context.Background(), "home", RemoveOptions{}) {
		t.Logf("warning: %v", w)
	}
	if fx.eng.Cluster.Node("home") != nil {
		t.Fatal("node still registered")
	}
	if !fx.replica.Ran("systemd-run --quiet --collect --on-active=3") || !fx.replica.Ran("disable --now "+RelayClientUnit) {
		t.Fatal("relay client / WireGuard teardown not scheduled on the node")
	}
	if _, err := cluster.ReadSecret(fx.p, "home", cluster.SecretRelayToken); err == nil {
		t.Fatal("relay token survived the removal")
	}
	if !fx.writer.Ran("systemctl disable --now " + RelayServerUnit) {
		t.Fatal("relay server kept running with no relay nodes")
	}
}
