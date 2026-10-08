---
name: worktree-pipeline
description: "Run a batch of issues as parallel worker worktrees under Orca orchestration, kept on task by rein (task contract, guard hook, OS sandbox, drift check). One coordinator writes code-checked specs, picks the cheapest fitting model per task from ledger evidence (falling back across providers when a quota runs out), gates every result itself, loops read-only reviews by a strong model from another vendor until APPROVE, opens MRs, merges only user-approved MRs in dependency order with rebase + range-diff + re-gate + pinned --sha, tests merged main on an isolated stack, and closes finished terminals and worktrees. Project rules come from a local project pack. Use it when the user wants work split across agents in worktrees and merged back after review, says 'แตกงานเป็น worktree', 'แจกงานให้ codex claude antigravity', 'ทำแบบรอบที่แล้ว', 'คุมด้วย orchestration', 'merge ตามลำดับแล้วทดสอบ', 'ปิด worktree ที่จบแล้ว', or names this pipeline."
---

# Worktree pipeline (coordinator playbook)

You are the **coordinator**. Workers build; reviewers judge; you gate, decide routing and keep resources lean. You never self-approve and never merge, release or deploy without the user's word in chat.

**Paths in this playbook:**
- `<skill>` = `${CLAUDE_PLUGIN_ROOT}/skills/worktree-pipeline` (scripts in `<skill>/scripts`, templates in `<skill>/templates`).
- `rein` = `${CLAUDE_PLUGIN_DATA}/bin/rein` (installed by the plugin's SessionStart hook; `/rein:setup` can also link it onto `PATH`). Pass `REIN=${CLAUDE_PLUGIN_DATA}/bin/rein` to `advise.sh` when `rein` is not on `PATH`.
- `<profile>` = the project's rein profile, `~/.config/rein/profiles/<project>.json` (or `$REIN_PROFILE`); `<pack>` = its `pack` field.

Read before starting: `<skill>/references/orca-cheatsheet.md` (commands, quirks, fallbacks), `<skill>/references/omc-toolkit.md` (which OMC helper at which step, and which not to use), `<skill>/references/project-pack.md`, and the project's `<pack>/PACK.md` (its gates, safety rules, isolated stack, deploy notes, standing owner rules). No profile or pack for this project yet: stop and help the user write them first (`/rein:setup`, `examples/pack/`).

## 0. Set up a run
- **Preflight:** `rein version` runs (else the guard is off: run `/rein:setup`); `orca status --json` reports runtime `ready`; `omc --version` matches the OMC plugin.
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
| Work or review when Claude Code quota is low | **kiro** (`kiro-cli`, Kiro credits). Not an Orca agent: shell terminal + preamble (cheatsheet). **Always pass `--model`** | separate credit pool; kiro's `claude-*` models count as the Anthropic vendor for review diversity |

- **Quota fallback:** the order lives in `~/.config/rein/fallback-chain.json` (worker chains per task type, review chains per worker vendor, quota signals per CLI). On a quota signal (Claude "usage limit", codex 429/"usage limit", agy exit 3/`RESOURCE_EXHAUSTED`, kiro/opencode limit errors):
  1. run `rein providers --chain worker:<type>` (add `--skip-claude` when Claude quota is the one that ran out); it probes the chain with one-line prompts and names the first provider that is up;
  2. move only the affected task to that provider through its launch route (`orca` = `--agent`; `shell` = shell terminal + preamble, e.g. `opencode2 run -m <provider/model> "$(cat preamble)"` or `kiro-cli chat --no-interactive --trust-all-tools --model <model> "$(cat preamble)"`);
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
- **Worktree + sandbox before the worker (Claude workers):** create the worktree first (`orca worktree create --name <task> --repo id:<repo-id> --base-branch origin/main --setup skip --json`), run `rein sandbox <task>` (writes `.claude/settings.local.json`: OS sandbox on, no unsandboxed retries, writes limited to the worktree/temp/report, the profile's excluded commands and allowed domains; kept out of git status), then start the worker on it with `--worktree path:<abs path>`. Codex/agy/opencode/kiro workers do not read Claude settings; `rein drift` covers them (codex also has its own `-s workspace-write`).
- **Start:** `orca orchestration worker-start --run <run> --spec "$(cat spec)" --worktree <path:... | new-top-level> --name <task> --repo id:<repo-id> --base-branch origin/main --setup skip --agent <agent> --model <model> --json`.
- **Check the receipt:** `launch.effective` must show the agent and model you asked for. Then confirm that the worktree base is `origin/main`.
- **Questions:** answer them from the design with `orca orchestration reply --id <msg> --body ...`. Record the answers in `<run>/specs/<task>-answers.md` so that the reviewer gets them too. Ack the delivery.

## 3. Gate every `worker_done` yourself (never trust the report)
- **Drift check first (cheap, every vendor):** `rein drift <task> --claimed-files <worker_done filesModified>`. Exit 1 = drift (out-of-scope or never-edit files, uncommitted work, missing report or scope sections, false file claims, TODO/skip/stubs, secrets): send a fix round with the DRIFT lines and do not spend gate or review time yet. Exit 2 = cannot judge: find out why before going on. Denials the hook made are in `~/.cache/worktree-pipeline/logs/guard.log` (the worker's `$PIPELINE_LOGDIR` when its environment sets one).
- **Claim audit for cheap or pilot workers** (Haiku, opencode, kiro, free models): `<skill>/scripts/advise.sh claim-auditor <worktree> <task file: spec + report path> <out>` before the strong review, so false claims cost a cheap call instead of a review round.
- **Inspect the commits:** `git log`, `git status` and `diff --stat`.
- **Exit codes are the verdict:** the scripts fail closed (`references/omc-toolkit.md`, last section, and `<pack>/PACK.md`); a nonzero exit is a failed gate even when the summary lines look fine.
- **Run the project's gates** exactly as `<pack>/PACK.md` says (typically: full backend suite in a throwaway DB, frontend tests/type check/build, the guarded e2e of the task's spec). **Serialize full suites:** parallel suites under load give timeouts; rerun a load-looking failure alone before you call it a defect.
- **No fake completion:** run the diff grep from `references/omc-toolkit.md`. A hit is a fix-round item.
- **On failure:** find the root cause quickly (fixture vs code vs load). When it is not obvious after one look, run `<skill>/scripts/advise.sh tracer <worktree> <task file> <out>` with the failing names, first error lines and log path, and run the one check it says separates the hypotheses yourself. Then send a precise fix round (`templates/fix-round.md`). For agy or codex terminals that cannot be injected, use the preamble route in the cheatsheet.

## 4. Review loop
- **Where:** a separate review worktree (new-top-level from the branch, or a detached checkout), with a strong model from a different vendor. Use `templates/review.md` plus `templates/review-rules.md` and `<pack>/review-rules.md`.
- **Reviewer duties:** check claims against the code; re-run red checks; run the gates.
- **Verdict:** `APPROVE`, `APPROVE WITH NITS` or `REQUEST CHANGES`, with severities and a "Realistic: yes/no" line per finding. **Stopping rule:** APPROVE when what remains is theoretical or nits.
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
Clean up after every settled task, not only at the end:
- `python3 <skill>/scripts/orca_cleanup.py <run> [--stop ctx_…]`: releases settled workers, stops the listed finished-but-unsettled ones and closes their terminals.
- `python3 <skill>/scripts/cleanup_worktrees.py <main checkout> --keep <running tasks | none> --root <worktree_root> [--ignore <the profile's local_artifacts, repo-relative, e.g. frontend/node_modules>] [--prune-images <test image repo>]` (`--keep` is required: a worker that has not committed yet looks merged): removes clean worktrees whose head is in `origin/main` or whose commits are patch-equivalent, deletes their branches, and removes old test images that no running container uses.
- **Keep:** the worktrees of running tasks, and review worktrees until their MR merges. **Never** run `docker volume prune` or `docker system prune`.

## 9. Report to the user (in the user's language)
Short status per task: what was merged and pushed; the numbers; the open decisions; what is running and what is blocked; what was cleaned up. Include `rein ledger report --since <run start>`.

Update memory with the run state (ids, worktrees, MRs, decisions, incidents). Log the run to the OMC wiki (`wiki_add`, category `session-log`) and, when nothing is left running, remove the notepad run pointer and restore any priority note the user had.

## Agents
- **`rein:orca-swarm`** (Opus): the coordinator that runs this whole playbook for a batch. It returns a `DECISIONS NEEDED` block whenever the user must decide (owner decisions, merges, deploys); resume it with the answers.
- **`rein:orca-steward`** (Sonnet; Haiku for cleanup and rebase-regate): the mechanical jobs (rebase and proof, re-gates, pushes, pinned merges of MRs the user approved, the isolated stack test, cleanup). `orca-swarm` calls it as a subagent, or starts it as an Orca Sonnet worker when nesting is not possible.
- When the main session coordinates by itself, it follows the same rules: decisions stay with the coordinator and the user, and mechanical work goes to `orca-steward`.
