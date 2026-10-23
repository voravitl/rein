# `rein` CLI Command Reference Manual for AI Agents

Complete, deterministic command manual for AI agents (coordinators, workers, reviewers, advisors) operating with `rein`.

---

## Command Quick Matrix

| Command | Primary User | Purpose | When to Call | Exit Codes |
|---|---|---|---|---|
| `rein contract` | Coordinator | Create/inspect task boundary | Before starting worker | `0` = ok, `1` = error, `2` = invalid flag |
| `rein hooks install/uninstall` | Coordinator | Manage Rein guard hooks in one worktree | Worker setup/teardown | `0` = success, `2` = refused/failed |
| `rein hook` | AI Harness | Hook event inspector | Auto-invoked by Claude/Codex/AGY | `0` = allow, `1` = bad flag (deny via stderr) |
| `rein sandbox` | Coordinator | Write OS sandbox profile | Before worker starts | `0` = written, `1` = failed |
| `rein drift` | Coordinator / Worker | Verify scope, claims, commits | Before saying `worker_done` or merging | `0` = clean, `1` = DRIFT, `2` = cannot judge |
| `rein run` | Coordinator | Session coordinator guard | Managing batch lifecycle & audit | `0` = ok, `1` = COORDINATOR_DRIFT |
| `rein budget` | Coordinator | Token spend & pool limits | Periodically and before dispatch | `0` = ok, `1` = soft limit, `2` = hard cap |
| `rein task status` | Coordinator | Health check on running task | Diagnosing slow or frozen tasks | Status string: HEALTHY, BUSY, STUCK... |
| `rein ledger` | Coordinator | Model telemetry & costs | Recording worker done & spending | `0` = ok, `1` = failed |
| `rein providers` | Coordinator | Check active AI models/keys | Checking available fallback models | `0` = ok, `1` = no providers |
| `rein route auto` | Coordinator | Automatic task + model selection | Instead of choosing a chain and model by hand | `0` = receipt written, `1` = refused (code on stderr), `2` = bad arguments |
| `rein verdict` | Reviewer | Record/check multi-model reviews | After code review on an MR | `0` = pass, `1` = fail, `2` = arg error |
| `rein approve` | Human / Coordinator | Human approval merge gate | Gating merge approval | `0` = approved, `1` = refused |
| `rein advise` | Coordinator | Dispatch read-only advisor | Second opinion on claims/defects | `0` = ok, `1` = failed |
| `rein orca` | Coordinator | Native Orca worker lifecycle | Launch, wait, drain, close, tidy | `0` = ok, 1 = error, 3 = dirty, 4 = unique commit |
| `rein e2e` | Coder / Reviewer | Orca Browser E2E verification | Testing UI routes & compiling PDF | `0` = pass, `1` = test fail, `2` = error |
| `rein spec check` | Coordinator | Lint task spec & tier | Before spawning worker | `0` = pass, `1` = lint fail, `2` = error |
| `rein checkpoint` | Controller | Finalize, verify or restore a snapshot | Safe recovery boundary | `0` = success, `1` = refused/error, `2` = bad arguments |
| `rein hunter` | Investigator / Reviewer / Owner | Report, reproduce, verify, review and approve findings | Evidence-based bug workflow | `0` = recorded, `1` = refused/error, `2` = bad arguments |
| `rein supervisor` | Authorized controller | Validate and execute bounded plans | Explicit supervised runs | `0` = verified/waiting result, `1` = blocked/error, `2` = bad arguments |
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
rein hooks install <TASK_ID> [--vendors codex,agy,kiro,opencode,hermes,claude]
```
Without `--vendors`, the guard is installed for codex, agy, kiro and opencode. A Hermes worker needs
`--vendors hermes`: its plugin lives in the task's Hermes home, and installing it by default would hide the
worker's own `.hermes` files from git status and drift. Claude is opt-in for the same reason: its hook is the
plugin's global one.

### `rein hooks uninstall`

```sh
rein hooks uninstall <TASK_ID> [--vendors codex,agy,kiro,opencode,hermes,claude]
```

Removes only identifiable Rein task hooks and updates both contract copies. Default removal scans all six supported vendors and every shared hook event; modified executable commands or residual copies outside the owned layout return exit 2 and retain that vendor's metadata. Other owners' hooks, settings and sibling files remain. Conflicting or modified artifacts and symlink destinations fail closed; inspect the error instead of deleting the parent directory. Repeated removal is safe. Reinstall with `rein hooks install` and use the newly printed launch flags. Restart an already-running harness to unload cached hooks; removal on disk cannot revoke a running process's launch arguments.

Codex, OpenCode, Kiro and Hermes hooks are task-bound project artifacts, not global plugin registrations. Native plugin registration is a separate operation (`scripts/install-harness.sh`); failures return nonzero rather than printing success.

### `rein hook`
Invoked automatically on every tool call by the agent harness via stdin:
```sh
# Called by harness PreToolUse / Stop hooks
rein hook [--vendor claude|codex|agy|kiro|opencode|hermes]
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
  [--expect-guard claude|codex|agy|kiro|opencode|hermes] \
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

# Resume after disconnect only while the original owner process is still alive
rein run resume [repo] [--session <ID> --pid <PID>]

# Allow exception for a task or commit (User-only; refused inside Claude Code)
rein run allow [repo] (--task <ID> | --commit <SHA>) --reason "<REASON>"

# Single-flight periodic tick (flock protected)
rein run tick [repo]
```

`rein run resume` rebinds the session only for the same live owner process (same PID and process-start identity). A new process cannot take over by supplying the old PID. If the coordinator dies, the supervisor must verify the repository and recorded run identity, execute `rein run end <repo>`, then `rein run start <repo> --run <new-run> --session <new-session> --pid <new-owner-pid>`. Preserve the previous run's evidence and reconcile outstanding workers before redispatching; resume is not crash failover.

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
# Render a terminal-approval preview (interactive hook approvals are disabled)
rein approve prompt --mr <N> [--sha <HEAD_SHA>]

# Direct human approval (User-only; refused inside Claude Code)
rein approve --mr <N> [--sha <HEAD_SHA>] --reason "<APPROVAL_REASON>"
```

---

## 11. `rein advise` — Read-Only Advisor Runner

Dispatches a secondary model to inspect claims, trace errors, or audit blast radius:
```sh
rein advise --run <RUN> --task <CONTRACT> --provider codex|kiro|claude|hermes --model <M> <ROLE> <CONTRACT_WORKTREE> <TASK_FILE> <OUT_FILE>
```
- Available roles: `code-reviewer`, `claim-auditor`, `blast-radius`, `tracer`.
- `--provider hermes` requires patched Hermes `chat --read-only-root DIR`; an older CLI or inherited worker `HERMES_HOME=<WORKTREE>/.hermes` is refused before consuming the receipt or replacing prior output. Advice runs from a disposable directory with the selected authenticated profile and fixed, bounded `read_file`/literal `search_files` tools rooted at the worktree. Shell, writes, network tools, plugins and delegation are denied; symlinks and nonregular files are refused. The explicit provider-qualified model is pinned before startup normalization; provider/model changes, configured fallbacks, smart routing and auxiliary model calls are refused or disabled. Provider discovery uses bundled implementations only. `--effort` maps to Hermes `--reasoning`. This is a tool permission mode, not an OS sandbox: inference and runtime profile writes remain possible. Credentials are never copied. The coordinator collects stdout as the report; task-bound writing workers use `rein hooks install <TASK_ID> --vendors hermes` separately and require patched `completion_check` lifecycle support.

---

Merge approvals are currently terminal-only (ADR 0002 B2 fallback). `approve prompt` emits a preview marked `approval_mode: terminal_only` and a terminal command. AskUserQuestion responses do not create approval records: the hook integration has not yet established canonical request identity and single-use tool/session pairing with real fixtures. `verdict record`, `verdict check`, `approve` and `approve prompt` resolve refs such as `HEAD` to immutable full commit IDs. Legacy symbolic-ref records cannot authorize a merge; record a fresh review and approval.

The GitLab `push_merge_pinned.sh` helper runs `rein verdict check` and `rein run audit` before pushing a merge candidate and again immediately before merge. It defaults to tier T3; an owner-configured `PIPELINE_TIER=T1|T2|T3` can set the required tier. A missing binary, missing run, failed gate or changed local HEAD stops the merge. `--no-merge` remains a push-only operation. The helper never exempts the current HEAD from the run audit.

## 12. `rein e2e` — Orca Browser E2E Testing & Board-PDF

Executes browser automation, verifies UI assertions, captures screenshots, and writes required JSON/Markdown evidence. Failed evidence persistence returns exit 1 even when browser steps passed. Each text assertion reads a fresh snapshot. PDF rendering is optional by default and failures appear in `evidence_warnings`, Markdown, and CLI output. Use `--require-pdf` on `test`/`run`, or `"require_pdf": true` in the spec, to require PDF generation for success.

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

## Routed model launches

```sh
rein route prepare --task <contract> --run <run> --chain worker:<type> [--config F] [--skip-claude] [--timeout 90s]
rein route prepare --task <contract> --run <run> --chain review:<name> --worker-model <actual-model>
rein route check --task <contract> --run <run> --agent <agent> --model <model> [--phase worker|review] [--worktree <exact-path>]
rein route launch --task <contract> --run <run> -- codex exec --model <selected-model> <prompt>
rein route cooldown --provider <name> --reason <quota-evidence> [--until <RFC3339-reset>] [--config F]
rein route clear --provider <name> --reason <availability-evidence> [--config F]
rein route status
```

Install hooks before preparation. JSON decisions report configured chain, attempts, actual harness/model/maker/pool and fallback. For ordered chains (`route prepare`): probe eligible candidates in order and stop at the first available; no price ranking (see "Automatic task and model selection" below for the scored path). Prepare/check/launch fail with exit1 on unknown/stale/mismatched identity, inaccessible ledger/state or hard configured budget; argument errors exit2. Review phase requires a different known model maker; implementation requires worker phase. `route check` records intent but does not execute a provider; `route launch` validates, records, executes argv in the exact checkout, and records process exit (not task completion). Codex hooks and Kiro rein-agent are injected automatically. Native Orca launches support only harnesses whose flags can be forwarded. Actual provider receipts/hook evidence must be verified separately.

Receipts live outside checkouts in `<contract-index>/routes/decisions/`; quota state in `<contract-index>/routes/cooldowns.json`. Receipts expire after30minutes and bind contract/config/hook generation. Quota without knownreset stays blocked until explicit clear. State writes are serialized; an interrupted writer can leave `<contract-index>/routes/.lock`: verify that no routing writer is alive before removing only that empty lock, then retry. No automatic stale-lock takeover. For ordered chains usage unknown is not free; missing budget supplies no cap, and in-flight spend is not reserved (automatic receipts do reserve it). Direct invocations outside instrumented guards and opaque terminal sends are not universally intercepted.

## 15. Automatic task and model selection (`rein route auto`)

Design, rationale and the exact boundary of what is implemented: [`docs/ROUTING_SELECTION_DESIGN.md`](ROUTING_SELECTION_DESIGN.md). The coordinator **describes** the task; rein classifies it, refreshes every harness catalog, gates and scores the candidates on task-specific evidence, ranks them by the owner's declared objective, reserves the probe and the funding plan, probes the top candidate and writes a receipt. Launch the returned `agent`/`model`/`effort`/`launch` **unchanged** through `rein route launch`; a different chain, model or effort cannot be passed.

```sh
rein contract hash <task>                 # digest the task profile must carry; run it AFTER `rein hooks install` (installation changes the contract)
rein route auto --task <T> --run <R> --profile-file <task-profile.json> [--config F] [--timeout 90s] [--skip-claude] [--worker-model M] [--base REF]
rein route launch --task <T> --run <R> -- <argv>        # unchanged command; for an automatic receipt it also binds the effort, spends the receipt and takes the task lease
rein route check --task <T> --run <R> --agent A --model M --effort E --phase review --worktree P   # a review launch (advise.sh does this): binds the effort, spends the receipt
rein route settle --attempt <A>           # after the outcome and the charges are recorded: close the reservations and end the attempt
rein route holds                          # reservations still held (JSON)
rein route reconcile --hold <H> --reason <evidence> [--used pool=amount ...]   # USER ONLY (refused inside Claude Code and by the coordinator guard)
rein route calibrate --task <T> --run <R> --provider <P> --amount <N> [--calls 1] [--config F]
rein route calibrate --task <T> --run <R> --provider <P> --amount <N> --prepare --profile-file <task-profile.json> [--config F]
rein route discover [--config F] [--timeout 60s] [--dir D] [--json]            # refresh and print every harness inventory; exits 1 if any would be excluded
```

`route calibrate --prepare` creates one explicitly authorized **unscored local worker** execution receipt. It checks the classified worker chain, exact model, installed guard, quota and fixed calibration caps, reserves before execution, and does not probe. Launch through `route launch`; record real worker usage/outcome and settle the returned attempt. The receipt binds the contract, profile, policy, provider configuration and reservation, expires, and is single-use. This starts evidence collection; it does not qualify the model or waive independent review. Without `--prepare`, calibration remains a reservation-only operation.

**Task profile** (the only coordinator input; strict JSON, unknown fields refused, and `model`, `provider`, `agent`, `harness`, `chain`, `effort`, `launch`, `pool`, `maker`, `reviewer` are refused by name):

```json
{"contract_hash":"<rein contract hash>","phase":"worker","kind":"backend","risk_flags":["authorization"],
 "required_capabilities":["tests"],"expected_context_tokens":50000,
 "planned_files":["src/auth.go"],"rationale":[{"path":"src/auth.go","reason":"Changes authorization decisions"}]}
```

- `phase` is `worker` or `review`; `kind` is one of `backend`, `frontend`, `fullstack`, `docs`, `mechanical` (a review keeps the kind of the work it reviews). A review also needs `changed_files` and may list `exclude_makers` (makers whose verdict for this revision already exists; it only narrows). For a review rein also reads the checkout's real changes from git (`git diff <base>...HEAD` plus uncommitted and untracked files, base `--base`, default `origin/main`) and classifies on the union: a list that understates the change cannot lower the tier, and a review whose diff cannot be computed (or is empty) is `classification_required`. The union is stored as the profile of record.
- Rein validates the profile against the contract: the hash must match, every planned file must be allowed and not denied, every rationale must explain a planned file, `docs` may touch documentation files only (`.md .mdx .rst .adoc`; a script or a `requirements.txt` under `docs/` is not documentation), and `mechanical` needs `bounded_operation` (`operation` + `acceptance_checks`) and a `max_changed_lines` bound on the contract. The owner's profile (or the contract's copy) must declare `sensitive_paths`: with none, rein cannot tell sensitive code from ordinary code and refuses to choose (`classification_required`). Sensitive paths and the risk flags `authentication authorization crypto payments pii production-data secrets security` force **T3**, whatever else is claimed; claims can only raise the tier. T3 is always the `high-risk` candidate policy, never `mechanical`, and needs two reviewer makers. A risk flag outside the vocabulary (`authentication ... security` above and the neutral `api concurrency config dependency migration performance schema ui`) is refused for every kind: restate the risk in the vocabulary. A worker always needs `repository-edit`.
- Candidate policy by kind: `backend`→`backend`, `frontend`→`frontend`, `docs`→`docs`, `mechanical`→`mechanical`, `fullstack`→`ordinary`, T3→`high-risk`. Each names a `worker_chains` key whose MEMBERS are the approved candidates (their order is ignored); approved reviewers are the union of `review_chains`.

**Owner policy** (profile `selection`, from `REIN_PROFILE`, else the contract's copy; nothing is defaulted, a missing value is refused as `quality_policy_required` or `selection_policy_required`). The report says which one applied (`policy_source`: `owner` or `contract`). Set `REIN_PROFILE` in the environment the session starts from: the contract's copy is made by whoever ran `rein contract new --profile`, which is usually the coordinator, so only the owner's variable keeps the policy out of the coordinator's hands. See `examples/profile.example.json`:

| Field | Meaning |
|---|---|
| `objective` | `balanced` (lowest comparable cost per independently verified task among quality-qualified pairs) or `quality_first` |
| `accept_freshness` | catalog evidence classes accepted: `remote_verified`, `client_catalog`, `unknown` (no adapter produces `remote_verified` yet; supported Claude Code SDK catalogs are `client_catalog`) |
| `inventory_max_age_minutes`, `evaluation_suite` | catalog evidence lifetime; only evidence recorded under this suite version counts |
| `worker.min_approval_rate`, `max_false_claims`, `min_complete_attempts`, `max_evidence_age_days` | worker floor: the 95% Wilson lower bound of verified successes must reach `min_approval_rate` (0,1]; `max_false_claims` is an allowed COUNT; 100% outcome coverage is required. The legacy `budget.quality_floor` is reused when `selection.worker` has none, with no spending cap needed |
| `reviewer.min_defect_fixtures`, `min_clean_fixtures`, `min_recall`, `min_specificity`, `max_false_positive_rate`, `max_evidence_age_days` | separate reviewer floor on frozen red/clean fixtures; approval frequency is never reviewer quality |
| `cost_basis` | `monetary_marginal`, or `converted` with per-pool `per_unit`, `source`, `as_of`, `valid_days`; needed to rank candidates drawing on different pools |
| `calibration` | `authorization`, `max_calls`, `pool_caps`, `max_cash_usd`: the only way `route calibrate` may reserve spend for an unqualified model |
| `baseline_chain` | an explicitly accepted UNSCORED fallback, e.g. `worker:backend`; used when no candidate qualified, only for candidates blocked by missing evidence, and never after a scored candidate failed. It relaxes worker quality evidence only. Complete measured worker cost, a qualified funded reviewer plan for the tier, every other gate, the probe bound, reservation, refresh and receipt remain required; without them baseline refuses. Reported as `selection_mode=baseline_insufficient_evidence` |
| `allow_manual_chains` | by default, once a policy exists `rein route prepare` is refused (a hand-picked chain would bypass the scored ordering); `true` keeps ordered chains available |

**Provider config additions** (`fallback-chain.json`, all required for a provider to be an automatic candidate): `effort` (must be expressible on the harness command line, `--effort` for Claude/Antigravity, `-c model_reasoning_effort=` for Codex, `--model provider/model#variant` for OpenCode V2, or left empty), `capabilities`, `context_tokens`, `billing` (`mode` api|subscription|credits|free, `unit`, `pool` qualified by provider/account/pool/window, optional `price_key`), `probe_bound` (`calls`, `amount` in the billing unit; without a finite bound a candidate is never probed: `probe_bound_required`). An optional top-level `discovery` pins the executable per harness (`{"codex":{"command":["/path/codex"]}}`).

**Evidence flow** (all append-only ledger rows; units and pools are never merged; the same `--charge-id` recorded twice counts once):

```sh
rein ledger add --attempt <A> --rounds N --gates-passed|--gates-failed [--approved --review-sha SHA --reviewer agent:model --blockers 0 --highs 0 --false-claims 0 --drift 0 --resolved-model M]
rein ledger charge --attempt <A> --pool <P> (--unit usd|kiro_credits|agy_credits|subscription_percent|tokens --amount X | --price-key K --in N --out N [--cache-read N --cache-write N --context N]) [--component worker|review|repair|probe|fallback|shared|calibration] [--charge-id ID] [--model M] [--provider P]
rein ledger fixture --provider <P> --fixture <ID> --class defect|clean (--detected|--missed|--false-positive|--clean-ok) --adjudicated --type <kind> [--tier T1] [--suite S]
```

A review charge names the reviewer provider (`--provider`, its name in `fallback-chain.json`) and model: without them it would price no reviewer, and it is refused. A pool keeps the unit its provider bills in (`--config` or the default providers file; a mismatch is refused). Record a review's charge against the REVIEW's attempt (`route auto --phase review` returns it), a worker's against the worker's. For the first measured reviewer-cost sample, `route calibrate` returns a capped attempt; after that reviewer call, use the same `ledger charge --component review` command with its actual amount, then `route settle --attempt <A>`. Rein fills the reviewer effort/config from that outstanding hold, refuses amounts above its cap, and charges the measured amount once. Other calibration holds cannot be used as reviewer evidence.

`ledger add --attempt` takes worker, kind, effort, configuration, suite and tier from the launch record, so an outcome cannot disagree with its launch. `--price-key` prices an API call from `prices.json` entries that carry `input_per_mtok`, `output_per_mtok`, optional `cache_read_per_mtok`/`cache_write_per_mtok` and `tiers`, plus `source`, `as_of`, `valid_until`; expired rates are refused, never extended. The legacy blended `usd_per_mtok` is not used.

**What the JSON report contains**: `launch` (what to run), `classification`, `rounds` (one per decision: every inventory with status/freshness/version/scope, every candidate with its evidence and the first gate that excluded it, the probe and its outcome), `comparison` (what the ranking does and does not claim), `receipt` (decision, attempt and parent IDs; task-profile, inventory, pricing, quality and policy hashes; `expires_at`; the reservation). On a refusal the same report is printed with `error`/`error_code` and the exit code is `1`.

**Refusal codes**: `classification_required`, `profile_invalid`, `selection_policy_required`, `quality_policy_required`, `cost_basis_required`, `probe_bound_required`, `no_eligible_candidate`, `worker_identity_required`, `calibration_cap_exhausted`.

**Receipts and state**: receipt and its evidence artifacts (`profile.json`, `inventory.json`, `report.json`) live outside checkouts under `<contract-index>/routes/` (`decisions/`, `artifacts/<task>/`, `leases/`, `used/`, `prepare/`); reservations under `<contract-index>/holds/`. A receipt is refused at launch when the policy, task profile, catalog snapshot, prices, billing, quality evidence or contract changed, when its evidence expired (the earliest expiry of any evidence it uses, at most 30 minutes) or when its reservation was released: prepare again. **A receipt buys one execution**: `route launch` (worker) and `route check --phase review` spend it, a second use is refused, and a retry, a fallback or a repeat is a new decision with a new refresh, a new reservation and its own evidence; a launch that is refused (effort, lease, budget) does not spend it. A quota signal cools the shared pool until `rein route clear`. One live writer per checkout is enforced by a lease proven by process identity; a crashed holder is replaced only once launcher and provider process are both proven gone. A worker dispatched through Orca outlives the launcher, so launcher exit cannot prove worker termination: its task remains held and settlement refuses without independent termination proof (the exit row records no worker duration). A successful `worker-start` must return exact ready task/dispatch/worker identity; Rein persists that dispatch fence before a bounded `worker-show` check using the same executable, environment, checkout and explicit API endpoint selectors. The check requires matching run/task/dispatch/worker, a ready worker and exact live observation; an explicitly exited or foreign projection, or `addressable:false`, is refused. Structured workers may have `terminal:null`. Missing, malformed, oversized or timed-out observations fail the launch and record failure while retaining the fence; ready/live proof never authorizes remote settlement, retry or graph execution.

**Reservations**: one atomic hold per decision holds the probe bound (`probe`), the attempt's own funding (`funding`) and, for a worker, the capacity its plan sets aside for the reviews that follow (`review_reserve`). The review that follows reserves under its own attempt and takes that capacity over in the same transaction (`review_funding`), so it is neither counted twice nor lost. `route settle` charges the probe at its declared bound; funding is charged at the reserved bound unless the attempt has a measured, non-zero charge of its own kind in that pool (a zero or approximate charge proves nothing), in which case the rest is released; a plan whose receipt was never used releases its funding uncharged; the review capacity is released, never charged as the worker's. Preparing again discards a receipt that was never used and releases its hold the same way. A hold whose owner crashed is released only by the user with `route reconcile`, and only when the owner is proven gone AND the task has no writer left (an Orca-dispatched worker or a live launcher keeps it). Reservations are worst cases; they are recorded under one lock but are not enforced against spend that bypasses rein.

**Trust model**: rein does not trust the coordinator to choose, but it does record what the coordinator reports. The ledger is writable by the coordinator: outcome fields (`--approved`, `--reviewer`, `--review-sha`, `--gates-passed`), charges, fixtures and `changed_files`/`exclude_makers` are assertions that rein records and counts, not facts it verified. What it enforces is that unknown is never success or zero, that a model's record is only its own, and that the coordinator cannot name a model, weaken the owner's policy, skip the refresh, or reuse a receipt. The coordinator guard denies setting or unsetting `REIN_PROFILE`, `PIPELINE_LEDGER`, `PIPELINE_PRICES`, `PIPELINE_CONTRACTS`, `PIPELINE_FALLBACK` and `REIN_RUN_DIR` (and `env -i`) in a command and denies `--config` on `rein route`; it is a seatbelt for a misled coordinator, not a sandbox against a hostile one. Merge stays gated by `rein verdict check` and `rein approve`, which do not depend on this evidence.

**Not implemented**: `remote_verified` freshness, direct agy `/usage` refresh (the optional quota gate reads Orca metadata), automatic rate refresh, a general-purpose calibration runner, a context-conditioned cost forecast with uncertainty, a shared-overhead allocation rule (shared charges are reported once and excluded), and pinning of the owner's policy at `rein run start`. See the design document's "Implementation status".

Untracked embedded Git repositories are refused during automatic review classification. Track them as submodules or move them outside the reviewed checkout; Rein does not classify only the directory name while omitting its nested sensitive paths.

---

## 16. `rein orca` — Native Orca Multi-Agent Orchestration

Governs the complete Orca worker lifecycle natively in Go without external scripts. Incorporates operational safety gates learned from production runs:

```sh
# Execute deterministic Orca tool call (JSON in/out, for AI agents and MCP)
rein orca call <status|launch|wait|drain|close|tidy|send|read|terminals|worktrees> [flags]
rein orca call '{"action": "status"}'
rein orca call --input request.json
echo '{"action": "status"}' | rein orca call --stdin

# Emit official JSON Schema for function-calling & MCP integration
rein orca schema
rein orca call --schema

# Launch an agent worker inside Orca on a clean worktree
rein orca launch <name> <base-branch> <spec-file> <kiro|agy|claude|codex> [--model M] [--effort E] [--run R] [--coord H] [--repo ID] [--repo-dir D]

# Wait in background for worker_done, escalation, or question
rein orca wait [--terminal <coord>] [--timeout-ms 600000] [--rounds 60] [--json]

# Drain pending deliveries and acknowledge them cleanly in loop
rein orca drain [--terminal <coord>] [--no-ack] [--json]

# Interactive terminal control
rein orca call send --terminal <handle> --text "go test ./..." --enter
rein orca call read --terminal <handle> --limit 50

# Close finished worker (enforces clean tree and no unanchored unique commits)
rein orca close --worktree <path> [--terminal <handle>] [--dispatch <id>] [--check-only] [--json]
rein orca call close --worktree <path> --terminal <worker-handle> [--dispatch <id>] [--check-only]

# Audit Orca sessions (shell candidates remain report-only, including --apply)
rein orca tidy [--apply] [--workspaces <dir>] [--coord-wt <name>] [--json]
```

### Safety Rules & Operational Gates
1. **Never guess coordinator handle**: Orchestration calls require explicit `--coord` (`ORCA_COORD`).
2. **Preserve unproven terminals**: Launch does not prune other tabs. Tidy reports path-title shells as `shell_candidate`; even `--apply` preserves them because titles do not prove idle state or ownership.
3. **Dirty tree protection**: `rein orca close` exits `3` if the worktree has uncommitted files.
4. **Unique commit retention**: `rein orca close` exits `4` if HEAD is not reachable from another ref (e.g. `keep/<name>`).
5. **Bounded drain**: Drain summarizes messages and ACKs preceding deliveries. At 25 deliveries it returns a partial-drain error with the last delivery unacknowledged; retry rather than claiming the queue is empty.
6. **Deterministic Tool Interface**: `rein orca call` guarantees structured JSON responses (`{"ok": true/false, "action": "...", "result": ...}`) for cross-harness autonomous agents.

`rein orca call close`, `send`, and `read` require an explicit worker `--terminal`; they refuse the handle in `ORCA_COORD`. The same checks apply to inline JSON, `--input`, and `--stdin`. Wait and drain may still use `ORCA_COORD` when no terminal is supplied. `rein orca call` without arguments prints usage and exits 2 without reading piped input; JSON stdin is consumed only with `--stdin`.

Call exit codes: **0** success, **2** invalid request or protected/missing target, **3** dirty-worktree refusal, **4** unique-commit refusal, **1** runtime/API failure (including Git safety-check failures). Safety checks fail closed before release, terminal close, or worktree removal. A launch failure includes the created worktree/terminal/task/dispatch identifiers; inspect those resources before retrying because launch does not automatically delete them.

Close honors `worker-release`: `retained`, `release_pending`, unknown/error receipts, or an unproven/nonempty terminal inventory block worktree removal. `released` and `already_released` avoid a second raw terminal close. The direct close command also protects `ORCA_COORD`; removal cancellation or timeout is an error.

Claude/Codex launches use the authoritative `worker-start` task/dispatch receipt rather than creating a preliminary task. A ready worker with an unconfirmed terminal surface returns an error containing its identifiers; inspect it before retrying. Launch flags may precede or follow positional operands.

Drain returns an error on a failed initial check. Later check failures return the partial result with an error, so callers must not report a complete drain. Without ACK, drain reads one delivery without advancing the queue. Wait detects signals only in `result.messages[].type`; logs and metadata do not constitute completion.

`rein orca call status` retains daemon status fields and adds a `terminal` envelope from `orca terminal show` when an explicit terminal or `ORCA_COORD` is available. With neither, it reports daemon-wide status.


## Checkpoint, report-only hunter and bounded supervisor

```sh
rein checkpoint finalize --task T --attempt A --run R --worktree /absolute/source --base main --contract-hash HASH [--untracked path1,path2]
rein checkpoint verify --task T --attempt A
rein checkpoint restore --task T --attempt A --dest /absolute/new-directory
rein hunter report --input finding.json
rein hunter get --id H-0001
rein hunter list
rein hunter triage --id H-0001 --controller CONTROLLER
rein hunter authorize-fix --id H-0001 --owner OWNER --task FIX_TASK
rein hunter start-fix --id H-0001 --worker WORKER --task FIX_TASK
rein hunter scan-report --task T --baseline FULL_SHA --covered-json '["internal/hunter/**"]' --skipped-json '["live provider calls"]'
rein hunter reproduce --id H-0001 --repo /absolute/repo --attempt repro-1 --timeout 5m --expect-exit 1 -- go test ./...
rein hunter verify --id H-0001 --repo /absolute/repo --attempt verify-1 --fixed FULL_SHA --red-attempt repro-1 --test-paths-json '["path_test.go"]' -- go test ./...
rein hunter review --id H-0001 --repo /absolute/repo --attempt verify-1 --verification-digest SHA256 --worker WORKER --reviewer REVIEWER --report /absolute/review.md
rein hunter approve --id H-0001 --repo /absolute/repo --verification-digest SHA256 --review-digest REVIEW_SHA256 --owner OWNER --reason "accept reviewed fix"
rein supervisor validate --plan approved-plan.json
rein run start /absolute/repo --run R --session S --pid PID --supervisor-plan approved-plan.json
rein supervisor run --plan approved-plan.json
rein supervisor invocation --plan approved-plan.json --invocation INVOCATION_ID
```

Checkpoint restore uses embedded Git objects and captured bytes; the source can be absent. Restore requires an existing destination parent. Existing destinations, mixed staged/unstaged edits to the same path, tracked deletions/renames/conflicts/submodules, hidden-index flags and unrepresentable checkout conversions are refused. It restores only the supported state into a fresh directory; it does not rewrite contracts, run markers, receipts or source evidence. Each checkpoint stores reachable Git history, so large repositories can produce large snapshots.

`triage`, `authorize-fix` and `start-fix` move a finding through the controller, owner and worker gates. Each step records the acting identity and the separately approved fix task; the lifecycle still cannot skip reproduction, independent review or owner approval. A controller cannot authorize its own fix, and a worker cannot start a task that the owner did not approve.

Hunter reproduction requires a full baseline SHA matching the source HEAD, a unique attempt and explicit expected nonzero exit. It runs argv without a shell in a disposable clone and records actual exit/output hashes before cleanup. Setup failures do not promote evidence. Unix timeout kills the execution process group; Windows execution is refused pending descendant containment. Each clone receives a distinct test-only contract allowing `hunter-tests/**` and `evidence/**`. Lasting changes to source, Git metadata, modes, symlinks or contract evidence produce `scope-violation` and cannot promote a finding. Clone integrity checking is not an OS sandbox: trusted commands retain normal user access, and writes restored before inspection or outside the clone are outside this proof.

`hunter verify` checks the recorded red evidence hash, replays the same argv on the baseline, then runs that argv and the requested suite on the exact fixed revision. Explicit protected test files must retain their baseline bytes and modes; source HEAD is checked again after execution. Evidence uses a unique attempt and persists even when runtime checks fail. A successful result is `WAITING_INDEPENDENT_REVIEW`; it does not close the finding or grant reviewer/owner approval. Caller-written red/green strings cannot close findings. `hunter review` binds the exact runtime verification digest, unchanged fixed revision, independent worker/reviewer identities and a hashed review report, then waits for owner approval. `hunter approve` rechecks that evidence and requires the owner's terminal plus typed finding/review digest confirmation before `VERIFIED_FIXED`. Identities remain controller assertions under the same-user trust model. `scan-report` records explicit covered/skipped scope and known matching findings; absence of confirmed findings is `NO_CONFIRMED_FINDINGS`, never `BUG_FREE`.

For schema-2 graphs, the optional `REIN_SUPERVISOR_INVOCATION` value is a fresh 64-character lowercase hex ID for telemetry correlation. The existing controller writes invocation start and normal graph return to its journal. `supervisor invocation` reads that exact ID without launching work or acquiring controller authority. Missing, malformed, reused or mismatched identities are refused. `graph_returned` and its intended CLI exit value do not prove OS process exit, descendant termination, settlement or permission to cancel/retry. Existing task reconciliation requirements remain unchanged.

Supervisor plan JSON has exactly these fields (IDs below are placeholders for recorded routing evidence):

```json
{"schema":1,"run":"R","repo":"/absolute/repo","terminal":"coordinator-handle","source":"orca","version":"1","max_checks":20,"timeout_ms":60000,"poll_interval_ms":1000,"pools":[],"tasks":[{"task":"T","attempt":"A","current_attempt":"A","orca_task":"recorded-task-id","dispatch":"recorded-dispatch-id"}]}
```

Plan validation rejects unknown/duplicate fields and unbounded budgets. Start checks run/repository identity and binds the exact file bytes to a new schema-2 marker. Existing runs cannot be converted; `run resume` refuses supervised ownership. A supervisor holds exclusive controller authority and rechecks marker, contract, current receipt, consumed attempt and exact dispatched lease before Orca checks/ACKs. Coordinator death does not grant or revoke that separate authority. Legacy launch/wait/drain/send/close paths cooperate with the same lock and refuse schema 2; arbitrary external Orca invocations are outside this boundary.

Schema 1 remains a bounded **monitor**. It does not launch workers or settle/release remote leases. Missing dispatch evidence blocks before polling. Mixed/unbound/noise batches remain unACKed; ambiguous crash windows park for reconciliation. Reported completion still needs worker-gone/accounting proof.

Schema 2 adds bounded task graph execution for explicitly approved **local, non-detaching shell providers**. Each task supplies `contract_hash`, `config_path` (clean absolute path), `config_digest` (SHA256 of exact configuration bytes), embedded routing `profile`, `base`, `max_attempts` (1..3), provider-keyed `launches` argv arrays, and `verify` argv arrays. Optional `depends_on` and `allowed_untracked` declare dependencies and checkpoint scope. Set `process_policy` to `non_detaching`; the CLI refuses missing/other policies and remote Orca launches before preparation. Graph tasks do not prebind attempts or dispatch IDs. Distinct tasks require distinct worktrees in the production adapter. CLI preflight also compares the current contract hash and exact config-byte digest with the plan, and requires the embedded profile to carry that same contract hash. A valid plan does not establish provider authentication, quota, routing eligibility or controller authority; those gates remain enforced at runtime.

The plan's `pools` must cover the canonical quota pool of every authorized launch provider (billing pools remain separately governed by routing/budget); missing coverage is refused before preparation. Launch argv may substitute whole-token `{model}`, `{effort}`, `{run}` and `{worktree}` placeholders. The exact composite token `{model}#{effort}` is also supported for OpenCode V2; it requires a nonempty `provider/model` and variant without whitespace, controls, braces or `#`. Other partial substitutions are refused. `PrepareAuto` selects a fresh receipt; only that provider's approved argv runs, with existing model/effort/hook gates. Launch also rechecks the exact expected attempt/decision/hold before consuming its receipt. Local completion requires the recorded process and its dedicated process group to be gone, followed by checkpoint and conservative reservation settlement. Objective verification checks the captured worktree before and after each command; successful task completion includes durable checkpoint and verification digests. Restart revalidates completed evidence before reusing it. All completed tasks still return `WAITING_HUMAN` for independent review and approval.

The non-detaching policy is a cooperative execution contract, not OS containment: a process that escapes its group is outside that proof. Unknown local registration/liveness, unsupported remote termination, missing evidence and unresolved side effects preserve fences or park for reconciliation. Configured literal quota signals are watched while the provider is alive. Recovery records stop intent, stops/reaps the owned process group and requires gone/checkpoint/accounting proof before cooldown and a fresh receipt within the attempt bound. Retries revalidate checkpoint, execution and handoff hashes before prepare and launch. The production graph adapter injects current run/task/attempt/worktree, contract hash, allow/deny paths, verification argv and a concise Thai communication policy into the single approved literal prompt. The original prompt remains verbatim. The continuation receipt now preserves the first provider’s original task prompt, source provider/harness and contract hash across subsequent provider changes; a fallback argv prompt cannot replace the objective. Old receipts without these fields require reconciliation. On retry, the existing continuation receipt content is included after checkpoint/execution/receipt identity revalidation; `REIN_HANDOFF_PATH` and `REIN_HANDOFF_SHA256` remain available. Caller-supplied handoff environment variables never establish continuation. Only the resulting input digest is recorded in launch notes, never the prompt. The digest is also persisted in execution evidence; the private continuation receipt contains the objective. See [cross-provider handoff](CROSS_PROVIDER_HANDOFF.md) for execution and validation scope.

Supported literal-input forms are `claude -p PROMPT --model MODEL`, `codex exec -m MODEL PROMPT`, `agy --print PROMPT --model MODEL` (or `--prompt PROMPT`), and `opencode2 run --standalone -m MODEL PROMPT` (also `opencode`). For supervised `opencode`/`opencode2` launches, Rein validates this literal form, removes the prompt positional argument and its optional terminal `--`, and sends the exact enriched prompt bytes through stdin because OpenCode V2 adds literal quotes around positional prompts containing spaces. Legacy route launches retain their existing argv/stdin behavior. Model/effort/directory values are preserved; unknown options, streaming input, native resume and multiple prompts fail before provider preparation. Direct OpenCode V2 launches (`opencode` and `opencode2`) bind the model before `#` to the prepared model and the case-sensitive suffix to the prepared effort, forwarding the original selector unchanged. Empty/duplicate suffixes and unsupported `--variant`, `--effort` or `--config` flags are refused before receipt consumption; plain models retain their existing behavior. Other harnesses and Orca retain their existing identity rules. This follows the [OpenCode v2.0.32 selector parser](https://github.com/anomalyco/opencode/blob/843a6c514e606b436a7fe713422ace0a93ad3d61/packages/schema/src/model.ts#L27-L40); selector validation does not establish that a live model offers that variant, and inventories listing only model IDs do not prove variant availability. This integration does not restore native conversation state or prove real provider exhaustion, remote descendant termination, automatic restoration, live model comprehension or a live pilot.

The legacy waiter uses `rein run controller-exec REPO --run R -- <check-options>` (or the executable in `REIN`) for every bounded check/ACK. The wrapper holds a shared legacy lock through Orca and refuses supervised runs. Missing Rein or refusal stops the waiter without falling back to raw Orca. `run controller-check` is a refusal diagnostic, never a reusable authorization token.

Orca structured launch uses exact `worker-show --dispatch` identity (task/run/assignee/worker), not terminal existence. `worker_handle` is emitted for supervised workers; `terminal_handle` is absent for structured workers without a PTY. Close accepts dispatch identity without a terminal; send/read still require a terminal. These launch/close compatibility checks do not enable Orca graph recovery. Installed Orca 1.4.224 lacks a retained process-tree termination proof after session eviction; unsupported recovery remains fenced.


## Crux provider configuration

```sh
rein config init --dir ./crux-config
rein config init --dir ./another-config --model codex=<verified-model-id>
rein config check --dir ./crux-config
rein config check --dir ./crux-config --quota orca
rein config check --dir ./crux-config --quota native --format text
```

`init` requires a new directory and writes private `fallback-chain.json` and `profile.json` for Claude Code, Codex, Antigravity and OpenCode2. It preserves existing directories and global settings. Unspecified models remain empty; no pricing, probe cost, review independence or availability is guessed. The generated config explicitly disables model calls. `check` reads configuration and checks CLI presence without launching a model. `valid` means the files can be read; it does not mean routing is ready.

`--quota orca` explicitly reads `orca account list --json` with a bounded response and timeout. Only normalized quota data is returned; account identities and credentials are excluded. Remaining percentages are `100 - usedPercent`, kept per window and model bucket. Missing, unavailable, future-dated or older-than-five-minute measurements never become a numeric remaining allowance. OpenCode Go subscription usage is not treated as OpenCode2 free-model quota. Lookup failure is reported as `quota_error` while configuration inspection still succeeds.

`--quota native` reads Codex directly through the existing version-checked app-server discovery adapter, including `account/rateLimits/read`, with a 15-second timeout and no user prompt. Only Codex is queried; Claude Code, Antigravity and OpenCode2 remain unsupported for this direct inspection source. Native limit IDs, window durations and reset times are retained without mapping them onto Orca window names. The observation time means this local read occurred then; it does not establish when the upstream limit was measured. Account evidence remains unverified. This is display-only: it neither refreshes Orca nor substitutes for the configured Orca routing veto, account binding, spending caps or a model call authorization.

Quota readings do not authorize spending, bind a model to an account, or replace Rein reservations. The optional conservative gate below excludes candidates that fail the configured windows. Before enabling `model_calls_enabled`, configure verified models, bounded probes, positive budget caps and independent review chains, then use the files through routing `--config` and owner-profile environment variable `REIN_PROFILE`. Existing configurations without the new flag retain their prior behavior.

### Conservative Orca quota gate

Newly generated Crux configs set `require_orca_quota: true` in addition to `model_calls_enabled: false`. Before enabling calls, set each provider's `quota_windows` to exact names returned by `config check --quota orca`; `quota_reserve_percent` optionally keeps a percentage margin (0 <= value < 100). No model/group mapping is guessed. Example provider fields (merge into a verified provider; not a callable config):

```json
{"quota_windows": ["weekly"], "quota_reserve_percent": 5}
```

With the gate enabled, direct/parallel/retry probes and launch validation read Orca again. Automatic routing checks before reserving probe capacity, including when a custom probe function is injected. Exhausted, unknown, stale, malformed, missing or duplicate mapped windows refuse the candidate without a model call. All observed shared session/weekly/monthly windows constrain Claude and Codex, so a healthy session cannot hide an exhausted week. Antigravity requires explicit model-group windows. OpenCode2 free-model quota remains unknown and therefore cannot pass this gate. Unbound remote Orca environment/pairing overrides refuse execution; inspection remains available.

This gate is a veto, not account authorization or a monetary spending cap. It does not prove the launch uses Orca's observed account, predict task consumption, reserve provider-side percentages, or account for concurrent external clients. Rein's existing budget reservations and owner account/launch binding remain necessary. Existing configurations without `require_orca_quota` keep their prior behavior.

### Account evidence in quota inspection

Every quota snapshot includes `account_evidence`. `unverified` is the default. For an explicit Orca system-default Codex OAuth account on the host, Rein compares `providerAccountId` with only `tokens.account_id` in the native `CODEX_HOME/auth.json` (or default user directory). This is a bounded read of a regular JSON file; IDs, tokens and credential-file paths are not returned. API-key/base-URL overrides, remote Orca overrides, managed-account selections, WSL, missing/malformed/oversized credentials and unsupported providers remain `unverified`.

`credential_metadata_match` means those two metadata values match. It does **not** prove the quota snapshot was collected for that identity or that a native worker uses that file/configuration. `credential_metadata_mismatch` makes the enabled quota gate refuse probes and launch validation; stale or unused credential metadata can therefore require investigation before a legitimate launch. No account is switched or reauthenticated automatically.


### Thai setup summary

`rein config check --dir plugins/crux/config --quota orca --format text` prints a concise Thai summary without prompting a model. Omit `--quota orca` for local configuration inspection only. The default `--format json` preserves the machine-readable schema. `init` accepts only the default JSON format flag.

The summary reports CLI discovery, configured models/probes, quota freshness and account metadata evidence separately. Stale quota percentages are hidden; unknown values remain unknown. A successful check means inspection succeeded, not that routing, account binding or live handoff is ready. Raw quota diagnostic messages remain available in JSON rather than being echoed in the human summary.


When `require_orca_quota` is enabled, routed Orca launches reject explicit `--environment`, `--pairing-code` and `--on` selectors before dispatch. Local account-list quota cannot establish the account or runtime of that selected worker. This restriction does not establish account binding for an unqualified local launch, and does not enable remote graph recovery. Legacy configurations without the quota guard retain their existing selector behavior.


A routed Orca launch confirms readiness only after `worker-show` proves the exact live dispatch and `worker.startOptions.launch.effective` matches the prepared agent, model and effort. Requested settings alone are insufficient; missing or different effective settings fail closed and preserve the remote lease fence. This checks Orca-reported launch settings, not the provider account, model compliance, worker completion or process termination. Older Orca hosts without effective settings cannot establish this readiness gate.


### Claude metadata discovery

Claude Code major 2, version 2.1.302 or newer, has a metadata-only SDK initialize adapter. It disables hooks and MCP servers, ignores setting sources, disables session persistence and sends exactly one initialize control request followed by EOF. No user prompt is submitted. The adapter consumes `resolvedModel` rather than alias labels, deduplicates consistent resolutions and records declared effort support. Missing resolution, conflicting duplicate metadata, invalid IDs or an unexpected control response fail closed. `supports_effort: false` excludes an explicitly configured effort before probing; inventories without that field retain their existing behavior.

This is a client catalog, not proof of subscription entitlement, model availability, quota, billing, context size or quality. Unknown values remain unknown; existing routing, budget, probe and review gates still apply. Initialize account/session fields are not returned or included in the normalized catalog hash.

Worker health is observational: ticks sample repository-matching local routing leases using PID plus process start identity and bounded CPU queries. Missing/stale observations, unavailable identity/CPU, and remote workers return `UNKNOWN`; absence from a tick is never proof of death. Process-group leader disappearance alone also remains unknown. Current-generation `pretool`/`posttool` activity and timezone-aware timestamps inform the health ladder. Sampling does not release leases, kill workers, or dispatch replacements.
