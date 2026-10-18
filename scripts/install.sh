#!/bin/sh
# Put the binary for this machine at bin/rein (what hooks/hooks.json runs). The npm tarball ships
# neither Go sources nor a native binary, so the install order is:
#   1. build from source            when this is a full checkout (go.mod + cmd/rein) and Go is available
#   2. copy the local artifact      dist/rein-<os>-<arch> (scripts/build.sh output)
#   3. verified release download    rein-<os>-<arch>[.exe] + SHA256SUMS from the GitHub release for this
#                                   package version; same conventions as scripts/ensure-binary.sh
#                                   (REIN_RELEASE_URL and REIN_RELEASE_REPO override the release location)
#   4. fail nonzero with a clear message
set -eu
cd "$(dirname "$0")/.."

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in mingw*|msys*|cygwin*|windows_nt) os=windows;; esac
arch=$(uname -m)
case "$arch" in x86_64|amd64) arch=amd64;; arm64|aarch64) arch=arm64;; esac
exe=
case "$os" in windows) exe=.exe;; esac
asset="rein-$os-$arch$exe"

version=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' package.json 2>/dev/null | head -1)
[ -n "$version" ] || version=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' .claude-plugin/plugin.json 2>/dev/null | head -1)
url=

mkdir -p bin
tmp="bin/.rein.tmp.$$$exe"
trap 'rm -f "$tmp" "$tmp.sums"' EXIT
how=

# 1. full checkout with Go
if [ -f go.mod ] && [ -d cmd/rein ] && command -v go >/dev/null 2>&1; then
  if (export GOOS= GOARCH= CGO_ENABLED=0; go build -trimpath -ldflags "-s -w" -o "$tmp" ./cmd/rein); then
    how="built from source"
  else
    rm -f "$tmp"
    echo "rein: go build failed; trying the prebuilt options" >&2
  fi
fi
# 2. a local dist/ artifact
if [ -z "$how" ] && [ -f "dist/$asset" ]; then
  cp "dist/$asset" "$tmp"
  how="copied dist/$asset"
fi
# 3. verified release download (curl or wget)
if [ -z "$how" ] && [ -n "$version" ]; then
  url=${REIN_RELEASE_URL:-https://github.com/${REIN_RELEASE_REPO:-voravitl/rein}/releases/download/v$version}
  fetched=
  if command -v curl >/dev/null 2>&1; then
    if curl -fsL --max-time 120 -o "$tmp" "$url/$asset" && curl -fsL --max-time 30 -o "$tmp.sums" "$url/SHA256SUMS"; then fetched=1; fi
  elif command -v wget >/dev/null 2>&1; then
    if wget -q -O "$tmp" "$url/$asset" && wget -q -O "$tmp.sums" "$url/SHA256SUMS"; then fetched=1; fi
  fi
  if [ -n "$fetched" ]; then
    want=$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1}' "$tmp.sums")
    got=$( (sha256sum "$tmp" 2>/dev/null || shasum -a 256 "$tmp") | cut -d' ' -f1)
    if [ -n "$want" ] && [ "$want" = "$got" ]; then
      how="downloaded release v$version"
    else
      rm -f "$tmp"
      echo "rein: checksum mismatch for $asset; not installing it" >&2
    fi
  else
    rm -f "$tmp"
    echo "rein: could not download $asset and SHA256SUMS from $url" >&2
  fi
  rm -f "$tmp.sums"
fi

if [ -z "$how" ]; then
  echo "rein: cannot provide a native binary for $os/$arch" >&2
  echo "rein: tried go build (a full checkout and Go 1.26+), dist/$asset, and the release download${url:+ at $url}" >&2
  echo "rein: set REIN_RELEASE_URL to a directory holding $asset + SHA256SUMS, or run this from a full checkout with Go installed" >&2
  exit 1
fi

mv -f "$tmp" "bin/rein$exe"
chmod +x "bin/rein$exe"
if ! "bin/rein$exe" version; then
  rm -f "bin/rein$exe"
  echo "rein: installed bin/rein$exe but it does not run on this machine ($how); removed it" >&2
  exit 1
fi
echo "rein: installed bin/rein$exe ($how)"
