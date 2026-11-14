# 2026-10-18: Writer lease delay and replica path failures

## Impact

On `hq`, writer lease renewal repeatedly took about 135 seconds while the lease TTL was 60 seconds. The API alternated between WRITABLE and READ_ONLY. PostgreSQL WAL streaming to `dr1` continued, but active checks from writer containers to the standby failed. Replica verification later failed its MinIO replication sentinel after 90 seconds. These observations are from operator-provided production command output; the precise start and end of user-visible impact were not established.

## Timeline (2026-10-18)

The `dr status` timestamp is explicitly UTC. The journal excerpt did not include a timezone, so its clock values are kept as displayed rather than incorrectly converted to UTC.

| Time | Observation |
| --- | --- |
| 18:51:35–19:14:06 (journal clock) | Consecutive lease service runs took roughly 135 seconds, exceeding the 60-second TTL. A child `docker exec ... psql` peer probe was present. |
| 19:14:06 onward (journal clock) | After installing CLI commit `57392401cf68ce0fa13590efa1299e517b86949e`, a manual renewal completed in 2.43 seconds and timer runs completed in roughly two seconds. |
| 17:16:55Z | An operator-provided `dr status` snapshot showed writer `hq` and streaming `dr1`, with a local-only lease and unreachable desktop replica. Clock conversion of the journal lines is unverified, so no cross-source ordering is inferred. |
| Later checks | Host TCP to `10.66.0.2:5432` and `:9000` succeeded; the corresponding PostgreSQL and MinIO container paths failed. The MinIO container attempt produced zero packets on `qabas0`. |
| Recovery | Two narrow runtime `inet fort forward` rules were added. PostgreSQL container `pg_isready` then accepted, MinIO container TCP connected, and `qabas replica verify dr1` passed all checks, including the replicated sentinel. |

## Root causes

1. The previously deployed CLI commit `829abf12cd3a0a03f8d62944de097b3f7ab3aa79` had a nested shell quoting bug in the PostgreSQL peer probe. The updated CLI fixed the probe and restored short lease renewals. The process listing alone did not prove argument parsing; the before/after renewal times support this conclusion.
2. The host's operator-owned `inet fort forward` chain had `policy drop` and lacked new-connection allowances from the PostgreSQL and MinIO container bridges to the peer's WireGuard address. Host TCP success did not imply container TCP success. The first rule restored PostgreSQL container egress; the second restored MinIO container egress and sentinel replication.

## Recovery evidence and firewall ownership

The working runtime rules allowed only new TCP connections from `172.21.0.2` on `br-8e0850eb62e2` to `10.66.0.2:5432`, and from `172.18.0.2` on `br-1d57913087d8` to `10.66.0.2:9000`, both leaving via `qabas0`. The `inet fort` rule counters incremented during successful probes. These `nft add rule` changes are **runtime-only** and can disappear on firewall reload or reboot. The operator must add equivalent rules to the persistent `inet fort` source configuration, using stable bridge and address assignments where possible, then reload and retest. Do not edit Docker-generated nftables tables.

## Follow-ups

- The incident review found that timer-driven self-fencing wrote `qabas.DEMOTED` even when the registry still named `hq` as writer. Renewal now expires the local API lease and stops running data services without writing the durable demotion marker. It checks Docker state before SQL and avoids repeated SQL/stop calls against an already stopped container. A deliberate role transition still uses the marker.
- Existing registries need an explicit lease mode selection. `local-only` is a documented availability choice without independent quorum fencing or cross-node promotion. `strict` requires three configured witnesses with unique declared failure domains and reachable endpoints; it remains blocked by the missing durability verifier described in [the strict consistency release gate](../dr-strict-consistency.md). Do not change production mode merely to clear an alert.

- Complete the strict consistency release gate in [dr-strict-consistency.md](../dr-strict-consistency.md) before deploying this CLI's fail-closed lease behavior. Once the writer has independent witnesses and verified PostgreSQL and MinIO durability, use `qabas dr lease install` to install both renewal and path-check timers. Confirm `qabas dr check` and `qabas replica verify dr1` after firewall reload and after reboot.
- Configure independent lease witnesses. The observed lease was local-only, so it lacked a witness majority to arbitrate a partition.
- Restore or remove the unreachable desktop replica at `10.66.0.3`; otherwise DR readiness remains degraded.
- Keep firewall changes operator-owned. The CLI should identify failing container paths and explain the required narrow forwarding policy without modifying host rules.

No credentials or private keys are included in this report.
