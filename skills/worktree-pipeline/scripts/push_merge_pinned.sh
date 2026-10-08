#!/bin/bash
# Force-push a gated head with a lease that names the old remote head, wait until GitLab says mergeable,
# then merge the MR pinned to that exact head. Run ONLY after the user approved this MR in chat.
# usage: push_merge_pinned.sh <worktree> <remote-branch> <mr-iid | - > [--no-merge]   (use - before the MR exists, with --no-merge)
set -uo pipefail
TARGET=${PIPELINE_TARGET_BRANCH:-main}
W=${1:?worktree}; RB=${2:?remote branch}; MR=${3:?mr iid}; NOMERGE=${4:-}
cd "$W" || exit 2
# Fail fast when GitLab is unreachable (VPN/DNS down makes git and glab hang for an hour).
REMOTE_HOST=$(git remote get-url origin | sed -E 's#^[a-z]+://##; s#^[^@]*@##; s#[:/].*$##')
timeout 10 curl -s -o /dev/null --max-time 8 "https://$REMOTE_HOST/" || { echo "[push] $REMOTE_HOST unreachable (VPN/DNS?); nothing pushed"; exit 5; }
NEW=$(git rev-parse HEAD); OLD=$(timeout 30 git ls-remote origin "refs/heads/$RB" | cut -f1)
if [ -n "$OLD" ] && [ "$OLD" != "$NEW" ]; then
  timeout 120 git push --force-with-lease="refs/heads/$RB:$OLD" origin "HEAD:refs/heads/$RB" 2>&1 | tail -1 || exit 1
elif [ -z "$OLD" ]; then timeout 120 git push origin "HEAD:refs/heads/$RB" 2>&1 | tail -1 || exit 1; fi
[ "$NOMERGE" = "--no-merge" ] && { echo "[push] pushed ${NEW:0:7}; not merging"; exit 0; }
for _ in $(seq 1 24); do
  ST=$(timeout 30 glab api "projects/:id/merge_requests/$MR" 2>/dev/null | python3 -c "import json,sys;d=json.load(sys.stdin);print(d['sha'],d.get('detailed_merge_status'),d['state'],d['target_branch'])")
  set -- $ST; [ "${1:-}" = "$NEW" ] && [ "${2:-}" = "mergeable" ] && break; sleep 5
done
[ "${1:-}" = "$NEW" ] && [ "${2:-}" = "mergeable" ] || { echo "[merge] MR !$MR not mergeable at ${NEW:0:7}: $ST"; exit 4; }
[ "${3:-}" = "opened" ] && [ "${4:-}" = "$TARGET" ] || { echo "[merge] REFUSED before merging: MR !$MR is '${3:-?}' targeting '${4:-?}', expected opened → $TARGET"; exit 8; }
timeout 120 glab mr merge "$MR" --sha "$NEW" -y 2>&1 | tail -1
# Trust the MR state, not glab's exit code: verify it is merged at the pinned head.
for _ in $(seq 1 12); do
  ST=$(timeout 30 glab api "projects/:id/merge_requests/$MR" 2>/dev/null | python3 -c "import json,sys;d=json.load(sys.stdin);print(d['state'],d.get('merge_commit_sha') or d.get('squash_commit_sha') or '-',d['sha'],d['target_branch'])")
  set -- $ST; [ "${1:-}" = "merged" ] && break; sleep 5
done
[ "${1:-}" = "merged" ] || { echo "[merge] MR !$MR is NOT merged (state: ${ST:-unknown}); stop the train"; exit 7; }
[ "${3:-}" = "$NEW" ] || { echo "[merge] MR !$MR merged a DIFFERENT head (${3:-?}) than the gated ${NEW:0:7}; stop and report"; exit 8; }
[ "${4:-}" = "$TARGET" ] || { echo "[merge] MR !$MR targets '${4:-?}', not '$TARGET'; stop and report"; exit 8; }
MERGED=${2:-}; [ -n "$MERGED" ] && [ "$MERGED" != "-" ] || { echo "[merge] MR !$MR reports no merge commit; stop and report"; exit 8; }
timeout 60 git fetch origin --quiet || { echo "[merge] merged as ${MERGED:0:7} but fetch failed; cannot prove it is on $TARGET"; exit 5; }
git merge-base --is-ancestor "$MERGED" "origin/$TARGET" 2>/dev/null || { echo "[merge] merge commit ${MERGED:0:7} is not on origin/$TARGET; stop and report"; exit 8; }
echo "[merge] !$MR merged ${MERGED:0:7} into $TARGET; origin/$TARGET now $(git log --oneline -1 "origin/$TARGET")"
