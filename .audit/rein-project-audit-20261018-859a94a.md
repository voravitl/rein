# Project Review Report: Rein — 2026-10-18

## 1. Work & Revision Identity

- Scope: current tracked project, documented guard/routing/recovery/hunter goals, distribution, and local verification. This is a project audit, not an MR review.
- Branch: `main`
- Final reviewed Head SHA: `859a94a1e942507a45d02562ef729ff5b4416316`
- Initial audited/tested SHA: `ec9cbe5ed0cc4be3b930fbb1df118b0b6069b054`. A concurrent external commit arrived during review. Its entire four-file delta was inspected separately; Go code/module files are identical between these SHAs (`git diff --quiet ec9cbe5..859a94a -- go.mod go.sum cmd internal`, exit 0).
- Reviewed revision: CURRENT at final identity check; no source edits by audit agents.
- Repository: `/Users/voravit.l/dev/rein`
- Inventory: 333 tracked files; 207 Go files, including 105 Go test files; 21 Go packages. The audit surveyed module wiring and reviewed critical paths; it does not claim manual inspection of every line or exhaustive bug absence.
- Review lanes: native `code-reviewer` (guard/approval/verdict/contracts/hooks/drift/run), native `architect` (goal completeness/supervisor/routing/hunter/Orca), native `executor` (scripts/distribution verification), primary agent (aggregate verification/E2E probes/integration).
- No MR/PR number supplied; no verdict ledger entry or human approval was created.
- Concurrent external work: `scripts/preflight-push.sh` initially appeared untracked, then was committed by another session with .gitignore and audit-record changes as `859a94a`. The delta was reviewed read-only and is included below (F-12); the audit did not author it.

## 2. Acceptance Criteria Checklist

| # | Goal | Status | Evidence |
|---|---|---|---|
| 1 | Contract/guard/drift baseline is executable | Local tests present and exercised; full gate below | `internal/guard`, `internal/contract`, `internal/drift`; guard remains a seatbelt, not an OS isolation boundary |
| 2 | Verdicts and approvals bind to current reviewed changes | FAIL | F-01, F-02 |
| 3 | Coordinator approval prompt validates and records owner decisions correctly | FAIL | F-03 |
| 4 | Checkpoint finalize/verify/restore is wired | Local implementation present; supported-state limitations remain | `cmd/rein/checkpoint.go:45`, `internal/routing/checkpoint.go:80`; full routing suite below |
| 5 | Bounded local supervisor can recover cooperative worker quota failure | Targeted local PASS | `cmd/rein/graph_recovery_test.go:77`; fake local harness/Git integration |
| 6 | Full Rein+Orca+OMC recovery/coordinator-failure journey | BLOCKED / NOT VERIFIED live | G-01; `cmd/rein/route_launch.go:72`, `cmd/rein/graph_adapter.go:383` |
| 7 | Hunter report/reproduce/verify/independent-review/owner-closure journey is executable from a new record | FAIL | F-04; unit closure fixtures manually seed inaccessible lifecycle transitions |
| 8 | npm/NPX distribution works from delivered package | FAIL | F-05, F-06, F-07 |
| 9 | E2E confines evidence writes and enforces configured timeout | FAIL | F-08, F-09; executable local mock probes |
| 10 | Current operational docs consistently describe production capabilities | FAIL | F-10 |
| 11 | Full real-provider, native-harness and live runtime acceptance | NOT RUN in this audit | No paid provider calls, user-session manipulation, installation or live worker lifecycle experiments |

## 3. Verification & Test Execution

Aggregate status is appended in the final verification record below. Individual successful checks do not override confirmed defects.

- `go vet ./...`: PASS, exit 0.
- `GOOS=windows go vet ./cmd/... ./internal/...`: PASS, exit 0; compile/static checking, not Windows runtime proof.
- `git ls-files '*.go' | xargs gofmt -l`: PASS, empty output.
- Initial `go test ./...`: FAIL, exit 1. `internal/drift/TestNonASCIIPathsAreJudgedAsWritten` and `internal/verdict/TestRevisionIdentitiesMatch_FileIntersection` failed TempDir cleanup (`directory not empty`); a `graphify-out` directory was observed. All other 19 packages passed. Drift's single-test rerun passed.
- Environment diagnosis: global Git `core.hooksPath` resolves to `/Users/voravit.l/.hermes/moa-gate/`; its `post-commit` and `post-checkout` scripts launch Graphify in the background. This can race disposable Git fixture cleanup. Global/user hook configuration was not changed.
- Fresh full rerun: `env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 go test -count=1 ./...` (result appended below). These variables apply only to the verification subprocess.
- `go test -race -count=1 ./...` (result appended below); initial environment retains the global Git hooks.
- `PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s skills/worktree-pipeline/scripts -p 'test*.py'`: PASS, 85 tests, exit 0.
- `PYTHONDONTWRITEBYTECODE=1 python3 -B -m unittest discover -s scripts -p 'test*.py'`: PASS, 12 tests, exit 0.
- Shell parsing for `scripts/*.sh`, `examples/pack/gates/*.sh`, pipeline shell scripts: PASS.
- `node --check` on `bin/*.js`: PASS. Python AST parsing (9 files) and JSON parsing (5 CI inputs): PASS.
- `npm pack --dry-run --ignore-scripts --json --cache /tmp/rein-audit-npm-cache`: package inspection PASS; distribution completeness FAIL (F-05).
- Fresh targeted hunter/closure CLI and graph/quota/monitor restart tests: PASS in both independent architecture commands.
- Temporary Go overlay probes: timeout=1 second, actual wait=1.200 seconds, run still reports success; screenshot filename `../outside.png` creates a file outside evidence directory. Both unwanted behaviors reproduced. Probe assertions intentionally confirm the observed defect; they are not product correctness tests.
- Source mutation/red-check: NOT RUN; no tracked source was modified. Production CLI misuse cases and temporary overlays supplied reproduction evidence instead.
- Real-browser E2E/Board PDF, real quota exhaustion, live Orca descendant termination: NOT RUN. Local mocks/fake harnesses do not establish those claims.

### Exact targeted verification commands

```sh
go test ./cmd/rein ./internal/hunter -run 'TestCmdHunter|TestReviewAndOwnerApprovalCloseOnlyExactRuntimeProof|TestReportCannotInjectClosureHistory' -count=1

go test ./cmd/rein ./internal/supervisor -run 'TestGraphCLIActualLocalHarnessRoutingCheckpointVerifyAndRestart|TestGraphCLIAliveQuotaStopsThenFallbackUsesPartialWorkAndHandoff|TestGraphCLIRejectsUnsupportedPoliciesAndRemoteBeforePrepare|TestMonitorAfterCoordinatorDeathDoesNotRebindMarker|TestMonitorDurableBeforeAckAndReplay' -count=1

go test -count=1 -v -overlay /var/folders/92/txh9xvd55hz_7y011n5xp8bh0000gp/T/rein-audit-e2e-ahzv41cv/overlay.json ./internal/e2e -run '^TestAudit'
```

## 4. Findings & Defects

### F-01 — HIGH: A new, changed commit inherits the old verdict and human approval

- Location: `internal/verdict/verdict.go:107-114`; caller `internal/verdict/check.go:199-202`.
- Scenario: create a feature commit B against unchanged origin/main; record an independent APPROVE and terminal approval for B; make commit C changing the same file; check C.
- Observed: production `verdict check --mr 1 --sha <C> --repo <fixture> --json` exits 0, `passed=true`, `has_approval=true`, matching B's old approval. B and C patch IDs differ (`eb7264d395881e008e6a1ff974637e9c4776cff5` vs `c9384050ba9b469f895b289fd10acf6413b3e99d`).
- Cause: differing identities are treated as equivalent if the calculated main-file intersection is empty. With main unchanged, that empty intersection says nothing about new feature edits.
- Impact: the central current-revision review/approval invariant is bypassed.
- Realistic: Yes; ordinary second commit after approval.
- Fix: require matching combined revision identity; permit only explicitly proven unchanged patch rebases. Test changed feature content and unchanged main as rejection.

### F-02 — HIGH: A subsequent REQUEST_CHANGES does not revoke the same reviewer's APPROVE

- Location: `internal/verdict/check.go:199-205`.
- Scenario: append APPROVE, then REQUEST_CHANGES for the same MR/SHA/reviewer, with owner approval present.
- Observed: production gate still passes; the loop ignores every non-APPROVE record and retains historical approvals.
- Impact: a reviewer cannot retract approval after discovering a defect.
- Realistic: Yes.
- Fix: reduce to the effective latest verdict per reviewer and revision before counting makers. Test APPROVE → REQUEST_CHANGES → APPROVE and stale revisions separately.

### F-03 — HIGH: Coordinator approval events bypass approval-specific validation and persistence

- Location: `internal/guard/coordinator.go:64-68`, `internal/guard/coordinator.go:134-135`; worker-only handling at `internal/guard/hook.go:213,263`.
- Scenario: actual live run owner session sends the canonical AskUserQuestion approval prompt, including a forbidden prefilled answer; then a canonical approval response.
- Observed: PreToolUse returns success with no denial. PostToolUse creates `decisions.tsv` but no MR approval record. Reproduced using a disposable repo and verified live coordinator PID/session.
- Cause: coordinator path returns via allowlist/decision logging without invoking approval validation/recording.
- Impact: the primary owner flow misses the stated prompt-integrity check and fails to register legitimate approval.
- Realistic: Yes.
- Fix: invoke shared approval validation and response handling from the coordinator path with explicit run owner binding. Exercise the real hook entrypoint; direct helper tests are insufficient.

### F-04 — HIGH: Hunter lifecycle cannot reach approved fix closure through production commands

- Location: `cmd/rein/hunter.go:25-139`, `internal/hunter/finding.go:191-209,403-417`, `internal/hunter/closure.go:130-139`.
- Scenario: report a new CANDIDATE, run real reproduction and runtime verification, then request independent review.
- Observed: reproduction updates strength/attempts but not lifecycle state. Review and approval require FIX_IN_PROGRESS. No production CLI action performs triage, fix authorization or start-fix; `Move` callers are tests only. `hunter triage --id H-0001` returns unknown action (application exit 2).
- Impact: a newly reported finding cannot traverse the advertised complete lifecycle without unsupported registry manipulation.
- Realistic: Yes.
- Fix: wire existing transitions through bounded authorization-aware commands/controller actions. Use a separate approved fix task. Add one empty-registry CLI journey through review and owner closure, without seeding state with internal test APIs.

### F-05 — HIGH: npm package omits everything required to supply/build the native binary

- Location: `package.json:9-20`, `scripts/install.sh:9-14`, `bin/install.js:20-23`.
- Scenario: fresh user installs the published package contents without an existing native Rein.
- Observed: pack dry-run lists 84 files but no Go module/source, native binary or dist artifact. Reconstructed package `sh scripts/install.sh` exits 1 with `go.mod file not found`. Postinstall only warns about failure.
- Impact: README's NPX installation path cannot bootstrap from this delivered package.
- Realistic: Yes.
- Fix: choose one complete distribution path (ship native assets or required source, or fetch verified release assets) and test the actual tarball in a clean directory. No new dependency is necessary.

### F-06 — HIGH: npm wrapper can recursively launch itself through PATH

- Location: `bin/rein.js:15-20,29-32,53`.
- Scenario: native binary absent, PATH contains npm's `rein` symlink to the JS wrapper.
- Observed: bounded subprocess-interception probe confirms selected spawn target resolves to the same JS wrapper. The build fallback is bypassed.
- Impact: recursive Node process chain/resource use instead of a usable CLI.
- Realistic: Yes; ordinary npm bin exposure.
- Fix: resolve real paths and exclude the current wrapper/package entrypoints from system-binary candidates; test a self-referential npm symlink with a bounded subprocess mock.

### F-07 — MEDIUM: Native launch errors return successful exit status

- Location: `bin/rein.js:57`.
- Scenario: `bin/rein` exists but is nonexecutable or cannot be spawned.
- Observed: real nonexecutable fixture makes `node bin/rein.js version` exit 0 silently because status and signal are null.
- Impact: callers/automation interpret failed execution as success.
- Realistic: Yes.
- Fix: handle `child.error` and null exit status as nonzero failures, printing the error.

### F-08 — HIGH: Screenshot filename escapes the evidence directory

- Location: `internal/e2e/runner.go:211-217`.
- Scenario: a supplied E2E spec uses screenshot filename `../outside.png` (or a symlinked parent).
- Observed: Go overlay against production executeStep creates `outside.png` alongside the evidence directory and returns nil.
- Impact: supplied spec can overwrite writable paths outside its evidence destination.
- Realistic: Yes; spec-controlled filename, constrained by process filesystem permissions.
- Fix: reject traversal and escaped/symlinked destinations before file creation; preserve write errors. Test traversal and symlink escapes against a temporary evidence root.

### F-09 — MEDIUM: Configured E2E scenario timeout is never enforced

- Location: `internal/e2e/spec.go:30`, `internal/e2e/runner.go:96,223-248`; client defaults at `internal/e2e/client.go` are independent of the spec.
- Scenario: TimeoutSeconds=1 and one wait step TimeoutMS=1200.
- Observed: elapsed 1.200 seconds, `success=true`, `err=nil`. Scenario deadline does not bound step execution or repeated client calls.
- Impact: advertised CLI/spec timeout does not bound run duration.
- Realistic: Yes.
- Fix: use a scenario context/deadline through waits and browser calls; test that expiry fails rather than succeeding late.

### F-10 — LOW: Current operator documents disagree about shipped capabilities

- Location: `docs/ARCHITECTURE_REIN_ORCA_OMC.th.md:4`, `docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md:3-5`, `agents/rein-bug-hunter.md:4,20`.
- Scenario: operator follows architecture/agent entrypoints that still describe supervisor or hunter registry as unimplemented/test-only.
- Observed: production commands already invoke these APIs while current entrypoints retain earlier status wording.
- Impact: supported capability and remaining boundary are unclear.
- Realistic: Yes.
- Fix: update active status summaries and distinguish historical receipts from present implementation and live verification.

### F-11 — MEDIUM: Census residual test assumes its waiting shell has no children and can leak it on failure

- Location: `internal/orca/census_test.go:247-261`; cleanup registration occurs after this assertion.
- Scenario: process scanning runs after the shell begins its `sleep 0.2` wait loop.
- Observed: full race suite failed because the snapshot correctly included that sleep child. The expected empty node set is timing-dependent. Three targeted race reruns passed, demonstrating this is intermittent. The failed assertion also left the shell looping after the test process exited.
- Impact: verification can fail nondeterministically and leave test-owned processes running; this is test reliability evidence, not proof of a production data race.
- Realistic: Yes; observed in this audit.
- Fix: establish deterministic readiness/sampling expectations and register process cleanup immediately after successful start, before any fatal assertion. Preserve the intended late-descendant limitation test.

### F-12 — MEDIUM: New preflight returns success for hidden sources and even Git failure

- Location: `scripts/preflight-push.sh:15-18,25,36` (new in final reviewed commit `859a94a`).
- Scenario A: ignored `bin/` contains `bin/lost.go`. Git emits only `!! bin/`; the script never enumerates contents and exempts the directory. It exits 0 claiming all sources are tracked.
- Scenario B: run the script outside a Git repo. Git exits 128, but successful awk masks the failure; the script exits 0.
- Additional message gap: ordinary untracked `lost.go` is discarded as a `??` row but success still asserts that all sources are tracked.
- Impact: the new release latch can give false assurance. It is not sufficient evidence that missing source files will be pushed.
- Realistic: Yes; all three cases reproduced in disposable fixtures by the script verification lane.
- Fix: capture Git failure separately; enumerate individual ignored files instead of directory summaries; distinguish untracked from ignored source checks and align the success message with what was actually verified. Add a small fixture test for each case.

### G-01 — Full-goal gap: Orca-owned recovery is intentionally blocked

- Evidence: `cmd/rein/route_launch.go:72-74`, `cmd/rein/graph_adapter.go:367-384`, `internal/supervisor/supervisor.go:195-197`, `internal/orca/census.go:1-8`.
- Local non-detaching shell workers are supported. Supervised Orca launches are refused because retained identity-bound root/descendant termination cannot be proven. Census samples do not prove that later or reparented descendants are gone.
- This is a deliberate safety boundary, not a request to remove the refusal. A new census helper/release does not by itself close graph recovery wiring.
- Close only after a reliable runtime termination contract exists, a remote adapter is wired, and an authorized live pilot proves the full journey. This audit does not assert that the installed current Orca version cannot support any future contract; it establishes that this Rein HEAD does not use one for graph recovery.

## 5. Handoff & Remediation Plan

1. [ ] Fix F-01/F-02 revision freshness and effective verdict semantics first; demonstrate the currently passing invalid cases turn into rejections.
2. [ ] Fix F-03 through production hook entrypoint tests with owner session identity.
3. [ ] Complete F-04 lifecycle wiring while preserving separate fix authorization and final owner acceptance.
4. [ ] Fix and test npm tarball bootstrap, self-resolution, and child execution errors (F-05–F-07).
5. [ ] Confine E2E evidence paths and enforce scenario deadlines (F-08/F-09).
6. [ ] Reconcile active documentation (F-10), including explicit local-vs-live status.
7. [ ] Repair the timing-dependent census fixture and preflight false assurance (F-11/F-12), then re-run fresh full Go tests, race/static checks, Python suites and tarball/CLI journey tests. Keep disposable Git fixtures independent of host hooks.
8. [ ] Handle G-01 separately through runtime contract/wiring/authorized live evidence. Do not weaken the current fail-closed checks to obtain a passing demonstration.

## 6. Verdict

- **Verdict: REQUEST_CHANGES — NOT READY to claim all project goals complete.**
- **Architectural status: BLOCK for full goal completion.**
- Confirmed findings: **12** (HIGH 7, MEDIUM 4, LOW 1); one additional explicit full-goal/runtime boundary (G-01).
- Functional local implementation exists across the core modules and has substantial tests. The approval gate defects, incomplete hunter production journey, distribution failures and E2E boundary defects prevent a whole-project success claim even if all existing tests pass.
- No implementation, commit, merge, install, user hook/config change, paid execution, or deployment was performed.

### Final verification record

- Fresh full local suite with isolated Git configuration: **PASS, all 21 packages, exit 0**. This validates unchanged Go code at both reviewed SHAs; it does not close the demonstrated behavioral defects.
- Full race suite: **FAIL, exit 1**. `TestCensusResidualSpawnAfterRecord` failed an invalid empty-descendant assertion; verdict test cleanup also raced external Git hooks. No Go data-race diagnostic was emitted. The failed gate is not reported as race-clean. The census targeted rerun passed 3/3, supporting timing sensitivity, not removal of the defect. See F-11.
- CI at initial `ec9cbe5`: **SUCCESS**, Ubuntu and macOS; https://github.com/voravitl/rein/actions/runs/39081170345 . Release workflow also completed successfully: https://github.com/voravitl/rein/actions/runs/39081174842 . Workflow success is not an npm package execution test.
- CI at final `859a94a`: **IN PROGRESS** at latest observation: https://github.com/voravitl/rein/actions/runs/39081419860 . Prior SHA's green CI is not promoted to this new SHA.
- New preflight script shell syntax: PASS; isolated behavioral probes reproduce F-12.
- Head identity final check: `859a94a1e942507a45d02562ef729ff5b4416316`; clean working tree at the last check. No source edits by audit agents.
- Cleanup: the census failure leaked a trigger-waiting shell because its cleanup registration occurs after the failing assertion. Its exact PID/group/command were verified and only that audit-created process group was terminated. No real worker/session was stopped.
