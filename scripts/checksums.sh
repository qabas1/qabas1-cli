#!/usr/bin/env bash
# Write dist/SHA256SUMS over Linux release assets present for VERSION.
# Run after all release assets have been assembled.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${VERSION:-dev}"

cd "${ROOT}/dist"
shopt -s nullglob
files=(qabas_"${VERSION}"_linux_* install.sh)
existing=()
for f in "${files[@]}"; do
  [[ -f "${f}" ]] && existing+=("${f}")
done
(( ${#existing[@]} > 0 )) || { echo "no release assets in dist/ for ${VERSION}" >&2; exit 1; }
sha256sum "${existing[@]}" > SHA256SUMS
echo "wrote dist/SHA256SUMS (${#existing[@]} files)"
