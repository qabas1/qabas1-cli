#!/usr/bin/env bash
# Build Debian packages from linux binaries produced by scripts/dist.sh.
# Requires: nfpm on PATH (scripts/ensure-nfpm.sh installs a pinned binary).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

VERSION="${VERSION:-dev}"
DEB_VERSION="${VERSION#v}"
# Debian versions must start with a digit (CI uses pr-N / rolling "dev").
if [[ ! "${DEB_VERSION}" =~ ^[0-9] ]]; then
  DEB_VERSION="0.0.0~${DEB_VERSION}"
fi

bash "${ROOT}/scripts/ensure-nfpm.sh"
export PATH="${ROOT}/.tools:${PATH}"

MAN_SRC_RAW="${ROOT}/packaging/man/qabas.1"
[[ -f "${MAN_SRC_RAW}" ]] || { echo "missing ${MAN_SRC_RAW}" >&2; exit 1; }

mkdir -p dist
MAN_SRC="dist/qabas.1.gz"
# Debian Policy: compress man pages with gzip -9 (no timestamp for reproducibility).
gzip -9 -n -c "${MAN_SRC_RAW}" > "${MAN_SRC}"

render_nfpm_config() {
  local arch="$1" bin="$2" out="$3"
  if command -v envsubst >/dev/null 2>&1; then
    ARCH="${arch}" VERSION="${DEB_VERSION}" BIN_SRC="${bin}" MAN_SRC="${MAN_SRC}" \
      envsubst '${ARCH} ${VERSION} ${BIN_SRC} ${MAN_SRC}' < packaging/nfpm.yaml > "${out}"
    return
  fi
  # Portable fallback when gettext/envsubst is unavailable.
  sed \
    -e "s|\${ARCH}|${arch}|g" \
    -e "s|\${VERSION}|${DEB_VERSION}|g" \
    -e "s|\${BIN_SRC}|${bin}|g" \
    -e "s|\${MAN_SRC}|${MAN_SRC}|g" \
    packaging/nfpm.yaml > "${out}"
}

for arch in amd64 arm64; do
  bin="dist/qabas_${VERSION}_linux_${arch}"
  [[ -f "${bin}" ]] || { echo "missing ${bin}; run scripts/dist.sh first" >&2; exit 1; }

  cfg="$(mktemp)"
  render_nfpm_config "${arch}" "${bin}" "${cfg}"
  out="dist/qabas_${VERSION}_linux_${arch}.deb"
  nfpm package --config "${cfg}" --packager deb --target "${out}"
  rm -f "${cfg}"
  echo "built ${out}"
done
