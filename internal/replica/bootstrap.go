package replica

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/qabasone/qabasone-cli/internal/cluster"
	"github.com/qabasone/qabasone-cli/internal/nodeexec"
	"github.com/qabasone/qabasone-cli/internal/redact"
)

// BootstrapOptions tunes the BOOTSTRAP phase (a bare Debian/Ubuntu host becomes a
// node that can run the replica stack). It only ever changes the replica.
type BootstrapOptions struct {
	// SkipOSUpgrade skips `apt-get upgrade` (packages qabas needs are still installed).
	SkipOSUpgrade bool
	// SkipReboot never reboots; a pending kernel update only warns.
	SkipReboot bool
	// InstallScript is install.sh from the writer's build (installs the qabas CLI).
	InstallScript []byte
}

// DockerKeyFingerprint is Docker's apt repository signing key
// (https://docs.docker.com/engine/install/ubuntu/).
const DockerKeyFingerprint = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"

// BootstrapSteps names what BOOTSTRAP does, in order (dry-run and help).
var BootstrapSteps = []string{
	"wait for cloud-init and the apt/dpkg lock",
	"apt-get update + upgrade (keeps existing config files)",
	"install ca-certificates curl gnupg jq iputils-ping wireguard-tools, enable time sync",
	"install Docker Engine + compose v2 from Docker's signed apt repository",
	"reboot if the upgrade requires it and wait for SSH (same pinned host key)",
	"store GitHub/GHCR credentials (0600) and install the qabas CLI at the writer's version",
	"authorize the cluster SSH key",
}

const aptEnv = `export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a NEEDRESTART_SUSPEND=1
APT="apt-get -y -q -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
`

// settleScript waits for first-boot provisioning and repairs an interrupted dpkg run
// (a resumed BOOTSTRAP may have been cut off mid-install).
const settleScript = `set -u
if command -v cloud-init >/dev/null 2>&1; then timeout 900 cloud-init status --wait >/dev/null 2>&1 || true; fi
` + aptEnv + `dpkg --force-confdef --force-confold --configure -a >/dev/null || { echo "dpkg --configure -a failed; repair the package database on the replica" >&2; exit 1; }
n=0
until $APT update >/dev/null; do
  n=$((n+1)); [ "$n" -ge 3 ] && { echo "apt-get update failed 3 times (network or mirror problem)" >&2; exit 1; }
  sleep 10
done
`

const upgradeScript = "set -eu\n" + aptEnv + "$APT upgrade --with-new-pkgs >/dev/null\n"

const toolsScript = "set -eu\n" + aptEnv + `$APT install --no-install-recommends ca-certificates curl gnupg jq iputils-ping wireguard-tools >/dev/null
timedatectl set-ntp true >/dev/null 2>&1 || true
`

// dockerScript installs Docker CE from Docker's repository after checking the signing
// key fingerprint. An existing Docker with compose v2 is kept as is; a distro Docker
// without compose v2 is refused rather than replaced (it may run other workloads).
const dockerScript = "set -eu\n" + aptEnv + `if docker version --format '{{.Server.Version}}' >/dev/null 2>&1; then
  if ! docker compose version --short >/dev/null 2>&1; then
    if dpkg -s docker-ce >/dev/null 2>&1; then
      $APT install docker-compose-plugin >/dev/null
    else
      echo "Docker is installed from another source and has no compose v2 plugin; install docker-compose-v2 (or Docker CE) on the replica, then resume" >&2
      exit 69
    fi
  fi
else
  . /etc/os-release
  case "${ID:-}" in ubuntu|debian) ;; *) echo "unsupported distribution ${ID:-unknown}" >&2; exit 69 ;; esac
  codename="${VERSION_CODENAME:-}"
  [ -n "$codename" ] || { echo "VERSION_CODENAME missing in /etc/os-release" >&2; exit 69; }
  for p in docker.io docker-compose docker-compose-v2 docker-doc podman-docker; do
    if dpkg -s "$p" >/dev/null 2>&1; then $APT remove "$p" >/dev/null; fi
  done
  install -m 0755 -d /etc/apt/keyrings
  tmp=$(mktemp)
  curl -fsSL --retry 3 "https://download.docker.com/linux/${ID}/gpg" -o "$tmp"
  fp=$(gpg --show-keys --with-colons "$tmp" 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')
  if [ "$fp" != "` + DockerKeyFingerprint + `" ]; then
    rm -f "$tmp"
    echo "Docker apt signing key fingerprint mismatch (got ${fp:-none}); refusing to trust it" >&2
    exit 65
  fi
  install -m 0644 "$tmp" /etc/apt/keyrings/docker.asc
  rm -f "$tmp"
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/${ID} ${codename} stable" > /etc/apt/sources.list.d/docker.list
  $APT update >/dev/null
  $APT install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
fi
fresh=no
if [ ! -e /etc/docker/daemon.json ]; then
  mkdir -p /etc/docker
  cat > /etc/docker/daemon.json <<'EOF'
{
  "log-driver": "json-file",
  "log-opts": { "max-size": "50m", "max-file": "5" },
  "live-restore": true
}
EOF
  fresh=yes
fi
systemctl enable docker.service containerd.service >/dev/null 2>&1 || true
if [ "$fresh" = yes ]; then systemctl restart docker; else systemctl start docker; fi
docker version --format '{{.Server.Version}}' >/dev/null
docker compose version --short >/dev/null
`

const rebootRequiredScript = "if [ -e /var/run/reboot-required ]; then echo yes; else echo no; fi"

const bootIDScript = "cat /proc/sys/kernel/random/boot_id"

const rebootScript = `if command -v systemd-run >/dev/null 2>&1; then
  systemd-run --quiet --collect --on-active=3 /bin/systemctl reboot
else
  nohup sh -c 'sleep 3; systemctl reboot' >/dev/null 2>&1 &
fi
`

var (
	rebootSettle  = 20 * time.Second
	rebootTimeout = 10 * time.Minute
)

// installPath holds install.sh on the replica only while it runs.
const installPath = "/run/qabas/install.sh"

// Bootstrap prepares the replica host: packages, Docker, reboot when required,
// credentials, the qabas CLI and the cluster SSH key. Every step is idempotent.
func (e *Engine) Bootstrap(ctx context.Context, name string, opt BootstrapOptions) error {
	steps := []struct {
		label  string
		script string
		skip   bool
	}{
		{"settle", settleScript, false},
		{"upgrade", upgradeScript, opt.SkipOSUpgrade},
		{"tools", toolsScript, false},
		{"docker", dockerScript, false},
	}
	for _, s := range steps {
		if s.skip {
			e.logf("    %-8s skipped", s.label)
			continue
		}
		e.logf("    %-8s ...", s.label)
		x, err := e.Exec(ctx, name)
		if err != nil {
			return err
		}
		if _, err := run(ctx, x, s.script); err != nil {
			return fmt.Errorf("bootstrap %s on %s: %w", s.label, name, err)
		}
	}
	if err := e.rebootIfRequired(ctx, name, opt.SkipReboot); err != nil {
		return err
	}
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	haveToken, err := e.pushCredentials(ctx, x, name)
	if err != nil {
		return err
	}
	if err := e.installCLI(ctx, x, opt, haveToken); err != nil {
		return err
	}
	return e.AuthorizeClusterKey(ctx, name)
}

func (e *Engine) rebootIfRequired(ctx context.Context, name string, skip bool) error {
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	out, err := run(ctx, x, rebootRequiredScript)
	if err != nil || strings.TrimSpace(out) != "yes" {
		return err
	}
	if skip {
		e.logf("  warning: %s needs a reboot to finish its update (--no-reboot given); reboot it in a quiet moment", name)
		return nil
	}
	before, err := run(ctx, x, bootIDScript)
	if err != nil {
		return err
	}
	e.logf("    %-8s %s requires a reboot; rebooting and waiting for SSH ...", "reboot", name)
	if _, err := run(ctx, x, rebootScript); err != nil {
		return fmt.Errorf("schedule reboot on %s: %w", name, err)
	}
	e.Reconnect(name)
	if err := e.awaitNode(ctx, name, rebootSettle, rebootTimeout, func(x nodeexec.Executor) bool {
		now, err := run(ctx, x, bootIDScript)
		return err == nil && strings.TrimSpace(now) != "" && strings.TrimSpace(now) != strings.TrimSpace(before)
	}); err != nil {
		return fmt.Errorf("%s did not come back after the reboot within %s: %w", name, rebootTimeout, err)
	}
	e.logf("    %-8s %s is back", "reboot", name)
	return nil
}

// pushCredentials stores the writer's GitHub/GHCR credentials on the node where
// install.sh and `qabas` look for them. Values travel on stdin only.
func (e *Engine) pushCredentials(ctx context.Context, x nodeexec.Executor, name string) (bool, error) {
	var gh, user, token string
	if e.GitHubToken != nil {
		gh = e.GitHubToken()
	}
	if e.Registry != nil {
		user, token = e.Registry()
	}
	redact.Register(gh, token)
	dir := e.Layout(name).ConfigDir
	for file, v := range map[string]string{"github-token": gh, "ghcr-token": token, "ghcr-username": user} {
		if v == "" {
			continue
		}
		if err := write(ctx, x, dir+"/"+file, []byte(v+"\n"), 0o600); err != nil {
			return false, err
		}
	}
	return gh != "", nil
}

// installCLI installs the package-managed qabas CLI when the node has none; version
// drift on an existing CLI is handled by ensureCLI during STACK.
func (e *Engine) installCLI(ctx context.Context, x nodeexec.Executor, opt BootstrapOptions, haveToken bool) error {
	if _, ok, err := nodeCLI(ctx, x); err != nil || ok {
		return err
	}
	if !haveToken {
		return fmt.Errorf("the writer has no GitHub token to install the qabas CLI on %s; run `sudo -E qabas auth login` on the writer, then resume", x.Node())
	}
	if len(opt.InstallScript) == 0 {
		return fmt.Errorf("this qabas build carries no installer; %s", InstallHint)
	}
	version := "latest"
	if e.CLI != nil && e.CLI.Version != "" {
		version = e.CLI.Version
	}
	e.logf("    %-8s installing qabas %s on %s", "cli", version, x.Node())
	if err := write(ctx, x, installPath, opt.InstallScript, 0o700); err != nil {
		return err
	}
	script := "set -eu\ntrap 'rm -f " + installPath + "' EXIT\nVERSION=" + nodeexec.Quote(version) +
		" bash " + installPath + " --cli-only --force >/dev/null\n"
	if _, err := run(ctx, x, script); err != nil {
		return fmt.Errorf("install the qabas CLI on %s: %w", x.Node(), err)
	}
	if _, err := run(ctx, x, CleanStaleBinaryScript); err != nil {
		return err
	}
	if _, ok, err := nodeCLI(ctx, x); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("install.sh finished but qabas is not on PATH on %s", x.Node())
	}
	return nil
}

// authorizeKeyScript replaces the qabas-cluster line in the SSH user's authorized_keys
// with the line on stdin, leaving every other key untouched.
const authorizeKeyScript = `set -eu
u=%s
home=$(getent passwd "$u" | cut -d: -f6)
[ -n "$home" ] || { echo "user $u not found" >&2; exit 67; }
d="$home/.ssh"; f="$d/authorized_keys"
umask 077
mkdir -p "$d"
touch "$f"
{ grep -v ' ` + cluster.SSHKeyComment + `$' "$f" || true; cat; } > "$f.qabas-tmp"
chmod 600 "$f.qabas-tmp"
mv -f "$f.qabas-tmp" "$f"
chmod 700 "$d"
chown "$u": "$d" "$f" 2>/dev/null || chown "$u" "$d" "$f"
`

// revokeKeyScript removes the cluster key from root's and the SSH user's
// authorized_keys and deletes a pushed copy of the key (args: user, key dir).
const revokeKeyScript = `for u in root %s; do
  home=$(getent passwd "$u" | cut -d: -f6) || continue
  f="$home/.ssh/authorized_keys"
  [ -f "$f" ] || continue
  grep -v ' ` + cluster.SSHKeyComment + `$' "$f" > "$f.qabas-tmp" || true
  chmod 600 "$f.qabas-tmp"; chown --reference="$f" "$f.qabas-tmp" 2>/dev/null || true
  mv -f "$f.qabas-tmp" "$f"
done
rm -rf %s
`

// clusterKeyLine renders the authorized_keys entry for a node: no forwarding, no pty,
// and for WireGuard-reached nodes only from the WireGuard network.
func (e *Engine) clusterKeyLine(n cluster.Node, pub string) string {
	opts := "restrict"
	if n.SSHVia == cluster.SSHViaWG {
		opts = `from="` + e.Cluster.WGNetwork + `",` + opts
	}
	return opts + " " + pub
}

func sshUser(n cluster.Node) string {
	if n.SSHUser == "" {
		return "root"
	}
	return n.SSHUser
}

// AuthorizeClusterKey installs the cluster key for the node's SSH user and, when a
// KeyCheck is wired, proves the node accepts it on its own.
func (e *Engine) AuthorizeClusterKey(ctx context.Context, name string) error {
	pub, err := cluster.EnsureSSHKey(e.Paths)
	if err != nil {
		return fmt.Errorf("cluster SSH key: %w", err)
	}
	n := e.Cluster.Node(name)
	x, err := e.Exec(ctx, name)
	if err != nil {
		return err
	}
	res, err := x.Run(ctx, fmt.Sprintf(authorizeKeyScript, nodeexec.Quote(sshUser(*n))), strings.NewReader(e.clusterKeyLine(*n, pub)+"\n"))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return &nodeexec.ScriptError{Node: name, ExitCode: res.ExitCode, Stderr: res.Stderr}
	}
	if e.KeyCheck != nil {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := e.KeyCheck(cctx, *n); err != nil {
			return fmt.Errorf("%s does not accept the cluster SSH key: %w", name, err)
		}
	}
	return nil
}
