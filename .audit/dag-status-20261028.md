# PR-B DAG read-only status / timing / watch — parent evidence
Base: `1a3d605635ba995c169e81a9f8079fcb34364474` (#34). Branch: `feat/dag-status-watch`. This record is parent verification, not independent QA or merge authorization.

## Authorship and scope
Guarded Codex `openai:cx/gpt-6.1-sol` high wrote product/tests under task `dag-status`. Hermes owned orchestration and container verification. Before writing, Rein denied a real out-of-contract sentinel write; OS sandbox probe independently denied an outside-scope write. Writer receipts report no protected-file changes. No dependencies added; no PR-A import, merge, deploy or release.

## Behavior
`rein supervisor status --plan FILE [--json] [--watch[=2s]]` observes schema-3 journals without acquiring controller authority or writing run state. It projects nodes, states, layers, attempts, prepared worker metadata and nullable elapsed/tokens. Partial final lines yield bounded `changing`; corrupt complete content is rejected. Guard lines remain unknown because seen logs lack reliable attempt identity.

Successful durable journal appends invoke a best-effort display-only timing append. Replay never reads this sidecar. Missing, invalid or oversized timing yields unknown elapsed. Watch exits on terminal views or interruption. Source changes to `dag_state.go` are restricted to the post-success timing hook.

## Red-first and discovered defect
The initial CLI tests compiled and failed because `status` returned unsupported-command exit 2. The corruption-negative test alone was not evidence of implementation.

Parent found a real candidate-1 defect: a FIFO timing sidecar blocked status (`TestParentStatusFIFO`, exit 1). Candidate-2 rejects nonregular paths, uses Unix no-follow/nonblocking opens, verifies descriptor/path identity and bounds sidecar reads to 128 bytes per journal entry, max 4 MiB. The unchanged parent probe passes on candidate-2 (exit 0); named FIFO/journal/oversize regressions pass. Original failure receipts are retained.

## Container verification, candidate-2
Pinned Go image `sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af`; Go 1.26.10 Linux arm64; Docker init, no network, read-only host source and module cache, frozen snapshot. Product mutations run only in disposable container tmpfs.

- Full `go test -count=1 -json ./...`: exit 0, 927 top-level passes, 1034 subtests, 5 skips, 21 packages.
- `go vet ./...`: exit 0. `gofmt -l .`: exit 0, empty output.
- Focused race (`^(TestSupervisorStatus|TestDAGStatus)`): exit 0, 29 top-level and 53 subtests, 2 packages.
- Nonroot full suite: exit 0, 928 top-level passes, 1034 subtests, 4 skips, 21 packages.
- Compile-valid mutation controls: 9/9 detected by named failing tests; original hashes restored, restored-green 29/53. Controls: controller lock acquisition, replay sidecar dependence, timing hook removal, backwards-clock clamp, sequence validation, partial-tail terminal handling, CLI wiring, FIFO read regression, sidecar size cap.
- Release cross-build: 6/6 (darwin/linux/windows, amd64/arm64). Build-only, not native runtime evidence.
- Candidate-2 source hashes unchanged during verification; no retained task containers.

## Baseline and outstanding checks
Broad `go test -race -timeout=600s ./cmd/rein ./internal/supervisor` remains FAIL: candidate 217 top-level pass / 1 fail, 318 subtests. Sole named failure is `TestRouteAutoLaunchAndSettleEndToEnd`. Same test fails on base (188 pass / 1 fail, 265 subtests), with no timeout. The earlier 240-second baseline stopped prematurely; its receipts remain separate. No route-race fixes are included here.

Independent QA and exact-head CI are NOT_RUN at creation of this record. Cross-model eligibility and explicit human approval remain separate merge gates; no merge authorized. Native non-Linux runtime behavior is untested. Timing and state views are observational, not authorization or proof of human identity.

Raw evidence home (local, ignored): `/Users/voravit.l/dev/rein/.omh/runs/2026-10-28-pr-b-dag-status/`, notably `verification/candidate-2/receipt.json`, `extended/candidate-2/receipt.json`, `probe-fifo-fixed/receipt.json` and `verification/baseline-pr34-600s/receipt.json`.


## Follow-up: candidate-3 remediation (historical record above retained)

# PR-B DAG read-only status / timing / watch — parent evidence
Base: `1a3d605635ba995c169e81a9f8079fcb34364474` (#34). Branch: `feat/dag-status-watch`. This record is parent verification, not independent QA or merge authorization.

## Authorship and scope
Guarded Codex `openai:cx/gpt-6.1-sol` high wrote product/tests under task `dag-status`. Hermes owned orchestration and container verification. Before writing, Rein denied a real out-of-contract sentinel write; OS sandbox probes independently denied outside-scope writes before review remediation. Writer receipts report no protected-file changes. No dependencies added; no PR-A import, merge, deploy or release.

## Behavior
`rein supervisor status --plan FILE [--json] [--watch[=2s]]` observes schema-3 journals without acquiring controller authority or writing run state. It projects nodes, states, layers, attempts, current prepared worker metadata and nullable elapsed/tokens. Partial final lines yield bounded `changing`; corrupt complete content is rejected. Outstanding invocations prevent terminal status/watch exit. Global stops and exhausted check budgets block unadmitted work without inventing failed attempts; active attempts retain their observed state. Guard lines remain unknown because seen logs lack reliable attempt identity.

Successful durable journal appends invoke a best-effort display-only timing append. Replay never reads this sidecar. Missing, invalid, oversized, nonregular, multiply-linked or unmatched timing yields unknown elapsed or no telemetry write. Unix writes use no-follow/nonblocking opens and descriptor/path identity checks. Windows code opens and rejects reparse points and checks link count; it is cross-compiled only. Watch has one observation reader in flight and returns 130 on interruption, although it cannot cancel an underlying kernel filesystem call.

## Review history
Independent read-only reviewer `anthropic:claude-opus-5-5` reviewed `eeba812` and returned REQUEST_CHANGES with three MEDIUM and six LOW findings. Container red verification reproduced seven findings with seven top-level and four subtest assertion failures. Static F3 cases passed on the old code and were not treated as race proof; candidate-3 adds direct opener tests and mutation controls. Sparse timing finding F8 is deferred by parent triage and timing remains unknown when incomplete.

## Container verification, candidate-3
Pinned Go image `sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af`; Go 1.26.10 Linux arm64; Docker init, no network, read-only host source and module cache, frozen snapshot. Product mutations run only in disposable container tmpfs.

- Full `go test -count=1 -json ./...`: exit 0, 942 top-level passes, 1045 subtests, 5 skips, 21 packages.
- `go vet ./...`: exit 0. `gofmt -l .`: exit 0, empty output.
- Focused race (`^(TestSupervisorStatus|TestDAGStatus|TestDAGTiming)`): exit 0, 44 top-level and 64 subtests, 2 packages.
- Nonroot full suite: exit 0, 943 top-level passes, 1045 subtests, 4 skips, 21 packages.
- Compile-valid mutation controls: 17/17 detected by named failing tests, all restored by hash and focused suite restored green. Controls include controller acquisition, replay sidecar dependence, timing hook, FIFO read, size cap, writer opener hardening, single-link check, lifecycle supersession, budget blocking, outstanding invocation, global-stop blocking, retry metadata and CLI cancellation.
- Release cross-build: 6/6 (darwin/linux/windows, amd64/arm64). Build-only, not native runtime evidence.
- Candidate-3 source hashes unchanged during verification; no retained task containers.

## Baseline and outstanding checks
Broad `go test -race -timeout=600s ./cmd/rein ./internal/supervisor` remains FAIL: candidate 232 top-level pass / 1 fail, 329 subtests. Sole named failure is `TestRouteAutoLaunchAndSettleEndToEnd`. Same test fails on #34 base (188 pass / 1 fail, 265 subtests), with no timeout. No route-race fixes are included here.

Exact-head independent re-review and CI are pending at creation of this record. Cross-model eligibility and explicit human approval remain separate merge gates; no merge authorized. Native macOS/Windows runtime behavior is untested. Timing and state views are observational, not authorization or proof of human identity.

Raw evidence home (local, ignored): `/Users/voravit.l/dev/rein/.omh/runs/2026-10-28-pr-b-dag-status/`, notably `qa-1/review-report.md`, `verification/review-red-2/receipt.json`, `verification/candidate-3/receipt.json`, `extended/candidate-3/receipt.json` and `verification/baseline-pr34-600s/receipt.json`.
