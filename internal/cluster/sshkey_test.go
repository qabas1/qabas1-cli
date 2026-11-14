package cluster

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qabasone/qabasone-cli/internal/paths"
	"golang.org/x/crypto/ssh"
)

func TestEnsureSSHKeyIsStable(t *testing.T) {
	dir := t.TempDir()
	p := paths.Paths{ConfigDir: dir, StateDir: filepath.Join(dir, "state")}
	line, err := EnsureSSHKey(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "ssh-ed25519 ") || !strings.HasSuffix(line, " "+SSHKeyComment) {
		t.Fatalf("authorized line = %q", line)
	}
	again, err := EnsureSSHKey(p)
	if err != nil || again != line {
		t.Fatalf("second call regenerated the key: %q vs %q (%v)", again, line, err)
	}
	if pub, err := ReadSSHPublicKey(p); err != nil || pub != line {
		t.Fatalf("ReadSSHPublicKey = %q, %v", pub, err)
	}
	priv, err := ReadSSHPrivateKey(p)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))) {
		t.Fatal("public key does not match the private key")
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(SSHKeyPath(p)); info.Mode().Perm() != 0o600 {
			t.Fatalf("private key mode %v", info.Mode().Perm())
		}
	}
}

func TestSSHEndpointAndVia(t *testing.T) {
	n := Node{Host: "203.0.113.4", SSHPort: 2200, WGIP: "10.66.0.5"}
	if h, p := n.SSHEndpoint(); h != "203.0.113.4" || p != 2200 {
		t.Fatalf("direct endpoint = %s:%d", h, p)
	}
	n.SSHVia = SSHViaWG
	if h, p := n.SSHEndpoint(); h != "10.66.0.5" || p != 22 {
		t.Fatalf("wg endpoint default = %s:%d", h, p)
	}
	n.WGSSHPort = 2222
	if _, p := n.SSHEndpoint(); p != 2222 {
		t.Fatalf("wg ssh port = %d", p)
	}

	c := sample()
	c.applyDefaults()
	c.Nodes[2].SSHVia = "tor"
	if err := c.Validate(); err == nil {
		t.Fatal("unknown sshVia accepted")
	}
	c.Nodes[2].SSHVia, c.Nodes[2].WGIP = SSHViaWG, ""
	if err := c.Validate(); err == nil {
		t.Fatal("sshVia wg without a WireGuard IP accepted")
	}
}

func TestValidateRelayConstraints(t *testing.T) {
	base := func() *Cluster {
		c := sample()
		c.applyDefaults()
		c.Nodes[2].Upstream = ""
		c.Nodes[2].Relay, c.Nodes[2].Mode = true, ModePull
		return c
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid relay node rejected: %v", err)
	}
	if !base().HasRelayNodes() || base().RelayPortOrDefault() != DefaultRelayPort {
		t.Fatal("relay helpers")
	}
	for name, mutate := range map[string]func(c *Cluster){
		"push mode":           func(c *Cluster) { c.Nodes[2].Mode = ModePush },
		"promotable":          func(c *Cluster) { c.Nodes[2].Promotable = true },
		"cascading upstream":  func(c *Cluster) { c.Nodes[2].Upstream = "dr-cairo-office" },
		"primary relay":       func(c *Cluster) { c.Nodes[0].Relay = true },
		"downstream of relay": func(c *Cluster) { c.Nodes[1].Upstream = "dr-alex-branch" },
	} {
		c := base()
		mutate(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
