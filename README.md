# qabas

Official system administration CLI for QabasOne.

`qabas` is the **deployment engine**. It does not contain a hardcoded list of
product releases. Complete QabasOne versions (API + App + Dash) are defined in
[`qabas1-deploy`](https://github.com/qabas1/qabas1-deploy).

```text
qabas1-cli     = deployment engine
qabas1-deploy  = release database / deployment definitions
```

## Install

### Linux

```bash
curl -fsSL https://install.qabas.one/install.sh | sudo bash
```

CLI only:

```bash
curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only
```

Pin a product release:

```bash
curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --release 2.8.0
```

On Debian/Ubuntu the bootstrap downloads a `.deb`, verifies `SHA256SUMS`, and
installs `/usr/bin/qabas` via `dpkg`, then delegates to `qabas install`.
On other Linux distros it falls back to a raw binary under `/usr/local/bin`.

Uninstall the CLI package (does **not** remove QabasOne product data):

```bash
sudo apt remove qabas
# or: sudo dpkg -r qabas
```

### Windows (CLI only)

Windows release assets are temporarily paused. QabasOne product deployment
requires Linux; the Windows CLI build scripts remain available under
`packaging/windows/` for local development.

Manual packages are also attached to each [GitHub Release](https://github.com/qabas1/qabas1-cli/releases)
(`.deb` and raw Linux binaries).

### Private GitHub repos + GHCR (one-time auth)

Product repos and container images are private. Configure credentials **once**;
they are stored under `/etc/qabasone/` (mode `0600`) so later `sudo qabas …`
works without re-exporting env.

Happy path (one PAT with `repo` + `read:packages`):

```bash
export GITHUB_TOKEN="$(gh auth token)"
sudo -E qabas auth login
sudo qabas auth status
sudo qabas install
```

On first product deployment, `qabas install` / `qabas deploy <version>` asks for
the public hostnames used by the stack:

```text
Base domain [qabas.one]:
API domain [api.qabas.one]:
App domain [app.qabas.one]:
Dashboard domain [dash.qabas.one]:
Storage domain [s3.qabas.one]:
Storage console domain [s3c.qabas.one]:
```

The CLI also creates a separate PostgreSQL runtime password. On an existing
installation, deployment adds this credential to `secrets.env`, initializes
the runtime role, then updates only the API and worker database URLs. It leaves
`.env.migrate` on the owner role.
The Compose `db-runtime-role` service provisions `qabasone_app` after migration
and before API startup. Rotate the runtime login separately with
`sudo qabas secrets rotate postgres-app`.

For unattended production installs, set them before deploying:

```bash
sudo qabas config set domain example.com
# optional overrides:
sudo qabas config set domains.api api.example.com
sudo qabas config set domains.app app.example.com
sudo qabas config set domains.dash admin.example.com
sudo qabas config set domains.s3 s3.example.com
sudo qabas config set domains.s3Console s3c.example.com
sudo qabas install --non-interactive
```

`auth login` saves both GitHub and GHCR credentials (same token) and auto-detects
your GitHub username when `gh` is available. If `ghcr-token` is unset, install
falls back to the GitHub token automatically.

Bootstrap (CDN) also persists both tokens when `GITHUB_TOKEN` is set:

```bash
export GITHUB_TOKEN="$(gh auth token)"
curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  https://install.qabas.one/install.sh | sudo -E bash
```

Private-repo tip: do **not** use `github.com/.../releases/download/...` URLs
(they return HTTP 404). Prefer `gh release download` or the GitHub API.

Advanced CRUD (separate GHCR PAT, etc.):

```bash
sudo qabas auth status
sudo -E qabas auth set github|ghcr
sudo qabas auth get github|ghcr
sudo qabas auth unset github|ghcr
sudo qabas auth logout
```

### Re-running the installer

The bootstrap script is **idempotent**. If `qabas` is already installed:

| Scenario | CLI (`qabas` binary) | QabasOne product stack |
|----------|----------------------|-------------------------|
| Re-run default (`curl … \| sudo bash`) | Skips download if already on target version | Skips if already installed; use `qabas upgrade` to move forward |
| `VERSION=1.0.0` with older CLI installed | Upgrades CLI | Unchanged unless `--release` given |
| `VERSION=1.0.0` with **newer** CLI installed | **Refuses** (use `--allow-downgrade`) | — |
| `--cli-only` | Updates CLI only | Never touches product |
| `--release 2.8.0` while on 1.0.0 | Updates CLI if needed | Runs `qabas deploy 2.8.0` |
| `--release 1.0.0` while on 2.8.0 | Updates CLI if needed | Runs `qabas downgrade 1.0.0` |
| `--force` | Re-downloads CLI even at same version | Re-runs `qabas install --force` |

Pin CLI version: `VERSION=1.0.0 curl … | sudo bash -s -- --cli-only`

### Dev channel (auto-published on push to `dev`)

Every push to the `dev` **branch** publishes a rolling GitHub prerelease tagged
**`dev-latest`** (not `dev`, so the branch and tag names do not collide).
Assets are still named `qabas_dev_*`. Install with `VERSION=dev`:

```bash
export GITHUB_TOKEN="$(gh auth token)"
VERSION=dev sudo -E bash install.sh --cli-only --token-file /path/to/token
```

Push the branch (avoid ambiguous `dev` refs):

```bash
git push -u origin HEAD:refs/heads/dev
```

### Product `dev` deploy (GHCR images + deploy compose)

`qabas deploy dev` (or `install`/`upgrade` with channel `dev`) fetches compose and
env templates from `qabasone/qabas1-deploy` on the **`dev`** branch, then pins
component images to the **last pushed** GHCR package version (GitHub Packages
`updated_at`, including versions tagged only `sha-*`, `dev`, or `latest`) for:

- `ghcr.io/qabas1/qabas1-api`
- `ghcr.io/qabas1/qabas1-app`
- `ghcr.io/qabas1/qabas1-dash`
- `ghcr.io/qabas1/qabas1-siraj_app`

Image pins do not come from the deploy catalog YAML. Infra images (Postgres,
Redis, MinIO, Gotenberg, jsreport) use the defaults from the fetched compose file.

```bash
sudo -E qabas auth login --username YOUR_GITHUB_USER
sudo qabas deploy dev
```

To refresh a single product component from last-pushed GHCR without redeploying
the full stack (live compose is kept; only that image pin is updated):

```bash
sudo qabas deploy api --dev
sudo qabas deploy app --dev
sudo qabas deploy dash --dev
sudo qabas deploy siraj --dev
```

To deploy one published image, select its version, tag, full SHA-256 digest,
or a unique digest prefix (at least 12 hex characters):

```sh
sudo qabas deploy app 2.8.0
sudo qabas deploy dash sha256:0123456789abcdef
```

The CLI resolves the selector through GHCR and pins the full digest. A pinned
API image must have a version higher than the installed API image. For an
intentional API downgrade, or when the version cannot be compared, pass
`--allow-downgrade`. API deploys also update the workers sharing its image.

`deploy api --dev` also recreates API-backed workers that share the API image.
Scoped deploy requires an existing installation.

Override the compose/templates ref with `QABAS_DEPLOY_REF` when needed.

## Commands (v1)

```text
Common:
  install | upgrade | status | ps | top (htop) | doctor | guide | logs | auth | login

Management:
  release   list|ls | show | current | latest
  backup    create | list|ls | show | verify | delete|rm | restore | schedule
  restore   (also: backup restore)
  config | db | maintenance | replica | dr
  autopilot run | enable | disable | status
  secrets   status | rotate
  limits    show | sync

Lifecycle:
  deploy | up | start | down | stop | restart | downgrade | rollback | uninstall

System:
  history | sysinfo | lock status | version | update | completion
```

Aliases: `qabas login` → `auth login`; `list` → `ls` on release/backup;
`delete` → `rm` on backup; `qabas backup restore` mirrors top-level `restore`. Nested: `qabas logs ui …`.

Service controls use the Docker Compose project `qabasone` and the installed
compose file under `/var/lib/qabasone/compose/`. They do not manage external
reverse proxies such as `edge-nginx`. They also work on provisioned replicas,
which use their local pinned images and Compose role overlays without an
installed-release record. On read-only replicas, writer-only workers cannot be
started.

```bash
qabas ps
sudo qabas up
sudo qabas up -d --build
sudo qabas up api -d --build
sudo qabas down
sudo qabas down api
sudo qabas restart
sudo qabas restart api
sudo qabas stop app
sudo qabas start app
```

### Guided walkthroughs

`qabas guide` prints A-to-Z walkthroughs for the multi-step tasks. Each step
says where to run it (this machine, the writer, a replica, a witness host) and
shows the exact commands, pre-filled from read-only detection of this machine:
role (not registered / single-node writer / writer / replica), first IPv4,
SSH host key fingerprint, cluster nodes, schedule and PITR state. Steps are
marked `[done]`, `[todo]`, or `[!!]` (blocked, with the detected reason).

```bash
qabas guide                      # this machine + suggested next topics + topic list
qabas guide replica              # add this machine as a read-only replica
qabas guide replica --name dr1 --host 203.0.113.10   # override pre-filled values
qabas guide backup               # create / ls / verify / cp / delete / restore
qabas guide schedule             # daily backups, retention, off-site copies
qabas guide pitr                 # WAL archive, base backups, restore tests
qabas guide dr                   # switchover, break-glass promote, rejoin, failback
qabas guide install              # auth, install, upgrade, deploy, rollback
qabas guide maintenance          # maintenance mode, db, logs, doctor, troubleshooting
qabas guide dr --no-detect       # generic version with <placeholders>
qabas guide backup --json
```

Topic aliases work too (`add-replica`, `restore`, `offsite`, `failover`,
`upgrade`, `troubleshoot`, ...).

### Replica domains

Replica domains are configured locally on the replica and do not change the
writer's configuration. Set a base domain (or individual `domains.*` values),
then apply the local gateway:

```bash
sudo qabas config set domain dr.example.com
sudo qabas config set domains.api api-dr.example.com # optional per-service override
sudo qabas gateway apply
```

Applying replica domains updates only that node's domain-related environment
values and restarts its affected services. DNS and external TLS routing to the
replica must be configured separately.

### Localhost logs UI

Realtime log viewing runs as a **separate** Compose project (`qabas1-logs`) so
it never interferes with `qabas up` / `down`. Default is Dozzle only (fast).
`--full` adds Loki + Grafana Alloy + Grafana for searchable history. Binds to
`127.0.0.1` only. Auth is on by default; generated passwords are stored in
`/etc/qabasone/logs-ui.credentials` (mode `0600`). Loki stays on the internal
network (not published on the host). Full-stack services include Docker
healthchecks; `up --full` waits until they are healthy.

The logs UI is isolated from the product stack: Dozzle and Alloy reach Docker
only through a read-only socket proxy (`docker-socket-proxy`, no exec, image,
volume or POST access) on an internal network, every container drops all
capabilities, runs with a read-only root filesystem and memory/pids limits,
and its own logs rotate (20 MiB x 3). Alloy masks passwords, URI credentials,
bearer/basic tokens, JWTs and GitHub tokens before lines reach Loki. Loki keeps
7 days by default (`QABAS_LOGS_RETENTION=14d`, units `h`/`d`/`w`); override the
proxy image with `QABAS_SOCKET_PROXY_IMAGE`.

```bash
qabas logs ui up              # Dozzle at http://127.0.0.1:9999 (login required)
qabas logs ui up --full       # + Grafana at http://127.0.0.1:3001
qabas logs ui status
qabas logs ui open
qabas logs ui down
qabas logs ui down --wipe     # also delete volumes + credentials
qabas logs api -f             # terminal logs (unchanged)
```

Override Dozzle port with `--port` or `QABAS_LOGS_UI_PORT`. Use `--all` to show
every local Docker container instead of only project `qabasone`. Override
passwords with `QABAS_DOZZLE_PASSWORD` / `QABAS_GRAFANA_PASSWORD` before first
`up` (or after `down --wipe`).

`qabas uninstall` only uninstalls the CLI package (`apt remove qabas` on
Debian/Ubuntu). It does not stop or delete the QabasOne system.

Maintenance mode serves a static page through `edge-nginx` for the app and
dashboard hosts while preserving the running services. The CLI discovers the
edge layout from `QABAS_EDGE_NGINX_DIR` or from the running edge container
mounts; it never edits operator-owned nginx `.conf` files. Edge server blocks
must already include the maintenance snippet (checked in once in the edge
repo):

```nginx
include /etc/nginx/includes/qabas1-maintenance.conf;
```

```bash
sudo qabas maintenance on --for 30m
sudo qabas maintenance on --until "2026-08-30 18:00"
sudo qabas maintenance on --page /etc/qabasone/maintenance.html
qabas maintenance status
sudo qabas maintenance off
```

For an existing installation, `qabas deploy <version>` automatically enables
maintenance mode before changing the release and disables it after a successful
deploy. If deployment fails or is interrupted, maintenance remains on until the
operator runs `sudo qabas maintenance off`. Maintenance that was already enabled
manually is left unchanged.

Custom page HTML may use `{{.Until}}` for the expected return time. `--page`
overrides `QABAS_MAINTENANCE_PAGE`; both override the built-in page.

Database operations run through the installed compose project:

```bash
sudo qabas db migrate
sudo qabas db seed
sudo qabas db seed --command 'node_modules/.bin/tsx prisma/seed.ts'
```

### Scheduled backups

A systemd timer (`qabas-backup.timer`) takes a full backup (PostgreSQL via vpg +
object storage) every day, verifies it, and prunes old ones with
grandfather-father-son retention: the newest backup of each of the last 7 days,
4 ISO weeks, and 6 months by default. Only backups named `scheduled` are pruned;
manual, `pre-deploy`, and `pre-restore` backups are never touched.

```bash
sudo qabas backup schedule enable                                  # daily 01:30, keep 7/4/6
sudo qabas backup schedule enable --at 02:15 --keep-daily 14 --offsite dr1
qabas backup schedule status                                       # next run, last run, retained copies
sudo qabas backup schedule run                                     # run once now (what the timer runs)
sudo qabas backup schedule disable                                 # remove timer; keep backups
```

- Runs only on the cluster writer; on replicas the run exits without doing
  anything, so the timer can safely stay installed through a failover.
- Waits up to 2h for other `qabas` operations (deploys) instead of failing,
  and runs at idle IO / low CPU priority with a random delay of up to 10 minutes.
- Refuses to start when free disk is below 1.5x the last scheduled backup
  (minimum 2 GiB) after pruning.
- `--offsite <replica>` streams each verified backup over the pinned SSH
  connection to `/var/lib/qabasone/backups-offsite/<writer>/` on that replica
  (root-only `0700`), checks every file against a SHA-256 manifest, then moves
  it into place. The same retention applies there.
- The last run is recorded in `/var/lib/qabasone/backup-schedule.json` and
  `qabas history`. `qabas status` and `qabas doctor` warn when the last
  successful backup is older than 36h or the last off-site push failed.

### Machine info

`qabas sysinfo` (aliases `machine`, `hostinfo`) shows hostname, OS, kernel,
uptime, CPU model/cores/load, memory and swap, free space on the volumes
holding QabasOne state, backups and logs (plus the Docker data root on Linux),
network addresses, and Docker engine resources (`docker system df`,
reclaimable space, and on Windows the Docker Desktop VM's CPU/RAM). Each
resource is graded `ok` / `warn` / `crit`:

| Check | warn | crit |
|---|---|---|
| memory available | < 10% | < 5% |
| disk free | < 15% or < 10 GiB | < 5% (or inodes < 5%) |
| Docker reclaimable | > 5 GiB | - |

```bash
qabas sysinfo
qabas sysinfo --watch          # refresh every 5s (--watch=10s for another interval)
qabas sysinfo --json
```

### Live dashboard

`qabas top` (alias `qabas htop`) is a live, read-only, full-screen dashboard of
the whole node, refreshed in place:

- **Header:** host, release and channel, uptime, CPU / memory / disk bars,
  maintenance mode, the operations lock, last and next backup, autopilot.
- **Services:** every QabasOne container with state, health, CPU%, memory
  against its limit, network and disk I/O per second, restarts and uptime.
- **Postgres:** role, connections used of `max_connections`, TPS, cache hit
  ratio, lock waits, deadlocks, WAL archiving, and the longest-running queries
  (string literals masked as `'?'`, secrets redacted).
- **Replicas:** on a writer, each standby's state, lag (time and bytes) and
  slot WAL retention; on a standby, its own replay lag.
- **Findings:** what needs attention now (unhealthy or restart-looping
  services, connections over 80%, replica lag over 1m, stuck lock, failing WAL
  archive, low disk/memory), most severe first.

Keys: `tab` / `1`-`4` switch pane, `↑`/`↓` scroll, `s` (or `F6`) cycle sort
(cpu, mem, net, io, restarts, name), `r` reverse, `/` filter services,
`+`/`-` change the interval, `p` pause, `F5` refresh now, `?` help, `q` quit.
`NO_COLOR` disables colors. Without a terminal (pipes, cron) it prints one
snapshot, like `--once`.

```bash
qabas top
qabas htop --interval 5s --sort mem
qabas top --once                      # one plain-text snapshot
qabas top --json                      # one JSON snapshot
qabas top --json --interval 10s       # stream one JSON snapshot per line
qabas top --no-postgres --no-replicas # skip database queries
```

Postgres and replica panes query the local `qabas1-postgres` container; on
Windows this needs a POSIX `sh` on `PATH` (for example Git Bash).

`qabas status` shows a MACHINE section with used, available, total, and percent
disk usage for the system volume and each distinct Qabas data volume. The JSON
output includes these volumes in `machine.disks`. `qabas doctor` adds `memory`,
`disk:*` and `docker-disk` checks (crit fails doctor), and install preflight
warns below 2 GiB RAM or 10 GiB free disk.

### Autopilot (self-maintenance)

`qabas install` schedules an hourly self-maintenance pass so the host keeps
running without operator care (`--no-autopilot` opts out). It runs from a
systemd timer (`qabas-autopilot.timer`) on Linux and a Task Scheduler task
(`QabasOne\Autopilot`, as SYSTEM) on Windows. Each pass:

- prunes dangling Docker images and build cache older than 7 days (all unused
  build cache when disk is low); volumes and in-use images are never touched
- removes PITR WAL segments older than the oldest kept base backup
  (`qabas backup pitr base --keep N` does the same after each base backup)
- applies scheduled-backup retention early when disk is low and trims to an
  emergency minimum (2 daily + 1 weekly) when disk is critical; manual,
  `pre-deploy` and `pre-restore` backups are never pruned
- writes `compose/compose.qabas-logging.yaml` so every service rotates its
  logs (`json-file`, 50 MiB x 5), applied on the next `qabas up` / deploy;
  deploys keep it in sync with the bundle and drop it rather than fail
- turns maintenance mode off once `--until` has passed by more than 1h
  (never during an incomplete deploy)
- restarts services Docker reports `unhealthy`, at most 3 times per service
  per 24h
- rotates `qabas.log` and `history.jsonl` when over their caps, re-tightens
  permissions on config, secrets, backups and logs, and flags a Loki volume
  over 5 GiB

```bash
sudo qabas autopilot enable
qabas autopilot status         # schedule, last run, space freed, failed tasks
sudo qabas autopilot run       # one pass now (what the timer runs)
sudo qabas autopilot disable   # remove the schedule (doctor stops nagging)
```

Runs skip while a healthy `qabas` operation holds the lock (a stuck holder is
stopped first, see [Operations lock](#operations-lock)); backup and WAL tasks
only run on the cluster writer. The last run is recorded in
`/var/lib/qabasone/autopilot.json` and `qabas history`; `qabas doctor` warns
when autopilot is not scheduled, has not run for 3h, or a task failed.

### Logs and retention

Every log QabasOne writes is bounded:

| Log | Policy |
|---|---|
| `/var/log/qabasone/qabas.log` | one redacted line per command (args, exit code, duration, error); 10 MiB x 5 gzip archives |
| `history.jsonl` (`qabas history`) | 5 MiB or 10,000 entries, 3 gzip archives (`qabas history --all` reads them) |
| container logs | `compose.qabas-logging.yaml`: `json-file` 50 MiB x 5 per service, written on install/deploy when Docker uses `json-file` |
| Loki (logs UI `--full`) | 7 days (`QABAS_LOGS_RETENTION`) |

```bash
qabas logs retention              # policy and current size of each log
sudo qabas logs retention --apply # rotate over-cap files now (autopilot does this hourly)
```

Tokens, passwords, `--password`/`--token` flag values and every value in
`secrets.env` are masked before anything reaches `qabas.log` or history.

### Secrets

`qabas secrets status` lists each credential with its age, file mode and what
it protects; secrets older than 180 days are flagged (also in `qabas doctor`).
Ages live in `/etc/qabasone/secrets.meta.json` (backfilled from file times on
first run).

```bash
qabas secrets status
sudo qabas secrets rotate postgres redis   # or: all (non-destructive set)
sudo qabas secrets rotate jwt              # signs everyone out
sudo qabas secrets rotate 2fa --force      # destructive: invalidates enrolled 2FA
sudo qabas secrets rotate logs-ui          # logs UI passwords
```

Stack rotation takes a `pre-rotate` backup (skip with `--no-backup`), turns
maintenance on, changes the PostgreSQL role password, rewrites `secrets.env`
and the compose env files atomically, recreates services and waits for them to
be healthy. If anything fails, files and the database password are restored
and the stack is re-applied. `2fa` and `pepper` need `--force` because existing
data depends on them. Replicas refuse; rotate on the writer. GitHub/GHCR tokens
are rotated with `qabas auth login --token-file`.

### Resource limits

On deploy, `compose/compose.qabas-limits.yaml` gives every long-running service
a memory limit sized from the RAM Docker sees (PostgreSQL 25%, API 15%, each
worker 8%, Redis/MinIO 10%, front-ends 5%, each with a floor and ceiling), a
pids limit (512; 1024 for PostgreSQL), `nofile` 65536, `no-new-privileges`,
and drops `NET_RAW`, `MKNOD`, `SYS_CHROOT` and `AUDIT_WRITE`. Anything the
release compose already sets is left alone; one-off services (migrate, seed,
`*-init`) are skipped.

```bash
qabas limits show          # per-service memory, pids, hardening, what the release sets
sudo qabas limits sync     # regenerate for the current RAM and apply
```

Override one service with `QABAS_LIMIT_API=2g` (`QABAS_LIMIT_<SERVICE>`, `-`
becomes `_`; `none` removes the limit), or turn the overlay off with
`QABAS_LIMITS=off`.

### Security checks

Commands that change the host (`deploy`, `up`, `backup`, `secrets rotate`, and
so on) require root on Linux; run them with `sudo`. Install hardens
permissions: `/etc/qabasone` is `0711`, secret files `0600`, backups `0700`,
logs `0750`, and on Windows `%ProgramData%\QabasOne` is restricted to
SYSTEM, Administrators and the installing user.

`qabas doctor` adds warn-level rows (they never fail doctor on their own):

- `secret-perms` / `wireguard-key`: secret files readable by group/others
- `secret-age`: credentials older than 180 days
- `log-rotation`, `log-sizes`, `container-logs`, `loki-volume`: unbounded or
  oversized logs
- `resource-limits`: limits overlay missing or disabled
- `exposure`: container ports published on all interfaces other than 80/443
- `docker-socket`: containers that mount the Docker socket directly

List formatting (Docker-style Go templates):

```bash
qabas backup ls --format '{{.ID}}'
qabas backup ls --name 'pre-deploy'
qabas release ls --format 'table {{.Version}}\t{{.Channel}}'
qabas history --format json
```

Shell completion:

```bash
# bash
source <(qabas completion bash)
# zsh
source <(qabas completion zsh)
# powershell
qabas completion powershell | Out-String | Invoke-Expression
```

Default channel is **stable**. `qabas install` and `qabas upgrade` with no
version select the latest stable product release from `qabas1-deploy`.

## Host layout

```text
/etc/qabasone/           config.yaml, secrets.env, secrets.meta.json, github-token, ghcr-token, ghcr-username, logs-ui.credentials, compose env files
/var/lib/qabasone/       state.json, history.jsonl, deployments, releases, backups, compose, compose/logs-ui, qabas.lock
/var/log/qabasone/       qabas.log (+ qabas.log.N.gz archives)
```

Production secrets and compose env files (`.env`, `.env.api`, `.env.migrate`,
`.env.worker`, `.env.app`, `.env.dash`, `secrets.env`) are generated **once**
on first setup by rendering `compose/templates/*.example` from the configured
`qabas1-deploy` release (not hardcoded in the CLI). Later `install` /
`deploy` / `upgrade` / `db` commands never overwrite, append, or rewrite
credential values — edit those files yourself if you need to change them.

Override with `QABAS_CONFIG_DIR`, `QABAS_STATE_DIR`, `QABAS_LOG_DIR`,
`QABAS_LOCK_PATH`, `QABAS_DEPLOY_REPO`, `QABAS_DEPLOY_REF`,
`QABAS_EDGE_NGINX_DIR`, `QABAS_EDGE_NGINX_CONTAINER`,
`QABAS_EDGE_NGINX_CONF` (verify-only path for the maintenance include; never
written by the CLI), and `QABAS_MAINTENANCE_PAGE` (HTML template for the
maintenance page; overridden by `--page`).

### Operations lock

State-changing commands (deploy, upgrade, backup, restore, schedule run,
autopilot, ...) hold one lock, `/var/lib/qabasone/qabas.lock`, so two of them
never run at once. The holder records its pid, command, start time, current
phase, and a heartbeat every 10s. A command that finds the lock held resolves
it on its own:

- **Stuck holder** (stopped with Ctrl+Z, no heartbeat for 2m, or running past
  its limit: 30m for autopilot, 6h otherwise, override with
  `QABAS_LOCK_STUCK_AFTER=8h`): stopped with SIGTERM, then SIGKILL after 30s,
  and the takeover is recorded in `qabas history` as `lock-takeover`.
- **Healthy holder**: waited on with a progress line (no limit in a terminal,
  30m for scripts, 2h for the backup timer); autopilot skips the pass instead.
- **Critical phase** (RESTORE, SWITCH, COMMIT): never interrupted.

A pid that no longer belongs to a `qabas` process is never signaled.

```bash
sudo qabas lock status          # holder, phase, heartbeat, and the verdict
```

Never delete the lock file: it does not release a running holder and lets a
second operation start alongside it.

## Install CDN

`install.qabas.one` is served by a Cloudflare Worker in `install/cloudflare/`.
Deploy with Wrangler after CLI releases change. See that directory's README.

## Build

```bash
make test
make build
```

Linux release binaries:

```bash
make dist-linux VERSION=0.1.0
```

Builds `linux` (amd64, arm64) binaries + `.deb` packages under `dist/`.

Windows release assets are built on Windows (PowerShell + .NET SDK):

```powershell
make dist-windows VERSION=0.1.0   # qabas.exe with icon, version info, manifest
make package-msi VERSION=0.1.0    # branded WiX v5 MSI (x64 + arm64)
```

Automated releases currently publish Linux binaries and `.deb` packages only.
Windows build and signing are paused. Brand assets live in
`packaging/windows/assets/`; regenerate them from a real logo with
`python packaging/windows/assets/generate.py --logo logo.png`.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | success |
| 1 | generic failure |
| 2 | invalid input or configuration |
| 3 | compatibility failure |
| 4 | health-check failure |
| 5 | backup or restore failure |
| 6 | network or registry failure |
