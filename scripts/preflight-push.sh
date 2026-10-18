#!/bin/sh
# preflight before push: no source file may sit hidden behind an ignore pattern.
# 2026-10-18 lesson: a bare 'rein' line in .gitignore matched the package dir
# cmd/rein itself, so `git add -A` skipped 25 new sources silently; CI/release
# builds from the checkout then failed with undefined acquireRouteMutation
# while host/docker builds (from disk) stayed green. Broken builds fail fast;
# invisible missing files do not.
#
# Rules:
#  1. no .go/.py/.sh/.md may be ignored anywhere (that is a lost source)
#  2. anything else ignored must be a recognized local artifact
set -eu
cd "$(dirname "$0")/.."

ignored="$(git status --porcelain --ignored=matching 2>/dev/null |
	awk '$1 == "!!" {print $NF}')"

hidden="$(printf '%s\n' "$ignored" | grep -E '\.(go|py|sh|md)$' || true)"
if [ -n "$hidden" ]; then
	echo "these source files are hidden by ignore patterns (would never be pushed):" >&2
	echo "$hidden" >&2
	exit 1
fi

unexpected="$(printf '%s\n' "$ignored" | grep -v '^bin/' |
	grep -v '^dist/' | grep -v '^graphify-out/' |
	grep -v '\.DS_Store$' | grep -v '^\.omc/' |
	grep -v '^\.agents/' | grep -v '^\.codex/' | grep -v '^\.opencode/' |
	grep -v '^rein$' | grep -v 'rein-run\.json' | grep -v '__pycache__' || true)"
if [ -n "$unexpected" ]; then
	echo "unexpected ignored files (verify each is a local artifact, then extend the allowlist deliberately):" >&2
	echo "$unexpected" >&2
	exit 1
fi

echo "preflight ok: all source files tracked; ignore list holds artifacts only"
