package cluster

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/qabas1/qabas1-cli/internal/paths"
	"github.com/qabas1/qabas1-cli/internal/redact"
)

// Per-node secret names (files under /etc/qabasone/nodes/<node>/, mode 0600).
const (
	SecretReplicationPassword = "replication.password"
	SecretMinioReplAccessKey  = "minio-replication.access"
	SecretMinioReplSecretKey  = "minio-replication.secret"
	SecretWGPrivateKey        = "wg.key"
	SecretBackupPassword      = "backup.password"
	SecretMinioRootUser       = "minio-root.user"
	SecretMinioRootPassword   = "minio-root.password"
	SecretWitnessToken        = "witness.token"
	// SecretRelayToken authenticates a relay node to the writer's TLS relay.
	SecretRelayToken = "relay.token"

	// SharedScope holds cluster-wide secrets (replication/backup role passwords,
	// witness token) that every node needs after a promotion.
	SharedScope = "cluster-shared"
)

func NodeSecretsDir(p paths.Paths, node string) string {
	return filepath.Join(p.ConfigDir, "nodes", node)
}

// ReadSecret returns a per-node secret and registers it for redaction.
func ReadSecret(p paths.Paths, node, name string) (string, error) {
	if err := ValidateNodeName(node); err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(NodeSecretsDir(p, node), name))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	redact.Register(v)
	return v, nil
}

// WriteSecret stores a per-node secret with 0700/0600 permissions.
func WriteSecret(p paths.Paths, node, name, value string) error {
	if err := ValidateNodeName(node); err != nil {
		return err
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid secret name %q", name)
	}
	dir := NodeSecretsDir(p, node)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	redact.Register(value)
	return os.Rename(tmp, filepath.Join(dir, name))
}

// EnsureSecret returns an existing secret or generates and stores a new random one.
func EnsureSecret(p paths.Paths, node, name string) (string, error) {
	if v, err := ReadSecret(p, node, name); err == nil && v != "" {
		return v, nil
	}
	v, err := RandomToken(32)
	if err != nil {
		return "", err
	}
	return v, WriteSecret(p, node, name, v)
}

// RemoveNodeSecrets deletes a node's secret directory.
func RemoveNodeSecrets(p paths.Paths, node string) error {
	if err := ValidateNodeName(node); err != nil {
		return err
	}
	return os.RemoveAll(NodeSecretsDir(p, node))
}

// RandomToken returns a URL-safe random string with n bytes of entropy.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.RawURLEncoding.EncodeToString(b), "="), nil
}
