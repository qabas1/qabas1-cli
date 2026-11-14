package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/qabasone/qabasone-cli/internal/paths"
)

// SSHKeyComment tags the cluster key in authorized_keys so it can be replaced or
// removed without touching operator keys.
const SSHKeyComment = "qabas-cluster"

// SSHKeyPath is the cluster SSH private key qabas uses to manage nodes. It is created
// on the writer and copied only to promotable nodes (a promoted writer needs it).
func SSHKeyPath(p paths.Paths) string { return filepath.Join(p.ConfigDir, "ssh", "cluster_ed25519") }

// EnsureSSHKey returns the cluster key's authorized_keys line, creating the key pair
// (0600 private, 0644 public) when it does not exist yet.
func EnsureSSHKey(p paths.Paths) (string, error) {
	if pub, err := ReadSSHPublicKey(p); err == nil {
		return pub, nil
	}
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, SSHKeyComment)
	if err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + SSHKeyComment
	path := SSHKeyPath(p)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	if err := os.WriteFile(path+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	return line, nil
}

// ReadSSHPublicKey returns the cluster key's authorized_keys line, derived from the
// private key (the .pub file is informational only).
func ReadSSHPublicKey(p paths.Paths) (string, error) {
	b, err := os.ReadFile(SSHKeyPath(p))
	if err != nil {
		return "", err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", SSHKeyPath(p), err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) + " " + SSHKeyComment, nil
}

// ReadSSHPrivateKey returns the PEM-encoded cluster private key.
func ReadSSHPrivateKey(p paths.Paths) ([]byte, error) { return os.ReadFile(SSHKeyPath(p)) }
