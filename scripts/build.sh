#!/bin/sh
# Cross-compile rein for every supported target into dist/. Pure Go, no cgo.
set -eu
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
mkdir -p dist
for t in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do
  os=${t%/*}; arch=${t#*/}; ext=; [ "$os" = windows ] && ext=.exe
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "dist/rein-$os-$arch$ext" ./cmd/rein
  echo "built dist/rein-$os-$arch$ext"
done
(cd dist && { sha256sum rein-* 2>/dev/null || shasum -a 256 rein-*; } > SHA256SUMS) && echo "wrote dist/SHA256SUMS"
