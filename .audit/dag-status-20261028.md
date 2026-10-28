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
