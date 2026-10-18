#!/bin/sh
# Package the Crux plugin as a release asset. Crux is a plugin, not a second execution engine; this
# tarball contains the shared skills, the four harness adapters and the session-mode hooks, without
# repository-only tests or generated caches. The release workflow appends its digest to SHA256SUMS.
set -eu
cd "$(dirname "$0")/.."

version="${VERSION:-$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' .claude-plugin/plugin.json | head -1)}"
[ -n "$version" ] || { echo "crux: no version found" >&2; exit 1; }
mkdir -p dist
asset="dist/crux-$version.tgz"

tar \
  --exclude='__pycache__' \
  --exclude='*.pyc' \
  --exclude='*.pyo' \
  --exclude='.DS_Store' \
  --exclude='tests' \
  --exclude='test_*.py' \
  --exclude='*.test.mjs' \
  --exclude='*.test.js' \
  --exclude='*.test.ts' \
  -czf "$asset" -C plugins crux
echo "packaged $asset"
