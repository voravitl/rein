# PR-A owner-only gate for every detected agent — parent verification, 2026-10-28

Append-only publication record for the owner-gate change. It is parent (orchestrator) evidence, not an independent review, and it does not grant merge or human approval.

## Problem
Owner-only commands (`rein run allow`, `rein run end --abandon`, `rein budget raise`, `rein route reconcile`, `rein run start --supervisor-plan`) refused only inside Claude Code (`run.UnderClaude`). A Codex, Hermes, AGY or OpenCode session could self-authorize. Red reproduction on base `e05f2e8`: `TestOwnerOnlyCodexCannotAllow` failed with `allow under Codex = 0, want 2`, added allowances and changed the run marker.

## Change
- `internal/run/agent.go`: one detector for Claude Code, ZCode, Codex, Hermes, Antigravity/AGY and OpenCode markers (non-empty value; deterministic order; refusal text names the kind, never the value).
- `internal/run/terminal*.go`: stdlib terminal API check (Linux `TCGETS`, macOS `TIOCGETA`, Windows `GetConsoleMode`, other platforms fail closed). A character device such as `/dev/null` is not accepted.
- `cmd/rein/owner_only.go`: the five commands refuse with exit 2 before any state change unless no agent marker is set AND stdin is a terminal. `rein hunter approve` uses the same detector. `rein approve` keeps its `--as-directed` provenance path, now covering the new agent kinds; no TTY requirement added to it.
- Guard (seatbelt, not a security boundary): `rein run start --supervisor-plan` joins the user-only list; workers of every vendor are refused; non-Claude shell events in a repo with an active run marker get only the parsed user-only policy, without the Claude coordinator lifecycle.
- Docs: `docs/CLI_REFERENCE.md`, ADR 0002 rule 6. Five legacy fixtures that exercise owner success paths now set only the terminal seam (`fixtureOwnerTerminal`); their assertions are unchanged.

## Authorship
Writer: guarded Codex `openai:cx/gpt-6.1-sol` (high) under a Rein contract in a dedicated worktree; out-of-scope writes were proven blocked before work started. Writer ran no tests. 594 protected files unchanged across writer runs. Parent: Hermes orchestrator (no product or test edits).

## Parent execution evidence (disposable Linux containers)
Go `go1.26.10 linux/arm64`, network none, read-only source and module cache, frozen source snapshot, task-owned containers removed after each run.

| Gate | Result |
|---|---|
| `go test -count=1 -json ./...` (root) | exit 0: 882 top-level PASS, 1376 subtests PASS, 5 existing skips, 21 packages |
| Same suite as uid 1000 | exit 0: 883 PASS, 4 existing skips, 1376 subtests |
| `go vet ./...`, `gofmt -l .` | exit 0, no files listed |
| Focused race (`OwnerOnly|UserOnly|AISession|AgentSession|InteractiveStdin`) | exit 0: 17 top-level, 457 subtests |
| Real PTY probe of the built binary | 8/8: pipe, file, `/dev/null` and closed stdin refused; Codex and Hermes markers refused inside a real PTY; no-marker PTY allowed and allowance persisted; no marker value printed |
| Mutation controls | 24/24 detected by a named failing test, each restored by hash: 12 marker removals, agent gate, terminal gate, five CLI wirings, worker shell, coordinator shell, supervisor-plan flag, run anchor, Linux terminal API |
| `scripts/build.sh` release matrix | 6/6 targets built (build only; not runtime support) |
| `GOOS=freebsd go build ./internal/run` | exit 0 (fail-closed fallback compiles) |

## Known limits
- Broad race: `go test -race ./cmd/rein ./internal/run ./internal/guard` fails only `TestRouteAutoLaunchAndSettleEndToEnd` (`route_auto_test.go:72` vs `route_launch.go:271`). The same race reproduces on base `e05f2e8` without this change (`-count=3`, exit 1), so it is pre-existing and not fixed here.
- Marker detection plus a terminal is a refusal gate. It does not prove a human is present; an agent that can drive a real PTY with a scrubbed environment is not stopped by this layer.
- Windows code compiles but was not run on Windows. AGY/OpenCode markers are taken from the accepted plan and were not probed with a live agent session.
- Independent cross-model review and exact-head CI: NOT_RUN at the time of this record.
