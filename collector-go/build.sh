#!/usr/bin/env bash
# Builds speedtest-collector for common platforms into dist/ with SHA-256 checksums.
# Usage: ./build.sh [version]     (default: git describe, or "dev")
set -euo pipefail
cd "$(dirname "$0")"
VERSION="${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
PKG=github.com/chmbrb/SpeedtestApp/collector-go/internal/version.Version
rm -rf dist && mkdir dist
for target in linux/amd64 linux/arm64 linux/arm windows/amd64 darwin/amd64 darwin/arm64; do
  os=${target%/*}; arch=${target#*/}
  ext=""; [ "$os" = windows ] && ext=".exe"
  out="dist/speedtest-collector-${os}-${arch}${ext}"
  echo "building $out"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X ${PKG}=${VERSION}" -o "$out" ./cmd/speedtest-collector
done
(cd dist && sha256sum speedtest-collector-* > SHA256SUMS)
echo "done: version ${VERSION}"
