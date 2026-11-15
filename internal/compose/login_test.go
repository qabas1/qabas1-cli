package compose_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/compose"
)

func TestLoginGHCR(t *testing.T) {
	var gotReg, gotUser, gotPass string
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Login: func(registry, username, password string) error {
			gotReg, gotUser, gotPass = registry, username, password
			return nil
		},
	}
	if err := rt.LoginGHCR("alice", "secret"); err != nil {
		t.Fatal(err)
	}
	if gotReg != "ghcr.io" || gotUser != "alice" || gotPass != "secret" {
		t.Fatalf("got %q %q %q", gotReg, gotUser, gotPass)
	}
}

func TestLoginGHCRMissingFields(t *testing.T) {
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Login:    func(string, string, string) error { return nil },
	}
	if err := rt.LoginGHCR("", "t"); err == nil {
		t.Fatal("expected username error")
	}
	if err := rt.LoginGHCR("u", ""); err == nil {
		t.Fatal("expected token error")
	}
}

func TestLoginGHCRFailureHint(t *testing.T) {
	rt := &compose.Runtime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Login:    func(string, string, string) error { return errors.New("unauthorized") },
	}
	err := rt.LoginGHCR("alice", "bad")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "read:packages") {
		t.Fatalf("expected packages hint: %v", err)
	}
}
