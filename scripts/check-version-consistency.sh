#!/bin/sh
# One version is used by the npm package, the Rein plugin manifests, the Crux plugin manifests and the
# release tag. A drift here makes the SessionStart installer or the Crux tarball fetch the wrong asset,
# so fail closed before packaging.
set -eu
cd "$(dirname "$0")/.."

read_version() {
  sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$1" | head -1
}

version=
for manifest in \
  package.json \
  .claude-plugin/plugin.json \
  plugin.json \
  plugins/rein/plugin.json \
  plugins/crux/plugin.json \
  plugins/crux/.claude-plugin/plugin.json \
  plugins/crux/.codex-plugin/plugin.json
do
  current=$(read_version "$manifest")
  if [ -z "$current" ]; then
    echo "rein: version missing in $manifest" >&2
    exit 1
  fi
  if [ -z "$version" ]; then
    version=$current
  elif [ "$current" != "$version" ]; then
    echo "rein: version drift: $manifest=$current, expected $version" >&2
    exit 1
  fi
done

if [ "$#" -gt 0 ]; then
  tag=$1
  if [ "$tag" != "v$version" ]; then
    echo "rein: tag $tag does not match version v$version" >&2
    exit 1
  fi
  echo "rein: version v$version is consistent across all manifests and tag $tag"
else
  echo "rein: version v$version is consistent across all package and plugin manifests"
fi
