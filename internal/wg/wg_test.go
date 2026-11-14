package wg

import (
	"strings"
	"testing"
)

func TestKeyPairDerivesPublic(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := PublicFromPrivate(kp.Private)
	if err != nil || pub != kp.Public {
		t.Fatalf("public mismatch: %v", err)
	}
}

func TestRenderSortsPeers(t *testing.T) {
	cfg := Interface{Address: "10.66.0.1/24", ListenPort: 51820, PrivateKey: "PRIV", Peers: []Peer{
		{Name: "z-node", PublicKey: "Z", AllowedIPs: []string{"10.66.0.3/32"}},
		{Name: "a-node", PublicKey: "A", AllowedIPs: []string{"10.66.0.2/32"}, Keepalive: 25},
	}}
	out := cfg.Render()
	if strings.Index(out, "# a-node") > strings.Index(out, "# z-node") {
		t.Fatalf("peers not sorted:\n%s", out)
	}
	if !strings.Contains(out, "PersistentKeepalive = 25") {
		t.Fatal("keepalive missing")
	}
}
