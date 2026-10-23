#!/bin/sh
# Register native Rein plugins. Task-bound hooks belong in contracted worktrees.
# Usage: sh scripts/install-harness.sh [uninstall] [claude|agy|codex|opencode|kiro|hermes|all]
set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ACTION=install
if [ "${1:-}" = uninstall ]; then
  ACTION=uninstall
  shift
fi
TARGET="${1:-all}"
[ "$#" -le 1 ] || { echo "Unexpected arguments" >&2; exit 1; }

say() { echo "rein-harness: $*"; }
require_cli() {
  command -v "$1" >/dev/null 2>&1 || {
    say "$1 CLI not found; no plugin registration changed." >&2
    return 1
  }
}
worktree_only() {
  say "$1 hooks are task-bound and cannot be installed globally." >&2
  say "In the contracted worktree, run: rein hooks install <task> --vendors $1" >&2
  return 1
}
# Older installers created global aliases. Only remove an exact link owned by
# this checkout; an unrelated directory or another checkout must survive.
remove_owned_link() {
  destination=$1
  source=$2
  if [ -L "$destination" ] && [ "$(readlink "$destination")" = "$source" ]; then
    rm "$destination"
  elif [ -e "$destination" ] || [ -L "$destination" ]; then
    say "Refusing to remove unowned target: $destination" >&2
    return 1
  fi
}

install_claude() {
  require_cli claude || return $?
  claude plugin marketplace add "$ROOT" || return $?
  claude plugin install rein@rein
}
uninstall_claude() {
  require_cli claude || return $?
  claude plugin uninstall rein@rein
}
install_agy() {
  require_cli agy || return $?
  require_cli rein || return $?
  rein version || return $?
  agy plugin install "$ROOT/plugins/rein"
}
uninstall_agy() {
  require_cli agy || return $?
  agy plugin uninstall rein
}
install_codex() { worktree_only codex; }
uninstall_codex() {
  require_cli codex || return $?
  codex plugin remove rein@rein
}
install_opencode() { worktree_only opencode; }
install_kiro() { worktree_only kiro; }
install_hermes() { worktree_only hermes; }
uninstall_hermes() {
  say "Hermes hooks are task-bound; no global registration changed." >&2
  say "In the contracted worktree, run: rein hooks uninstall <task> --vendors hermes" >&2
  return 1
}
uninstall_opencode() {
  remove_owned_link "${XDG_CONFIG_HOME:-$HOME/.config}/opencode/plugins/rein" "$ROOT/.opencode/plugins/rein" || return $?
  # Previous releases incorrectly omitted the application directory under XDG.
  if [ -n "${XDG_CONFIG_HOME:-}" ]; then
    remove_owned_link "$XDG_CONFIG_HOME/plugins/rein" "$ROOT/.opencode/plugins/rein" || return $?
  fi
  say "Global links checked; remove worktree hooks with: rein hooks uninstall <task> --vendors opencode"
}
uninstall_kiro() {
  remove_owned_link "$HOME/.kiro/agents/rein.json" "$ROOT/.kiro/agents/rein.json" || return $?
  say "Global link checked; remove worktree hooks with: rein hooks uninstall <task> --vendors kiro"
}

case "$TARGET" in
  claude|agy|codex|opencode|kiro|hermes) "${ACTION}_${TARGET}" ;;
  all)
    # All attempts run, but any unsupported/failed harness makes the result fail.
    failed=0
    for harness in claude agy codex opencode kiro hermes; do
      if (set -e; "${ACTION}_${harness}"); then :; else failed=1; fi
    done
    exit "$failed"
    ;;
  *) say "Unknown harness: $TARGET" >&2; exit 1 ;;
esac
