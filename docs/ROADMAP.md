# Roadmap

Status as of v0.2.0. Each item names the problem it closes, the design, and how it is enforced. rein's rule: a
safety property lives in code that decides (hook, drift, script exit code), not in prose a model may skip.

## Done

| Version | What |
|---|---|
| 0.1.0 | Contract, Claude guard hook (mvdan/sh parser), OS sandbox writer, drift check, model ledger, provider fallback probes; plugin with self-installing binary; worktree-pipeline skill and orca-swarm / orca-steward agents; project packs |
| 0.2.0 | One contract guards codex, agy, kiro and opencode workers too (`rein hook --vendor`, `rein hooks install`); per-task seen log and `drift --expect-guard` (`GUARD_INACTIVE`); drift pins the HEAD sha it judged |

## Next, in order

Decided in a design interview on 2026-10-08 and revised after an adversarial review; the full design, the
acceptance tests and the claude-longrun lessons behind each choice are in
[ADR 0002](adr/0002-run-guard-and-approvals.md). ADR 0002 is authoritative where this summary is shorter.

Delivery, one provable slice at a time (each runs in a real Orca batch before the next starts):
**0.2.1** worker guard fix for submodules / nested repos → **0.3** B0 core → **0.4** B1 light → **0.5** B2 after
its spike → B3, B4.

### B0. Coordinator guard
**Problem.** rein guards workers, but the coordinator is guarded by prose only. Observed: a coordinator that was told
to split work into Orca workers implemented it through its own subagents instead, spending the wrong quota and
skipping the worker contract, guard and ledger. Observed again on 2026-10-08: asked to build PRD #1 with opencode
workers, the main session spawned `rein:orca-swarm` itself as a Claude subagent instead of driving Orca. Interim fix
(#3): the hook denies `rein:orca-swarm` / `rein:orca-steward` as `Agent` subagents in every session.

**Design.**
- `rein run start <repo> --run <name>` writes a run marker under the git common dir (found without exec'ing git);
  `rein run end`, `rein run resume`. The marker belongs to one session (session id, rebound on `/clear`; liveness
  of the Claude process by pid + start time) and never expires on a timer: a dead owner leaves the hook silent and SessionStart points at `rein run resume`; a corrupt
  marker denies. (claude-longrun removed a timer-expiring gate after it cut long runs.)
- While a run is active, the owner session's hook denies:
  - `Edit`/`Write`/`NotebookEdit`, Bash writes (the worker parser, including git commands that rewrite the work
    tree) and any tool outside the read-only `coordinator_tools` allowlist (MCP included), in any uncontracted
    worktree of the repo, outside the profile's `coordinator_writable` allowlist (default `docs/**`, `**/*.md`,
    plugin version, changelog);
  - user-only commands (`rein run allow`, `run end --abandon`, `budget raise`, `approve`): the user runs them in
    their own terminal;
  - the `Agent` tool with a writing subagent type unless the prompt names `rein-task: <t>` and
    `rein run allow --task <t> --reason "<text>"` was recorded. The deny reason names the default path:
    `rein contract new` → `orca worktree create` → `rein hooks install` → `worker-start`.
  - Subagent types on the profile's `readonly_agents` list and `advise.sh` stay allowed; their Bash still goes
    through the write rules.
- Subagents are told apart by the hook input's `agent_id`; `PreToolUse(Agent)` + `SubagentStart` bind an
  `agent_id` to its task, whose contract then judges every call (two pending bindings at once: deny).
- `rein run audit <run>` reports `COORDINATOR_DRIFT` (commits not from a contracted worktree, exceptions used,
  subagent token use versus Orca workers); `push_merge_pinned.sh` runs it as a gate.
- Acceptance includes a real-session proof that the deny holds under `bypassPermissions`.

### B1. Budget: cost and quality together
**Problem.** The ledger measures but nothing caps spend; USD shows `-` when only a blended price is known; Claude
subagent tokens are not recorded; review loops have no ceiling (one installer review took six rounds).

**Design.**
- Profile `budget`: per pool (Claude tokens, codex tokens, Kiro/agy credits, USD for API billing), per task and per
  run, `max_review_rounds` (default 2, early stop when a round adds no new failing evidence), `timebox_minutes`,
  `soft_ratio` (0.7), `quality_floor` (approval rate, false claims), per-pool cost estimates for unknown spend.
- Enforced by the coordinator hook on spawn and review calls (not by scripts remembering to call it); exit codes of
  `rein budget check`: 0 ok, 1 soft (no new tasks), 2 hard or round cap (ask the user). Spend counts from the
  marker's `started_at`; unknown costs are estimated and flagged `approx`; USD is always labelled an estimate.
- Ledger: rows appended automatically at `drift` / `verdict record`; effective model recorded (downgrades
  visible); Claude subagent tokens from `PostToolUse(Agent)`; persisted provider cooldowns; `ledger call` rejects
  `--model agent:model` when `--provider` is set; `ledger suggest` ranks cost per approved task above the quality
  floor and never below the task's tier.
- Stuck workers: `rein task status` (HEALTHY/BUSY/SLOW/STUCK/THRASHING/DEAD/UNKNOWN) → nudge → grace → kill by the
  terminal id and generation recorded at spawn → fold in zombie output → respawn at most twice; UNKNOWN never
  kills. No new daemon: the coordinator's existing waiter (`ocloop2.py`, every minute) calls `rein run tick`
  (single-flight under `flock`); a stale tick makes the hook deny new spawns until the waiter runs again.

### B2. Verdicts and approvals bound to a revision
**Problem.** The merge train merges the worktree HEAD for an approved MR number. A commit added after review is not
caught before merge, and an approval is just text the coordinator can write.

**Design.** Deterministic tier engine (sensitive paths from the profile force T3; cost never lowers a tier), computed
by `drift` from the real diff. `rein verdict record` / `rein verdict check --mr --sha HEAD` in
`push_merge_pinned.sh`: pass only with verdicts from two model makers for T3 (one otherwise), no reviewer equal to
the worker's model after normalisation, and a human approval for that sha or an equal combined `git patch-id --verbatim`
(three-dot diff with explicit flags; the approval proves the delta, the merged-main re-gate proves the result).
Approvals: an `Approve` button in an `AskUserQuestion` whose text rein renders (`rein approve prompt`; any other
text, pre-filled `answers` or a subagent asker are denied; recorded by `PostToolUse`), fallback `rein approve` in
the user's own terminal. A human-attention gate, not forgery-proof. No time
expiry. Gate files are read whole and fail closed. **Starts with a real-session spike of six cases; if any fails,
only the terminal path ships.**

### B3. Spec lint
`rein spec check <spec> <contract>`, enforced on spawn, refuses to start a worker when a scope id lacks a section or
a red-check test line, "Not in scope" is missing, the pasted contract block differs from `rein contract show`, the
report path, gates or timebox are missing, or the standing block differs from `standing.md`. Records the pre-start
tier.

### B4. Standing orders and decision trail
Every `AskUserQuestion` answered in the run's owner session is appended to `<run>/decisions.tsv` by the hook
(append-only; option labels, free text only after a secret scan). `<run>/standing.md` is written by the user only,
injected by SessionStart on resume and compaction and checked by spec lint. Tasks outside the recorded scope ruling
need approval before spawn. The trail is audited by a different vendor at the end of the run.

### Later
Trunk regression lane and feature maps in packs; cleanup holds for
locked worktrees, open MRs and unpushed commits (`git rev-list <b> --not --remotes`); assert the main
checkout's branch before a merge train; a per-profile cap on concurrent full test suites; golden hook-payload
fixtures; a pack table mapping each rule to what enforces it.

## Known gaps (documented, backstopped by drift)
Interpreter writes (`python -c`, REPLs, `script`, `tmux`), paths built at run time, opencode `execute`, kiro
`use_subagent`, codex workers started through Orca `worker-start` (no way to pass the hook flags: use the shell
route), kiro through `worker-start` (no `--model`, no `--agent rein`).

## Lessons that shaped this list
- Reviews from two vendors find different defects; cap the rounds and ask the user instead of looping.
- A quota outage mid-review exposed config bugs no test reached (a missing quota signal, a fallback reviewer from
  the same vendor as the worker). Vendor diversity is counted by the model's maker, not by the CLI.
- Writers can be cheap when a strong review gate follows; spend the strong model on review.
- Ideas adopted from pstack (MIT) are reimplemented; see `NOTICE.md` for the two adapted prompts.
