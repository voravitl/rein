# Mandatory AI Code Review & MR Report Policy

This policy governs all AI model code reviews across Merge Requests (MR) and Pull Requests (PR) managed by `rein`.
Every reviewing AI agent MUST generate, save, and post a structured review report that provides clear verification evidence and actionable remediation steps for downstream AI workers and coordinators.

---

## 1. Core Principles

1. **Mandatory Report on Every Review:**
   A code review is incomplete without a structured report. Merely outputting conversational feedback or an unformatted verdict is strictly prohibited.
2. **Strict Head SHA Binding:**
   Every review report MUST record the exact `Head SHA` it evaluated.
   - If a new commit is pushed to the MR (`Head SHA` changes), the previous report is automatically marked **STALE** for the new commit range.
   - The subsequent review round must explicitly reference the previous report, inspect `git diff <OLD_HEAD_SHA>..<NEW_HEAD_SHA>`, and verify whether earlier findings were fixed without introducing regressions.
3. **Structured for AI-to-AI Handoff:**
   Reports must be unambiguous, machine-readable, and immediately actionable so another AI (worker/coder) can remediate defects without human interpretation.
4. **Verdicts Advise, Humans Approve:**
   An `APPROVE` verdict signifies that technical gates and review criteria have passed. Merge authority strictly requires human attention and approval (`rein approve`).
5. **Verdict Registration:**
   Upon completing the report, the reviewer must register the outcome in rein's verdict ledger:
   ```sh
   rein verdict record --mr <N> --sha <HEAD_SHA> --verdict <APPROVE|REQUEST_CHANGES> --reviewer <MODEL> --worker <MODEL>
   ```

---

## 2. Standard Review Report Schema

Every review report MUST contain the following 6 sections:

```markdown
# Review Report: MR !<MR_NUMBER> (Round <K>)

## 1. Work & Revision Identity
- **MR / PR**: !<MR_NUMBER> - <MR_TITLE>
- **Base SHA**: `<BASE_SHA>` (`origin/main`)
- **Head SHA**: `<HEAD_SHA>` (Commit under review)
- **Reviewer**: `<MODEL_MAKER>:<MODEL_NAME>` (e.g. `openai:gpt-5`, `anthropic:claude-3-7-sonnet`)
- **Worker**: `<MODEL_MAKER>:<MODEL_NAME>` (e.g. `anthropic:claude-sonnet`)
- **Task Tier**: `T1` | `T2` | `T3`
- **Revision Status**: `CURRENT` (Evaluated against exact `<HEAD_SHA>`)

## 2. Acceptance Criteria Checklist
| # | Criterion | Status | Evidence (`file:line` / output) |
|---|---|---|---|
| 1 | <Requirement 1> | PASS | `path/to/file.go:42` - implemented correctly |
| 2 | <Requirement 2> | FAIL | `path/to/other.go:108` - missing boundary check |
| 3 | <Security / Invariant> | PASS | `internal/guard/...` - invariant preserved |
| 4 | <E2E / Browser Proof> | PASS | `report/e2e/report.pdf` - real screenshot verified |

*Status options: `PASS`, `FAIL`, `NOT VERIFIED`*

## 3. Verification & Test Execution
- **Commands Executed**:
  ```sh
  <exact command run by reviewer, e.g. go test -count=1 ./...>
  ```
- **Local Test Outcome**: <PASS (exit 0) / FAIL (exit 1)> (<e.g. 17/17 packages passed>)
- **CI / External Status**: <GREEN / PENDING / NOT RUN>
- **Red Check (Mutation Testing)**:
  - Mutation: `<temporary change made to break a rule in detached worktree>`
  - Result: `<failed as expected, reverted cleanly; git status clean>`
- **E2E / Visual Evidence**: `<link to evidence or report/e2e/report.md>`

## 4. Findings & Defects
### [F-01] <Brief Title of Finding>
- **Severity**: `BLOCKER` | `HIGH` | `MEDIUM` | `LOW` | `NIT`
- **Location**: `path/to/file.go:123-130`
- **Failure Scenario**: When `<input or state>` occurs, `<unexpected outcome>`.
- **Risk / Impact**: `<why this breaks invariants, stability, or contracts>`.
- **Realistic**: `Yes` | `Theoretical`
- **Recommended Fix**:
  ```go
  <code snippet or exact implementation guidance>
  ```

*(If no defects found, explicitly state: "No defects found. All checks pass.")*

## 5. Handoff & Remediation Plan (Next AI Action)
Before merge, the remediating AI must:
1. [ ] Fix `[F-01]`: `<concrete action>`.
2. [ ] Add regression test: `<test file and expected behavior>`.
3. [ ] Re-run gates: `<exact test command>`.
4. [ ] Note: Pushing new commits will invalidate this report for `<HEAD_SHA>`.

## 6. Verdict
- **Verdict**: `APPROVE` | `REQUEST_CHANGES` | `COMMENT`
- **Blockers**: `<None / List of BLOCKER / HIGH finding IDs>`
- **Recorded Command**:
  ```sh
  rein verdict record --mr <N> --sha <HEAD_SHA> --verdict <APPROVE|REQUEST_CHANGES> --reviewer <REVIEWER> --worker <WORKER>
  ```
- **Merge Gate Note**: `APPROVE` is a review recommendation. Merge is gated by `rein verdict check` and requires human approval via `rein approve`.
```

---

## 3. Severity Classification Table

| Severity | Definition | Merge Impact |
|---|---|---|
| `BLOCKER` | Security vulnerability, data loss risk, invariant break, build/test failure | Blocks merge (`REQUEST_CHANGES`) |
| `HIGH` | Bug in core logic, missing error handling on critical path, broken contract | Blocks merge (`REQUEST_CHANGES`) |
| `MEDIUM` | Non-critical bug, edge case unhandled, performance regression | Blocks unless explicit coordinator waiver |
| `LOW` | Code clarity issue, minor inefficiency, sub-optimal naming | Does not block (`APPROVE` with notes) |
| `NIT` | Typo, style preference, comment phrasing | Does not block (`APPROVE`) |

---

## 4. Head SHA Drift & Multi-Round Review Protocol

```text
[Round 1 @ SHA-A] ──► Report 1 (REQUEST_CHANGES: F-01, F-02)
                            │
                      Coder pushes SHA-B (Report 1 becomes STALE)
                            │
                            ▼
[Round 2 @ SHA-B] ──► Inspect git diff SHA-A..SHA-B
                      Verify F-01 & F-02 resolved
                      Check for regressions
                      Report 2 (APPROVE @ SHA-B)
                            │
                            ▼
                      rein verdict record --sha SHA-B --verdict APPROVE
                            │
                            ▼
                      rein verdict check -> PASS
                            │
                            ▼
                      Human Approval: rein approve --sha SHA-B
                            │
                            ▼
                      Merge allowed
```
