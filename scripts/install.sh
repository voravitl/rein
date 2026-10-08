#!/bin/sh
# Put the binary for this machine at bin/rein (what hooks/hooks.json runs). Builds it when Go is available,
# otherwise copies the matching file from dist/.
set -eu
cd "$(dirname "$0")/.."
os=$(uname -s | tr '[:upper:]' '[:lower:]'); arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; *) echo "unsupported arch $arch" >&2; exit 1;; esac
mkdir -p bin
if command -v go >/dev/null 2>&1; then
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/rein ./cmd/rein
elif [ -f "dist/rein-$os-$arch" ]; then
  cp "dist/rein-$os-$arch" bin/rein && chmod +x bin/rein
else
  echo "no Go toolchain and no dist/rein-$os-$arch; run scripts/build.sh on a machine with Go" >&2; exit 1
fi
bin/rein version
