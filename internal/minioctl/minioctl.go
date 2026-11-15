// Package minioctl manages MinIO one-way bucket replication and app access policies
// through one-off `mc` containers (same mc digest as the release). Credentials are passed
// through 0600 env files (MC_HOST_<alias>), never argv.
package minioctl

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qabas1/qabasone-cli/internal/nodeexec"
)

const (
	PolicyAppRO      = "qabas-app-ro"
	PolicyAppRW      = "qabas-app-rw"
	PolicyReplTarget = "qabas-replication-target"
	// ComposeDataNetwork is the compose data network the minio service lives on.
	ComposeDataNetwork = "qabasone_qabasone_data"
	// SentinelPrefix holds verify sentinels (tiny objects proving replication flows).
	SentinelPrefix = "qabas-dr/sentinel/"
)

// ReplicateFlags are the flags for one-way replication (never site replication).
const ReplicateFlags = "existing-objects,delete,delete-marker,metadata-sync"

type statement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource []string `json:"Resource"`
}

type policy struct {
	Version   string      `json:"Version"`
	Statement []statement `json:"Statement"`
}

func render(p policy) string {
	b, _ := json.MarshalIndent(p, "", "  ")
	return string(b)
}

// AppReadOnlyPolicy: DR_READ_ONLY app key can read and list, nothing else.
func AppReadOnlyPolicy(bucket string) string {
	return render(policy{Version: "2012-10-17", Statement: []statement{
		{Effect: "Allow", Action: []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketVersions"}, Resource: []string{"arn:aws:s3:::" + bucket}},
		{Effect: "Allow", Action: []string{"s3:GetObject", "s3:GetObjectVersion"}, Resource: []string{"arn:aws:s3:::" + bucket + "/*"}},
	}})
}

// AppReadWritePolicy: writer app key (primary / DR_PRIMARY), bucket-scoped.
func AppReadWritePolicy(bucket string) string {
	return render(policy{Version: "2012-10-17", Statement: []statement{
		{Effect: "Allow", Action: []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketVersions", "s3:ListBucketMultipartUploads"}, Resource: []string{"arn:aws:s3:::" + bucket}},
		{Effect: "Allow", Action: []string{"s3:GetObject", "s3:GetObjectVersion", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"}, Resource: []string{"arn:aws:s3:::" + bucket + "/*"}},
	}})
}

// ReplicationTargetPolicy: the source MinIO uses this key to push into the destination.
func ReplicationTargetPolicy(bucket string) string {
	return render(policy{Version: "2012-10-17", Statement: []statement{
		{Effect: "Allow", Action: []string{"s3:GetReplicationConfiguration", "s3:ListBucket", "s3:ListBucketMultipartUploads", "s3:GetBucketLocation", "s3:GetBucketVersioning", "s3:GetBucketObjectLockConfiguration"}, Resource: []string{"arn:aws:s3:::" + bucket}},
		{Effect: "Allow", Action: []string{"s3:GetReplicationConfiguration", "s3:ReplicateTags", "s3:AbortMultipartUpload", "s3:GetObject", "s3:GetObjectVersion", "s3:GetObjectVersionTagging", "s3:PutObject", "s3:PutObjectRetention", "s3:PutBucketObjectLockConfiguration", "s3:PutObjectLegalHold", "s3:DeleteObject", "s3:ReplicateObject", "s3:ReplicateDelete"}, Resource: []string{"arn:aws:s3:::" + bucket + "/*"}},
	}})
}

// HostEnv returns an MC_HOST_<alias> line for an env file.
func HostEnv(alias, endpoint, access, secret string) string {
	scheme := "http://"
	rest := endpoint
	if i := strings.Index(endpoint, "://"); i >= 0 {
		scheme, rest = endpoint[:i+3], endpoint[i+3:]
	}
	return fmt.Sprintf("MC_HOST_%s=%s%s:%s@%s", alias, scheme, access, secret, rest)
}

// Script wraps mc commands in a one-off container on the compose data network. envFile
// must hold MC_HOST_* (and any REMOTE_* values referenced by the commands); it is
// deleted afterwards.
func Script(image, envFile, mcCommands string) string {
	return fmt.Sprintf(`set -eu
trap 'rm -f %[2]s' EXIT
docker run --rm --network %[3]s --env-file %[2]s -e MC_CONFIG_DIR=/tmp/mc --entrypoint sh %[1]s -c %[4]s
`, image, nodeexec.Quote(envFile), ComposeDataNetwork, nodeexec.Quote("set -eu\n"+mcCommands))
}

// EnsureAppUserCommands creates/updates the app IAM user on a node with the given policy
// (the app keeps its STORAGE_ACCESS_KEY_ID; the node's root credentials differ).
// Expects env APP_ACCESS, APP_SECRET, POLICY_JSON_RO, POLICY_JSON_RW.
func EnsureAppUserCommands(alias, bucket string, readOnly bool) string {
	attach := PolicyAppRW
	detach := PolicyAppRO
	if readOnly {
		attach, detach = PolicyAppRO, PolicyAppRW
	}
	return fmt.Sprintf(`printf '%%s' "$POLICY_JSON_RO" > /tmp/ro.json
printf '%%s' "$POLICY_JSON_RW" > /tmp/rw.json
mc admin policy create %[1]s %[3]s /tmp/ro.json >/dev/null
mc admin policy create %[1]s %[4]s /tmp/rw.json >/dev/null
mc admin user add %[1]s "$APP_ACCESS" "$APP_SECRET" >/dev/null
mc admin policy detach %[1]s %[6]s --user "$APP_ACCESS" >/dev/null 2>&1 || true
mc admin policy attach %[1]s %[5]s --user "$APP_ACCESS" >/dev/null 2>&1 || true
mc version enable %[1]s/%[2]s >/dev/null
echo "app policy: %[5]s"
`, alias, bucket, PolicyAppRO, PolicyAppRW, attach, detach)
}

// EnsureReplicationTargetCommands creates the replication-target user on the destination.
// Expects env REPL_ACCESS, REPL_SECRET, POLICY_JSON_REPL.
func EnsureReplicationTargetCommands(alias, bucket string) string {
	return fmt.Sprintf(`mc mb --ignore-existing %[1]s/%[2]s >/dev/null
mc version enable %[1]s/%[2]s >/dev/null
printf '%%s' "$POLICY_JSON_REPL" > /tmp/repl.json
mc admin policy create %[1]s %[3]s /tmp/repl.json >/dev/null
mc admin user add %[1]s "$REPL_ACCESS" "$REPL_SECRET" >/dev/null
mc admin policy attach %[1]s %[3]s --user "$REPL_ACCESS" >/dev/null 2>&1 || true
`, alias, bucket, PolicyReplTarget)
}

// AddReplicationCommands adds (idempotently) a one-way rule source -> destination.
// Expects env REMOTE_BUCKET_URL (http://REPL_ACCESS:REPL_SECRET@<wg-ip>:9000/<bucket>).
func AddReplicationCommands(srcAlias, bucket, targetWGIP string, priority int) string {
	return fmt.Sprintf(`mc version enable %[1]s/%[2]s >/dev/null
rules=$(mc replicate ls %[1]s/%[2]s 2>/dev/null || true)
case "$rules" in
*'%[3]s:9000'*)
  echo "replication rule to %[3]s already present"
  ;;
*)
  mc replicate add %[1]s/%[2]s --remote-bucket "$REMOTE_BUCKET_URL" --replicate "%[4]s" --priority %[5]d
  ;;
esac
`, srcAlias, bucket, targetWGIP, ReplicateFlags, priority)
}

// RemoveReplicationCommands removes every rule on the bucket pointing at targetWGIP.
func RemoveReplicationCommands(srcAlias, bucket, targetWGIP string) string {
	return fmt.Sprintf(`rules=$(mc replicate ls %[1]s/%[2]s 2>/dev/null || true)
target_match=0
total=0
matched=0
target_ids=
old_ifs=$IFS
IFS='
'
set -f
for line in $rules; do
  case "$line" in
    *"Remote Bucket:"*)
      case "$line" in
        *'%[3]s:9000'*) target_match=1 ;;
        *) target_match=0 ;;
      esac
      ;;
    *"Rule ID:"*)
      total=$((total + 1))
      if [ "$target_match" -eq 1 ]; then
        id=${line#*Rule ID: }
        id=${id%% *}
        if [ -n "$id" ]; then
          matched=$((matched + 1))
          target_ids="$target_ids $id"
        fi
      fi
      target_match=0
      ;;
  esac
done
set +f
IFS=$old_ifs
if [ "$total" -gt 0 ] && [ "$matched" -eq "$total" ]; then
  mc replicate rm %[1]s/%[2]s --all --force >/dev/null
else
  for id in $target_ids; do
    mc replicate rm %[1]s/%[2]s --id "$id" >/dev/null
  done
fi
`, srcAlias, bucket, targetWGIP)
}

// DisableAllInboundCommands removes all rules from a bucket (used when a promoted node
// must stop being a destination; destinations have no rules of their own, so this only
// clears rules pointing elsewhere that are stale after a role change).
func RemoveAllReplicationCommands(alias, bucket string) string {
	return fmt.Sprintf("mc replicate rm %[1]s/%[2]s --all --force >/dev/null 2>&1 || true\n", alias, bucket)
}

// StatusCommands prints replication status as JSON lines.
func StatusCommands(alias, bucket string) string {
	return fmt.Sprintf("mc replicate status %[1]s/%[2]s --json 2>/dev/null || true\n", alias, bucket)
}

// SentinelPutCommands writes a sentinel object on the source.
func SentinelPutCommands(alias, bucket, key string) string {
	return fmt.Sprintf("date -u +%%FT%%TZ | mc pipe %[1]s/%[2]s/%[3]s >/dev/null\n", alias, bucket, key)
}

// SentinelStatCommands exits 0 when the sentinel exists on the target.
func SentinelStatCommands(alias, bucket, key string) string {
	return fmt.Sprintf("mc stat %[1]s/%[2]s/%[3]s >/dev/null\n", alias, bucket, key)
}
