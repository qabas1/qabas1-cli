#!/usr/bin/env bash
# Bootstrap the qabas CLI, then optionally install QabasOne.
#   curl -fsSL https://install.qabas.one/install.sh | sudo bash
#   curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only
#   curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --release 2.8.0
#
# Pin CLI version:
#   VERSION=1.0.0 curl -fsSL https://install.qabas.one/install.sh | sudo -E bash -s -- --cli-only
#
# Private GitHub repos (do NOT use github.com/.../releases/download — returns 404):
#   export GITHUB_TOKEN="$(gh auth token)"
#   gh release download dev-latest -R qabasone/qabas1-cli -p install.sh -O /tmp/install.sh
#   VERSION=dev sudo -E bash /tmp/install.sh --cli-only --force
# Or API raw:
#   curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" \
#     -H "Accept: application/vnd.github.raw" \
#     "https://api.github.com/repos/qabasone/qabas1-cli/contents/install/install.sh?ref=dev" \
#     | VERSION=dev sudo -E bash -s -- --cli-only --force
set -euo pipefail

REPO="${REPO:-qabasone/qabas1-cli}"
VERSION="${VERSION:-latest}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
GITHUB_API="${GITHUB_API:-https://api.github.com}"
GITHUB_RELEASES="${GITHUB_RELEASES:-https://github.com/${REPO}/releases}"
GITHUB_TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
FORCE="${FORCE:-0}"
ALLOW_DOWNGRADE="${ALLOW_DOWNGRADE:-0}"
# Canonical credential paths so plain `sudo qabas` works after bootstrap.
SYSTEM_TOKEN_FILE="${SYSTEM_TOKEN_FILE:-/etc/qabasone/github-token}"
SYSTEM_GHCR_TOKEN_FILE="${SYSTEM_GHCR_TOKEN_FILE:-/etc/qabasone/ghcr-token}"
SYSTEM_GHCR_USERNAME_FILE="${SYSTEM_GHCR_USERNAME_FILE:-/etc/qabasone/ghcr-username}"

CLI_ONLY=0
RELEASE_SPEC=""
TOKEN_FILE=""
SKIP_CLI=0
BROWSER_AUTH=0

log() { printf '%s %s\n' "[qabas-install]" "$*"; }
die() { log "ERROR: $*"; exit 1; }

usage() {
  cat <<'EOF'
Usage: install.sh [OPTIONS]

Install or update the qabas CLI and, on Linux, the QabasOne product stack.

Options:
  --help                 Show this help and exit
  --cli-only             Install or update the CLI only
  --browser-auth         Open GitHub device login through `gh auth login --web`
  --force                Reinstall the selected CLI/product version
  --allow-downgrade      Allow installing an older CLI version
  --release VERSION      Install a specific QabasOne product version
  --token-file PATH      Read a GitHub token from a file

Environment:
  VERSION, REPO, INSTALL_DIR, GITHUB_API
  GITHUB_TOKEN, GH_TOKEN, QABAS_GITHUB_TOKEN_FILE
  GHCR_TOKEN, QABAS_GHCR_TOKEN, GHCR_USERNAME, GITHUB_USER

Examples:
  curl -fsSL https://install.qabas.one/install.sh | sudo bash
  curl -fsSL https://install.qabas.one/install.sh | sudo bash -s -- --cli-only
  sudo bash install.sh --browser-auth --cli-only
EOF
}

# Explain private-repo auth without noisy curl "(22) 404" on stderr.
die_auth_required() {
  local reason="${1:-Could not download from ${REPO}.}"
  log "${reason}"
  if [[ -z "${GITHUB_TOKEN}" ]]; then
    cat <<EOF

The qabasone GitHub repositories are private — GitHub authentication is required.
Securely authenticate once using the browser:

Automatic (browser-based, no manual token copy/paste):

  gh auth login --web --git-protocol https --hostname github.com
  export GITHUB_TOKEN="\$(gh auth token)"
  curl -fsSL -H "Authorization: Bearer \${GITHUB_TOKEN}" \\
    https://install.qabas.one/install.sh | sudo -E bash -s -- --cli-only

Or re-run with --browser-auth:

  sudo bash /tmp/install.sh --browser-auth --cli-only

Or via CDN (when available):

  curl -fsSL -H "Authorization: Bearer \${GITHUB_TOKEN}" \\
    https://install.qabas.one/install.sh | sudo -E bash

The installer saves GitHub + GHCR credentials under /etc/qabasone/. After that:

  sudo qabas install

Do not pass tokens on the command line (visible in ps).
Do not use github.com/.../releases/download URLs for private assets (HTTP 404).
EOF
  else
    cat <<EOF

GITHUB_TOKEN is set but GitHub still refused the request.
Verify the token has read access to ${REPO} (and qabas1-deploy for product install),
plus read:packages for ghcr.io/qabas1 images (authorize org SSO if required).
EOF
  fi
  exit 1
}

hint_auth_login() {
  cat <<EOF

If this looks like a credentials / GHCR pull problem:

  export GITHUB_TOKEN="\$(gh auth token)"
  sudo -E qabas auth login
  sudo qabas install

EOF
}

load_github_token_from_disk() {
  local sudir token_file
  token_file="${QABAS_GITHUB_TOKEN_FILE:-}"
  if [[ -z "${GITHUB_TOKEN}" && -n "${token_file}" ]]; then
    if [[ -r "${token_file}" ]]; then
      GITHUB_TOKEN="$(tr -d '[:space:]' < "${token_file}")"
      log "using GitHub token from QABAS_GITHUB_TOKEN_FILE"
    else
      die "QABAS_GITHUB_TOKEN_FILE is not readable: ${token_file}"
    fi
  fi
  if [[ -z "${GITHUB_TOKEN}" && -r "${SYSTEM_TOKEN_FILE}" ]]; then
    GITHUB_TOKEN="$(tr -d '[:space:]' < "${SYSTEM_TOKEN_FILE}")"
    log "using GitHub token from ${SYSTEM_TOKEN_FILE}"
  fi
  if [[ -z "${GITHUB_TOKEN}" && -n "${SUDO_USER:-}" && "${SUDO_USER}" != root ]]; then
    sudir="$(getent passwd "${SUDO_USER}" 2>/dev/null | cut -d: -f6)"
    if [[ -n "${sudir}" && -r "${sudir}/.config/qabas/github-token" ]]; then
      GITHUB_TOKEN="$(tr -d '[:space:]' < "${sudir}/.config/qabas/github-token")"
      log "using GitHub token from ${sudir}/.config/qabas/github-token"
    fi
  fi
  if [[ -z "${GITHUB_TOKEN}" ]] && command -v gh >/dev/null 2>&1; then
    if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != root ]]; then
      GITHUB_TOKEN="$(sudo -u "${SUDO_USER}" -H gh auth token 2>/dev/null | tr -d '[:space:]' || true)"
    else
      GITHUB_TOKEN="$(gh auth token 2>/dev/null | tr -d '[:space:]' || true)"
    fi
    [[ -n "${GITHUB_TOKEN}" ]] && log "using token from gh auth"
  fi
}

browser_auth() {
  command -v gh >/dev/null 2>&1 || die "--browser-auth requires the GitHub CLI (https://cli.github.com/)"
  
  log "launching GitHub browser authentication..."
  local gh_user="${SUDO_USER:-}"
  if [[ -n "${gh_user}" && "${gh_user}" != root ]]; then
    if ! sudo -u "${gh_user}" -H gh auth login --web --git-protocol https --hostname github.com 2>&1; then
      die "GitHub browser authentication failed (check your browser)"
    fi
    GITHUB_TOKEN="$(sudo -u "${gh_user}" -H gh auth token 2>/dev/null | tr -d '[:space:]' || true)"
  else
    if ! gh auth login --web --git-protocol https --hostname github.com 2>&1; then
      die "GitHub browser authentication failed (check your browser)"
    fi
    GITHUB_TOKEN="$(gh auth token 2>/dev/null | tr -d '[:space:]' || true)"
  fi
  
  if [[ -z "${GITHUB_TOKEN}" ]]; then
    die "GitHub browser login completed but no token was returned (check permissions)"
  fi
  log "successfully authenticated with GitHub"
}

detect_ghcr_username() {
  local user=""
  user="${GHCR_USERNAME:-${GITHUB_USER:-}}"
  if [[ -z "${user}" && -r "${SYSTEM_GHCR_USERNAME_FILE}" ]]; then
    user="$(tr -d '[:space:]' < "${SYSTEM_GHCR_USERNAME_FILE}")"
  fi
  if [[ -z "${user}" ]] && command -v gh >/dev/null 2>&1; then
    if [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != root ]]; then
      user="$(sudo -u "${SUDO_USER}" -H gh api user -q .login 2>/dev/null | tr -d '[:space:]' || true)"
    else
      user="$(gh api user -q .login 2>/dev/null | tr -d '[:space:]' || true)"
    fi
  fi
  printf '%s' "${user}"
}

persist_credentials() {
  [[ -n "${GITHUB_TOKEN}" ]] || return 0
  mkdir -p "$(dirname "${SYSTEM_TOKEN_FILE}")"
  umask 077
  printf '%s\n' "${GITHUB_TOKEN}" > "${SYSTEM_TOKEN_FILE}"
  chmod 600 "${SYSTEM_TOKEN_FILE}"
  log "saved GitHub token to ${SYSTEM_TOKEN_FILE}"

  # Same PAT for GHCR by default (needs read:packages).
  local ghcr_token="${GHCR_TOKEN:-${QABAS_GHCR_TOKEN:-${GITHUB_TOKEN}}}"
  printf '%s\n' "${ghcr_token}" > "${SYSTEM_GHCR_TOKEN_FILE}"
  chmod 600 "${SYSTEM_GHCR_TOKEN_FILE}"
  log "saved GHCR token to ${SYSTEM_GHCR_TOKEN_FILE}"

  local user
  user="$(detect_ghcr_username)"
  if [[ -n "${user}" ]]; then
    printf '%s\n' "${user}" > "${SYSTEM_GHCR_USERNAME_FILE}"
    chmod 600 "${SYSTEM_GHCR_USERNAME_FILE}"
    log "saved GHCR username ${user} to ${SYSTEM_GHCR_USERNAME_FILE}"
  else
    log "GHCR username not detected; run: sudo -E qabas auth login --username YOUR_GITHUB_USER"
  fi
}

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --cli-only) CLI_ONLY=1; shift ;;
    --browser-auth) BROWSER_AUTH=1; shift ;;
    --force) FORCE=1; shift ;;
    --allow-downgrade) ALLOW_DOWNGRADE=1; shift ;;
    --release)
      [[ $# -ge 2 ]] || die "--release requires a version"
      RELEASE_SPEC="$2"
      shift 2
      ;;
    --release=*)
      RELEASE_SPEC="${1#--release=}"
      [[ -n "${RELEASE_SPEC}" ]] || die "--release requires a version"
      shift
      ;;
    --token-file)
      [[ $# -ge 2 ]] || die "--token-file requires a path"
      TOKEN_FILE="$2"
      shift 2
      ;;
    *)
      usage >&2
      die "unknown argument: $1"
      ;;
  esac
done

if [[ -n "${TOKEN_FILE}" ]]; then
  resolved=""
  resolve_token_file() {
    local path="$1" sudir
    if [[ -r "${path}" ]]; then
      printf '%s\n' "${path}"
      return 0
    fi
    if [[ "${path}" != /* && -n "${SUDO_USER:-}" && "${SUDO_USER}" != root ]]; then
      sudir="$(getent passwd "${SUDO_USER}" 2>/dev/null | cut -d: -f6)"
      if [[ -n "${sudir}" && -r "${sudir}/${path#./}" ]]; then
        printf '%s\n' "${sudir}/${path#./}"
        return 0
      fi
    fi
    return 1
  }
  resolved="$(resolve_token_file "${TOKEN_FILE}")" \
    || die "token file not readable: ${TOKEN_FILE} (use absolute path or check permissions)"
  TOKEN_FILE="${resolved}"
  GITHUB_TOKEN="$(tr -d '[:space:]' < "${TOKEN_FILE}")"
  if [[ -z "${GITHUB_TOKEN}" ]]; then
    die "token file is empty or unreadable: ${TOKEN_FILE}"
  fi
  log "loaded GitHub token from ${TOKEN_FILE}"
fi

# Early validation: check for required commands and root before doing any I/O.
log "validating system requirements..."
need_cmd curl
need_cmd uname
need_cmd sha256sum

if [[ "$(id -u)" -ne 0 ]]; then
  die "run as root (e.g. curl -fsSL https://install.qabas.one/install.sh | sudo bash)"
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
if [[ "${os}" != "linux" ]]; then
  die "unsupported OS: ${os} (Linux only; use install.ps1 on Windows)"
fi

# Load any cached token, then optionally trigger browser auth.
load_github_token_from_disk

if [[ "${BROWSER_AUTH}" -eq 1 ]]; then
  browser_auth
fi

arch="$(uname -m)"
case "${arch}" in
  x86_64|amd64) goarch="amd64" ;;
  aarch64|arm64) goarch="arm64" ;;
  *) die "unsupported architecture: ${arch} (supported: amd64, arm64)" ;;
esac

# Prefer .deb on dpkg-based systems; otherwise install the raw binary.
USE_DEB=0
if command -v dpkg >/dev/null 2>&1; then
  USE_DEB=1
else
  need_cmd install
fi

semver_norm() {
  printf '%s\n' "${1#v}"
}

# Prints -1 if a<b, 0 if equal, 1 if a>b (sort -V semantics).
semver_cmp() {
  local a b
  a="$(semver_norm "$1")"
  b="$(semver_norm "$2")"
  if [[ "${a}" == "${b}" ]]; then
    printf '%s' '0'
    return
  fi
  if [[ "$(printf '%s\n%s\n' "${a}" "${b}" | sort -V | head -1)" == "${a}" ]]; then
    printf '%s' '-1'
  else
    printf '%s' '1'
  fi
}

qabas_bin() {
  if [[ -x /usr/bin/qabas ]]; then
    printf '%s\n' /usr/bin/qabas
  elif [[ -x "${INSTALL_DIR}/qabas" ]]; then
    printf '%s\n' "${INSTALL_DIR}/qabas"
  elif command -v qabas >/dev/null 2>&1; then
    command -v qabas
  fi
}

installed_cli_version() {
  local bin
  bin="$(qabas_bin || true)"
  [[ -n "${bin}" ]] || return 0
  "${bin}" version 2>/dev/null | awk '/^qabas CLI[[:space:]]+/{print $3; exit}'
}

installed_product_version() {
  local bin v
  bin="$(qabas_bin || true)"
  [[ -n "${bin}" ]] || return 0
  v="$("${bin}" version 2>/dev/null | awk '/^QabasOne[[:space:]]+/{print $2; exit}')"
  [[ -n "${v}" && "${v}" != "(not" ]] && printf '%s\n' "${v}"
}

normalize_tag() {
  local v="$1"
  # Branch is "dev"; rolling release tag is "dev-latest" (avoids branch/tag name clash).
  if [[ "${v}" == "dev" ]]; then
    printf 'dev-latest\n'
  elif [[ "${v}" == v* ]]; then
    printf '%s\n' "${v}"
  elif [[ "${v}" == dev* ]]; then
    printf '%s\n' "${v}"
  else
    printf 'v%s\n' "${v}"
  fi
}

curl_github_api() {
  local url="$1"
  local args=(
    -fsL
    --retry 3
    --retry-delay 1
    -H "Accept: application/vnd.github+json"
    -H "X-GitHub-Api-Version: 2022-11-28"
  )
  if [[ -n "${GITHUB_TOKEN}" ]]; then
    args+=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi
  # -s without -S: fail quietly; we print our own errors (no "curl: (22) ...").
  curl "${args[@]}" "${url}"
}

curl_public() {
  curl -fsL --retry 3 --retry-delay 1 "$@"
}

download_release_asset() {
  local asset_id="$1"
  local dest="$2"
  [[ -n "${GITHUB_TOKEN}" ]] || die_auth_required "Release download requires GITHUB_TOKEN or GH_TOKEN."
  curl -fsL --retry 3 --retry-delay 1 \
    -H "Authorization: Bearer ${GITHUB_TOKEN}" \
    -H "Accept: application/octet-stream" \
    -H "X-GitHub-Api-Version: 2022-11-28" \
    -o "${dest}" \
    "${GITHUB_API}/repos/${REPO}/releases/assets/${asset_id}" \
    || die_auth_required "Failed to download release asset ${asset_id} from ${REPO}."
}

asset_id_for() {
  local release_json="$1"
  local asset_name="$2"
  if command -v python3 >/dev/null 2>&1; then
    printf '%s' "${release_json}" | python3 -c '
import json, sys
name = sys.argv[1]
data = json.load(sys.stdin)
for asset in data.get("assets", []):
    if asset.get("name") == name:
        print(asset["id"])
        sys.exit(0)
sys.exit(1)
' "${asset_name}"
    return
  fi
  if command -v jq >/dev/null 2>&1; then
    printf '%s' "${release_json}" | jq -er --arg n "${asset_name}" '.assets[] | select(.name == $n) | .id'
    return
  fi
  die "private repo install requires python3 or jq to read release metadata"
}

release_endpoint() {
  if [[ "${VERSION}" == "latest" ]]; then
    printf '%s/repos/%s/releases/latest\n' "${GITHUB_API}" "${REPO}"
  else
    local tag
    tag="$(normalize_tag "${VERSION}")"
    printf '%s/repos/%s/releases/tags/%s\n' "${GITHUB_API}" "${REPO}" "${tag}"
  fi
}

resolve_tag() {
  local v="${VERSION}"
  if [[ "${v}" == "latest" ]]; then
    local json tag
    json="$(curl_github_api "$(release_endpoint)" || true)"
    tag="$(printf '%s' "${json}" \
      | grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' \
      | head -n1 \
      | sed -E 's/.*"([^"]+)".*/\1/' || true)"
    if [[ -z "${tag}" ]]; then
      die_auth_required "Could not resolve the latest CLI release on ${REPO}."
    fi
    printf '%s\n' "${tag}"
    return
  fi
  normalize_tag "${v}"
}

plan_cli_install() {
  local installed target cmp
  installed="$(installed_cli_version || true)"
  target="${SEMVER}"
  [[ -n "${installed}" ]] || return 0

  cmp="$(semver_cmp "${installed}" "${target}")"
  if [[ "${cmp}" -eq 0 && "${FORCE}" -eq 0 ]]; then
    log "qabas CLI ${installed} already installed (target ${target}); skipping download"
    SKIP_CLI=1
    return 0
  fi
  if [[ "${cmp}" -eq 1 && "${FORCE}" -eq 0 && "${ALLOW_DOWNGRADE}" -eq 0 ]]; then
    die "refusing CLI downgrade ${installed} → ${target} (use --allow-downgrade or ALLOW_DOWNGRADE=1)"
  fi
  if [[ "${cmp}" -eq -1 ]]; then
    log "upgrading qabas CLI ${installed} → ${target}"
  elif [[ "${cmp}" -eq 1 ]]; then
    log "downgrading qabas CLI ${installed} → ${target}"
  fi
}

download_and_install_cli() {
  local release_json asset_id sums_id
  if [[ -n "${GITHUB_TOKEN}" ]]; then
    log "using authenticated GitHub API (private repo)"
    release_json="$(curl_github_api "$(release_endpoint)" || true)"
    if [[ -z "${release_json}" ]] || ! printf '%s' "${release_json}" | grep -q '"tag_name"'; then
      die_auth_required "Could not read release ${TAG} metadata from ${REPO}."
    fi
    if ! asset_id="$(asset_id_for "${release_json}" "${ASSET}")"; then
      if [[ "${USE_DEB}" -eq 1 ]]; then
        log "release ${TAG} has no ${ASSET}; falling back to raw binary"
        USE_DEB=0
        ASSET="qabas_${SEMVER}_linux_${goarch}"
        ASSET_URL="${GITHUB_RELEASES}/download/${TAG}/${ASSET}"
        need_cmd install
        asset_id="$(asset_id_for "${release_json}" "${ASSET}")" \
          || die "release ${TAG} does not publish ${ASSET}"
      else
        die "release ${TAG} does not publish ${ASSET}"
      fi
    fi
    sums_id="$(asset_id_for "${release_json}" "SHA256SUMS")" \
      || die "release ${TAG} does not publish SHA256SUMS"

    log "downloading ${ASSET} (release asset ${asset_id})"
    download_release_asset "${asset_id}" "${tmpdir}/${ASSET}"

    log "downloading SHA256SUMS (release asset ${sums_id})"
    download_release_asset "${sums_id}" "${tmpdir}/SHA256SUMS"
  else
    log "downloading ${ASSET_URL}"
    if ! curl_public -o "${tmpdir}/${ASSET}" "${ASSET_URL}"; then
      if [[ "${USE_DEB}" -eq 1 ]]; then
        log "could not download ${ASSET}; falling back to raw binary"
        USE_DEB=0
        ASSET="qabas_${SEMVER}_linux_${goarch}"
        ASSET_URL="${GITHUB_RELEASES}/download/${TAG}/${ASSET}"
        need_cmd install
        log "downloading ${ASSET_URL}"
        curl_public -o "${tmpdir}/${ASSET}" "${ASSET_URL}" \
          || die_auth_required "Could not download ${ASSET} (release ${TAG})."
      else
        die_auth_required "Could not download ${ASSET} (release ${TAG})."
      fi
    fi

    log "downloading ${SUMS_URL}"
    if ! curl_public -o "${tmpdir}/SHA256SUMS" "${SUMS_URL}"; then
      die_auth_required "Could not download SHA256SUMS for release ${TAG}."
    fi
  fi

  (
    cd "${tmpdir}"
    grep -E "  ${ASSET}(\$| )" SHA256SUMS | sha256sum -c -
  ) || die "checksum verification failed"

  if [[ "${USE_DEB}" -eq 1 ]]; then
    log "installing ${ASSET} via dpkg"
    dpkg -i "${tmpdir}/${ASSET}" || die "dpkg -i failed for ${ASSET}"
    log "installed /usr/bin/qabas (${TAG})"
  else
    install -m 0755 "${tmpdir}/${ASSET}" "${INSTALL_DIR}/qabas"
    log "installed ${INSTALL_DIR}/qabas (${TAG})"
  fi
}

run_qabas() {
  local bin="$1"
  shift
  log "running: qabas $*"
  "${bin}" "$@" || {
    local rc=$?
    hint_auth_login
    exit "${rc}"
  }
}

run_product_step() {
  local bin product target cmp
  bin="$(qabas_bin || true)"
  [[ -n "${bin}" ]] || die "qabas binary not found after CLI install"

  product="$(installed_product_version || true)"

  if [[ -z "${RELEASE_SPEC}" ]]; then
    if [[ -n "${product}" && "${FORCE}" -eq 0 ]]; then
      log "QabasOne ${product} already installed; skipping product step"
      log "To upgrade the product stack use: qabas upgrade"
      log "To reinstall the same product use: curl ... | sudo bash -s -- --force"
      return 0
    fi
    local args=(install --yes --non-interactive)
    if [[ "${FORCE}" -eq 1 ]]; then
      args+=(--force)
    fi
    run_qabas "${bin}" "${args[@]}"
    return 0
  fi

  target="${RELEASE_SPEC}"
  if [[ -n "${product}" && "${product}" == "${target}" && "${FORCE}" -eq 0 ]]; then
    log "QabasOne ${product} already installed at requested version; skipping product step"
    return 0
  fi

  if [[ -z "${product}" ]]; then
    local args=(install --yes --non-interactive "${target}")
    [[ "${FORCE}" -eq 1 ]] && args+=(--force)
    run_qabas "${bin}" "${args[@]}"
    return 0
  fi

  cmp="$(semver_cmp "${product}" "${target}")"
  if [[ "${cmp}" -eq 0 ]]; then
    local args=(install --yes --non-interactive "${target}")
    [[ "${FORCE}" -eq 1 ]] && args+=(--force)
    run_qabas "${bin}" "${args[@]}"
    return 0
  fi
  if [[ "${cmp}" -lt 0 ]]; then
    log "switching QabasOne ${product} → ${target} (deploy)"
    run_qabas "${bin}" deploy "${target}" --yes --non-interactive
    return 0
  fi

  log "switching QabasOne ${product} → ${target} (downgrade)"
  run_qabas "${bin}" downgrade "${target}" --yes --non-interactive
}

TAG="$(resolve_tag)"
# Asset basename uses channel id "dev"; GitHub tag is "dev-latest".
if [[ "${TAG}" == "dev-latest" || "${VERSION}" == "dev" ]]; then
  SEMVER="dev"
elif [[ "${TAG}" == v* ]]; then
  SEMVER="${TAG#v}"
else
  SEMVER="${TAG}"
fi
if [[ "${USE_DEB}" -eq 1 ]]; then
  ASSET="qabas_${SEMVER}_linux_${goarch}.deb"
else
  ASSET="qabas_${SEMVER}_linux_${goarch}"
fi
ASSET_URL="${GITHUB_RELEASES}/download/${TAG}/${ASSET}"
SUMS_URL="${GITHUB_RELEASES}/download/${TAG}/SHA256SUMS"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

plan_cli_install

if [[ "${SKIP_CLI}" -eq 0 ]]; then
  download_and_install_cli
fi

persist_credentials
export GITHUB_TOKEN

bin="$(qabas_bin || true)"
if [[ -n "${bin}" ]]; then
  "${bin}" version || true
  log "credential status:"
  "${bin}" auth status 2>/dev/null || log "(auth status unavailable on this CLI build)"
fi

if [[ "${CLI_ONLY}" -eq 1 ]]; then
  log "CLI-only install complete"
  exit 0
fi

run_product_step
