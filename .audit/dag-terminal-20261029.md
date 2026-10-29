# PR-C terminal DAG graph — parent evidence
Base: `4d560bb8dfceee59c8ceab43cf25761f4a31fa81` (PR #36 head). Branch: `feat/dag-terminal-graph`. This record is parent verification plus one read-only review. It is not merge authorization.

## Authorship and scope
Writers were Hermes OMH subagents (`custom:888router` pool `9-free`). The planned guarded Codex writer hit provider 429 twice, so the owner chose OMH subagents and accepted that this lane had no Rein guard. To compensate, the parent hashed every allowed file before and after each writer stage and confirmed that no other path changed. No dependencies were added. No merge, deploy or release.

The HTML replay lane from the original plan was superseded by an owner-approved amendment: the graph must render in the terminal used by Claude Code, AGY and Codex CLIs, not in HTML.

## Behavior
`rein supervisor status --plan FILE [--view table|graph] [--watch[=2s]] [--seq N]`.
- `table` stays the default and uses the unchanged table writer.
- `graph` prints plain ASCII: a header (run, status, node and edge counts), then layers top to bottom. Each node is a card with task, state, kind, `attempt N/M`, optional agent/model, `<- dependencies` and `-> dependents`. Order is deterministic. Labels are escaped to visible ASCII (C0/C1 controls, DEL, invalid UTF-8, non-ASCII runes). Long labels wrap; nodes and edges are never dropped.
- Width comes from the terminal ioctl on Linux/macOS, then `COLUMNS`, then 80, with a minimum of 16.
- `--seq N` replays exactly the first N complete journal rows, including header and lifecycle rows, through the live parser. It never reports `changing` and never reads timing.
- Invalid combinations exit 2 before the plan is loaded: unknown or empty view, graph with `--json`, `--seq` with `--json` or `--watch` or without `--view graph`, non-positive or non-integer seq. A missing journal, N beyond the complete rows, or N landing on a partial row exits 1 with empty stdout.
- Watch reuses the existing loop. Clear-screen is sent only to a real TTY; pipes get plain frames.
- Status stays read-only: no controller lock, no writes, no execution.

## Red first
`verification/red-omh-1`: 9 top-level and 27 subtests compiled and failed only with `flag provided but not defined: -view|-seq`. The RED test file kept sha256 `6122fb04…` through implementation; later additions were append-only (58 + 47 lines, no removals).

## Container verification, candidate-omh-2
Pinned Go image `sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af`, Go 1.26.10 linux/arm64, `--init`, no network, read-only host source and module cache, frozen snapshot.
- Full `go test -count=1 -json ./...`: exit 0, 960 top-level pass, 5 skip, 1103 subtests, 21 packages.
- `go vet ./...` and `gofmt -l .`: clean.
- Focused race (graph, option, status, timing tests): 62 top-level, 122 subtests.
- Nonroot full suite: 961 pass, 4 skip, 1103 subtests.
- Mutation controls: 15/15 detected by named failing tests, all restored by hash. `seq-json-reject` is semantically equivalent (a later check still rejects the combination) and is caught by the direct option-table test.
- Real PTY probe with the compiled CLI: one-shot at 50 columns exit 0, no escapes, max width 35, all nodes; watch at 72 columns exit 130 after 7 clear-screen redraws, max width 61; piped watch exit 130, 6 plain frames, no escapes.
- Release cross-build 6/6 and `GOOS=freebsd go build ./internal/run`. Build-only, not native runtime evidence.

## Review
One read-only OMH subagent reviewer (`9-free` pool; self-identified as Claude, exact model not verifiable) returned APPROVE with two LOW and three NIT findings. The reviewer ran no tests and cross-checked parent receipts. Cross-model independence is NOT established because the writer and reviewer used the same pool. Findings left open:
- F-01 LOW: `TestSupervisorGraphWatchTerminalSnapshotUsesPlainPipeOutput` places control bytes in a cancel reason that the graph does not display, so that assertion cannot fail. Sanitization is covered by `TestDAGGraphSanitizesLabels`.
- F-02 LOW: the macOS width ioctl is compiled but never executed on macOS; failure falls back to `COLUMNS`/80.
- NITs: wrapping may split an escape sequence across card lines; docs omit the `\UNNNNNNNN` form; `--seq +3` parses as 3.

## Baseline and outstanding checks
`TestRouteAutoLaunchAndSettleEndToEnd` has a pre-existing DATA RACE in the `capture` test helper (`cmd/rein/route_auto_test.go:72`). Broad race failed in one extended run and passed in two others. It also fails on earlier bases. Not touched here.

Native macOS/Windows runtime, exact-head CI, cross-model review and human approval are outstanding. A colored, animated TUI mode is a separate follow-up; this PR is plain terminal text only.

Raw evidence home (local, ignored): `/Users/voravit.l/dev/rein/.omh/runs/2026-10-28-pr-c-terminal-graph/`, notably `VERIFICATION-SUMMARY.md`, `verification/red-omh-1`, `verification/candidate-omh-2/receipt.json`, `extended/ext-omh-3/{receipt.json,mutations.json,pty-results.json}` and `qa-1/review-report.md`.
