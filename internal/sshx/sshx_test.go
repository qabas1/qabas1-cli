package sshx

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPinnedCallback(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint(key)

	if err := PinnedCallback(fp)("h", nil, key); err != nil {
		t.Fatalf("pinned key rejected: %v", err)
	}
	if err := PinnedCallback("SHA256:other")("h", nil, key); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("mismatch not detected: %v", err)
	}
	if err := PinnedCallback("")("h", nil, key); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("unpinned host accepted: %v", err)
	}
}

// testServer serves SSH with both an ECDSA and an ed25519 host key (like a stock sshd)
// and accepts any public key.
func testServer(t *testing.T) (addr string, ecdsaFP, ed25519FP string) {
	t.Helper()
	_, edPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edSigner, err := ssh.NewSignerFromKey(edPriv)
	if err != nil {
		t.Fatal(err)
	}
	ecPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecSigner, err := ssh.NewSignerFromKey(ecPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(ecSigner)
	cfg.AddHostKey(edSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "test")
				}
			}()
		}
	}()
	return ln.Addr().String(), Fingerprint(ecSigner.PublicKey()), Fingerprint(edSigner.PublicKey())
}

func clientAuth(t *testing.T) []ssh.AuthMethod {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(s)}
}

func TestScanHostKeyPrefersEd25519(t *testing.T) {
	addr, _, edFP := testServer(t)
	fp, kind, err := ScanHostKey(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if kind != ssh.KeyAlgoED25519 || fp != edFP {
		t.Fatalf("scanned %s %s, want ed25519 %s", kind, fp, edFP)
	}
}

func TestDialPinnedKeys(t *testing.T) {
	addr, ecFP, edFP := testServer(t)
	auth := clientAuth(t)
	for name, pin := range map[string]string{"ed25519": edFP, "legacy ecdsa": ecFP} {
		c, err := Dial(context.Background(), addr, "u", pin, auth)
		if err != nil {
			t.Fatalf("%s pin: %v", name, err)
		}
		_ = c.Close()
	}
	if _, err := Dial(context.Background(), addr, "u", "SHA256:other", auth); !errors.Is(err, ErrHostKeyMismatch) {
		t.Fatalf("wrong pin: %v", err)
	}
}
