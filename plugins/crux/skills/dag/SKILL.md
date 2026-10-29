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
Draft a specification JSON or YAML document adhering to `rein_dag_spec/v1`:
- **Nodes**:
  - `kind: "worker"`: isolated worktrees with non-overlapping `write` globs.
  - `kind: "action"`: lightweight containerized actions (verification, linting, packaging).
- **Ordering**:
  - Place baseline verification first when required.
  - Place goal verification actions last, depending on the terminal worker nodes.
- **Rules**:
  - Disjoint write scopes: no two workers may have overlapping write globs.
  - Acyclic graph: `depends_on` must reference valid node IDs without cycles.
  - Idempotency & isolation: each node declares its own provider, prompt, inputs, and outputs.

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
- `<output-dir>/plan.md`: Human-readable summary of tasks, scopes, providers, and approval commands.
- Contracts under `~/.rein/contracts/` and git worktrees under `~/.rein/worktrees/`.

### Phase 4: Owner Approval Hand-Off
Present the compiled summary from `plan.md` to the user.
Instruct the repository owner to execute the printed owner commands in an authorized terminal:
```bash
# Example owner commands shown in plan.md:
rein run start <repo> --run <run-id> --session <session-id> --pid <owner-pid> --supervisor-plan <output-dir>/plan.json
rein supervisor run --plan <output-dir>/plan.json
```
**CRITICAL**: Do NOT execute these commands yourself or claim human approval. Wait for the owner to start the supervisor.

### Phase 5: Observe Execution
Monitor DAG execution using bounded single-shot status inspection. Do NOT use long-running `--watch` loops:
```bash
rein dag status --plan <output-dir>/plan.json --json
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
- Verifies every task in the plan is marked `done`.
- Verifies worktrees are clean and match live checkpoints (`rein checkpoint verify`).
- Verifies HEAD commits inherit from `task.Base`.
- Cherry-picks worker commits in topological order onto `<target-branch-name>`.
- Fails closed on any merge/cherry-pick conflict without touching the target branch.
- Runs the final goal verification commands in the integrated worktree.

### Phase 7: Independent Review & Completion
- Inspect git log and diff on `<target-branch-name>`.
- Run full test suite to confirm end-to-end green status.
- Report delivery with exact commit hashes, review verdicts, and artifact locations.
