#!/bin/bash
# Rebase one worker branch onto origin/main and prove the commits did not change.
# usage: rebase_proof.sh <worktree> <old-base-or-range-start>
#   prints: old head -> new head, the count of NON-identical commits in range-diff (must be 0, else read it),
#   exits 6 when any commit is not identical, and stops on a conflict (resolve it, git add, GIT_EDITOR=true git rebase --continue, then rerun the range-diff by hand).
set -uo pipefail
W=${1:?worktree}; OLDBASE=${2:?old base commit}
cd "$W" || exit 2
timeout 60 git fetch origin --quiet || { echo "[rebase] fetch failed or timed out (VPN/DNS?)"; exit 5; }
git rev-parse --verify --quiet "$OLDBASE^{commit}" >/dev/null || { echo "[rebase] old base $OLDBASE is not a commit here"; exit 2; }
[ -z "$(git status --porcelain | grep -v '^??')" ] || { echo "[rebase] uncommitted tracked changes; commit or stash first"; exit 3; }
OLD=$(git rev-parse --verify HEAD) || { echo "[rebase] cannot resolve HEAD"; exit 2; }
if ! RB=$(git rebase origin/main 2>&1); then
  if [ -n "$(git diff --name-only --diff-filter=U)" ]; then echo "[rebase] CONFLICT:"; git diff --name-only --diff-filter=U; exit 3; fi
  echo "[rebase] REFUSED by git (no rebase started):"; printf '%s\n' "$RB" | tail -5; exit 3
fi
NEW=$(git rev-parse --verify HEAD) || { echo "[rebase] cannot resolve new HEAD"; exit 2; }
RD=$(git range-diff "$OLDBASE..$OLD" "origin/main..$NEW") || { echo "[rebase] range-diff failed; proof NOT established"; exit 6; }
[ -n "$RD" ] || { echo "[rebase] range-diff is empty (no commits?); proof NOT established"; exit 6; }
DIFF=$(printf '%s\n' "$RD" | grep -vc " = ")
echo "[rebase] ${OLD:0:7} -> ${NEW:0:7} on $(git rev-parse --short origin/main); non-identical commits: $DIFF"
[ "$DIFF" = "0" ] || { printf '%s\n' "$RD" | head -40; echo "[rebase] commits changed: read the range-diff, re-gate, do not push blindly"; exit 6; }
