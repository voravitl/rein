# ADR 0001 — rein: a supervisor that keeps worker models on task

- **Status:** Accepted (owner decision, 2026-10-08)
- **Context:** a multi-agent worktree pipeline (a coordinator, workers in their own git worktrees, a strong-model review gate) falls back to cheaper models when quota runs out (small Claude models, opencode providers, free tiers, Kiro). Cheaper models drift: out-of-scope edits, false "done" claims, touching the live stack. pstack-claude was evaluated: in Claude Code it only injects a SessionStart mandate (its PreToolUse hook is Copilot-only) and its routing would compete with an existing orchestration layer (oh-my-claudecode), so it is not used.

## Decision
Build `rein` (one Go binary, subcommands) with three layers under an OS sandbox:

| Layer | Who it covers | Catches | Cannot catch |
|---|---|---|---|
| Task contract (`rein contract`) | everyone | — (the rules: allow/deny globs, scope ids, report path, writable files, size budget) | — |
| Guard hook (`rein hook`) — seatbelt | Claude workers | push/rebase/glab/gh, docker prune, dependency installs, writes to never-edit / out-of-ownership existing files / other worktrees / the run dir / contracts, stopping without a report; plus the project profile's protected containers, ports, owner scripts and denied commands | writes through interpreters (`python -c`, `node -e`) |
| Drift check (`rein drift`) | every vendor (codex, agy, opencode, kiro, Claude) | out-of-scope diff, uncommitted work, missing scope sections, false file claims, TODO/skip/stubs, secrets, commit refs, size budget | intent; that is the reviewer's job |
| OS sandbox — the wall | Claude Code Bash sandbox, codex `-s` | writes outside allowed paths regardless of how | — |

The guard parses Bash with `mvdan.cc/sh` (a full bash grammar) instead of regexes: a 2026-10-08 review of the Python prototype showed regex guards are bypassed by `git -C . push` / `docker rm -f <live container>` and block normal work (`git add . && git commit -m "…push…"`).

Project-specific rules (protected container prefixes, ports, owner-only scripts, denied commands, never-edit paths, local artifacts) live in a **profile** that `rein contract new --profile` copies into each contract. The binary carries only generic rules, and real profiles stay out of public repos.

## Why Go
Measured 2026-10-08 on macOS arm64: python3 42 ms (-S 26 ms), node 54 ms, bun 27 ms, native binaries 1.4–3.2 ms; the Python prototype hook cost 28.7 ms per call inside a worker, and `rein hook` measures ~5 ms p50 in every case. A sibling project's ADR reached the same choice with a 3.2 ms Go p50. A plugin cannot assume a runtime (the Claude Code native installer ships no Node; Windows has no `python3`). Go cross-compiles to every target with `CGO_ENABLED=0`, and `mvdan.cc/sh` is the strongest shell parser available.

## Rules
1. Tests ship with the code; the Python prototype's cases are the parity baseline.
2. No "supports Windows" claim without a run on real Windows.
3. Hooks use exec form (`command` + `args`), never `bash script.sh`.
4. The guard is never described as a security boundary.
