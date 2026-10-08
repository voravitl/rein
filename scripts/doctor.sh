#!/bin/sh
# Check the prerequisites of the rein plugin and its worktree-pipeline skill. Prints one line per tool:
#   ok / MISSING / old, the version found, and what needs it. Exit 1 when a REQUIRED item is missing.
# usage: sh scripts/doctor.sh            (CLAUDE_PLUGIN_DATA, when set, is where the plugin keeps the binary)
set -u
fail=0
ver() { "$@" 2>&1 | head -1 | tr -d '\r' | cut -c1-40; }
row() { printf '%-8s %-12s %-40s %s\n' "$1" "$2" "$3" "$4"; }
check() { # level name "version cmd" need
  lvl=$1; name=$2; cmd=$3; need=$4; bin=${cmd%% *}
  if command -v "$bin" >/dev/null 2>&1; then row ok "$name" "$(ver sh -c "$cmd")" "$need"
  else row MISSING "$name" "-" "$need"; [ "$lvl" = required ] && fail=1; fi
}
echo "== rein prerequisites (required)"
check required claude "claude --version" "Claude Code with plugin hooks (tested 2.1.293)"
check required git "git --version" "worktrees, drift check (tested 2.50)"
check required sh "sh -c 'echo POSIX sh'" "SessionStart installer (Git Bash on Windows)"
if command -v go >/dev/null 2>&1; then row ok go "$(go version)" "builds the guard binary (1.26+, see go.mod)"
else row MISSING go "-" "builds the guard binary (1.26+); without it a release binary is downloaded with curl"; fi
check optional curl "curl --version" "downloads a release binary when Go is missing"

# The hooks run the plugin's own binary; a manual install on PATH does not count for them.
if [ -n "${CLAUDE_PLUGIN_DATA:-}" ]; then
  BIN="$CLAUDE_PLUGIN_DATA/bin/rein"
  if v=$("$BIN" version 2>/dev/null); then row ok rein "$v" "$BIN (what the hooks run)"
  else row MISSING rein "-" "$BIN does not run: start a new session (SessionStart installs it) or run /rein:setup"; fail=1; fi
fi
if p=$(command -v rein 2>/dev/null); then
  if v=$("$p" version 2>/dev/null); then row info rein-PATH "$v" "$p (CLI use; not what the plugin hooks run)"
  else row broken rein-PATH "-" "$p does not run"; [ -z "${CLAUDE_PLUGIN_DATA:-}" ] && fail=1; fi
elif [ -z "${CLAUDE_PLUGIN_DATA:-}" ]; then row MISSING rein "-" "no rein on PATH and CLAUDE_PLUGIN_DATA not set"; fail=1; fi

echo "== worktree-pipeline skill (needed only for multi-agent runs)"
check pipeline orca "orca --version" "Orca app + CLI (worktrees, workers, orchestration; tested 1.4.219)"
check pipeline python3 "python3 --version" "waiter, cleanup scripts, advise.sh (tested 3.12)"
check pipeline bash "bash --version" "advise / rebase / push scripts"
check pipeline timeout "timeout --version" "advise / push scripts (macOS: brew install coreutils, gnubin on PATH)"
check pipeline perl "perl -e 'print \"perl $^V\"'" "advise.sh with kiro (ANSI cleanup)"
check pipeline glab "glab --version" "MRs and pinned merges (GitLab)"
if claude plugin list 2>/dev/null | grep -q 'oh-my-claudecode'; then row ok omc-plugin "installed" "OMC plugin: notepad, wiki, session_search MCP tools"
else row MISSING omc-plugin "-" "OMC plugin (/plugin install oh-my-claudecode): notepad, wiki, session_search"; fi
check pipeline omc "omc --version" "OMC CLI: advisor role prompts (npm i -g oh-my-claude-sisyphus)"
if command -v npm >/dev/null 2>&1 && [ -d "$(npm root -g 2>/dev/null)/oh-my-claude-sisyphus/agents" ]; then row ok omc-roles "$(ls "$(npm root -g)/oh-my-claude-sisyphus/agents" | wc -l | tr -d ' ') prompts" "advise.sh critic/tracer/code-reviewer"
else row MISSING omc-roles "-" "advise.sh critic/tracer/code-reviewer (npm i -g oh-my-claude-sisyphus)"; fi

echo "== worker / reviewer / advisor CLIs (install the ones you route to)"
check vendor codex "codex --version" "worker, review gate, default advisor (codex login)"
check vendor agy "agy --version" "antigravity worker (Gemini)"
check vendor opencode2 "opencode2 --version" "cheap worker on its own provider quota"
check vendor kiro-cli "kiro-cli --version" "fallback worker/reviewer, kiro advisor (kiro-cli login)"

echo "== optional"
check optional docker "docker --version" "project gates that use throwaway containers"
check optional codegraph "codegraph --version" "code facts for specs (codegraph_explore)"
check optional gh "gh --version" "GitHub issues (the merge scripts are GitLab-only today)"

[ -f "$HOME/.config/rein/fallback-chain.json" ] && row ok config "fallback-chain.json" "rein providers" || row MISSING config "fallback-chain.json" "rein providers (copy examples/fallback-chain.example.json)"
n=$(ls "$HOME/.config/rein/profiles/"*.json 2>/dev/null | wc -l | tr -d ' ')
[ "$n" -gt 0 ] && row ok config "$n profile(s)" "~/.config/rein/profiles" || row MISSING config "no profile" "rein contract --profile (copy examples/profile.example.json)"
exit $fail
