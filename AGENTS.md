# AGENTS.md — Universal Multi-Agent Operational Guidelines

Universal instructions for all AI coding harnesses (Claude Code, Codex, Antigravity, OpenCode, Kiro, Cursor) working with or developing `rein`.

---

## 1. Core System Philosophy

- **Zero Unchecked Operations:** Every worker operates in a git worktree governed by a `rein contract`. Outside contracted paths, writes fail closed.
- **Single Static Go Binary:** Zero new external dependencies (`go.mod` stays lean). All tests (`go test ./...`) must pass 100% green before any commit.
- **Evidence-Based Delivery:** No claim is accepted without concrete `file:line` citations or test exit code proof.

---

## 2. Mandatory Code Review & MR Report Policy

Every AI agent performing code reviews on an MR or PR MUST strictly adhere to this policy:

### 2.1 Universal Invariants
1. **Mandatory Structured Report:** Conversational approvals or unformatted feedback are forbidden. Every review MUST produce a full review report following the standard schema below.
2. **Strict Head SHA Binding:**
   - Every review report MUST bind to the exact `Head SHA` evaluated.
   - If a new commit is pushed to the MR (`Head SHA` changes), previous reviews become **STALE**.
   - The subsequent review round must inspect `git diff <OLD_HEAD>..<NEW_HEAD>`, verify earlier findings were resolved, and emit a fresh report for the new Head SHA.
3. **Structured for Downstream AI Handoff:**
   Reports must provide an actionable checklist so the next AI worker can remediate defects directly without guesswork.
4. **Verdict Registration:**
   The reviewer must register its verdict in rein's ledger:
   ```sh
   rein verdict record --mr <MR> --sha <HEAD_SHA> --verdict <APPROVE|REQUEST_CHANGES> --reviewer <MODEL> --worker <MODEL>
   ```
5. **Human Approval Gate:**
   `APPROVE` is a review recommendation. Merge is gated by `rein verdict check` and requires explicit human approval via `rein approve`.

### 2.2 Standard Review Report Schema (6 Sections)
```markdown
# Review Report: MR !<MR_NUMBER> (Round <K>)

## 1. Work & Revision Identity
- **MR / PR**: !<MR_NUMBER> - <MR_TITLE>
- **Base SHA**: `<BASE_SHA>` (`origin/main`)
- **Head SHA**: `<HEAD_SHA>` (Commit under review)
- **Reviewer**: `<MODEL_MAKER>:<MODEL_NAME>`
- **Worker**: `<MODEL_MAKER>:<MODEL_NAME>`
- **Task Tier**: `T1` | `T2` | `T3`
- **Revision Status**: `CURRENT`

## 2. Acceptance Criteria Checklist
| # | Criterion | Status | Evidence (`file:line` / output) |
|---|---|---|---|
| 1 | <Requirement 1> | PASS | `path/to/file.go:42` - verified |
| 2 | <Requirement 2> | FAIL | `path/to/other.go:108` - missing check |

*Status: PASS | FAIL | NOT VERIFIED*

## 3. Verification & Test Execution
- **Commands Executed**: `<exact test command>`
- **Local Test Outcome**: <PASS (exit 0) / FAIL (exit 1)>
- **CI / External Status**: <GREEN / PENDING / NOT RUN>
- **Red Check (Mutation Testing)**: `<mutation details, failed as expected, restored cleanly>`
- **E2E / Browser Proof**: `<report/e2e/report.pdf or evidence link>`

## 4. Findings & Defects
### [F-01] <Title>
- **Severity**: `BLOCKER` | `HIGH` | `MEDIUM` | `LOW` | `NIT`
- **Location**: `path/to/file.go:123-130`
- **Failure Scenario**: `<concrete inputs/state leading to wrong outcome>`
- **Risk / Impact**: `<why this breaks invariants or contracts>`
- **Realistic**: `Yes` | `Theoretical`
- **Recommended Fix**: `<code snippet or guidance>`

## 5. Handoff & Remediation Plan (Next AI Action)
Before merge, the remediating AI must:
1. [ ] Fix `[F-01]`: `<concrete action>`.
2. [ ] Add regression test: `<test details>`.
3. [ ] Re-run gates: `<exact test command>`.

## 6. Verdict
- **Verdict**: `APPROVE` | `REQUEST_CHANGES` | `COMMENT`
- **Blockers**: `<None / List of blocker IDs>`
- **Command**: `rein verdict record --mr <N> --sha <HEAD_SHA> --verdict <VERDICT> --reviewer <R> --worker <W>`
```

---

## 3. Subagent Collaboration & Safety Rules

- **Role Separation:** Researcher (Read-Only), Coder (Scoped-Write within contract), Critic/Reviewer (Read-Only), Tester (Verification).
- **Depth Cap:** Subagent depth must not exceed 1 (subagents must NEVER spawn other subagents).
- **Tool Scoping:** Read-only subagents are strictly blocked from writing or running mutation commands by `rein hook`.
- **Zero Hallucination:** Every finding and claim must cite verifiable `file:line` evidence.

---

## 4. E2E Browser Testing & Board-PDF Reports

- When tasks touch web UI, routes, or HTTP interfaces, verify end-to-end via `rein e2e`:
  ```sh
  rein e2e test <url-or-spec> --out report/e2e
  ```
- Every E2E test automatically compiles an executive Board-PDF report (`report/e2e/report.pdf`) with real screenshots and accessibility snapshots for PR evidence.
