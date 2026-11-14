package nodestack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeCommandPerRole(t *testing.T) {
	ro := ComposeCommand(Remote, ProfileReplicaRO, true, "up -d")
	if !strings.Contains(ro, "-f /var/lib/qabasone/compose/compose.replica-ro.yaml") || strings.Contains(ro, "--profile writers") || strings.Contains(ro, OverlayPITR) {
		t.Fatalf("replica-ro: %s", ro)
	}
	drp := ComposeCommand(Remote, ProfileDRPrimary, false, "up -d")
	if !strings.Contains(drp, OverlayPrimary) || !strings.Contains(drp, OverlayDRPrimary) || !strings.Contains(drp, "--profile writers") {
		t.Fatalf("dr-primary: %s", drp)
	}
	if !strings.Contains(drp, "--env-file /etc/qabasone/dr/node.env") {
		t.Fatalf("node.env must be an interpolation env-file: %s", drp)
	}
}

func TestDirectivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, OverlaysFile), []byte(LocalOverlays(Remote, ProfileDRPrimary, false)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddDirective(dir, "file:"+OverlayPITR); err != nil {
		t.Fatal(err)
	}
	if err := AddDirective(dir, "file:"+OverlayPITR); err != nil {
		t.Fatal(err)
	}
	d := ReadDirectives(dir)
	if len(d.Files) != 3 || filepath.Base(d.Files[2]) != OverlayPITR {
		t.Fatalf("files = %v", d.Files)
	}
	if len(d.Profiles) != 1 || d.Profiles[0] != "writers" || len(d.EnvFiles) != 1 {
		t.Fatalf("directives = %+v", d)
	}
	_ = os.WriteFile(filepath.Join(dir, OverlaysFile), []byte("file:../../etc/passwd\n"), 0o644)
	if len(ReadDirectives(dir).Files) != 0 {
		t.Fatal("overlay paths outside the compose dir must be ignored")
	}
}

func TestSetEnvPreservesOtherLines(t *testing.T) {
	in := []byte("# c\nA=1\nMINIO_ROOT_USER=old\nB=2\n")
	out := string(SetEnv(in, map[string]string{"MINIO_ROOT_USER": "new", "C": "3"}))
	if out != "# c\nA=1\nMINIO_ROOT_USER=new\nB=2\nC=3\n" {
		t.Fatalf("got %q", out)
	}
	if ParseEnv([]byte(`K="v w"`))["K"] != "v w" {
		t.Fatal("quoted values must be unquoted")
	}
}
