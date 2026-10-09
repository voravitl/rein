#!/bin/bash
# One advisor call (spec critique, gate-failure triage, disputed finding) with an ENFORCED read-only sandbox.
# Uses an OMC agent prompt (critic, tracer, code-reviewer, security-reviewer, test-engineer, ...) as the role and
# templates/advisor-rules.md as the rules. Providers (ADVISE_PROVIDER):
#   codex: `codex exec -s read-only` (no file writes, no docker socket; network stays open)
#   claude: headless restricted settings with only Read,Grep,Glob; MCP configuration excluded.
#   kiro: `kiro-cli chat --no-interactive --trust-tools=read,grep,glob` (only read tools run; writes and shell are
#         rejected; costs Kiro credits, not Claude Code quota). Never `omc ask`: it runs codex without a sandbox.
# usage: advise.sh <role> <repo-or-worktree dir> <task prompt file> <output file> [model]
# env: ADVISE_PROVIDER, ADVISE_MODEL, ADVISE_TIMEOUT, ADVISE_PURPOSE, ADVISE_RUN, ADVISE_TASK (contract task ID), PIPELINE_PACK (project pack dir;
#      required unless ADVISE_NO_PACK=1),
#      REIN (rein binary; default `rein` on PATH)
set -uo pipefail
ROLE=${1:?role}; DIR=${2:?repo or worktree dir}; TASK=${3:?task prompt file}; OUT=${4:?output file}
RUN=${ADVISE_RUN:-}; TASK_ID=${ADVISE_TASK:-}
PROVIDER=${ADVISE_PROVIDER:-}; MODEL=${5:-${ADVISE_MODEL:-}}
for VALUE in "$RUN" "$TASK_ID" "$PROVIDER" "$MODEL"; do
  case "$VALUE" in *[![:space:]]*) ;; *) echo "[advise] explicit ADVISE_RUN, ADVISE_TASK, ADVISE_PROVIDER and model are required"; exit 2;; esac
done
case "$PROVIDER" in codex|kiro|claude) ;; *) echo "[advise] ADVISE_PROVIDER must be codex, kiro or claude"; exit 2;; esac
case "$MODEL" in *fable*) echo "[advise] fable is never used"; exit 2;; esac
HERE="$(cd "$(dirname "$0")" && pwd)"; RULES="$HERE/../templates/advisor-rules.md"
# Pipeline roles (templates/roles: blast-radius, claim-auditor) first, then OMC agent prompts.
ROLEFILE="$HERE/../templates/roles/$ROLE.md"
[ -f "$ROLEFILE" ] || ROLEFILE="$(npm root -g 2>/dev/null)/oh-my-claude-sisyphus/agents/$ROLE.md"
[ -f "$ROLEFILE" ] || { echo "[advise] role '$ROLE' not found (templates/roles or OMC agents)"; exit 2; }
[ -d "$DIR" ] && [ -s "$TASK" ] || { echo "[advise] need an existing dir and a non-empty task file"; exit 2; }
"${REIN:-rein}" route check --task "$TASK_ID" --run "$RUN" --agent "$PROVIDER" --model "$MODEL" --phase review --worktree "$DIR" || {
  echo "[advise] route check failed; advisor was not called"; exit 2;
}
LOGDIR="${PIPELINE_LOGDIR:-$HOME/.cache/worktree-pipeline/logs}"; mkdir -p "$LOGDIR" "$(dirname "$OUT")"
LOG="$LOGDIR/advise-$PROVIDER-$ROLE-$(date +%H%M%S).log"
ROLETEXT=$(awk 'NR==1 && /^---$/ {f=1; next} f && /^---$/ {f=0; next} !f' "$ROLEFILE")   # drop YAML frontmatter
[ -n "$ROLETEXT" ] || { echo "[advise] role file $ROLEFILE is empty or unreadable"; exit 2; }
RULETEXT=$(DIR="$DIR" python3 -c 'import os,sys;print(open(sys.argv[1]).read().replace("<repo absolute path>",os.environ["DIR"]))' "$RULES")
# Project pack addendum (protected containers, ports, DB of this project): $PIPELINE_PACK/advisor-rules.md
[ -n "$RULETEXT" ] || { echo "[advise] could not read $RULES (python3 missing?); refusing to call an advisor without rules"; exit 2; }
# Required unless ADVISE_NO_PACK=1 (a project without protected services): a missing pack must not silently drop the
# project's "never touch" rules.
PACK=${PIPELINE_PACK:-}; case "$PACK" in "~/"*) PACK="$HOME/${PACK#"~/"}";; esac
if [ -n "$PACK" ]; then
  PACKTEXT=$(cat "$PACK/advisor-rules.md" 2>/dev/null) && [ -n "$PACKTEXT" ] || { echo "[advise] cannot read $PACK/advisor-rules.md (missing, empty or unreadable)"; exit 2; }
  RULETEXT=$(printf '%s\n%s\n' "$RULETEXT" "$PACKTEXT")
elif [ "${ADVISE_NO_PACK:-}" != 1 ]; then
  echo "[advise] set PIPELINE_PACK=<project pack dir> on this command (or ADVISE_NO_PACK=1 for a project without one)"; exit 2
fi
TASKTEXT=$(cat "$TASK" 2>/dev/null) && [ -n "$TASKTEXT" ] || { echo "[advise] cannot read the task file $TASK"; exit 2; }
PROMPT=$(printf '%s\n\n%s\n\nRepo (read-only): %s\n\n%s\n' "$ROLETEXT" "$RULETEXT" "$DIR" "$TASKTEXT")
rm -f "$OUT"   # an old answer must never count as this call's result
T0=$(date +%s)
if [ "$PROVIDER" = codex ]; then
  timeout "${ADVISE_TIMEOUT:-1500}" codex exec -s read-only --skip-git-repo-check --ephemeral --color never \
    -m "$MODEL" -c model_reasoning_effort=high -C "$DIR" -o "$OUT" "$PROMPT" </dev/null >"$LOG" 2>&1
  RC=$?
elif [ "$PROVIDER" = claude ]; then
  (cd "$DIR" && timeout "${ADVISE_TIMEOUT:-1500}" claude -p --model "$MODEL" --restricted \
    --tools Read,Grep,Glob --strict-mcp-config "$PROMPT") </dev/null >"$LOG" 2>&1
  RC=$?
  [ "$RC" -ne 0 ] || cp "$LOG" "$OUT"
else
  # kiro prints the answer on stdout; keep the raw stream in the log and the cleaned answer in $OUT.
  (cd "$DIR" && timeout "${ADVISE_TIMEOUT:-1500}" kiro-cli chat --no-interactive --model "$MODEL" --trust-tools=read,grep,glob "$PROMPT") </dev/null >"$LOG" 2>&1
  RC=$?
  perl -pe 's/\e\[[0-9;?]*[a-zA-Z]//g' "$LOG" | awk '/^> /{p=1} p' | grep -v 'Credits:' > "$OUT"
fi
# Record the call in the model ledger (tokens come from codex's "tokens used" footer; missing = unknown).
TOK=""; CRED=""
if [ "$PROVIDER" = codex ]; then
  TOK=$(awk '/^tokens used/{getline; gsub(/[^0-9]/,""); print; exit}' "$LOG")
elif [ "$PROVIDER" = kiro ]; then
  CRED=$(sed -n 's/.*Credits: *\([0-9.]*\).*/\1/p' "$LOG" | tail -1)
fi
MIN=$(awk -v s="$T0" -v e="$(date +%s)" 'BEGIN{printf "%.1f", (e-s)/60}')
"${REIN:-rein}" ledger call --role "$ROLE" --model "$MODEL" --purpose "${ADVISE_PURPOSE:-}" ${TOK:+--tokens "$TOK"} ${CRED:+--credits "$CRED"} --provider "$PROVIDER" --minutes "$MIN" --run "$RUN" --task "$TASK_ID" \
  $([ "$RC" -eq 0 ] && [ -s "$OUT" ] || echo --failed) >/dev/null 2>&1 || {
  echo "[advise] ledger write failed after advisor call; output retained at $OUT, retry recording before another call"; exit 1;
}
[ "$RC" -eq 0 ] && [ -s "$OUT" ] || { echo "[advise] $PROVIDER failed (exit $RC); log: $LOG"; tail -5 "$LOG"; exit 1; }
echo "[advise] $ROLE ($PROVIDER $MODEL${CRED:+, $CRED credits}${TOK:+, $TOK tokens}) → $OUT ($(wc -l <"$OUT" | tr -d ' ') lines); log: $LOG"
