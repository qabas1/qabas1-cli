package minioctl

import (
	"strings"
	"testing"
)

func TestReadOnlyPolicyHasNoWrites(t *testing.T) {
	p := AppReadOnlyPolicy("qabasone")
	for _, forbidden := range []string{"PutObject", "DeleteObject", "s3:*"} {
		if strings.Contains(p, forbidden) {
			t.Fatalf("read-only policy grants %s:\n%s", forbidden, p)
		}
	}
	if !strings.Contains(p, "s3:GetObject") {
		t.Fatal("read-only policy cannot read")
	}
}

func TestReplicationIsOneWayNotSiteReplication(t *testing.T) {
	cmds := AddReplicationCommands("src", "qabasone", "10.66.0.2", 10)
	if strings.Contains(cmds, "admin replicate") {
		t.Fatal("site replication (active-active) must never be used")
	}
	if !strings.Contains(cmds, ReplicateFlags) || !strings.Contains(cmds, "$REMOTE_BUCKET_URL") {
		t.Fatalf("unexpected replicate command:\n%s", cmds)
	}
}

func TestReplicationRuleScriptsDoNotRequireTextUtilities(t *testing.T) {
	add := AddReplicationCommands("src", "qabasone", "10.66.0.2", 10)
	if strings.Contains(add, "grep") || strings.Contains(add, "--json") ||
		!strings.Contains(add, `case "$rules" in`) || !strings.Contains(add, "*'10.66.0.2:9000'*") {
		t.Fatalf("add script must detect an existing target using shell built-ins:\n%s", add)
	}

	remove := RemoveReplicationCommands("src", "qabasone", "10.66.0.2")
	for _, forbidden := range []string{"grep", "sed"} {
		if strings.Contains(remove, forbidden) {
			t.Fatalf("remove script depends on unavailable %s:\n%s", forbidden, remove)
		}
	}
	for _, want := range []string{
		`for line in $rules`,
		`*"Remote Bucket:"*`,
		`*'10.66.0.2:9000'*`,
		`*"Rule ID:"*`,
		`id=${line#*Rule ID: }`,
		`mc replicate rm src/qabasone --id "$id" >/dev/null`,
		`mc replicate rm src/qabasone --all --force >/dev/null`,
		`[ "$matched" -eq "$total" ]`,
	} {
		if !strings.Contains(remove, want) {
			t.Fatalf("remove script missing %q:\n%s", want, remove)
		}
	}
	if strings.Contains(remove, `--id "$id" --force`) {
		t.Fatalf("targeted rule removal must not use --force:\n%s", remove)
	}
	if strings.Contains(remove, "--json") {
		t.Fatalf("remove script cannot match endpoints in JSON output:\n%s", remove)
	}
}

func TestScriptKeepsSecretsOutOfArgv(t *testing.T) {
	s := Script("mc@sha256:x", "/run/qabas/mc.env", "mc ls src")
	if strings.Contains(s, "MC_HOST_") {
		t.Fatalf("credentials inline in script:\n%s", s)
	}
	if !strings.Contains(s, "--env-file '/run/qabas/mc.env'") || !strings.Contains(s, "rm -f") {
		t.Fatalf("env file not used/cleaned:\n%s", s)
	}
}

func TestHostEnv(t *testing.T) {
	if got := HostEnv("src", "http://minio:9000", "AK", "SK"); got != "MC_HOST_src=http://AK:SK@minio:9000" {
		t.Fatalf("HostEnv = %q", got)
	}
}
