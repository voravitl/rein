## Rules for Subagent Collaboration (paste into standing.md or task spec)
Use subagents to parallelize discovery, verification, and reviews without polluting the main context window.

### 1. 4-Tier Roles and Capabilities
- **Researcher (Read-Only):**
  - Read code, trace dependencies, locate symbols (`read`, `grep`, `glob`, `codegraph_explore`).
  - NEVER edit or write files.
  - Handoff rule: every finding MUST cite concrete `file:line`.
- **Coder (Scoped-Write):**
  - Write and modify code ONLY within the task's contract allowlist (`rein contract show <task>`).
  - NEVER push to git, rebase, or edit out-of-scope files.
  - Handoff rule: list modified files and pass to Tester/Critic.
- **Critic / Reviewer (Read-Only):**
  - Inspect diffs, verify claims, check security invariants, and validate against spec.
  - Output format: ranked findings with severity, scenario, and `Realistic: yes/no`.
  - NEVER edit files to fix findings directly; report back to Coder.
- **Tester (Verification):**
  - Execute test suites, regression tests, and red-checks.
  - NEVER modify production code.

### 2. Provider Dispatch Adapter
Invoke subagent capabilities according to the active provider:
- **Claude Code:**
  - With OMC: `Agent(subagent_type="Explore")` (Researcher), `Agent(subagent_type="Critic")` (Critic).
  - Vanilla: `Agent(subagent_type="general-purpose", prompt="[REIN ROLE: <ROLE>] ...")`.
- **Antigravity (AGY):**
  - Researcher: `invoke_subagent(TypeName="research", Prompt="...")`.
  - Specialized: `invoke_subagent(TypeName="self", Prompt="[REIN ROLE: <ROLE>] ...")`.
- **Codex / Kiro / OpenCode (No native subagent tool):**
  - *Option A (Advisor Script):* Run `<skill>/scripts/advise.sh <role> <worktree> <task-file> <report-out>`.
  - *Option B (In-Process Phasing):* Execute in strict phases:
    1. Phase 1 (Explore): Read/grep only; formulate plan with file:line citations.
    2. Phase 2 (Edit): Modify code within scope.
    3. Phase 3 (Verify): Run tests and red-checks.

### 3. Hard Safety Guardrails (Rein Enforced)
- **Depth Cap:** Subagent depth must not exceed 1 (subagents must NEVER spawn other subagents).
- **Tool Scoping:** Researcher and Critic roles are stripped of write permissions by `rein hook`.
- **Contract Boundary:** Writing subagents are strictly constrained by the task's contract allowlist.
- **Zero Hallucination:** Claims without file:line or test exit-code proof are rejected by `rein drift`.
- **Conciseness:** Subagent reports must be concise (under 20 lines), highlighting verdict, files touched, and verification proof.
