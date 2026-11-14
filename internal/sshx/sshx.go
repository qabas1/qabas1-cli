// Package sshx is qabas's SSH client with mandatory host-key pinning.
//
// There is no StrictHostKeyChecking=no equivalent: every connection verifies the
// server key against the SHA256 fingerprint pinned in the cluster registry. The only
// unpinned operation is ScanHostKey, which returns the fingerprint for the operator
// to confirm (or to compare with --host-key-fingerprint) before it is pinned.
package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const dialTimeout = 15 * time.Second

var ErrHostKeyMismatch = errors.New("SSH host key mismatch")

// Fingerprint returns the OpenSSH-style SHA256 fingerprint ("SHA256:...").
func Fingerprint(key ssh.PublicKey) string { return ssh.FingerprintSHA256(key) }

// Addr joins host and port.
func Addr(host string, port int) string {
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// PreferredHostKeyAlgorithms makes the server present its ed25519 key when it has one,
// so the pinned fingerprint matches `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
// The library default puts ECDSA first.
var PreferredHostKeyAlgorithms = []string{
	ssh.KeyAlgoED25519,
	ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256,
}

// HostKeyListHint prints every host key fingerprint on the server.
const HostKeyListHint = `for f in /etc/ssh/ssh_host_*_key.pub; do ssh-keygen -lf "$f"; done`

type errScanned struct{}

func (errScanned) Error() string { return "host key captured" }

// ScanHostKey connects just far enough to read the server host key.
func ScanHostKey(ctx context.Context, addr string) (fingerprint, keyType string, err error) {
	var captured ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "qabas-hostkey-scan",
		Auth: []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = key
			return errScanned{}
		},
		HostKeyAlgorithms: PreferredHostKeyAlgorithms,
		Timeout:           dialTimeout,
	}
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", "", err
	}
	defer conn.Close()
	_, _, _, herr := ssh.NewClientConn(conn, addr, cfg)
	if captured == nil {
		if herr == nil {
			herr = errors.New("server presented no host key")
		}
		return "", "", herr
	}
	return Fingerprint(captured), captured.Type(), nil
}

// PinnedCallback accepts only a key whose fingerprint equals pinned.
func PinnedCallback(pinned string) ssh.HostKeyCallback {
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if pinned == "" {
			return fmt.Errorf("%w: no pinned fingerprint for %s (run: qabas replica add)", ErrHostKeyMismatch, hostname)
		}
		if got := Fingerprint(key); got != pinned {
			return fmt.Errorf("%w for %s: expected %s, got %s key %s - possible MITM or reinstalled host; "+
				"verify out of band on the host with: %s", ErrHostKeyMismatch, hostname, pinned, key.Type(), got, HostKeyListHint)
		}
		return nil
	}
}

// AuthMethods returns identity-file + agent auth. identity may be empty. extraKeys
// (for example the cluster key) are tried first and skipped silently when absent.
func AuthMethods(identity string, extraKeys ...string) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	cleanup := func() {}
	var signers []ssh.Signer
	for _, path := range extraKeys {
		if path == "" {
			continue
		}
		if b, err := os.ReadFile(path); err == nil {
			if s, err := ssh.ParsePrivateKey(b); err == nil {
				signers = append(signers, s)
			}
		}
	}
	candidates := []string{identity}
	if identity == "" {
		if home, err := os.UserHomeDir(); err == nil {
			candidates = []string{
				filepath.Join(home, ".ssh", "id_ed25519"),
				filepath.Join(home, ".ssh", "id_ecdsa"),
				filepath.Join(home, ".ssh", "id_rsa"),
			}
		}
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			if identity != "" {
				return nil, cleanup, fmt.Errorf("read identity %s: %w", path, err)
			}
			continue
		}
		s, err := ssh.ParsePrivateKey(b)
		if err != nil {
			if identity != "" {
				return nil, cleanup, fmt.Errorf("parse identity %s (passphrase keys must be loaded in ssh-agent): %w", path, err)
			}
			continue
		}
		signers = append(signers, s)
	}
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if c, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(c).Signers))
			cleanup = func() { _ = c.Close() }
		}
	}
	if len(methods) == 0 {
		return nil, cleanup, errors.New("no SSH credentials: start ssh-agent or pass --identity")
	}
	return methods, cleanup, nil
}

// KeyFileAuth authenticates with exactly one private key file (no agent, no defaults).
func KeyFileAuth(path string) ([]ssh.AuthMethod, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(s)}, nil
}

// Dial opens a pinned SSH connection. Pins taken by older releases may be of the key
// the library negotiated by default (ECDSA), so a mismatch retries with that order.
func Dial(ctx context.Context, addr, user, pinned string, auth []ssh.AuthMethod) (*ssh.Client, error) {
	c, err := dial(ctx, addr, user, pinned, auth, PreferredHostKeyAlgorithms)
	if errors.Is(err, ErrHostKeyMismatch) && pinned != "" {
		if legacy, lerr := dial(ctx, addr, user, pinned, auth, nil); lerr == nil {
			return legacy, nil
		}
	}
	return c, err
}

func dial(ctx context.Context, addr, user, pinned string, auth []ssh.AuthMethod, algos []string) (*ssh.Client, error) {
	cfg := &ssh.ClientConfig{
		User:              user,
		Auth:              auth,
		HostKeyCallback:   PinnedCallback(pinned),
		HostKeyAlgorithms: algos,
		Timeout:           dialTimeout,
	}
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(c, chans, reqs), nil
}
