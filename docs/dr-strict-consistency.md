# Strict DR consistency requirements

**Deployment status:** Lease mode must now be selected explicitly with `qabas dr lease mode local-only` or `qabas dr lease mode strict`. An older registry without a mode fails closed. Local-only renews the node-local API lease without independent fencing and disables cross-node promotion and switchover. Strict mode requires at least three declared failure domains and reachable witnesses, and fences renewal when a promotable peer probe is unknown. Strict renewal, initial grants, promotion, and switchover also require a PostgreSQL-and-MinIO durability verifier. No production verifier exists yet, so strict mode deliberately stops writes. This is a safety stop, not a completed zero-loss DR system.

jsreport Studio now owns report templates in its own volume. Managed backups include that
volume for releases that declare jsreport, but replica synchronization does not replicate
it. Restore and verify the Studio data before issuing documents after a recovery; automatic
DR promotion cannot claim template availability until a replication check is added.

`qabas dr witness add NAME --url URL --failure-domain SITE` records the claimed failure domain. The CLI can reject duplicate names, endpoints, and declared domains and probe witness connectivity. It cannot prove that hosts or storage are physically independent; operators must verify this outside the CLI. Do not select strict mode until the full release gate below is satisfied. Mode changes are audited and the registry must be synchronized to every node.

## Non-negotiable invariants

1. Exactly one writer may accept business writes. The old writer must be fenced at the database and object-store write paths before a new writer is opened. A failed TCP or SSH probe is not proof of fencing. Promotion needs an independently witnessed lease and a verified fencing mechanism for the old writer, including after reboot.
2. Every acknowledged PostgreSQL commit must be durably present on the intended failover target. Configure PostgreSQL synchronous replication with a specifically named target, `synchronous_commit=on` or `remote_apply`, `fsync=on`, and protected server-side settings. Verify `pg_stat_replication.sync_state` and the exact target identity. An unavailable synchronous standby must block commits. Do not promote an asynchronous or unverified standby. A zero-second lag sample does not prove zero recovery point.
3. Every acknowledged object mutation must be durably present on the intended failover target. Current MinIO one-way bucket replication is asynchronous. For the AGPL MinIO product in use here, its documentation says `--sync` may still return a successful source `PUT` when replication fails; its implementation notes also say `DELETE` operations remain queued in sync mode. Do not transfer guarantees from MinIO AIStor documentation to this deployment. The application must verify the destination object version, content hash, and replication completion before acknowledging the operation, or use a storage architecture that provides an equivalent durable cross-site acknowledgment. Apply the rule to deletes and overwrites as well as uploads.
4. PostgreSQL rows and MinIO objects are separate systems. Attachment workflows need an explicit state machine or recovery protocol so an acknowledged accounting operation cannot reference an object that is absent after failover. Never present a partially durable operation as committed.
5. Witnesses must be at least three independently hosted, authenticated, and persistently stored voters. Three paths or files on the same machine are not independent. A CLI witness count cannot validate failure-domain independence.

## Current gaps outside this CLI workspace

- `qabas1-deploy/compose/compose.primary-replication.yaml` enables WAL streaming but does not configure `synchronous_standby_names` or force a synchronous commit policy.
- `qabas1-cli/internal/minioctl/minioctl.go` creates replication without `--sync`; changing that flag alone would still leave a successful-`PUT` loss window.
- `qabas1-api` uses `writer.json` to reject API mutations, but its lease check does not fence direct PostgreSQL or MinIO writers. When the writer-state file is not configured, `resolveWriterState` returns `holdsLease: true`; DR deployments must prohibit that fail-open path.
- The current promotion path has no durable proof that a selected standby received every acknowledged PostgreSQL commit or every acknowledged object mutation.
- The CLI durability hook is deliberately unbound in production. It must be backed by an end-to-end proof from the database, object store, and application before lease grants or writer transitions can succeed. A passing TCP probe or MinIO sentinel is insufficient.
- A delayed watchdog, paused host, root-level direct access, storage failure, or unverified clock skew can defeat a lease-based claim unless the fencing design and failure assumptions address them explicitly.

## Required release gate

Keep the new fail-closed CLI changes out of production until independent witnesses, PostgreSQL synchronous acknowledgment, object acknowledgment, database and object fencing, and promotion proofs have been implemented together. Exercise partitions in both directions, witness loss, standby loss, host pause/reboot, stale registry, and object-target failure. In every test, either the acknowledged write is present on the selected new writer or the client never receives success. A fault that cannot be proven safe must pause writes and refuse promotion.

Even this design cannot promise protection against simultaneous loss or corruption of every durable copy. Maintain verified, immutable backups and restore drills for that failure class.

## References

- [PostgreSQL synchronous replication and commit guarantees](https://www.postgresql.org/docs/16/warm-standby.html#SYNCHRONOUS-REPLICATION)
- [PostgreSQL failover and old-primary fencing](https://www.postgresql.org/docs/16/warm-standby-failover.html)
- [AGPL MinIO replication modes and successful PUT after failed synchronous replication](https://minio.community/community/minio-object-store/administration/bucket-replication.html)
- [MinIO replication design, including queued deletes](https://github.com/minio/minio/blob/master/docs/bucket/replication/DESIGN.md)
