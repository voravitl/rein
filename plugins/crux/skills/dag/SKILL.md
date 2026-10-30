---
name: dag
description: "Coordinate multi-worker DAG workflows on Rein. Compile declarative specs into schema-3 plans, guide owner approval, observe execution, and integrate verified worker branches. Use for /dag, 'run as dag', or multi-agent parallel workflows."
---

Read [Runtime guide](../../references/runtime.md) before executing this skill. It defines native tools, role/model resolution, history scope, and unsupported integrations.


# DAG Workflow

Decompose complex engineering tasks into declarative directed acyclic graphs (DAGs), compile them into governed schema-3 supervisor plans, present them for single human owner approval per plan hash, monitor execution with bounded single-shot checks, and safely integrate verified worker outputs.

## Principles

1. **Declarative Spec First**: Write a `rein_dag_spec/v1` document defining disjoint write scopes, explicit dependencies, concrete deliverables, and automated verification commands.
2. **Deterministic Governance**: Compile the spec into Rein contracts, worktrees, hooks, and a schema-3 plan using `rein dag compile`.
3. **Owner Approval Boundary**: The supervisor run requires an explicit human owner command (`rein run start` and `rein supervisor run`). Never claim human approval or run owner-gated commands autonomously.
4. **Clean Integration**: When all nodes succeed and pass live checkpoint verification, integrate changes topologically with `rein dag integrate` into a single target branch.

## Workflow Phases

### Phase 1: Clarify the Goal & Grounding
- Clarify acceptance criteria, repository invariants, target branches, and verification commands.
- Check baseline test suite on the base commit before authoring the spec.

### Phase 2: Author the Spec (`rein_dag_spec/v1`)
Draft a JSON specification (`rein dag lint` reads JSON only, 8 MiB cap) adhering to `rein_dag_spec/v1`:
- **Nodes**:
  - `kind: "worker"`: isolated worktrees with non-overlapping `write` globs.
  - `kind: "action"`: lightweight containerized actions (verification, linting, packaging).
- **Ordering**:
  - Place baseline verification first when required.
  - Place goal verification last: at least one node must depend on every producer, or lint refuses the spec with `missing_verification`.
- **Rules**:
  - Disjoint write scopes: no two workers may have overlapping write globs.
  - Acyclic graph: `depends_on` must reference valid node IDs without cycles.
  - Each node declares its own prompt, verify commands, inputs and outputs; workers name allowed providers.
  - Owner-only fields (`launches`, `profile`, `config_path`, `config_digest`, `process_policy`, `contract_hash`) are refused anywhere in the spec: argv and config come only from the owner's files.

Validate the spec format:
```bash
rein dag lint --spec <spec.json>
```

### Phase 3: Compile and Apply
Compile the specification into concrete worktrees, contracts, hooks, and a schema-3 plan:
```bash
rein dag compile \
  --spec <spec.json> \
  --config <providers.json> \
  --profile <profile.json> \
  --out <output-dir> \
  --apply
```
This generates:
- `<output-dir>/plan.json`: Validated schema-3 execution plan.
- `<output-dir>/plan.md`: Human-readable summary of tasks and providers, plus the exact owner commands.
- Task worktrees under `<output-dir>/worktrees/<run>-<id>` and task contracts in the contract index (`$PIPELINE_CONTRACTS`, default `~/.cache/worktree-pipeline/contracts`).

Without `--apply` the compiler writes only `plan.json` and `plan.md` (dry run).

### Phase 4: Owner Approval Hand-Off
Present the compiled summary from `plan.md` to the user.
Ask the repository owner to run the commands printed in `plan.md` in their own interactive terminal. `rein run start --supervisor-plan` and `rein run allow` refuse agent sessions and non-TTY input, so an agent cannot run them. The sequence is:
```bash
# Example owner commands shown in plan.md:
rein supervisor validate --plan <output-dir>/plan.json
OWNER_SESSION="owner-$(id -un)-$$"
rein run start <repo> --run <run-id> --session "$OWNER_SESSION" --pid $$ --supervisor-plan <output-dir>/plan.json
rein run allow <repo> --task <run>-<id> --reason '<why>'   # once per task
rein supervisor run --plan <output-dir>/plan.json
```
**CRITICAL**: Do NOT execute these commands yourself or claim human approval. Wait for the owner to start the supervisor.

### Phase 5: Observe Execution
Monitor DAG execution with bounded single-shot status checks. Do NOT use `--watch` from an agent shell tool: it never returns until the run ends:
```bash
rein supervisor status --plan <output-dir>/plan.json --json
```
Inspect the output:
- If nodes fail: examine failure reasons in the supervisor journal.
- If re-planning is needed: refine the spec and compile a new plan iteration.

### Phase 6: Integrate Verified Branches
Once all nodes reach terminal state `done`:
```bash
rein dag integrate \
  --plan <output-dir>/plan.json \
  --branch <target-branch-name>
```
`rein dag integrate`:
- Refuses a branch name that already exists, so it never rewrites an existing branch.
- Verifies every task in the plan is marked `done` in the supervisor journal.
- Verifies each worker checkpoint digest matches the journal, the worktree is clean, and the live checkout still matches the checkpoint.
- Verifies each worker HEAD descends from `task.Base`.
- Creates the new branch at the base in a separate integration worktree and cherry-picks worker commits in topological order.
- Stops on the first cherry-pick conflict (exit 1): the new branch and integration worktree stay for inspection, and nothing is pushed.
- Runs the plan's verify commands in the integrated worktree.

### Phase 7: Independent Review & Completion
- Inspect git log and diff on `<target-branch-name>`.
- Run full test suite to confirm end-to-end green status.
- Report delivery with exact commit hashes, review verdicts, and artifact locations.
