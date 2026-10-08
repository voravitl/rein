# Roadmap

Status as of v0.2.0. Each item names the problem it closes, the design, and how it is enforced. rein's rule: a
safety property lives in code that decides (hook, drift, script exit code), not in prose a model may skip.

## Done

| Version | What |
|---|---|
| 0.1.0 | Contract, Claude guard hook (mvdan/sh parser), OS sandbox writer, drift check, model ledger, provider fallback probes; plugin with self-installing binary; worktree-pipeline skill and orca-swarm / orca-steward agents; project packs |
| 0.2.0 | One contract guards codex, agy, kiro and opencode workers too (`rein hook --vendor`, `rein hooks install`); per-task seen log and `drift --expect-guard` (`GUARD_INACTIVE`); drift pins the HEAD sha it judged |

## Next, in order

### B0. Coordinator guard
**Problem.** rein guards workers, but the coordinator is guarded by prose only. Observed: a coordinator that was told
to split work into Orca workers implemented it through its own subagents instead, spending the wrong quota and
skipping the worker contract, guard and ledger.

**Design.**
- `rein run start <repo> --run <name>` writes a run marker (and `rein run end`).
- While a run is active, the Claude hook in the coordinator session (cwd inside that repo's main checkout) denies:
  - `Edit`/`Write` to source files of the repo (run dir, specs, memory and docs paths stay writable per profile);
  - the `Agent` tool with a writing subagent type (implementers), with the reason naming the Orca path:
    `rein contract new` → `orca worktree create` → `rein hooks install` → `worker-start`.
  - Read-only subagents (explore, review, critique) and `advise.sh` stay allowed.
- `rein run allow --reason "<text>"` grants a one-shot exception and records it.
- `rein run audit <run>` reports `COORDINATOR_DRIFT`: commits in the run that did not come from a contracted
  worktree, exceptions used, subagent token use versus Orca workers.
- Optional `UserPromptSubmit` reminder while a run is active (a nudge, not the enforcement).

### B1. Budget: cost and quality together
**Problem.** The ledger measures but nothing caps spend; USD shows `-` when only a blended price is known; Claude
subagent tokens are not recorded; review loops have no ceiling (one installer review took six rounds).

**Design.**
- Profile `budget`: per pool (Claude tokens, codex tokens, Kiro/agy credits, USD for API billing), per task and per
  run, `max_review_rounds` (default 2), `timebox_minutes`, `soft_ratio` (0.7), `quality_floor`
  (approval rate, false claims).
- `rein budget check --run R [--task T]`: exit 0 ok, 1 soft (stop starting new tasks, finish in-flight ones),
  2 hard or round cap reached (pause and ask the user). Called before `worker-start`, before each review round and
  by `advise.sh`.
- Ledger USD from input/output prices; `ledger call` rejects `--model agent:model` when `--provider` is set.
- `ledger suggest` ranks by cost per approved task among models above the quality floor.
- Stuck detection: no side effect (commit, report, heartbeat) past the timebox → stop, confirm stopped, respawn
  fresh; retry by failure mode (quota → `rein providers`; cap/oom → split; tool error → other vendor; unknown →
  once); two retries then abandon.

### B2. Verdicts and approvals bound to a revision
**Problem.** The merge train merges the worktree HEAD for an approved MR number. A commit added after review is not
caught before merge.

**Design.** `rein verdict record --task --mr --sha --patch-id --level --reviewer --report` and
`rein approve --mr --sha --quote`; `push_merge_pinned.sh` runs `rein verdict check --mr --sha HEAD` (pass only when
a verdict and an approval exist for that sha, or for an equal `git patch-id --stable` after a range-diff-identical
rebase). Evidence levels: live, test, typecheck-only, blocked, failed.

### B3. Spec lint
`rein spec check <spec> <contract>` refuses to start a worker when a scope id lacks a section or a red-check test
line, "Not in scope" is missing, the pasted contract block differs from `rein contract show`, or the report path,
gates or timebox are missing.

### B4. Standing orders and decision trail
`<run>/standing.md`: the user's rulings verbatim with source and date, pasted into every spec, fix round and
coordinator resume. `<run>/decisions.tsv` (append-only), audited by a different vendor at the end of the run.

### Later
Two-vendor review panel for DB/security tasks; trunk regression lane and feature maps in packs; cleanup holds for
locked worktrees and open MRs; a pack table mapping each rule to what enforces it.

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
