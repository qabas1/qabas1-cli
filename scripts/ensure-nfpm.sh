#!/usr/bin/env bash
# Install a pinned nfpm binary into .tools/ (gitignored) when missing.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NFPM_VERSION="${NFPM_VERSION:-2.41.3}"
TOOLS="${ROOT}/.tools"
BIN="${TOOLS}/nfpm"

if [[ -x "${BIN}" ]]; then
  export PATH="${TOOLS}:${PATH}"
  exit 0
fi

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "${arch}" in
  x86_64|amd64) goarch="x86_64" ;;
  aarch64|arm64) goarch="arm64" ;;
  *) echo "unsupported arch for nfpm: ${arch}" >&2; exit 1 ;;
esac

case "${os}" in
  linux) asset="nfpm_${NFPM_VERSION}_Linux_${goarch}.tar.gz" ;;
  darwin) asset="nfpm_${NFPM_VERSION}_Darwin_${goarch}.tar.gz" ;;
  *) echo "unsupported OS for nfpm bootstrap: ${os}" >&2; exit 1 ;;
esac

url="https://github.com/goreleaser/nfpm/releases/download/v${NFPM_VERSION}/${asset}"
tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT
mkdir -p "${TOOLS}"
curl -fsSL -o "${tmpdir}/nfpm.tgz" "${url}"
tar -xzf "${tmpdir}/nfpm.tgz" -C "${tmpdir}" nfpm
mv "${tmpdir}/nfpm" "${BIN}"
chmod +x "${BIN}"
export PATH="${TOOLS}:${PATH}"
echo "installed nfpm ${NFPM_VERSION} -> ${BIN}"
