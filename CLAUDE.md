# rein

Supervisor for pipeline worker models: `rein hook` (worker hook for Claude Code, codex, agy, kiro, opencode via `--vendor`), `rein hooks install`, `rein contract`, `rein drift`, `rein ledger`, `rein providers`.
Read `docs/adr/0001-rein-supervisor.md` first.

- Go 1.26+ (`go.mod`; module `github.com/voravitl/rein`); `gofmt -w . && go vet ./... && go test ./...` before every commit.
- Universal multi-agent rules live in `AGENTS.md`. All reviews must follow the 6-part Review Report Policy in `AGENTS.md` (Head SHA bound, criteria checklist with file:line proof, ranked findings, downstream handoff plan, and `rein verdict record`).
- The hook runs on EVERY tool call of EVERY session: stay silent outside a worker (no contract -> no output), keep startup in single-digit ms, never panic (a crash must not block the user's session).
- Inside a worker, unknown or broken state fails closed (deny with a reason the worker can act on).
- Project-specific values belong in a profile, never in code; never commit real profiles or personal provider configs.
- Never claim Windows support without a real Windows run (ADR rule 2).
- CLI command reference for all 16 subcommands: `docs/CLI_REFERENCE.md`.


