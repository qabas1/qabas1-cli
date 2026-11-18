package manifest_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/qabas1/qabas1-cli/internal/exitcode"
	"github.com/qabas1/qabas1-cli/internal/manifest"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	p := filepath.Join(filepath.Dir(file), "..", "release", "testdata", "releases", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseAndCompat(t *testing.T) {
	rel, err := manifest.Parse(fixture(t, "2.8.0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Release.Version != "2.8.0" {
		t.Fatalf("version %s", rel.Release.Version)
	}
	if err := rel.CheckCompatibility(); err != nil {
		t.Fatal(err)
	}
	if got := rel.Components.API.DigestRef(); got != "ghcr.io/qabas1/qabas1-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("digest ref %s", got)
	}
}

func TestParseSecretFileCapability(t *testing.T) {
	raw := append(bytes.Clone(fixture(t, "2.8.0.yaml")), []byte("\ncapabilities:\n  secretFileConfig: true\n")...)
	rel, err := manifest.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !rel.Capabilities.SecretFileConfig {
		t.Fatal("secret file capability was lost")
	}
}

func TestCompatMismatch(t *testing.T) {
	rel, err := manifest.Parse(fixture(t, "2.8.0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rel.Compatibility.Dash.API = ">=9.0.0"
	if err := rel.CheckCompatibility(); err == nil {
		t.Fatal("expected compatibility error")
	} else if exitcode.CodeOf(err) != exitcode.Compatibility {
		t.Fatalf("code %d", exitcode.CodeOf(err))
	}
}

func TestCLIRequirement(t *testing.T) {
	rel, err := manifest.Parse(fixture(t, "2.9.0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rel.CheckCLI("dev"); err != nil {
		t.Fatal(err)
	}
	if err := rel.CheckCLI("1.3.0"); err == nil {
		t.Fatal("expected CLI too old")
	} else if exitcode.CodeOf(err) != exitcode.Compatibility {
		t.Fatalf("code %d", exitcode.CodeOf(err))
	}
	if err := rel.CheckCLI("1.4.0"); err != nil {
		t.Fatal(err)
	}
}

func TestCompare(t *testing.T) {
	if manifest.Compare("2.9.0", "2.8.0") <= 0 {
		t.Fatal("expected 2.9.0 > 2.8.0")
	}
	if manifest.Compare("2.8.1", "2.8.1") != 0 {
		t.Fatal("equal")
	}
}

func TestRejectLatestTag(t *testing.T) {
	rel, err := manifest.Parse(fixture(t, "2.8.0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rel.Components.API.Image = "ghcr.io/qabas1/qabas1-api:latest"
	if err := rel.Validate(); err == nil {
		t.Fatal("expected :latest rejection")
	}
}

func TestDevChannelIsValid(t *testing.T) {
	rel, err := manifest.Parse(fixture(t, "2.8.0.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	rel.Release.Channel = "dev"
	if err := rel.Validate(); err != nil {
		t.Fatal(err)
	}
}
