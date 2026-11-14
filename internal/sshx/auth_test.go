package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestAuthMethodsExtraKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_AUTH_SOCK", "")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(home, "cluster_ed25519")
	if err := os.WriteFile(key, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := AuthMethods("", filepath.Join(home, "missing")); err == nil {
		t.Fatal("no credentials at all must fail")
	}
	m, cleanup, err := AuthMethods("", key)
	defer cleanup()
	if err != nil || len(m) != 1 {
		t.Fatalf("extra key: methods=%d err=%v", len(m), err)
	}
	if m, err := KeyFileAuth(key); err != nil || len(m) != 1 {
		t.Fatalf("KeyFileAuth: %d %v", len(m), err)
	}
	if _, err := KeyFileAuth(filepath.Join(home, "missing")); err == nil {
		t.Fatal("KeyFileAuth accepted a missing file")
	}
}
