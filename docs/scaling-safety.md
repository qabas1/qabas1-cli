# Scaling safety and the gateway

The managed gateway uses separate NGINX upstreams for API traffic:

- Ordinary REST: least connections, independent of the caller's public IP.
- `/socket.io` and `/socket.io/`: plain client-address hashing over resolved peers for polling
  affinity. WebSocket upgrades retain the existing headers and timeout.

Docker DNS still discovers replica addresses dynamically. REST uses short passive
connect-failure suppression, not an active readiness probe. Multiple office users
behind NAT can use different REST replicas; polling from one source stays on one
replica while the peer set is stable. Scaling changes can remap polling sessions,
so clients must reconnect/refetch normally. The trusted edge must overwrite
forwarding headers, not forward client-supplied values unchanged.

Do not add NGINX's `consistent` option to the single Docker-DNS server entry:
it hashes the logical server name and can then distribute requests among that
name's resolved addresses, defeating polling affinity. The live regression
checks requests against two addresses, not merely the rendered directive.

`qabas scale api N` refuses legacy gateway routing and an installed scale overlay
that lacks PgBouncer session pooling or explicit `PG_POOL_MAX`. It never silently
replaces an operator's cached overlay. Update that file from the reviewed deploy
revision and rerun `qabas gateway apply` before scaling with the new CLI.

The CLI renders effective Compose settings before changing capacity. It sums
API replica pools and each defined API-image worker's own pool maximum against
`DB_MAX_CONNECTIONS_BUDGET` (default 80), and checks session-pool server/client
capacity. Profiled worker definitions are conservatively included. This does not
discover arbitrary extra processes, actual PostgreSQL max_connections, or pools
in other applications; reserve headroom for them. The backend startup guard
remains independent and must also be configured consistently.

Changing PgBouncer mode can break existing connections. Use a drained maintenance
window and validate audit attribution before reopening ingress. The deploy
repository documents the opt-in resource profile and rollout sequence in
[`docs/api-autoscaling.md`](https://github.com/qabas1/qabas1-deploy/blob/main/docs/api-autoscaling.md).
No CLI build or repository change deploys to a server automatically.

Local checks:

```sh
go test -count=1 ./...
go vet ./...
QABAS_GATEWAY_INTEGRATION=1 go test -count=1 -timeout 3m ./internal/cli -run TestGatewaySameClientRESTAndPollingIntegration
```

The last check uses isolated disposable NGINX containers and Docker DNS, proving
same-IP REST distribution and polling-path affinity. It runs explicitly in CI;
the ordinary test suite skips it when Docker is unavailable. It tests routing
affinity, not the application Socket.IO handshake or financial posting behavior.
