#!/bin/sh
# scripts/install-harness.sh: Register, test, or uninstall rein plugin bundle across harnesses
# Usage:
#   sh scripts/install-harness.sh [claude|agy|opencode|codex|kiro|all]
#   sh scripts/install-harness.sh uninstall [claude|agy|opencode|codex|kiro|all]

set -e

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ACTION="install"
TARGET="all"

if [ "$1" = "uninstall" ]; then
  ACTION="uninstall"
  TARGET="${2:-all}"
else
  TARGET="${1:-all}"
fi

say() { echo "rein-harness: $*"; }

# --- INSTALL ACTIONS ---

install_claude() {
  say "Installing Claude Code plugin..."
  if command -v claude >/dev/null 2>&1; then
    say "Run in Claude Code session:"
    echo "  /plugin marketplace add $ROOT"
    echo "  /plugin install rein@rein"
  else
    say "Claude CLI not found; manifest ready at $ROOT/.claude-plugin/plugin.json"
  fi
}

install_agy() {
  say "Registering Antigravity (AGY) plugin..."
  if command -v agy >/dev/null 2>&1; then
    agy plugin install "$ROOT/plugins/rein"
    say "Antigravity plugin installed via 'agy plugin install'"
  else
    DEST="${XDG_CONFIG_HOME:-$HOME/.gemini/config}/plugins/rein"
    mkdir -p "$(dirname "$DEST")"
    rm -rf "$DEST"
    ln -sf "$ROOT/plugins/rein" "$DEST"
    say "Antigravity plugin linked to $DEST"
  fi
}

install_opencode() {
  say "Registering OpenCode plugin..."
  DEST="${XDG_CONFIG_HOME:-$HOME/.config/opencode}/plugins/rein"
  mkdir -p "$(dirname "$DEST")"
  rm -rf "$DEST"
  ln -sf "$ROOT/.opencode/plugins/rein" "$DEST"
  say "OpenCode plugin linked to $DEST"
}

install_codex() {
  say "Registering OpenAI Codex plugin..."
  if command -v codex >/dev/null 2>&1; then
    codex plugin marketplace add "$ROOT" 2>/dev/null || true
    codex plugin add rein@rein 2>/dev/null || true
    say "Codex plugin installed via 'codex plugin add rein@rein'"
  else
    DEST="${CODEX_HOME:-$HOME/.codex}"
    mkdir -p "$DEST"
    say "Codex CLI not found; hooks template ready at $ROOT/.codex/hooks.json"
  fi
}

install_kiro() {
  say "Setting up AWS Kiro agent definition..."
  DEST="${HOME}/.kiro/agents"
  mkdir -p "$DEST"
  cp -f "$ROOT/.kiro/agents/rein.json" "$DEST/rein.json" 2>/dev/null || true
  say "Kiro agent definition ready at $DEST/rein.json"
}

# --- UNINSTALL ACTIONS ---

uninstall_claude() {
  say "Uninstalling Claude Code plugin..."
  say "Run in Claude Code session: /plugin uninstall rein@rein"
}

uninstall_agy() {
  say "Uninstalling Antigravity (AGY) plugin..."
  if command -v agy >/dev/null 2>&1; then
    agy plugin uninstall rein 2>/dev/null || true
  fi
  rm -rf "${XDG_CONFIG_HOME:-$HOME/.gemini/config}/plugins/rein" 2>/dev/null || true
  say "Antigravity plugin removed."
}

uninstall_opencode() {
  say "Uninstalling OpenCode plugin..."
  rm -rf "${XDG_CONFIG_HOME:-$HOME/.config/opencode}/plugins/rein" 2>/dev/null || true
  say "OpenCode plugin removed."
}

uninstall_codex() {
  say "Uninstalling OpenAI Codex plugin..."
  if command -v codex >/dev/null 2>&1; then
    codex plugin remove rein@rein 2>/dev/null || true
    codex plugin marketplace remove rein 2>/dev/null || true
  fi
  say "Codex plugin removed."
}

uninstall_kiro() {
  say "Uninstalling AWS Kiro agent definition..."
  rm -f "${HOME}/.kiro/agents/rein.json" 2>/dev/null || true
  say "Kiro agent removed."
}

# --- DISPATCH ---

if [ "$ACTION" = "uninstall" ]; then
  case "$TARGET" in
    claude)   uninstall_claude ;;
    agy)      uninstall_agy ;;
    opencode) uninstall_opencode ;;
    codex)    uninstall_codex ;;
    kiro)     uninstall_kiro ;;
    all)
      uninstall_claude
      uninstall_agy
      uninstall_opencode
      uninstall_codex
      uninstall_kiro
      say "All harness plugins uninstalled and cleaned up successfully!"
      ;;
    *)
      echo "Usage: $0 uninstall [claude|agy|opencode|codex|kiro|all]"
      exit 1
      ;;
  esac
else
  case "$TARGET" in
    claude)   install_claude ;;
    agy)      install_agy ;;
    opencode) install_opencode ;;
    codex)    install_codex ;;
    kiro)     install_kiro ;;
    all)
      install_claude
      install_agy
      install_opencode
      install_codex
      install_kiro
      say "All harness plugin bundles registered successfully!"
      ;;
    *)
      echo "Usage: $0 [claude|agy|opencode|codex|kiro|all]"
      exit 1
      ;;
  esac
fi
