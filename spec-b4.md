# Spec: Slice B4 — Standing Orders & Decision Trail

## Scope
- **B4.1** Decision Trail (`decisions.tsv`):
  - Every `AskUserQuestion` answered in the run's owner session is appended to `<run>/decisions.tsv` (append-only)
  - Fields: timestamp (RFC3339), question text, chosen option label, free text / notes
  - Free text is scanned for secrets (API keys, tokens, passwords, bearer tokens, AWS keys, sk- tokens, private keys) and redacted with `[REDACTED]` when detected
  - red-check: test appending decision row with simulated secret and verify redaction in decisions.tsv

- **B4.2** User-only `standing.md` write denial:
  - `<run>/standing.md` is written by the user only
  - Coordinator write attempt to `standing.md` (via Edit, Write, MultiEdit, NotebookEdit, or Bash redirection) is denied with: `user-only: standing.md cannot be written by coordinator (propose text for user to write)`
  - red-check: test coordinator write tool and bash write command to standing.md and verify denial

- **B4.3** SessionStart injection of `standing.md`:
  - SessionStart (`resume`, `compact`, `startup`) reads `<run>/standing.md` if present and injects into additionalContext
  - red-check: test coordSessionStart with standing.md present and verify additionalContext contains standing orders content

- **B4.4** Scope ruling & approval before spawn:
  - In `coordWorkerStart`, if a task is spawned outside the recorded scope ruling, require approval
  - red-check: test worker-start for task requiring approval and verify denial until allowed

## Ownership (machine-checked; the guard hook and the coordinator's drift check enforce it)
- Edit ONLY files matching: cmd/rein/main.go, cmd/rein/run.go, internal/decision, internal/decision/*, internal/decision/**, internal/run, internal/run/*, internal/run/**, internal/guard, internal/guard/*, internal/guard/**, internal/approval, internal/approval/*, internal/approval/**, internal/spec, internal/spec/*, internal/spec/**
- Never edit: **/node_modules/**, .git, **/.git/**
- Scope ids: your report needs one heading per id (B4.1, B4.2, B4.3, B4.4) with status done / partly / not done
- Report path (write it before you finish): /Users/voravit.l/.cache/worktree-pipeline/runs/rein-0.5/reports/b4-standing-orders.md

## Not in scope
- B5 and later roadmap items
- Cloud / remote secret manager integration

## Standing Orders
# Standing Orders for rein-0.5

1. Safety first: every guard rule is machine-checked, not prose-only.
2. Zero Claude Code personal quota: worker runs via Kiro CLI (AWS credits) or OpenCode GLM-5.3-Flash.
3. Fail closed: all gate checks must be 100% green before merge.

Report path: /Users/voravit.l/.cache/worktree-pipeline/runs/rein-0.5/reports/b4-standing-orders.md
Gates: gofmt -l cmd/ internal/ hooks/ && go vet ./... && XDG_CONFIG_HOME=/dev/null GIT_CONFIG_GLOBAL=/dev/null go test ./...
Timebox: 2h
