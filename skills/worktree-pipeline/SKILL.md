---
name: worktree-pipeline
description: "Run a batch of issues as parallel worker worktrees under Orca orchestration, kept on task by rein (task contract, guard hook, OS sandbox, drift check). One coordinator writes code-checked specs, picks the cheapest fitting model per task from ledger evidence (falling back across providers when a quota runs out), gates every result itself, loops read-only reviews by a strong model from another vendor until APPROVE, opens MRs, merges only user-approved MRs in dependency order with rebase + range-diff + re-gate + pinned --sha, tests merged main on an isolated stack, and closes finished terminals and worktrees. Project rules come from a local project pack. Use it when the user wants work split across agents in worktrees and merged back after review, says 'แตกงานเป็น worktree', 'แจกงานให้ codex claude antigravity', 'ทำแบบรอบที่แล้ว', 'คุมด้วย orchestration', 'merge ตามลำดับแล้วทดสอบ', 'ปิด worktree ที่จบแล้ว', or names this pipeline or the coordinator ('orca-swarm', 'ให้ orca-swarm คุมงาน'). Load this skill and coordinate from the current session through Orca; never start rein:orca-swarm as a subagent."
---

# Worktree pipeline (coordinator playbook)

You are the **coordinator**. Workers build; reviewers judge; you gate, decide routing and keep resources lean. You never self-approve and never merge, release or deploy without the user's word in chat.

**The coordinator is the session the user talks to.** It runs this playbook itself and drives every worker, helper
and steward job through Orca orchestration (`orca orchestration run-create` → `worker-start` → `check --wait`). Never
start the coordinator or a steward job through the `Agent` tool (`rein:orca-swarm`, `rein:orca-steward` as Claude
subagents): that bypasses Orca, its supervision and the worker contracts. The rein hook denies those calls.

**Paths in this playbook:**
- `<skill>` = `${CLAUDE_PLUGIN_ROOT}/skills/worktree-pipeline` (scripts in `<skill>/scripts`, templates in `<skill>/templates`).
- `rein` = `${CLAUDE_PLUGIN_DATA}/bin/rein` (installed by the plugin's SessionStart hook; `/rein:setup` can also link it onto `PATH`). Pass `REIN=${CLAUDE_PLUGIN_DATA}/bin/rein` to `advise.sh` when `rein` is not on `PATH`.
- `<profile>` = the project's rein profile, `~/.config/rein/profiles/<project>.json` (or `$REIN_PROFILE`); `<pack>` = its `pack` field.

Read before starting: `<skill>/references/orca-cheatsheet.md` (commands, quirks, fallbacks), `<skill>/references/omc-toolkit.md` (which OMC helper at which step, and which not to use), `<skill>/references/project-pack.md`, and the project's `<pack>/PACK.md` (its gates, safety rules, isolated stack, deploy notes, standing owner rules). No profile or pack for this project yet: stop and help the user write them first (`/rein:setup`, `examples/pack/`).

## 0. Set up a run
- **Preflight:** `rein version` runs (else run `/rein:setup`); `orca status --json` reports runtime `ready`; `omc --version` matches the OMC plugin. Use the selected Orca executable (`orca`, `orca-dev`, or `orca-ide`) consistently; read its `orchestration worker-start --help` and version-matched `skills get --topic orchestration --reference low-level-topology`. A ready runtime or idle TUI does not prove guard activity or supervised process ownership.
- **Run:** create or reuse one (`orca orchestration run-create` / `run-use`).
- **Run dir (durable):** `~/.cache/worktree-pipeline/runs/<run-name>/{specs,reports,logs,designs}`, Shell variables do not survive between Bash tool calls, so put `PIPELINE_LOGDIR=<run dir>/logs PIPELINE_PACK=<absolute pack dir>` in front of **every** script call (`advise.sh` refuses to run without a pack unless `ADVISE_NO_PACK=1`). Not the session scratchpad: it is ephemeral. Anything the team must keep long-term goes to the repo (an MR), to memory, or into the pack.
- **Run pointer:** write `orca run <id> | dir | repo | tasks:state | waiting on` with the OMC `notepad_write_priority` tool and update it on each state change, so a compaction or resume finds the run.
- **Resume / "ทำแบบรอบที่แล้ว":** re-read the repo's `MEMORY.md` first; find the previous run with OMC `session_search`.
- **Waiter:** start it in the background: `python3 <skill>/scripts/ocloop2.py <run> 3300`. It acks heartbeats every minute and returns only on `worker_done`, `escalation` or `question`. Restart it after every return.

## 1. Plan and assign (resource policy)
Default routing (change it only with ledger evidence and the user's yes):

| Work | Default | Why |
|---|---|---|
| Backend logic, migrations, security-heavy code | **codex** (strongest codex model) | strong, does not use Claude quota |
| Ordinary features, UI, glue, docs, mechanical runs | **claude Sonnet** | cheap and reliable in Orca |
| Frontend when credits allow | **antigravity** (Gemini flash) | third vendor; **on a credit/limit error (agy exit 3, 429 RESOURCE_EXHAUSTED) move that task to codex** |
| Review gate | a **strong model from a different vendor than the worker**: codex reviews Sonnet/agy work; Claude Opus (`--effort max`) reviews codex work on DB/security | the only place a strong model is required |
| Mechanical, high-volume jobs (steward cleanup and rebase-regate, log summaries, code-fact lookups, classification) | **claude Haiku** | a fraction of Sonnet's price; not for feature work or reviews; shares the Claude pool, so it is not a quota fallback |
| Cheap bulk work on another provider's quota | **opencode2** (`--agent opencode2`; Orca ignores `--model`, the model is `model` in opencode's own config) | separate quota; pilot until the ledger has data |
| Work or review when Claude Code quota is low | **kiro** (`kiro-cli`, Kiro credits). A known Orca agent, but `worker-start --agent kiro` passes neither `--model` nor `--agent rein` (no guard, default model): start it in an Orca shell terminal + preamble (cheatsheet). **Always pass `--model`** | separate credit pool; kiro's `claude-*` models count as the Anthropic vendor for review diversity |

- **Quota fallback:** the order lives in `~/.config/rein/fallback-chain.json` (worker chains per task type, review chains per worker vendor, quota signals per CLI). On a quota signal (Claude "usage limit", codex 429/"usage limit", agy exit 3/`RESOURCE_EXHAUSTED`, kiro/opencode limit errors):
  1. run `rein providers --chain worker:<type>` (add `--skip-claude` when Claude quota is the one that ran out); it probes the chain with one-line prompts and names the first provider that is up;
  2. move only the affected task to that provider through its launch route (`orca` = `--agent`; `shell` = shell terminal + preamble, e.g. `opencode2 run -m <provider/model> "$(cat preamble)"` or `kiro-cli chat --no-interactive --trust-all-tools --agent rein --model <model> "$(cat preamble)"` (`--agent rein` loads the guard hook installed by `rein hooks install`));
  3. record it in the ledger (`--fallback "<from> quota -> <to>"`) and say it in the report.
  - **Reviews only fall back to the strong models listed in `review_chains`**; when none is up, pause reviews and ask the user. Free models are for docs/mechanical/ordinary work only, never high-risk tasks and never a review gate.
  - Every fallback worker gets the same spec, rules, contract and gates; opencode and kiro run tools without prompts, so the worker rules and `rein drift` are the boundary, exactly as for codex.
- **Evidence (model ledger):** before assigning, run `rein ledger report` and `rein ledger suggest`. A routing change needs enough data (`--min-n`, default 3) and the user's yes. After each task settles, record it with `rein ledger add` (review rounds until APPROVE, first gate, blockers/highs, false claims, drift, fallback, minutes, tokens when known). `advise.sh` records advisor calls itself.
- **Issues:** one issue per task. Search first (umbrella issues too) and file only what is untracked; use `templates/issue.md`.
- **Dependencies:** order tasks by dependency. Independent tasks start in parallel, each in its own new top-level worktree from `origin/main`. Do not stack on unmerged branches unless the user agrees.
- **Owner decisions:** ask the open decisions a task needs *before* starting it (AskUserQuestion, recommended option first), then write the answers into the spec.

## 2. Spec, contract and start
- **Spec:** `templates/spec.md`. It covers scope ids `S1..Sn` with design references, owner decisions, explicit "not in scope", the tests with a red check for each, rules (`templates/worker-rules.md` + `<pack>/worker-rules.md`), gates and the report path.
- **Spec facts from code:** every sentence about how the code behaves today cites file:line, found with `codegraph_explore` (repo has `.codegraph/`) or an OMC `explore` agent. A claim you cannot cite becomes a question for the user.
- **Spec critique (large or risky tasks):** `<skill>/scripts/advise.sh critic <repo> <task file> <run>/reports/<task>-critique.md`: the OMC `critic` prompt under an enforced read-only sandbox (codex or kiro). Never `omc ask` for this: it runs codex without a sandbox.
- **Contract first:** `rein contract new --profile <profile> --name <task> --issue <n> --run-dir <run> --allow '<globs the task may edit>' [--deny '<globs>'] --scope S1,S2,... [--max-changed-lines N]`, then paste `rein contract show <task>` into the spec. The plugin's guard hook enforces it for Claude workers (it parses each Bash command with a real shell grammar and blocks push/rebase/`gh`/`glab`, dependency installs, the profile's protected containers, ports, commands and scripts, writes outside the ownership or into other worktrees, the run dir or contracts, and stopping without a report); `rein drift` checks every vendor's result. The guard is a seatbelt (it cannot see interpreter writes such as `python -c`); the OS sandbox is the wall; the drift check is the judge.
- **Worktree + sandbox before the worker (every worker; the sandbox is Claude-only):** create the worktree first (`orca worktree create --name <task> --repo id:<repo-id> --base-branch origin/main --setup skip --json`), run `rein sandbox <task>` (writes `.claude/settings.local.json`: OS sandbox on, no unsandboxed retries, writes limited to the worktree/temp/report, the profile's excluded commands and allowed domains; kept out of git status), then start the worker on it with `--worktree path:<abs path>`. Codex/agy/opencode/kiro workers do not read Claude settings: install the required vendor hooks with `rein hooks install <task>` after the worktree exists and before launch. Re-install before every retry or provider fallback, then use the newly printed flags and test a tool call against that fresh hooks generation. It writes each CLI's project hook file into the worktree, so the same contract and the same judge guard them, and it prints the launch flags you MUST use or the hook is silently skipped: **codex** `--dangerously-bypass-hook-trust` plus the printed `-c hooks...` flags (Orca `worker-start --agent codex` cannot pass them: start codex in a shell terminal with the preamble route); **kiro** `kiro-cli chat --agent rein ...` on every launch; **agy** and **opencode2** must be started inside the worktree. `rein drift <task> --expect-guard <vendor>` reports `GUARD_INACTIVE` retrospectively if no current-generation hook call was logged; it is not a launch receipt or lifecycle engine. Codex also has its own `-s workspace-write`.
- **Spec preflight:** rein contract names (for example `rein-launch-20261009`) and Orca Task IDs (`task_<opaque-id>`) are distinct. Run `rein spec check <absolute-source-spec> <rein-contract-name>` against the prepared worktree contract before creating/dispatching the Task; save the Task ID returned by Orca. Use that same checked source for `task-create --spec "$(cat <absolute-source-spec>)"`. Do not infer a spec from a Task ID or fabricate one. The guard lints literal inline `--spec` text against the exact worktree contract; unresolved shell expansion is denied on tracked worktrees, so use the explicit Task-ID route for file-backed specs.
- **Start** on an existing prepared worktree: in one shell call, `rein spec check <absolute-source-spec> <rein-contract-name> && orca orchestration worker-start --run <run> --task <returned-task-id> --worktree path:<absolute-worktree> --agent <agent> --model <model> --json`. The immediate successful `&&` preflight is required for opaque Task IDs; negated/background checks, `;`, `||`, and mismatched source/contract/worktree do not qualify. This validates the local source, not Orca's stored Task content: preserve the task-create receipt linking the same source to the Task ID. No Orca `--contract` flag exists. Keep `--name`, `--repo`, `--base-branch`, `--display-name`, `--comment`, and `--setup` on worktree creation/metadata operations; `worker-start` rejects them for existing/current worktrees. Guarded Codex needs the shell/preamble fallback below because `worker-start --agent codex` cannot pass its hook flags.
- **Codex preamble fallback:** after hooks installation and spec preflight, create the shell terminal with the printed Codex flags; obtain `dispatch --task <returned-task-id> --to <terminal> --return-preamble --json`, persist its exact authoritative preamble, then deliver it with `terminal send`. Copy the supplied executable, handle, Task ID, and Dispatch ID for messaging. Low-level dispatch supplies authoritative task context while the process/resources remain **operator-owned and unsupervised**. `worker-start --terminal` may adopt a recognized agent, but do not promise supervised adoption until its receipt proves ownership; an idle TUI is insufficient. On the 2026-10-09 parent run Claude hit quota and a freshly shell-launched Codex returned `agent_unconfigured`; preamble delivery worked and hook logs showed Codex current-generation calls. Treat those as separate ownership and guard facts. Keep operator resource ownership explicit on fallback.
- **IDE Visibility (Mandatory Titles & Models):** To keep the Orca IDE UI readable for the user at all times, every worker, task, and terminal MUST explicitly state what it is doing and with what model:
  - `task-create`: pass `--task-title "<task>: <what>"` and `--display-name "<task> [<agent>/<model>]"`. For an existing-worktree `worker-start`, set worktree display name/comment separately via `worktree set`; creation flags are rejected there.
  - Shell terminal workers (`codex`, `kiro`): pass `orca terminal create --title "<task> [<agent>/<model>]" ...` and update worktree metadata via `orca worktree set --worktree <wt> --display-name "<task> [<agent>/<model>]" --comment "<what> using <model>"`.
- **Check the receipt:** for supervised starts, `launch.effective` must show the requested agent/model, and the receipt must prove resource ownership. For low-level preamble dispatch, report unsupervised operator ownership instead. Independently verify the prepared worktree base and a current-generation hook call before assigning implementation.
- **Questions:** answer them from the design with `orca orchestration reply --id <msg> --body ...`. Record the answers in `<run>/specs/<task>-answers.md` so that the reviewer gets them too. Ack the delivery.

## 3. Gate every `worker_done` yourself (never trust the report)
- **Drift check first (cheap, every vendor):** `rein drift <task> --expect-guard <vendor that ran it: claude|codex|agy|kiro|opencode> --claimed-files <worker_done filesModified>` (the flag is required when `rein hooks install` covered several vendors, so another vendor's hook calls cannot vouch for this launch). Exit 1 = drift (a guard that never ran (`GUARD_INACTIVE`), out-of-scope or never-edit files, uncommitted work, missing report or scope sections, false file claims, TODO/skip/stubs, secrets): send a fix round with the DRIFT lines and do not spend gate or review time yet. Exit 2 = cannot judge: find out why before going on. Denials the hook made are in `~/.cache/worktree-pipeline/logs/guard.log` (the worker's `$PIPELINE_LOGDIR` when its environment sets one).
- **Claim audit for cheap or pilot workers** (Haiku, opencode, kiro, free models): `<skill>/scripts/advise.sh claim-auditor <worktree> <task file: spec + report path> <out>` before the strong review, so false claims cost a cheap call instead of a review round.
- **Inspect the commits:** `git log`, `git status` and `diff --stat`.
- **Exit codes are the verdict:** the scripts fail closed (`references/omc-toolkit.md`, last section, and `<pack>/PACK.md`); a nonzero exit is a failed gate even when the summary lines look fine.
- **Run the project's gates** exactly as `<pack>/PACK.md` says (typically: full backend suite in a throwaway DB, frontend tests/type check/build, the guarded e2e of the task's spec). **Serialize full suites:** parallel suites under load give timeouts; rerun a load-looking failure alone before you call it a defect.
- **No fake completion:** run the diff grep from `references/omc-toolkit.md`. A hit is a fix-round item.
- **On failure:** find the root cause quickly (fixture vs code vs load). When it is not obvious after one look, run `<skill>/scripts/advise.sh tracer <worktree> <task file> <out>` with the failing names, first error lines and log path, and run the one check it says separates the hypotheses yourself. Then send a precise fix round (`templates/fix-round.md`). For agy or codex terminals that cannot be injected, use the preamble route in the cheatsheet.

## 4. Review loop (Mandatory Review Report Policy)
- **Where:** a separate review worktree (new-top-level from the branch, or a detached checkout), with a strong model from a different vendor. Use `templates/review.md`, `templates/review-rules.md`, and strictly follow `templates/review-policy.md` (and `AGENTS.md`).
- **Reviewer duties:**
  1. Check claims against the code with `file:line` proof; re-run red checks; run the gates.
  2. Produce the mandatory **6-part Review Report** bound to the exact `Head SHA`:
     - 1. Work & Revision Identity (MR, Base SHA, Head SHA, Models, Tier)
     - 2. Acceptance Criteria Checklist (PASS / FAIL / NOT VERIFIED with evidence)
     - 3. Verification & Test Execution (exact test commands, local/CI, red checks)
     - 4. Ranked Findings (Severity, `file:line`, Failure Scenario, Risk, Fix)
     - 5. Handoff & Remediation Plan (actionable items for downstream AI worker)
     - 6. Verdict & Blockers (`APPROVE` / `REQUEST_CHANGES` / `COMMENT`)
  3. Register verdict:
     ```sh
     rein verdict record --mr <N> --sha <HEAD_SHA> --verdict <APPROVE|REQUEST_CHANGES> --reviewer <MODEL> --worker <MODEL>
     ```
- **Head SHA Invalidation:** If new commits are pushed (`Head SHA` changes), previous reports are STALE; subsequent rounds evaluate `git diff <old-sha>..<new-sha>` and check resolved findings.
- **Stopping rule:** `APPROVE` when remaining issues are theoretical or nits. Merge is gated by `rein verdict check` and human approval (`rein approve`).
- **Fix rounds:** each round fixes the ranked findings and comes back for a delta review (`git diff <old>..<new>`). The same reviewer context is best when its terminal still lives.
- **A finding outside the task's scope:** ask the user whether to fix it here or split it into a new issue. Never silently override a reviewer.
- **A disputed finding** (the worker shows evidence against it): get one advisory opinion with `<skill>/scripts/advise.sh code-reviewer <review worktree> <task file> <out>`, decide, and record both views in the MR.


## 5. Push and open the MR (no merge)
- **Blast radius (DB, security, shared code):** `<skill>/scripts/advise.sh blast-radius <worktree> <task file> <out>`; run the proof command it names yourself and put the result in the MR.
- **Before pushing:** run `<skill>/scripts/rebase_proof.sh <wt> <old-base>` if main moved. Every commit must be identical in range-diff; re-run the gates after any conflict.
- **Push:** `<skill>/scripts/push_merge_pinned.sh <wt> <remote-branch> - --no-merge` (the MR does not exist yet), then `glab mr create` with `templates/mr.md`.
- **The MR text must contain:** what changed; the decisions the owner must confirm; deploy notes (from the pack); the exact verification numbers; which reviewers ran and their verdicts; the dependencies ("merge with or after !N").
- **Tell the user:** the MR links, the shas and what is waiting for them. A merge waits for one explicit yes, unless `<pack>/PACK.md` records a standing owner rule that covers it (for example docs-only MRs); if unsure, ask. A standing rule never authorises a release or deploy.

## 6. Merge train (only after the user approves)
For each MR in dependency order:
1. `<skill>/scripts/rebase_proof.sh` onto the newest main.
2. Re-run the gates that the change between the old and new main can affect.
3. `<skill>/scripts/push_merge_pinned.sh <wt> <branch> <mr>`. It force-pushes with a lease that names the old head, waits for the forge's `checking → mergeable`, then merges with `--sha` of the gated head and proves the merge commit is on the target.
4. Add a short "Rebased onto <main> (date): new head / old head / range-diff identical / suite numbers" note to the MRs that are still open.

Never release or deploy as part of the train unless the user said so. Version numbers come from the user.

## 7. Test merged main
- Run the full suite on the merged main. Load-related timeouts must pass when re-run alone.
- **Isolated stack:** follow the recipe in `<pack>/PACK.md` (other ports, a read-only copy of the data, smoke checks, full e2e), ideally delegated to `orca-steward`. **Teardown is mandatory.**

## 8. Close what is finished (memory and disk)
**Session closure is part of task completion**, for every provider (including Kiro), workers, reviewers, helpers and unused launch shells. After the final result/report is archived and coordinator verification is complete, close the session immediately; do not wait for MR merge or the end of the run. Do not ask again for routine closure of a finished session created for this run. Keep a finished session live only when the user explicitly requests retention; record the reason. An idle TUI alone is not completion evidence.

Clean up after every settled task, not only at the end:
- `python3 -B <skill>/scripts/orca_cleanup.py <run> [--stop ctx_…] [--dry-run]`: enumerates every worker page (including remote observations) before acting, using one resolved Orca binary. Releases only workers whose exact `projection.nextAction.argv` recommends release and whose `dispatchStatus` or authoritative `projection.outcome` is `succeeded`/`failed`, even when the TUI is live; `--stop` requires positively exited liveness and the exact stop recommendation, otherwise it refuses. Unsettled active, unverifiable and missing-state workers remain preserved; retained/reused terminals follow Orca ownership receipts. Read release receipts and follow their exact recovery recommendations; never substitute `terminal close` for supervised release. The helper re-enumerates every page after release and exits nonzero unless each released dispatch has a fresh `terminalState: released`; retained/pending/unknown or missing rows are unfinished cleanup. Retries also fail when a finished supervised dispatch remains unresolved and only inspection is recommended.
- **Operator-owned sessions (shell/preamble fallback, direct advisors/smokes, unused shells):** record the launch receipt's handle, incarnation, host and worktree under the run logs when creating it. After the final transcript/exit and report are archived and gates are complete, re-check `orca terminal show --terminal <handle> --json` against that receipt and verify no new turn/job or takeover. Close only that exact run-created session with `orca terminal close --terminal <handle> --json`; require `ptyKilled: true`, then re-list its worktree to verify the handle is gone. This is operator cleanup, not a fallback for a failed supervised release. Unknown/reused identity or active work stops closure and is reported as unresolved; preserve the main conversation. Headless CLI exit does not close the surrounding shell. Never bulk-close a workspace containing unverified sessions.
- **Before the final user report:** account for every run-created session as closed (with receipt), explicitly retained by the user, or unresolved (with blocker). Do not report the run fully cleaned up while a finished session remains open. Keep reports/history and unmerged worktrees; session closure is separate from worktree deletion.
- `python3 <skill>/scripts/cleanup_worktrees.py <main checkout> --keep <running tasks | none> --root <worktree_root> [--ignore <the profile's local_artifacts, repo-relative, e.g. frontend/node_modules>] [--prune-images <test image repo>]` (`--keep` is required: a worker that has not committed yet looks merged): removes clean worktrees whose head is in `origin/main` or whose commits are patch-equivalent, deletes their branches, and removes old test images that no running container uses.
- **Keep:** the worktrees of running tasks, and review worktrees until their MR merges. **Never** run `docker volume prune` or `docker system prune`.

## 9. Report to the user (in the user's language)
Short status per task: what was merged and pushed; the numbers; the open decisions; what is running and what is blocked; what was cleaned up. Include `rein ledger report --since <run start>`.

Update memory with the run state (ids, worktrees, MRs, decisions, incidents). Log the run to the OMC wiki (`wiki_add`, category `session-log`) and, when nothing is left running, remove the notepad run pointer and restore any priority note the user had.

## Who implements (applies to every coordinator, agent or main session)
- The coordinator never implements. Every implementation unit is a **worker**: it has a rein contract, a model
  chosen by the routing table and ledger, and a ledger record when it settles.
- **Default worker: an Orca worker** in its own worktree (visible in the Orca UI; guard, sandbox and drift bound to
  that worktree). When routing picks Claude, start it with `--agent claude`.
- **Exception: a Claude subagent as the worker.** Allowed when routing picks Claude, the task has a contract, and the
  exception is recorded (`rein run allow --task <t> --reason "<why not an Orca worker>"`, once B0 ships; until
  then, say it in the report). A subagent is the Claude model like any other; what is never allowed is skipping
  routing, the contract or the ledger.
- Subagents without a contract are for read-only work only: code facts, review, critique.

## Agents
- **`rein:orca-swarm`** (Opus): the coordinator that runs this whole playbook for a batch. It returns a `DECISIONS NEEDED` block whenever the user must decide (owner decisions, merges, deploys); resume it with the answers.
- **`rein:orca-steward`** (Sonnet; Haiku for cleanup and rebase-regate): the mechanical jobs (rebase and proof, re-gates, pushes, pinned merges of MRs the user approved, the isolated stack test, cleanup). The coordinator starts it as an Orca Sonnet worker (never as a Claude subagent).
- When the main session coordinates by itself, it follows the same rules: decisions stay with the coordinator and the user, and mechanical work goes to `orca-steward`.
