# Orca cheatsheet for the pipeline (Orca 1.4.219, verified 2026-10-07/08)

## Commands that worked
```sh
orca status --json                                   # runtime.state must be "ready"
orca orchestration run-create / run-use / run-show
orca orchestration worker-start --run R --spec "$(cat s.md)" --task-title T --display-name D \
  --worktree new-top-level --name NAME --repo id:<repo-id> --base-branch origin/main --setup skip \
  --agent codex|claude|antigravity --model <id> [--effort max] --json
orca orchestration worker-start ... --worktree path:/abs/existing/worktree   # reuse an Orca-known worktree
orca orchestration task-create --run R --task-title T --display-name D --spec "$(cat s.md)" --json
orca orchestration dispatch --run R --task <task> --to <terminal> --return-preamble --json   # preamble route
orca terminal send --terminal <term> --text "read the file <preamble> and do the TASK" --enter --json
orca terminal read --terminal <term> --json          # last screen lines
orca orchestration check --run R [--peek] [--types worker_done,escalation,question] [--wait --timeout-ms N] --json
orca orchestration check --run R --ack <deliveryId> --peek --json   # ack by DELIVERY id, not message id
orca orchestration reply --run R --id <msg> --body "..." --json      # answer a worker's ask
orca orchestration send --run R --to dispatch:<ctx> --type status --subject S --body B --json  # follow-up note
orca orchestration worker-list --run R --json        # workers[].dispatchStatus / terminalState / resource
orca orchestration worker-stop --dispatch <ctx> --json      # (no --run flag)
orca orchestration worker-release --dispatch <ctx> --json
orca terminal close --terminal <term> --json          # "terminal_handle_stale" = already closed
orca worktree rm --worktree path:/abs/path --force --json
```
Keep your repo's Orca id (`--repo id:<repo-id>`) in the project pack's `PACK.md`.

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
- **Claude terminals reuse fine** with `worker-start --terminal <term> --worktree path:...`.
- **Heartbeats pile up** as "You have N orchestration messages": `scripts/ocloop2.py` acks them every minute.
- **A long `check --wait` returns `runtime_timeout`:** Orca is busy or hung. Check `orca status --json`.
- **Orca runtime hung** (2026-10-07 23:00–06:20: `state: starting`, `reachable: false`, renderer above 100% CPU):
  - Workers keep running on disk, but their `worker_done` may be lost.
  - **Fallback that worked:** run the CLIs directly in the background and read their result files:
    `codex exec --dangerously-bypass-approvals-and-sandbox --ephemeral --color never -m gpt-6.1-sol -c model_reasoning_effort=high -C <worktree> -o <final.txt> - < prompt.md > log 2>&1`
    It can run docker, vitest and e2e. Tell it: "Orca is unavailable, do NOT run any orca command; end with a 3-sentence summary, files and commits".
  - agy has a direct form too: `agy --print "$(cat prompt)" --mode accept-edits --dangerously-skip-permissions --model gemini-3.8-flash-high --output-format text`.
  - When Orca comes back, settle the lost dispatches with `orca_cleanup.py --stop`.
- **Too many open terminals slow the Orca renderer.** Close finished ones promptly (`orca_cleanup.py`).
- **The worker-list `terminalState` stays `retained`** after a release. Closing the terminal is what frees it.

## opencode / kiro (verified 2026-10-08)
- **opencode2 (v2.0.20)** is a native Orca agent: `worker-start ... --agent opencode2` (or `opencode`). `--model` is ignored for it; the model comes from `~/.config/opencode/opencode.jsonc` (`model`). For a different model per task use the shell route: `opencode2 run -m <provider/model> "$(cat preamble)"`. List models: `opencode2 models`. Headless probe answered in seconds.
- **kiro-cli (2.21.0)** is NOT in Orca's agent list. Run it like agy: a shell terminal + `dispatch --return-preamble`, then
  `kiro-cli chat --no-interactive --trust-all-tools --model claude-sonnet-5.5 "$(cat preamble)"` (worker; it needs write + shell tools), or headless in the background with the same flags and the output to a log.
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
The user watches the team in the Orca UI. Every worker and reviewer starts through `worker-start`. On a start failure, retry inside Orca first (`--retry-of`, or a fresh worker on the same worktree). Use a direct CLI (`codex exec`, `claude -p`) only when Orca is down or keeps failing, and tell the user in one line before switching.
