#!/bin/sh
# preflight before push: no source file may sit hidden behind an ignore pattern,
# and no source file may sit untracked next to the push.
# 2026-10-18 lesson: a bare 'rein' line in .gitignore matched the package dir
# cmd/rein itself, so `git add -A` skipped 25 new sources silently; CI/release
# builds from the checkout then failed with undefined acquireRouteMutation
# while host/docker builds (from disk) stayed green. Broken builds fail fast;
# invisible missing files do not.
#
# Rules:
#  1. no .go/.py/.sh/.md may be ignored anywhere outside a vendored or
#     generated tree (that is a lost source)
#  2. anything else ignored must be a recognized local artifact
#  3. no .go/.py/.sh/.md may be untracked (a push would not carry it)
#
# Every git query is captured with its own exit status, so a git failure (no
# work tree, broken repository) stops the latch instead of being masked by a
# filter and reported as a clean tree. Ignored paths are enumerated file by
# file, never as directory summaries: "!! bin/" hides a bin/lost.go completely.
set -eu
cd "$(dirname "$0")/.."

# vendored or generated trees: their contents are never project sources, so an
# ignored .md inside them (package README, generated report, bytecode cache) is
# not a lost source.
vendored='^(\.opencode|\.omc|\.agents|\.codex|graphify-out)/|(^|/)__pycache__/'
sources='\.(go|py|sh|md)$'

git_failed() { # git_failed <git exit code> <what was being listed>
	echo "preflight: git failed (exit $1) while listing $2;" >&2
	echo "  run this inside the git work tree you are about to push" >&2
	exit 1
}

count() { # count <newline separated paths>
	printf '%s\n' "$1" | grep -c . || true
}

show() { # show <paths>: bound the listing so a large tree cannot flood stderr
	total="$(count "$1")"
	printf '%s\n' "$1" | sed -n '1,20p' >&2
	if [ "$total" -gt 20 ]; then
		echo "  ... and $((total - 20)) more" >&2
	fi
}

ignored="$(git ls-files --others --ignored --exclude-standard)" ||
	git_failed "$?" "ignored files"
untracked="$(git ls-files --others --exclude-standard)" ||
	git_failed "$?" "untracked files"
tracked="$(git ls-files --cached -- '*.go' '*.py' '*.sh' '*.md')" ||
	git_failed "$?" "tracked source files"

hidden="$(printf '%s\n' "$ignored" | grep -Ev "$vendored" | grep -E "$sources" || true)"
if [ -n "$hidden" ]; then
	echo "preflight: source files are hidden by ignore patterns (a push would never carry them):" >&2
	show "$hidden"
	exit 1
fi

loose="$(printf '%s\n' "$untracked" | grep -Ev "$vendored" | grep -E "$sources" || true)"
if [ -n "$loose" ]; then
	echo "preflight: source files are untracked (not in the index; a push would not carry them):" >&2
	show "$loose"
	exit 1
fi

unexpected="$(printf '%s\n' "$ignored" | grep -v '^bin/' |
	grep -v '^dist/' | grep -v '^graphify-out/' |
	grep -v '\.DS_Store$' | grep -v '^\.omc/' |
	grep -v '^\.agents/' | grep -v '^\.codex/' | grep -v '^\.opencode/' |
	grep -v '^rein$' | grep -v 'rein-run\.json' | grep -v '__pycache__' || true)"
if [ -n "$unexpected" ]; then
	echo "preflight: unexpected ignored paths (verify each is a local artifact, then extend the allowlist deliberately):" >&2
	show "$unexpected"
	exit 1
fi

echo "preflight ok: no source file is ignored or untracked" \
	"(tracked source files: $(count "$tracked"); ignored paths: $(count "$ignored"))"
