# OMC toolkit for the pipeline (OMC 5.6.2, verified 2026-10-08)

OMC adds cheap helpers around the Orca loop. Use each one at the step named below; none of them replaces a gate, a reviewer or the user's approval. Check once per run that `omc --version` (the npm CLI `oh-my-claude-sisyphus`) matches the installed OMC plugin version.

## Which helper at which step
| Step | Helper | Cost | What it fixes |
|---|---|---|---|
| 0 Set up / resume | `notepad_write_priority` (MCP) + durable run dir | free | lost run ids and lost design docs after compaction or a session end |
| 0 "ทำแบบรอบที่แล้ว" | `session_search` (MCP) + the repo's `MEMORY.md` | free | the previous run's routing, ids and incidents |
| 2 Spec facts | `codegraph_explore` (MCP, repo has `.codegraph/`) or `oh-my-claudecode:explore` (Haiku) | low | spec sentences that contradict the code (a worker built on a false spec claim once) |
| 2 Spec critique (large or risky tasks only) | `scripts/advise.sh critic <repo> <task> <out>` (OMC `critic` prompt, codex read-only sandbox) | codex quota, not Claude | missing scope, untestable criteria, ownership clashes before a worker spends hours |
| 3 Gate failure, cause not obvious after one look | `scripts/advise.sh tracer <worktree> <task> <out>` (OMC `tracer` prompt, read-only) | codex quota | fixture vs code vs load guesswork |
| 3 Gate, always | the no-fake-completion grep below | free | TODO/skip/only/stub that a green suite hides |
| 4 Disputed finding (worker and reviewer disagree) | `scripts/advise.sh code-reviewer <review worktree> <one finding + code refs> <out>` | one advisory call | stalemates; the result is advisory, the coordinator decides and names it in the MR |
| 1 Plan / 8 Settle | `rein ledger report / suggest / add` | free | model choice by evidence (review rounds, blockers, false claims, tokens) instead of habit |
| 9 End of run | `wiki_add` (MCP, category `session-log`) | free | incidents and recipes that would otherwise live only in scrollback |

## Commands
**Run pointer (survives compaction; 500 chars max):**
`notepad_write_priority`: `orca run <run-id> | dir <run dir> | repo <path> | tasks <name:state,...> | waiting on <who>`. Update it when a task changes state.
- The tool **replaces** the whole priority section. Run `notepad_read` first; if the user already has a priority note, keep it in front of the pointer (within 500 chars), or use `notepad_write_working` for the pointer instead. At the end of the run, restore the user's note.

**Durable run dir:** `~/.cache/worktree-pipeline/runs/<run-name>/{specs,reports,logs,designs}` (prefix each script call with `PIPELINE_LOGDIR=<run dir>/logs PIPELINE_PACK=<absolute pack dir>`; shell variables do not survive between Bash calls). Design docs and owner answers go here, never only in the session scratchpad.

**Spec facts:** for every sentence in the spec that states how the current code behaves, quote the code (`codegraph_explore "<symbols>"` with `projectPath` set to the repo, or an `explore` agent). A spec claim without a code reference is a question for the user, not a fact.

**Advisor calls — `scripts/advise.sh` (enforced read-only):**
```sh
scripts/advise.sh <role> <repo or worktree dir> <task prompt file> <output file> [model]
# e.g. spec critique, run from anywhere:
scripts/advise.sh critic ~/src/myapp <run>/specs/<task>-critique-task.md <run>/reports/<task>-critique.md
```
- **Providers:** `ADVISE_PROVIDER=codex` (default, `codex exec -s read-only`) or `kiro` (`kiro-cli --trust-tools=read,grep,glob`, default model `claude-sonnet-5.5`, Kiro credits; verified 2026-10-08: reads run, writes and shell rejected). Use kiro when codex quota is short or a second vendor is wanted; `ADVISE_MODEL` overrides the model.
- `<role>` is an OMC agent prompt (`critic`, `tracer`, `code-reviewer`, `security-reviewer`, `test-engineer`, `architect`, …) read from `$(npm root -g)/oh-my-claude-sisyphus/agents/` (the same prompts `omc ask --agent-prompt` uses). The script adds `templates/advisor-rules.md` plus `$PIPELINE_PACK/advisor-rules.md` (the project's protected resources) and runs the provider read-only.
- **Enforced** (probe 2026-10-08, codex-cli 0.160.0): file writes fail (`Operation not permitted`) and the docker socket is denied. **Network stays open**, so the advisor rules still forbid DB and HTTP calls.
- The task file says what to judge, e.g. for a spec critique: the spec, the files other running tasks own, and "missing scope, untestable criteria, claims the code contradicts; no style notes".
- **Why not `omc ask`:** it runs `codex exec --dangerously-bypass-approvals-and-sandbox` and `agy --dangerously-skip-permissions`; pasted rules are the only boundary there. Use `omc ask` only for questions that need no repo access at all.
- **`omc ask antigravity` hangs headless** (2026-10-08: `agy timed out after 300000ms`, `spawnSync agy ETIMEDOUT`, upstream antigravity-cli#76). agy stays usable as an Orca worker through the preamble route.
- Providers follow the user's standing vendor choices. Never `fable`. If a provider fails, report it; do not silently switch the vendor of a *review*.

**No-fake-completion grep (on the worker's diff; prints `file: line`):**
```sh
git -C <wt> diff origin/main...HEAD -U0 | awk '/^\+\+\+ /{f=substr($2,3); next}
  /^\+/ && /(TODO|FIXME|\.skip\(|\.only\(|\[Fact\(Skip|\[Ignore|throw new NotImplementedException|it\.todo)/ {print f": "substr($0,2); n++}
  END{if(!n) print "clean"}'
```
Any hit is a fix-round item unless the spec allows it.

**Gate-failure triage:** `scripts/advise.sh tracer <worktree> <task> <out>` with the failing names, the first error lines, the log path and the diff stat in the task file. Ask for ranked hypotheses (fixture / code / load / environment) with the one check that separates them. Run that check yourself through the gate scripts, then write the fix round.

**Script exit codes are the contract** (the scripts fail closed): `rebase_proof.sh` exits 2 on an invalid base, 3 on a conflict or dirty tree, 5 when fetch fails and 6 when a commit is not identical or range-diff fails or is empty; `push_merge_pinned.sh` exits 1 when the push fails, 4 when the MR never becomes mergeable at the gated head, 5 when the forge is unreachable, 7 unless GitLab reports the MR `merged`, and 8 when the merged head is not the gated one, the target is not `$PIPELINE_TARGET_BRANCH` (default `main`), or the merge commit is not on `origin/<target>`; `orca_cleanup.py` exits 1 on failed/incomplete list, unconfirmed stop/release, operator identity/output/ownership changes, missing PTY/absence proof or remaining sessions in a requested finished worktree; it never force-closes supervised resources. `cleanup_worktrees.py` exits nonzero for unknown/failed Git/Orca/archive/removal checks, with no raw fallback or forced branch delete; it requires an external archive and settled empty exact worktree identity. Both callers must honor these gates before downstream cleanup. The project pack's gate scripts document their own codes in `PACK.md`; a nonzero exit is a failed gate.

## Do not use (checked, rejected)
- **`omc ralph verify` as the gate.** Its baseline treats every output line as a signature (only durations, timestamps, hex ids and `/tmp` paths are normalized), so "Passed: 1039" vs "Passed: 1186" reads as a new failure, and paths differ per worktree. The project pack's gate scripts stay the gate.
- **OMC unattended modes in the coordinator session** (`ralph`, `autopilot`, `team`, `ultragoal`). An active mode turns on `git-guardrails`, which blocks `git push`, `git reset --hard`, `git clean -f`, `git branch -D` and `git checkout .` for the session, and arms the budget and stale-run hooks. Orca is the orchestrator here.
- **`omc team` as an Orca replacement.** It works, but the Orca-hang fallback that is proven in this pipeline is direct `codex exec` (cheatsheet).
