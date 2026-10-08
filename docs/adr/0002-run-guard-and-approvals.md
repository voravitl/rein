# ADR 0002 — Run guard, budget, revision-bound approvals and standing orders

- **Status:** Accepted (owner decisions in a design interview, 2026-10-08), revised the same day after an
  adversarial review (four parallel checks: Claude Code hook docs, OS mechanics, git mechanics, red-team logic).
  Not yet implemented.
- **Context:** ROADMAP items B0–B4. rein guards workers, but the coordinator, spend, approvals and the user's
  rulings are still guarded by prose. claude-longrun (a sibling project) built and removed a similar coordinator
  write gate (its ADR 0002 → ADR 0004) and logged incidents that shape the choices below.

## Delivery: one provable slice at a time
The observed problem is one incident: a coordinator implemented work through its own subagents. Each slice ships,
runs in a real Orca batch, and only then does the next one start. This ADR keeps the full design so later slices do
not contradict earlier ones.

| Release | Content |
|---|---|
| 0.2.1 | Fix: worker guard silent inside a submodule / nested repo (see "Fix first") |
| 0.3 | B0 core: run marker, coordinator write deny, Agent rule, user-only authorization commands, `run audit` |
| 0.4 | B1 light: budget check, round cap, waiter-driven tick and stuck ladder |
| 0.5 | B2, only after its spike passes |
| later | B3, B4 |

## Fix first (0.2.1)
`toplevel()` stops at the first `.git`. Inside a worker worktree, a file in a submodule or nested clone
resolves to that inner root, `contract.Load(basename)` finds nothing, and the hook stays silent: an unguarded
write. Fix: keep walking up past nested `.git` roots; if an ancestor is a contracted worktree, judge with that
contract (the nested path is not in its allow globs, so it is denied). Test with a submodule and a nested clone.

## Lessons taken from claude-longrun

| longrun evidence | What went wrong | Choice here |
|---|---|---|
| ADR 0002 → 0004 (implementation gate removed) | The marker expired on a timer and stopped long runs. It was found via `--show-toplevel`, so linked worktrees could not see it. Tests deleted the real marker. The deny floor blocked two-line version bumps. Agents worked around it with interpreter writes. | Owner-liveness marker, never time based. The marker is found through the git common dir. Tests redirect its path with an env var. `coordinator_writable` lists small coordinator edits. Interpreter writes stay a known gap, and audit catches them. |
| Incident #60 (10 review rounds, ~69M tokens on a low-risk guard) | No round ceiling, and rigor was not matched to risk | `max_review_rounds` (default 2), early stop when a round adds no new evidence, deterministic tier |
| Incident #520 (backends "eligible" through an active quota) | Failures were never recorded | Persisted provider cooldowns, written on a failed dispatch |
| Silent model downgrade (5 of 10 "opus" reviews ran on sonnet) | The ledger trusted the requested model | The ledger stores the effective model |
| `bufio.Scanner` dropped events after one long line, so a gate failed open | Partial reads were treated as data | Gate files are read whole; any read error denies |
| goal.txt named 3 issues, the run grew to 22 tasks | Scope drift was invisible | Tasks outside the recorded scope ruling need approval (B4) |

## Corrections from the adversarial review
Each item records what the first draft assumed and what was found:
- **Self-authorization.** The coordinator could grant itself exceptions with `rein run allow`, or end a run. These
  commands are now user-only.
- **Approval text.** The model wrote the approval question, so it could mislead. rein now renders it.
- **Owner identity.** The draft used the hook's ppid as the owner. An intermediate shell makes that a dead pid,
  as shown by experiment.
- **Lock race.** The runlock-style stale-lock takeover races; the lock is now `flock`.
- **Hook-driven tick.** The draft relied on the hook firing during waits. The real coordinator waits in a
  background `ocloop2.py` for up to 55 minutes with no tool calls, so the hook never fires then, and the
  "deny long waits" rule would have blocked that waiter.
- **Bypasses.** MCP tools, `NotebookEdit`, git commands that rewrite the work tree, Bash in "read-only"
  subagents and linked worktrees all bypassed the write guard.
- **patch-id.** `git patch-id --stable` ignores whitespace, including Python indentation.
- **Audit by sha.** Matching commits by sha breaks after a rebase.
- **Merge commits.** They would have raised COORDINATOR_DRIFT on every merge train.
- **Decision trail.** `decisions.tsv` could store pasted secrets.

## Decisions

### B0. Coordinator guard (0.3)
1. **Run marker.**
   - `rein run start <repo> --run <name>` writes the marker under the repo's git common dir.
   - The marker records the main checkout root, because the CLI may exec git. With separate-git-dir that root
     is not derivable from the common dir.
   - The marker carries a schema version. A hook that does not know the version denies.
   - `run start` refuses when a run is already active.
   - Writes use temp file + rename. `REIN_RUN_DIR` redirects the path for tests.
2. **Marker lookup in the hook.** The hook never execs git. It reads `.git`, then `gitdir:` (relative to the
   `.git` file's directory), then `commondir`. A gitdir counts as a linked worktree only when it sits under
   `<common>/worktrees/`; submodules do not. When `GIT_DIR` or `GIT_COMMON_DIR` is set, the hook denies inside a
   run. Bare repos are out of scope. Outside a run the hook costs a few stats and small reads (measured
   microseconds).
3. **Owner.**
   - Primary key: `session_id` from the hook input.
   - Liveness: the Claude process, identified by pid + process start time (macOS `kern.proc.pid`, Linux
     `/proc/<pid>/stat`, Windows `GetProcessTimes`). Boot id is only an extra guard. A permission error on the
     liveness probe means alive.
   - Where the pid comes from: `rein run start` runs inside the coordinator's Bash tool, whose environment carries
     `CLAUDE_PID` and `CLAUDE_CODE_SESSION_ID` (observed 2026-10-08, not documented). Record those. Fall back to
     walking ancestors when they are absent, and verify in the first real session that the hook sees the same
     values.
   - SessionStart with source `clear` / `compact` / `resume` from the same live Claude process rebinds the
     session id.
   - No time expiry.
   - DEAD: the hook stays silent; SessionStart in that repo says a run is pending.
   - Unknown schema or corrupt marker: deny, with a reason that says how to clear it.
4. **What is guarded.** Every worktree of the repo that has no contract (main checkout and linked worktrees),
   in the owner session, while a run is active. Contracted worker worktrees keep their own contract rule.
5. **What the hook denies there.**
   - Writes outside `coordinator_writable` (profile allowlist, snapshotted into the marker; default `docs/**`,
     `**/*.md`, `.claude-plugin/plugin.json`, the changelog). Writes include:
     - `Edit`, `Write`, `NotebookEdit`;
     - Bash writes found by the worker parser, including git commands that change the work tree or index
       (`restore`, `checkout -- <f>`, `stash pop|apply`, `merge`, `pull`, `cherry-pick`, `reset --hard`).
   - Any tool not on the profile's `coordinator_tools` read-only allowlist. MCP and other unknown tools are
     denied by default; defaults cover the usual read, search, list, get, explore and ask tools.
   - An `Agent` call unless one of these holds:
     - its `agent_type` is on the profile's `readonly_agents` list; even then, every call of that subagent goes
       through the same write rules;
     - its prompt names `rein-task: <t>` and the user recorded `rein run allow --task <t>`.
   - Orca `worker-start` or `terminal send` aimed at an uncontracted worktree.
   - Bash in the owner session that runs a user-only command (rule 6).
6. **User-only commands.** These must be run by the user in their own terminal: `rein run allow`,
   `rein run end --abandon`, `rein budget raise`, `rein approve`. The hook matches the command text (including
   `env -u …` and variable indirection the parser resolves), and the CLI refuses when it detects it runs under
   Claude Code. `rein run start`, `rein run end` (clean) and `rein contract new` stay coordinator commands.
7. **Subagent binding.** Tool calls from a subagent carry `agent_id`/`agent_type` (Claude Code hook docs).
   - `PreToolUse(Agent)` records a pending (session, task).
   - `SubagentStart` binds the `agent_id` to it. It carries only `agent_id`/`agent_type` (no `tool_use_id`, no
     prompt) and cannot block, so FIFO pairing is the only option.
   - Two pending bindings at once: deny.
   - A bound subagent is judged by its task's contract.
8. **Audit.** `rein run audit` reports `COORDINATOR_DRIFT` for any non-merge commit in `start..main`
   (`git rev-list --no-merges`) that is none of:
   - reachable from a drift-pinned sha;
   - equal by per-commit `patch-id --verbatim` to a pinned commit;
   - for a squash, equal to a pinned combined id;
   - touching only `coordinator_writable` paths;
   - covered by a recorded exception.

   `push_merge_pinned.sh` runs it as a gate. `rein run end` exits non-zero on drift. `rein run end --abandon`
   (user-only) always closes the run and records why.
9. **Acceptance.**
   - A real-session proof that a hook deny holds under `bypassPermissions`.
   - Tests for: submodule and linked-worktree lookup, the `/clear` rebind, MCP default-deny, git work-tree
     writes, and user-only commands under `env -u`.

### B1. Budget, timebox and stuck workers (0.4)
1. **Enforcement.** The owner session's hook runs the budget check on spawn and review calls (profile-declared
   spawn commands, review `Agent` calls). `rein budget check` stays a CLI as well. A hard stop or the round cap is
   lifted only by `rein budget raise --reason` (user-only, logged).
2. **Counting.**
   - Spend counts from the marker's `started_at`. An exhausted state is scoped to its run.
   - Unknown costs are estimated per pool/model from the profile and flagged `approx`. USD is always labelled an
     estimate.
   - Ledger rows are appended at `drift` and `verdict record`. The ledger records the effective model.
   - Claude subagent tokens come from `PostToolUse(Agent)` telemetry.
   - Provider cooldowns persist (ok / quota / rate-limit / hang / empty). "Ledger unavailable" is distinct from
     "backend down".
3. **Tick.** No new daemon. The waiter the coordinator already runs (`ocloop2.py`, every minute) calls
   `rein run tick`.
   - The waiter is a background task tracked by Claude Code. If it dies, the coordinator is woken, and the skill
     already restarts it after every return.
   - The hook may also tick on coordinator tool calls.
   - Every tick is single-flight under `flock` (`LockFileEx` on Windows). The kernel drops the lock with the
     process, so there is no stale-lock takeover.
   - If the last tick is older than two minutes, the hook denies new spawns with the command that starts the
     waiter.
   - There is no Stop blocking and no wait-timeout rule.
4. **Stuck ladder.** `rein task status` reports HEALTHY / BUSY / SLOW / STUCK / THRASHING / DEAD / UNKNOWN.
   - Signals: the seen log (in-flight at Pre, cleared at Post, so a long command reads as BUSY with its own
     ceiling), commits, the report, and for vendors without post events CPU time, read in the tick, never in the
     hook.
   - STUCK needs both: no output or commit change, and flat CPU.
   - STUCK: nudge once through Orca, wait a grace period, then kill the terminal id recorded at spawn, only if
     the task's generation is unchanged.
   - After the kill: confirm the worker is dead, fold in zombie output (remote branches, open MRs), record the
     failure mode, respawn at most twice.
   - UNKNOWN never kills.
5. **Rounds.** `max_review_rounds` defaults to 2, with an early stop when a round adds no new failing evidence.
   Every reviewer and advisor call gets a hard wall-clock timeout.

### B2. Verdicts and approvals bound to a revision (0.5)
1. **Tier.** Deterministic (from longrun `tier-score`, no LLM).
   - Profile sensitivity patterns force T3. Cost never lowers a tier.
   - Before start, the tier is the higher of the sensitivity match on the contract's allow globs and the
     profile's default for the paths.
   - `rein drift` recomputes it from the real diff and can only raise it.
2. **Revision identity.** The combined id is
   `git diff --no-ext-diff --no-textconv --no-renames --no-color <merge-base>...HEAD | git patch-id --verbatim`.
   - An empty diff never matches.
   - After a rebase the id may match only if the files changed on main since the approved base do not
     intersect the MR's files.
   - The approval proves the delta, not the merged result. The re-gate of merged main stays the real control.
3. **Verdict check.** `rein verdict check --mr --sha` passes when all of these hold:
   - verdicts come from two model makers for T3, or one otherwise;
   - no reviewer normalizes to the worker's model (except escalated models like Claude Opus reviewing Sonnet/Haiku);
   - a human approval exists for that sha or an equal combined id.

   When passed, `verdict check` emits explicit **Approval & Review Evidence** (`[APPROVAL & REVIEW EVIDENCE]`),
   displaying the reviewers, models, makers (and escalated flags), timestamps, human approver reasons, and patch-IDs
   to ensure the user has visible proof of review and approval before merge.

   Approvals have no time expiry. Gate files are read whole and fail closed.
4. **Approval, primary path.** This is a human-attention gate, not forgery-proof.
   - `rein approve prompt --mr <n>` prints the exact `AskUserQuestion` input: rein's own wording with the MR
     title, sha, tier, verdict summary and a fixed "Approve" option.
   - `PreToolUse` denies an approval question in these cases:
     - it is not byte-equal to the current template output;
     - `answers` is pre-filled;
     - `agent_id` is present;
     - it does not come from the owner session.
   - `PostToolUse` (paired by `tool_use_id`, which both events carry) records the approval only when the answer
     is the fixed label.
   - Residual, documented: Claude Code lets any `PreToolUse` hook answer `AskUserQuestion` programmatically
     (`permissionDecision: allow` + `updatedInput.answers`). `PostToolUse` cannot tell such an answer from a
     click, so another installed hook that auto-answers questions would defeat this path. The model itself does
     not set `answers`, and rein denies it when present at its own `PreToolUse`.
5. **Approval, fallback path.** `rein approve` run by the user in their own terminal.
6. **Denies.** Every deny names what is missing and the exact next step. Every rejected-approval case gets a
   golden test, because a previous implementation rejected genuine human approvals.
7. **Gate before coding B2.** A real interactive spike captures payloads as golden fixtures for six cases:
   - (a) a real click;
   - (b) `answers` pre-filled by the model;
   - (c) Other or decline;
   - (d) multiSelect across MRs;
   - (e) `bypassPermissions`;
   - (f) a subagent asking;
   - (g) another `PreToolUse` hook answering through `updatedInput` (expected to pass unnoticed; this measures the
     residual and decides whether the button path is acceptable on a machine with such hooks).

   If any of (a)–(f) fails, B2 ships with the terminal path only and this ADR is amended.

### B3. Spec lint (later)
`rein spec check`, enforced on spawn, refuses to start a worker when:
- a scope id lacks a section or a red-check test line;
- "Not in scope" is missing;
- the pasted contract differs from `rein contract show`;
- the report path, gates or timebox are missing;
- the standing block's hash differs from `standing.md`.

It records the pre-start tier.

### B4. Standing orders and decision trail (later)
1. `AskUserQuestion` answers in the owner session during a run are appended to `<run>/decisions.tsv`
   (append-only): the question, the chosen option label and a timestamp. Free text is stored only after a
   secret scan, and redacted when the scan hits.
2. `<run>/standing.md` is written by the user only (the coordinator may propose text; a write to it from the
   owner session is denied). SessionStart (`resume`, `compact`) injects it, and `rein spec check` checks it.
3. A task outside the recorded scope ruling needs an approval before spawn. A re-read trail drops items already
   done.

## Rules
1. Gate state is trusted local state. These gates catch accidents and drift, not a determined malicious agent
   (longrun ADR 0003); interpreter writes stay a documented gap. Server-side branch protection stays the backstop.
   The guard is never described as a security boundary.
2. The hook stays silent outside a run's owner session and outside workers, never panics, and keeps single-digit
   ms. Anything slow (Orca calls, `ps`, ledger scans) runs in a tick or a CLI.
3. Windows: the detached tick, the process start-time probe and `LockFileEx` are unverified. No Windows claim
   until a real Windows run (ADR 0001 rule 2).
4. Git calls in rein and its scripts pass explicit flags (`--no-ext-diff --no-textconv --no-color --no-renames`)
   and never rely on user config or shell wrappers.
