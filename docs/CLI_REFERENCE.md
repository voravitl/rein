# `rein` CLI Command Reference Manual for AI Agents

Complete, deterministic command manual for AI agents (coordinators, workers, reviewers, advisors) operating with `rein`.

---

## Command Quick Matrix

| Command | Primary User | Purpose | When to Call | Exit Codes |
|---|---|---|---|---|
| `rein contract` | Coordinator | Create/inspect task boundary | Before starting worker | `0` = ok, `1` = error, `2` = invalid flag |
| `rein hooks install` | Coordinator | Install guard hooks in worktree | While setting up worker worktree | `0` = installed, `1` = failed |
| `rein hook` | AI Harness | Hook event inspector | Auto-invoked by Claude/Codex/AGY | `0` = allow, `1` = bad flag (deny via stderr) |
| `rein sandbox` | Coordinator | Write OS sandbox profile | Before worker starts | `0` = written, `1` = failed |
| `rein drift` | Coordinator / Worker | Verify scope, claims, commits | Before saying `worker_done` or merging | `0` = clean, `1` = DRIFT, `2` = cannot judge |
| `rein run` | Coordinator | Session coordinator guard | Managing batch lifecycle & audit | `0` = ok, `1` = COORDINATOR_DRIFT |
| `rein budget` | Coordinator | Token spend & pool limits | Periodically and before dispatch | `0` = ok, `1` = soft limit, `2` = hard cap |
| `rein task status` | Coordinator | Health check on running task | Diagnosing slow or frozen tasks | Status string: HEALTHY, BUSY, STUCK... |
| `rein ledger` | Coordinator | Model telemetry & costs | Recording worker done & spending | `0` = ok, `1` = failed |
| `rein providers` | Coordinator | Check active AI models/keys | Checking available fallback models | `0` = ok, `1` = no providers |
| `rein verdict` | Reviewer | Record/check multi-model reviews | After code review on an MR | `0` = pass, `1` = fail, `2` = arg error |
| `rein approve` | Human / Coordinator | Human approval merge gate | Gating merge approval | `0` = approved, `1` = refused |
| `rein advise` | Coordinator | Dispatch read-only advisor | Second opinion on claims/defects | `0` = ok, `1` = failed |
| `rein e2e` | Coder / Reviewer | Orca Browser E2E verification | Testing UI routes & compiling PDF | `0` = pass, `1` = test fail, `2` = error |
| `rein spec check` | Coordinator | Lint task spec & tier | Before spawning worker | `0` = pass, `1` = lint fail, `2` = error |
| `rein version` | Anyone | Print binary version | Verification / diagnostics | `0` = ok |

---

## 1. `rein contract` — Task Boundary Governance

Sets and inspects the write allowlist, scope items, and report path for a task.

```sh
# Create a new contract for a task
rein contract new --name <TASK_ID> \
  --run-dir <RUN_DIR> \
  --allow 'src/auth/**,tests/auth/**' \
  --scope S1,S2,S3 \
  [--deny 'src/auth/secret.go'] \
  [--profile ~/.config/rein/profiles/app.json] \
  [--issue 42] \
  [--worktree-root /path/to/worktrees] \
  [--report-path <RUN_DIR>/reports/<TASK_ID>.md] \
  [--writable f1,f2] \
  [--max-changed-lines 300]

# Show active contract rules
rein contract show <TASK_ID>

# Print path to contract JSON file
rein contract path <TASK_ID>
```

- **Rule:** Outside `--allow` globs, writes fail closed immediately.
- **Rule:** The worker cannot edit files outside its worktree or overwrite another task's contract.

---

## 2. `rein hook` & `rein hooks install` — PreToolUse Guard

### `rein hooks install`
Installs vendor hook configuration into the worker's worktree so tool calls are guarded:
```sh
rein hooks install <TASK_ID> [--vendors codex,agy,kiro,opencode,claude]
```

### `rein hook`
Invoked automatically on every tool call by the agent harness via stdin:
```sh
# Called by harness PreToolUse / Stop hooks
rein hook [--vendor claude|codex|agy|kiro|opencode]
```
- **Silent outside contracted worktrees:** Costs ~5ms, does nothing if no contract.
- **Inside contracted worktree:** Blocks `git push`, `gh/glab`, destructive git resets, writes outside allowlist, and touching live ports/databases.

---

## 3. `rein drift` — Quality Gate & Anti-Drift Verification

Verifies that the worker stayed on task, did not touch unpermitted files, provided proof for all scope items, and kept the repo clean.

```sh
rein drift <TASK_ID> \
  [--worktree <PATH>] \
  [--base origin/main] \
  [--claimed-files file1,file2] \
  [--expect-guard claude|codex|agy|kiro|opencode] \
  [--json]
```

- **Exit 0:** PASS — all files in allowlist, all scope items verified with evidence, zero uncommitted edits, no secrets.
- **Exit 1:** DRIFT — out-of-scope edits, fake completion claims, missing scope heading in report, `GUARD_INACTIVE`.
- **Exit 2:** ERROR — cannot evaluate (e.g. invalid arguments or missing files).

---

## 4. `rein run` — Coordinator Run Guard & Lifecycle

Ensures the coordinator does not implement code itself, tracks session liveness, and audits for unauthorized edits.

```sh
# Start a coordinator run (writes run marker)
rein run start [repo] --run <RUN_NAME> [--profile <PROFILE>] [--session <ID> --pid <PID>]

# Audit coordinator worktrees for unauthorized writes
rein run audit [repo] [--pinned sha,..] [--json]

# End coordinator run cleanly
rein run end [repo] [--pinned sha,..]

# Resume run after session disconnect
rein run resume [repo] [--session <ID> --pid <PID>]

# Allow exception for a task or commit (User-only; refused inside Claude Code)
rein run allow [repo] (--task <ID> | --commit <SHA>) --reason "<REASON>"

# Single-flight periodic tick (flock protected)
rein run tick [repo]
```

- **Rule:** `COORDINATOR_DRIFT` is raised if coordinator writes code outside `coordinator_writable` allowlist.

---

## 5. `rein budget` — Spend Ceiling & Timebox Enforcement

```sh
# Check current spend and pool caps
rein budget check [--task <TASK_ID>] [--run <RUN_NAME>]

# Request budget ceiling raise (User-only)
rein budget raise [--pool <POOL>] [--amount <N>] --reason "<REASON>"
```

- **Exit 0:** OK — spend within limits.
- **Exit 1:** Soft limit reached — review spend.
- **Exit 2:** Hard cap reached — cannot dispatch further tasks without approval.

---

## 6. `rein task status` — Worker Health Ladder

Inspects active worker heartbeat and detects stuck/frozen states:
```sh
rein task status <TASK_ID>
```
Outputs: `HEALTHY`, `BUSY`, `SLOW`, `STUCK`, `THRASHING`, `DEAD`, or `UNKNOWN`.

---

## 7. `rein ledger` — Multi-Model Cost & Telemetry

```sh
# Add settled task execution record
rein ledger add --task <TASK_ID> --type <TYPE> --worker <MODEL> --rounds <N> [--approved]

# Record advisor or reviewer call
rein ledger call --role <ROLE> --provider <PROVIDER> --model <MODEL> [--tokens <N>] [--cost-usd <X>]

# Generate spend & performance report
rein ledger report [--type <TYPE>] [--since <ISO_DATE>]

# Get optimal worker model suggestion based on history
rein ledger suggest [--min-n 3]
```

---

## 8. `rein providers` — Model Availability & Fallback Chains

Checks credentials and quota status across available LLM providers:
```sh
rein providers [--chain worker:backend] [--only claude,codex] [--skip-claude] [--timeout 90s] [--json]
```

---

## 9. `rein verdict` — Multi-Model Code Review Gate

Enforces peer review standards across MRs and PRs.

```sh
# 1. Generate standard Review Report markdown scaffold
rein verdict template --mr <N> --sha <HEAD_SHA> --base origin/main --reviewer <MODEL> --worker <MODEL>

# 2. Record review outcome
rein verdict record --mr <N> --sha <HEAD_SHA> --verdict <APPROVE|REQUEST_CHANGES> --reviewer <MODEL> --worker <MODEL>

# 3. Check if MR meets merge conditions (quorum, maker diversity, human approval)
rein verdict check --mr <N> --sha <HEAD_SHA> [--tier T1|T2|T3] [--json]
```

- **Exit 0:** PASS — all verdict criteria and human approval satisfied.
- **Exit 1:** FAIL — missing approvals, worker reviewed itself, or not enough independent model makers.

---

## 10. `rein approve` — Human Approval Merge Gate

```sh
# Render AskUserQuestion interactive modal payload
rein approve prompt --mr <N> [--sha <HEAD_SHA>]

# Direct human approval (User-only; refused inside Claude Code)
rein approve --mr <N> [--sha <HEAD_SHA>] --reason "<APPROVAL_REASON>"
```

---

## 11. `rein advise` — Read-Only Advisor Runner

Dispatches a secondary model to inspect claims, trace errors, or audit blast radius:
```sh
rein advise <ROLE> <DIR> <TASK_FILE> <OUT_FILE> [--provider codex|kiro] [--model <M>]
```
- Available roles: `code-reviewer`, `claim-auditor`, `blast-radius`, `tracer`.

---

## 12. `rein e2e` — Orca Browser E2E Testing & Board-PDF

Executes browser automation, verifies UI assertions, captures screenshots, and compiles executive Board-style PDF reports matching `/board-pdf`.

```sh
# Check Orca browser runtime availability
rein e2e check [--json]

# Quick 1-liner smoke test for a URL
rein e2e test http://localhost:3000 --out report/e2e

# Run declarative test spec (JSON or embedded in Markdown)
rein e2e run tests/e2e/flow.json --out report/e2e [--base-url <URL>] [--timeout 30] [--json]
```

- **Exit 0:** PASS — all steps succeeded, `report.pdf` compiled.
- **Exit 1:** FAIL — assertion failure or timeout (`failure-step-XX.png` captured).
- **Exit 2:** ERROR — runtime unreachable or invalid spec.

---

## 13. `rein spec check` — Spec Linter

Lints task specification against standing rules before dispatch:
```sh
rein spec check <SPEC_PATH> <CONTRACT_PATH> [--standing <PATH>] [--repo <PATH>]
```

- **Exit 0:** PASS.
- **Exit 1:** Lint failures.
- **Exit 2:** File/arg error.

---

## 14. `rein sandbox` — OS Sandbox Profile Generator

Writes OS sandbox profiles into the worker's worktree:
```sh
rein sandbox <TASK_NAME>
```
