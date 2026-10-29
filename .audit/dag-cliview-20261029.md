# PR-D0: DAG status inside agent CLIs (`--view statusline`, `--view tui`) — 2026-10-29

## Scope
- `rein supervisor status --view statusline [--lines N]`: compact colored DAG for Claude Code / AGY status lines. Plan from `--plan` or `REIN_PLAN`; width from `COLUMNS`, then host JSON `terminal_width`, then 80; stdin capped at 64 KiB / 500 ms; exit 0 with one `rein: <reason>` line on plan or journal errors.
- `rein supervisor status --view tui [--watch=d]`: full-screen rounded-card view (alternate screen, hidden cursor, 100 ms spinner, journal re-read at the watch interval, restored on exit/SIGINT/SIGTERM). Non-TTY stdout prints exactly the `--view graph` output.
- Docs: usage, Claude Code / AGY settings snippets, Codex tmux pane, honest limits. Rein installs no host settings.
- Status stays read-only, no controller lock, no new dependencies.

## Process and review boundary
- Work mode: OMH solo (owner decision 2026-10-29). Rein's own verdict-ledger/cross-model gate was deliberately not applied because Rein is not yet stable.
- Writers: RED tests and implementation by OMH subagents (`custom:888router` 9-free); parent edited one docs paragraph (AGY `stack_with_default` semantics, verified against AGY statusline docs).
- Review: **author-side self-review by the orchestrator only. No independent review.** Merge needs explicit human approval.

## Verification (pinned golang:1.26 container, read-only source/module cache, no network)
- RED on base 515663b: 11 top-level / 13 subtests failed, all with `invalid --view "statusline"|"tui": want table or graph`; no build/setup errors.
- Candidate (green-omh-2): full suite 981 pass / 5 skip, 1146 subtests, 21 packages; `go vet` and `gofmt -l` clean; focused race 69 / 160 pass.
- Extended (ext-2): 15/15 mutations detected and restored; nonroot full suite 982 / 1146 pass; six release builds; unknown-OS build.
- Real PTY (80x24, 120x40; SIGINT and SIGTERM): exit 130, alternate screen entered/left once, cursor hidden/shown once, restore is the last output, 20 redraws in 1.5 s, all four node cards present, no line wider than the terminal; `NO_COLOR` emits no SGR; statusline with a TTY stdin exits 0 in 0.01 s with 4 lines.
- Broad race: `TestRouteAutoLaunchAndSettleEndToEnd` (route stdout capture) failed once; it also fails 1/12 under `-race` on base 515663b without this change. Pre-existing and unrelated.

## Host proof (quota approved by owner)
Claude Code 2.1.312 in a non-root container, routed to 888route, project `statusLine` running the candidate `rein` linux binary against real exported DAG fixtures: the status line rendered running (`⠧ task` → `⠇ task`), failure (`✗ task`, `⊘ left ⊘ right`, `⊘ join`) and fan-out (`○ task`, `◌ left ◌ right`, `◌ join`) states, and Claude still answered prompts. AGY and Codex hosts were not run.
