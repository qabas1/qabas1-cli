#!/usr/bin/env bash
# Cross-compile linux release assets and Debian packages.
# Windows build scripts remain under packaging/windows, but release publishing is Linux-only.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${ROOT}"

VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-none}"
MODULE="github.com/qabas1/qabasone-cli"
LDFLAGS="-s -w -X ${MODULE}/internal/version.Version=${VERSION} -X ${MODULE}/internal/version.Commit=${COMMIT}"

mkdir -p dist
GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "${LDFLAGS}" -o "dist/qabas_${VERSION}_linux_amd64" ./cmd/qabas
GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "${LDFLAGS}" -o "dist/qabas_${VERSION}_linux_arm64" ./cmd/qabas
cp install/install.sh dist/install.sh

VERSION="${VERSION}" bash scripts/package-deb.sh
VERSION="${VERSION}" bash scripts/checksums.sh
