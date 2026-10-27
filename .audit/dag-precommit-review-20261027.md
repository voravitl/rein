# DAG lightweight workflows — precommit independent review
Point-in-time evidence, 2026-10-27. This is a parent-curated publication record of the independent reviewer report, not an exact-commit review or merge approval. The product snapshot is candidate-5, with manifest SHA256 `2ad3887aed6ffd3fdc9d04d5fb6097dc1ce4737005701165ad98a28aac20a54a` (633 entries) and source archive SHA256 `a299e558886718a9da7b969bed6e2bf34e45af1c21175990537aa6871f879cb7`. This audit file is an additional, non-product publication artifact; it is not part of that tested manifest.

## 1. Work & Revision Identity
- **MR / PR**: not yet assigned at the time of this review.
- **Base SHA**: `e05f2e842c57edcd5b5911cc091a3d85a229fc87` (`origin/main`).
- **Head SHA**: base plus 23 uncommitted feature files, identified by the snapshot hashes above. No feature commit existed during review.
- **Reviewer**: `anthropic:cc/claude-opus-5-5`, through `custom:888router` (independent QA Round 3).
- **Worker**: `openai:cx/gpt-6.1-sol`, high, guarded Codex CLI.
- **Task Tier**: `T3`.
- **Revision Status**: precommit snapshot only. A commit or PR head requires a new exact-head review; this report must not be relabeled as that review.
- **Identity proof**: reviewer recomputed archive and manifest hashes, compared all 633 entries against both archive and worktree, and found no mismatches. Candidate-4 to candidate-5 changed only `internal/supervisor/dag_safety_test.go`, adding `TestDAGReplayDependencyOrder`; production files were unchanged.

## 2. Acceptance Criteria Checklist
| # | Criterion | Status | Evidence |
|---|---|---|---|
| A1 | Opt-in schema 3; schema 1/2 compatibility and strict binding | PASS | Full suite; unchanged candidate-4 production and plan-bound mutation proof. |
| A2 | Overlap, bounded concurrency, verified dependencies, reservations and controller ownership | PASS within stated scope | Real Docker action overlap; worker process tests use a **simulated provider**, not live model calls. |
| A3 | Branch-local failure, global fail-closed on ambiguity, drain and replay fences | PASS | Prior independent Docker branch/cancel checks; `dag_state.go:376-379` and `dag_safety_test.go:164-184` red/restored-green dependency-order regression. |
| A4 | Literal-argv actions and containment; no model routing or paid reservation | PASS with N-01 limitation | Prior independent Docker containment and mutation checks on identical production source. |
| A5 | Artifact identity/ownership/hash binding and tamper/path rejection | PASS | Prior independent Docker tamper and second-read hash mutation checks on identical production source. |
| A6 | Full/static/focused race and compile-valid mutation controls | PASS for scoped gates | Independent full/static/focused race below; F-10 mutant now detected. **Broad race remains FAIL (F-08).** |
| A7 | Documentation and executable example | PASS | Prior independent Docker recipe on unchanged documentation and production. |
| A8 | Commit, PR, exact-head review, CI and verdict ledger | NOT VERIFIED | Publication had not occurred. Human merge approval was neither requested nor granted. |

## 3. Verification & Test Execution
Independent QA executed these gates in Docker image `sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af`, Go `go1.26.10 linux/arm64`. Repository, snapshot and module cache were mounted read-only; the disposable clone and cache were in tmpfs, with no network or Docker socket. Runner exit 0, 201.9 seconds, no retained container; snapshot unchanged.

| Command | Exit | Observed result |
|---|---|---|
| `go test -count=1 -json ./...` | 0 | 897 top-level PASS, 981 subtests PASS, 5 existing skips, 21 packages PASS. |
| `go vet ./...` | 0 | Empty output. |
| `gofmt -l` over all 288 Go files | 0 | No unformatted paths. |
| `go test -race -count=3 -json ./internal/supervisor -run '^TestDAGReplayDependencyOrder$'` | 0 | 3 top-level and 6 subtests PASS; no race warning. |
| `go test -race -timeout=300s -count=1 -json ./internal/supervisor ./cmd/rein -run 'TestGraph\|TestDAG\|TestAction\|TestArtifact'` | 0 | 68 top-level and 104 subtests PASS; no race warning. |

**Independent F-10 red check:** replace `if states[dep].Stage != "done"` in `internal/supervisor/dag_state.go:377` with `if false && states[dep].Stage != "done"`. The mutant passed `go vet ./internal/supervisor` (exit 0). `go test -count=1 -json ./internal/supervisor -run '^(TestDAG|TestArtifact)'` then exited 1 with the assertion failure `TestDAGReplayDependencyOrder/B` at `dag_safety_test.go:180`; positive control `/A` remained green. Restoring from the immutable snapshot restored the original file hash and the same command passed 18 top-level and 38 subtests (exit 0). Compile failures, panics and timeouts were not counted as detection.

**Separate parent evidence, not independent QA:** on candidate-5 all 21 mutation controls were detected by actual named test failures and passed again after restoration. This does not turn the Round 3 one-control independent mutation run into a 21-control independent rerun.

**Carried-forward independent evidence, not rerun in Round 3:** candidate-4 Docker E2E passed 6/6 scenarios plus the exact documentation recipe; 41 scenario-owned and 8 recipe-owned containers were absent after cleanup. The Round 3 reviewer confirmed the delta was test-only before relying on those checks. Broad race still exited 1 at `TestRouteAutoLaunchAndSettleEndToEnd`, matching the pinned-base failure stack.

- **CI / External Status**: NOT RUN at precommit review time.
- **E2E / Browser Proof**: CLI/container work, no web UI or HTTP interface change.
- **Raw evidence**: task-local `verify-round3/identity-check.json`, `verify-round3/c4-c5-test.diff`, `verify-round3/gates/receipt.json` and raw per-gate JSONL/exit files. Their location is external to the repository; hashes above identify the reviewed source, not a claim that raw logs are committed.

## 4. Findings & Defects
### F-10 — Replay dependency-order test gap: CLOSED
`internal/supervisor/dag_safety_test.go:164-184` now proves that a dependent prepare before the upstream task is done returns exactly `DAG journal dependency violation`, while an independent prepare is accepted. The compile-valid mutation fails only the dependent negative control. No production change was needed.

F-01 through F-07 and F-09 remained closed from independent Round 2; relevant production code and tests are unchanged and passed the Round 3 suite. No new findings were reported.

### F-08 — Pre-existing broad race: DISCLOSED, NOT FIXED
- **Severity**: LOW for this feature change, per independent reviewer.
- **Location**: `cmd/rein/route_auto_test.go:72` versus `cmd/rein/route_launch.go:271`.
- **Evidence**: candidate-4 broad race and pinned base have matching failure stacks.
- **Impact**: the broad suite must not be described as green or the repository as race-free. This feature does not fix the baseline race.

### N-01 — Aggregate output bound is post-termination
`cmd/rein/action_adapter.go:474` checks aggregate output size after execution. The per-file `fsize` limit is not an aggregate runtime disk quota. The feature must not claim otherwise.

## 5. Handoff & Remediation Plan
1. [x] Close F-10 without weakening production guards.
2. [x] Verify candidate-5 full/static/focused race and red/restored-green proof independently.
3. [ ] Parent: commit exactly the reviewed product files plus this publication-only audit; compare the committed tree against all snapshot entries before push.
4. [ ] Parent: open an unmerged PR, disclose F-08, N-01, simulated worker provider and the absence of live Windows proof or benchmark claims.
5. [ ] Obtain a fresh exact-head review and revision-specific CI result; register only an actual exact-head verdict using `rein verdict record`.
6. [ ] Do not merge, deploy, release or record human merge approval: none was authorized.

## 6. Verdict
- **Verdict**: APPROVE for precommit candidate-5 only.
- **Blockers**: none for the precommit candidate. Publication and exact-head gates remain unverified.
- **Not merge approval**: do not treat this report as a ledger verdict for a future SHA.
- **Command after an actual exact-head review**: `rein verdict record --mr <REAL_PR> --sha <REVIEWED_HEAD_SHA> --verdict <ACTUAL_VERDICT> --reviewer anthropic:cc/claude-opus-5-5 --worker openai:cx/gpt-6.1-sol`.
