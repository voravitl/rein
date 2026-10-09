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
| `rein route auto` | Coordinator | Automatic task + model selection | Instead of choosing a chain and model by hand | `0` = receipt written, `1` = refused (code on stderr), `2` = bad arguments |
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
rein advise --run <RUN> --task <CONTRACT> --provider codex|kiro|claude --model <M> <ROLE> <CONTRACT_WORKTREE> <TASK_FILE> <OUT_FILE>
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
rein route calibrate --task <T> --run <R> --provider <P> --amount <N> [--calls 1]
rein route discover [--config F] [--timeout 60s] [--dir D] [--json]            # refresh and print every harness inventory; exits 1 if any would be excluded
```

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
| `accept_freshness` | catalog evidence classes accepted: `remote_verified`, `client_catalog`, `unknown` (no adapter produces `remote_verified` yet; Claude Code is `unknown`) |
| `inventory_max_age_minutes`, `evaluation_suite` | catalog evidence lifetime; only evidence recorded under this suite version counts |
| `worker.min_approval_rate`, `max_false_claims`, `min_complete_attempts`, `max_evidence_age_days` | worker floor: the 95% Wilson lower bound of verified successes must reach `min_approval_rate` (0,1]; `max_false_claims` is an allowed COUNT; 100% outcome coverage is required. The legacy `budget.quality_floor` is reused when `selection.worker` has none, with no spending cap needed |
| `reviewer.min_defect_fixtures`, `min_clean_fixtures`, `min_recall`, `min_specificity`, `max_false_positive_rate`, `max_evidence_age_days` | separate reviewer floor on frozen red/clean fixtures; approval frequency is never reviewer quality |
| `cost_basis` | `monetary_marginal`, or `converted` with per-pool `per_unit`, `source`, `as_of`, `valid_days`; needed to rank candidates drawing on different pools |
| `calibration` | `authorization`, `max_calls`, `pool_caps`, `max_cash_usd`: the only way `route calibrate` may reserve spend for an unqualified model |
| `baseline_chain` | an explicitly accepted UNSCORED fallback, e.g. `worker:backend`; used when no candidate qualified, only for candidates blocked by missing evidence, and never after a scored candidate failed. It relaxes the evidence qualification and the reviewer plan and nothing else: every other gate, the probe bound, the reservation, the refresh and the receipt are the scored path's. Reported as `selection_mode=baseline_insufficient_evidence` |
| `allow_manual_chains` | by default, once a policy exists `rein route prepare` is refused (a hand-picked chain would bypass the scored ordering); `true` keeps ordered chains available |

**Provider config additions** (`fallback-chain.json`, all required for a provider to be an automatic candidate): `effort` (must be expressible on the harness command line, `--effort`/`--variant`/`-c model_reasoning_effort=`, or left empty), `capabilities`, `context_tokens`, `billing` (`mode` api|subscription|credits|free, `unit`, `pool` qualified by provider/account/pool/window, optional `price_key`), `probe_bound` (`calls`, `amount` in the billing unit; without a finite bound a candidate is never probed: `probe_bound_required`). An optional top-level `discovery` pins the executable per harness (`{"codex":{"command":["/path/codex"]}}`).

**Evidence flow** (all append-only ledger rows; units and pools are never merged; the same `--charge-id` recorded twice counts once):

```sh
rein ledger add --attempt <A> --rounds N --gates-passed|--gates-failed [--approved --review-sha SHA --reviewer agent:model --blockers 0 --highs 0 --false-claims 0 --drift 0 --resolved-model M]
rein ledger charge --attempt <A> --pool <P> (--unit usd|kiro_credits|agy_credits|subscription_percent|tokens --amount X | --price-key K --in N --out N [--cache-read N --cache-write N --context N]) [--component worker|review|repair|probe|fallback|shared|calibration] [--charge-id ID] [--model M] [--provider P]
rein ledger fixture --provider <P> --fixture <ID> --class defect|clean (--detected|--missed|--false-positive|--clean-ok) --adjudicated --type <kind> [--tier T1] [--suite S]
```

A review charge names the reviewer provider (`--provider`, its name in `fallback-chain.json`) and model: without them it would price no reviewer, and it is refused. A pool keeps the unit its provider bills in (`--config` or the default providers file; a mismatch is refused). Record a review's charge against the REVIEW's attempt (`route auto --phase review` returns it), a worker's against the worker's.

`ledger add --attempt` takes worker, kind, effort, configuration, suite and tier from the launch record, so an outcome cannot disagree with its launch. `--price-key` prices an API call from `prices.json` entries that carry `input_per_mtok`, `output_per_mtok`, optional `cache_read_per_mtok`/`cache_write_per_mtok` and `tiers`, plus `source`, `as_of`, `valid_until`; expired rates are refused, never extended. The legacy blended `usd_per_mtok` is not used.

**What the JSON report contains**: `launch` (what to run), `classification`, `rounds` (one per decision: every inventory with status/freshness/version/scope, every candidate with its evidence and the first gate that excluded it, the probe and its outcome), `comparison` (what the ranking does and does not claim), `receipt` (decision, attempt and parent IDs; task-profile, inventory, pricing, quality and policy hashes; `expires_at`; the reservation). On a refusal the same report is printed with `error`/`error_code` and the exit code is `1`.

**Refusal codes**: `classification_required`, `profile_invalid`, `selection_policy_required`, `quality_policy_required`, `cost_basis_required`, `probe_bound_required`, `no_eligible_candidate`, `worker_identity_required`, `calibration_cap_exhausted`.

**Receipts and state**: receipt and its evidence artifacts (`profile.json`, `inventory.json`, `report.json`) live outside checkouts under `<contract-index>/routes/` (`decisions/`, `artifacts/<task>/`, `leases/`, `used/`, `prepare/`); reservations under `<contract-index>/holds/`. A receipt is refused at launch when the policy, task profile, catalog snapshot, prices, billing, quality evidence or contract changed, when its evidence expired (the earliest expiry of any evidence it uses, at most 30 minutes) or when its reservation was released: prepare again. **A receipt buys one execution**: `route launch` (worker) and `route check --phase review` spend it, a second use is refused, and a retry, a fallback or a repeat is a new decision with a new refresh, a new reservation and its own evidence; a launch that is refused (effort, lease, budget) does not spend it. A quota signal cools the shared pool until `rein route clear`. One live writer per checkout is enforced by a lease proven by process identity; a crashed holder is replaced only once launcher and provider process are both proven gone. A worker dispatched through Orca outlives the launcher, so its lease has no process to prove gone: the task stays held until `rein route settle --attempt` ends the attempt (and the exit row of such a launch records no duration).

**Reservations**: one atomic hold per decision holds the probe bound (`probe`), the attempt's own funding (`funding`) and, for a worker, the capacity its plan sets aside for the reviews that follow (`review_reserve`). The review that follows reserves under its own attempt and takes that capacity over in the same transaction (`review_funding`), so it is neither counted twice nor lost. `route settle` charges the probe at its declared bound; funding is charged at the reserved bound unless the attempt has a measured, non-zero charge of its own kind in that pool (a zero or approximate charge proves nothing), in which case the rest is released; a plan whose receipt was never used releases its funding uncharged; the review capacity is released, never charged as the worker's. Preparing again discards a receipt that was never used and releases its hold the same way. A hold whose owner crashed is released only by the user with `route reconcile`, and only when the owner is proven gone AND the task has no writer left (an Orca-dispatched worker or a live launcher keeps it). Reservations are worst cases; they are recorded under one lock but are not enforced against spend that bypasses rein.

**Trust model**: rein does not trust the coordinator to choose, but it does record what the coordinator reports. The ledger is writable by the coordinator: outcome fields (`--approved`, `--reviewer`, `--review-sha`, `--gates-passed`), charges, fixtures and `changed_files`/`exclude_makers` are assertions that rein records and counts, not facts it verified. What it enforces is that unknown is never success or zero, that a model's record is only its own, and that the coordinator cannot name a model, weaken the owner's policy, skip the refresh, or reuse a receipt. The coordinator guard denies setting or unsetting `REIN_PROFILE`, `PIPELINE_LEDGER`, `PIPELINE_PRICES`, `PIPELINE_CONTRACTS`, `PIPELINE_FALLBACK` and `REIN_RUN_DIR` (and `env -i`) in a command and denies `--config` on `rein route`; it is a seatbelt for a misled coordinator, not a sandbox against a hostile one. Merge stays gated by `rein verdict check` and `rein approve`, which do not depend on this evidence.

**Not implemented**: a Claude Code catalog adapter, `remote_verified` freshness, agy quota refresh, automatic rate refresh, the calibration runner (the gate only bounds the spend), a context-conditioned cost forecast with uncertainty, a shared-overhead allocation rule (shared charges are reported once and excluded), and pinning of the owner's policy at `rein run start`. See the design document's "Implementation status".
