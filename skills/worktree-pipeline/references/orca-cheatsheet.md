# Orca cheatsheet for the pipeline (Orca 1.4.219, launch help/guides verified 2026-10-09)

## Commands that worked
```sh
orca status --json                                   # runtime.state must be "ready"
orca orchestration run-create / run-use / run-show
# Prepare an exact worktree, install required hooks, and check the source spec before launch.
# Existing worktrees reject creation flags; rein names and Orca Task IDs are distinct.
# After task-create, use the returned task_<opaque-id>, never the rein contract name.
rein spec check /abs/source.md <rein-contract-name>
orca orchestration task-create --run R --task-title "<task>: <what>" --display-name "<task> [<agent>/<model>]" --spec "$(cat /abs/source.md)" --json
rein spec check /abs/source.md <rein-contract-name> && \
  orca orchestration worker-start --run R --task <returned-task-id> \
  --worktree path:/abs/existing/worktree --agent claude --model <id> --json
# Creation flags belong to worktree create, before hooks installation, not this launch.
orca terminal create --worktree path:<wt> --title "<task> [<agent>/<model>]" --command "..." --json
orca worktree set --worktree <wt> --display-name "<task> [<agent>/<model>]" --comment "<what> using <model>" --json
orca orchestration dispatch --run R --task <returned-task-id> --to <terminal> --return-preamble --json   # preamble route
orca terminal send --terminal <term> --text "read the file <preamble> and do the TASK" --enter --json
orca terminal read --terminal <term> --json          # last screen lines
orca orchestration check --run R [--peek] [--types worker_done,escalation,question] [--wait --timeout-ms N] --json
orca orchestration check --run R --ack <deliveryId> --peek --json   # ack by DELIVERY id, not message id
orca orchestration reply --run R --id <msg> --body "..." --json      # answer a worker's ask
orca orchestration send --run R --to dispatch:<ctx> --type status --subject S --body B --json  # follow-up note
orca orchestration worker-list --run R --include-remote --json  # projection.liveness / nextAction; follow page.nextCursor until !page.hasMore
orca orchestration worker-stop --dispatch <ctx> --json      # only positive exited proof + exact nextAction; no --run flag
orca orchestration worker-release --dispatch <ctx> --json
orca worktree rm --worktree <exact-verified-selector> --json
```
Keep your repo's Orca id (`--repo id:<repo-id>`) in the project pack's `PACK.md`.

## Guard hooks per vendor (`rein hooks install <rein-contract-name>` prints the exact flags)
Install before the first launch; re-install on every retry/provider fallback for a fresh generation before any test tool call. Use the newly printed flags. `GUARD_INACTIVE` remains retrospective current-generation evidence, not a launch preflight or resource-ownership proof.
Run `rein spec check /abs/source.md <rein-contract-name>` before Task creation/dispatch. For Task-ID worker-start, keep it immediately before the launch with `&&` in the same shell call, using that checked source's Task receipt and exact worktree. Negated/background checks, semicolon/OR separation, and mismatched sources/contracts do not qualify. The guard cannot fetch a Task's source spec; never invent it. Literal inline specs are linted against the exact worktree contract; use the Task-ID recipe for shell-expanded file contents.

- **codex:** `worker-start --agent codex` cannot pass CLI flags (only `--model`, `--effort`). The guard needs `--dangerously-bypass-hook-trust` plus the two `-c 'hooks.PreToolUse=[...]'` / `-c 'hooks.Stop=[...]'` flags, so start a guarded codex worker in a shell terminal (preamble route: `codex <printed flags> -C <worktree> "$(cat preamble)"`) or with the direct `codex exec <printed flags> -C <worktree> ...` fallback. Without the flags the hook is skipped silently (`rein drift` then says GUARD_INACTIVE).
- **kiro:** `kiro-cli chat --agent rein --trust-all-tools --model <model> "$(cat preamble)"`; the `--agent rein` is what loads the hook.
- **agy, opencode2:** no flag; start them inside the worktree.

## Quirks and the fix for each
- **The codex update prompt blocks start-up** (`agent-update-prompt`): pick "Skip until next version" (send `3`), then retry the start with `--retry-of`.
- **antigravity TUI 1.3.1 cannot be injected** (agent_prompt_blocked or readiness timeout):
  1. open a shell terminal;
  2. run `dispatch --return-preamble` and save the preamble to a file;
  3. run `agy --model gemini-3.8-flash-high --mode accept-edits --dangerously-skip-permissions -i "$(cat preamble)"`;
  4. send later tasks with `terminal send` ("read preamble file …").
- **agy sometimes forgets `worker_done`:** ask it to send it.
- **Out of credits:** agy exits 3 with `RESOURCE_EXHAUSTED (429)` → move the task to codex.
- **codex terminals become "not a recognized agent" after a task:** use the same preamble route, or close the terminal and start a fresh worker.
- **Recognized Claude terminals can be reused** with `worker-start --terminal <term> --worktree path:...`.
- **Heartbeats pile up** as "You have N orchestration messages": `scripts/ocloop2.py` acks them every minute.
- **A long `check --wait` returns `runtime_timeout`:** Orca is busy or hung. Check `orca status --json`.
- **Orca runtime hung** (2026-10-07 23:00–06:20: `state: starting`, `reachable: false`, renderer above 100% CPU):
  - Workers keep running on disk, but their `worker_done` may be lost.
  - **Fallback that worked:** run the CLIs directly in the background and read their result files:
    `codex exec <hook flags from rein hooks install> --dangerously-bypass-approvals-and-sandbox --ephemeral --color never -m gpt-6.1-sol -c model_reasoning_effort=high -C <worktree> -o <final.txt> - < prompt.md > log 2>&1`
    It can run docker, vitest and e2e. Tell it: "Orca is unavailable, do NOT run any orca command; end with a 3-sentence summary, files and commits".
  - agy has a direct form too: `agy --print "$(cat prompt)" --mode accept-edits --dangerously-skip-permissions --model gemini-3.8-flash-high --output-format text`.
  - When Orca comes back, enumerate every worker page and inspect positive liveness evidence. Timeout, lost `worker_done`, missing state and `unverifiable` never authorize cleanup. `orca_cleanup.py --stop` refuses unless the dispatch has positively exited and its exact `projection.nextAction.argv` recommends `worker-stop`; a `worker-read` or `worker-show` recommendation requires inspection first.
- **Cleanup follows Orca ownership receipts.** `python3 -B scripts/orca_cleanup.py <run> [--dry-run]` resolves `ORCA_CLI_COMMAND`, then `ORCA_DEV_REPO_ROOT` → `orca-dev`, then Linux `orca-ide`, otherwise `orca`, once per invocation. It reads all pages before mutation and requires an exact release recommendation plus `dispatchStatus` or authoritative `projection.outcome` of `succeeded`/`failed`. Accepted settled workers may be released while the TUI is live; exited liveness alone never authorizes release.
- **Retained/reused terminals remain preserved.** `worker-release` archives output and closes only the terminal owned by the settled dispatch; reused, setup, coordinator, active and unproven terminals are retained. For `release_pending` or `release_unknown`, inspect the receipt and follow its exact recovery action. Never substitute `terminal close` for supervised release. After release the helper re-enumerates all pages and requires fresh `terminalState: released`; unconfirmed closure exits nonzero.
- **Current recovery authority:** load `orca skills get orchestration --reference references/recovery-and-cleanup.md` and `orca skills get orchestration --reference references/messaging-and-gates.md`; a listing argument is never proof of exit.

## opencode / kiro (verified 2026-10-08)
- **opencode2 (v2.0.20)** is a native Orca agent: `worker-start ... --agent opencode2` (or `opencode`). `--model` is ignored for it; the model comes from `~/.config/opencode/opencode.jsonc` (`model`). For a different model per task use the shell route: `opencode2 run -m <provider/model> "$(cat preamble)"`. List models: `opencode2 models`. Headless probe answered in seconds.
- **kiro-cli (2.21.0)** IS a known Orca TUI agent (Orca 1.4.219 app bundle: `agent-kind.js`, launched with `--trust-all-tools`; the `worker-start --help` agent list is only examples). But `worker-start --agent kiro` cannot pass `--model` (help: --model only for Claude, Codex, Cursor, Antigravity, Muse) nor `--agent rein`, so it runs kiro's configured default model (here `claude-opus-4.8`, effort max, 2.2x credits) WITHOUT the rein hook. Until kiro defaults are verified, run a guarded kiro worker in an Orca shell terminal + `dispatch --return-preamble` (authoritative Orca Task/Dispatch context; operator-owned unsupervised process):
  `kiro-cli chat --no-interactive --trust-all-tools --agent rein --model claude-sonnet-5.5 "$(cat preamble)"` (worker; it needs write + shell tools), or headless in the background with the same flags and the output to a log.
  - **Always pass `--model`** (`kiro-cli chat --list-models`); the configured default is `claude-opus-4.8` at effort max.
  - Each answer ends with `▸ Credits: N` — record it in the model ledger (`--credits`).
  - **Read-only mode** = `--trust-tools=read,grep,glob`: reads run, writes and shell are rejected ("non-interactive mode (no user to approve)"). A batch that mixes a rejected call with a read cancels the read too. `scripts/advise.sh` uses this with `ADVISE_PROVIDER=kiro`.
  - It warns "Not all mcp servers loaded" in non-interactive mode; raise `mcp.noInteractiveTimeout` in its settings if a run needs MCP.

## GitLab facts
- **Right after a push** the MR shows `detailed_merge_status: checking` and `glab mr merge` answers "Branch cannot be merged". Poll until `mergeable`; `push_merge_pinned.sh` does this.
- **Always merge with `--sha <gated head>`.** This glab has no `--no-squash`.
- **To update an MR description:** `glab api -X PUT projects/:id/merge_requests/<iid> -F description=@file`.
- **A project without MR pipelines:** your gates are the only evidence; put the numbers in the MR.

## Keep sessions visible in Orca
The user watches the team in the Orca UI. Prefer `worker-start` when it can carry the required launch arguments. Guarded Codex needs a shell terminal with the printed hook flags and low-level preamble delivery; install required hooks before all launches and re-install for every retry/fallback. Check the receipt and current-generation tool-hook logs separately.

Orca 1.4.219's version-matched `skills get --topic orchestration --reference low-level-topology` distinguishes authoritative Task/Dispatch context from resource ownership. Low-level `dispatch --return-preamble` plus `terminal send` leaves the shell-created process **operator-owned and unsupervised**; dispatch does not adopt it. Supervised adoption via `worker-start --terminal` requires a successful receipt. In the 2026-10-09 parent run Claude exhausted quota and newly shell-launched Codex returned `agent_unconfigured` despite an idle TUI; preamble delivery worked with current-generation Codex hook evidence. Report that fallback honestly, retain operator process/resource ownership, and do not infer adoption from readiness or hook activity. Direct CLI fallback has the same operator ownership; announce the switch and use fresh hook flags.

## Orca Browser E2E Testing
Orca provides native browser automation (`orca tab`, `orca goto`, `orca snapshot`, `orca click`, `orca fill`, `orca keypress`, `orca eval`, `orca screenshot`).
- **Check readiness:** `rein e2e check [--json]`
- **Execute scenario:** `rein e2e run <spec.json|spec.md> [--out <dir>] [--base-url <url>]`
- **Evidence:** Automatically captures pass/fail screenshots, accessibility tree snapshots, and `report.md` for `rein contract` audits.
- Full reference: [`skills/worktree-pipeline/references/orca-browser-e2e.md`](orca-browser-e2e.md).

## Mandatory finished-session closure
Session closure is a completion gate in the pipeline, not optional end-of-run housekeeping. Archive the final result and finish coordinator verification, then close run-created worker/reviewer/helper sessions (including Kiro TUI) and unused launch shells without waiting for merge. Retain only at the user's explicit request.

For supervised workers use worker-release and authoritative recovery. For operator-owned fallback/advisor sessions, match terminal show against the original launch handle, incarnation, host and worktree, verify completion and no newer activity/takeover, archive output, then terminal close that exact handle. Verify ptyKilled=true and absence in a fresh worktree terminal list. Idle alone is not completion proof, headless CLI exit leaves the enclosing shell alive, and a release failure never authorizes this operator path. Preserve the main session, active or unknown sessions, history and worktrees still needed for active work or an open integration MR. Every session must be accounted for as closed, explicitly retained, or unresolved before final reporting.

The supervised cleanup gate also fails on retries when a finished dispatch remains retained/pending/unknown and only inspection is recommended; a new invocation cannot turn unresolved closure into success. Operator-owned sessions still follow the separate identity-verified procedure above.

Finished auxiliary worktrees are removed after their contribution is integrated and verified, even before the integration MR merges. Archive reports, ignored/local files and a verified Git bundle outside them first; require a clean checkout, no live terminal/job or active dispatch, exact repo/host/worktree identity and no dependency needing the checkout. Re-check before exact Orca worktree removal, preserve unmerged branches and verify absence in Orca/Git/disk. A failed or unverifiable check retains the worktree with a reason, with no raw-deletion fallback. Keep main and the integration/review checkout for an open MR; account for every run-created worktree as removed or retained with its reason.

Executable completion gates: `orca_cleanup.py <run> --close-operator <launch.json> <completed-show.json> <report> --check-worktree <exact selector>` (repeat options). The coordinator declares verified operator completion; all Runs/workers are enumerated only to veto coordinator/supervised/active ownership. Exact local identity/runtime/output rechecks, positive PTY receipt and fresh absence are mandatory. Stop as well as release must have fresh released-state proof.

Then `cleanup_worktrees.py <main> --root <worktree-root> --keep <running/open-MR names|none> --archive-dir <external run archive> [--integrated-into <full SHA>] [--dry-run]`. Mark only verified finished worktrees completed. No force, raw Git fallback or branch -D; required archives and fresh process/terminal/Orca/Git/disk checks fail nonzero if unverifiable. Keep main, active work, linked reviews and dependent children with reasons. Coordinator must exclude concurrent reuse/writers while checking and closing/removing because Orca provides no atomic conditional primitive.
