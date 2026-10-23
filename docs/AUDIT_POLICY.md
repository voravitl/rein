# .audit/ Policy

`.audit/` holds machine-generated audit and trail reports: project audits, review outcomes, release trackers, and handover records. It is evidence, not working state.

## Rules

- **Tracked in git.** `.audit/` stays committed (decision `75e526f`, reverting `8a2de5b`). Ignoring it would silently drop reports from the next commit that touches one. Do not re-add it to `.gitignore`.
- **Append-only.** Reports are point-in-time evidence bound to a date and, when relevant, a SHA. Never rewrite or truncate an existing report; supersede it with a new file.
- **No secrets.** New reports must not contain API keys, tokens, passwords, or private keys. Redact before committing.
- **Not a work order.** Run-scoped specs and per-run work orders (e.g. the removed `spec-b4.md`) do not belong in the repo — they carry machine-local paths and expire with their run.

## Naming convention

- Dated reports: `<topic>-<yyyymmdd>.md` — e.g. `release-crux-20261019.md`, `handover-codex-crux-20261018.md`.
- Commit-bound reports append the short SHA: `rein-project-audit-20261018-859a94a.md`.
- Living trackers keep a bare topic stem and are mutated in place by design: `crux-continuation.todo.md` / `.tsv`, `orca-goal-success.todo.md` / `.tsv`.

Companion data files (`.tsv`, `.todo.md`) reuse the stem of their report.
