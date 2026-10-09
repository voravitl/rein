---
name: cli-reference
description: "Complete CLI command reference and operational guide for rein. Use whenever you need to know how to invoke rein commands (contract, drift, hook, run, budget, task, ledger, providers, verdict, approve, advise, e2e, spec, sandbox), understand CLI flags, interpret exit codes, or find copy-paste operational recipes."
---

# `rein` CLI Command Reference & AI Guide

This skill provides AI models (coordinators, workers, reviewers, advisors) with immediate, authoritative usage instructions for all 16 `rein` commands.

---

## 1. Quick Command Dispatch Matrix

| When you need to... | Run this command | Key flags | Exit code |
|---|---|---|---|
| Set task boundary for worker | `rein contract new` | `--name`, `--allow`, `--scope`, `--run-dir` | `0` = ok |
| Inspect active task allowlist | `rein contract show <name>` | `<name>` | `0` = ok |
| Guard worker worktree | `rein hooks install <name>` | `--vendors` | `0` = installed |
| Write OS sandbox profile | `rein sandbox <name>` | `<name>` | `0` = written |
| Check for scope drift / false claims | `rein drift <name>` | `--expect-guard`, `--claimed-files` | `0` = clean, `1` = drift |
| Start coordinator run | `rein run start [repo]` | `--run <name>`, `--profile` | `0` = ok |
| Audit coordinator worktrees | `rein run audit [repo]` | `--pinned sha` | `0` = ok, `1` = drift |
| Check token & cost budget | `rein budget check` | `--task`, `--run` | `0` = ok, `2` = hard cap |
| Check if worker is stuck/dead | `rein task status <task>` | `<task>` | String status |
| Record model spending/ledger | `rein ledger add` | `--task`, `--worker`, `--rounds` | `0` = recorded |
| Check provider fallback chains | `rein providers` | `--chain`, `--timeout` | `0` = ok |
| Generate Review Report scaffold | `rein verdict template` | `--mr`, `--sha`, `--reviewer`, `--worker` | `0` = printed |
| Record code review verdict | `rein verdict record` | `--mr`, `--sha`, `--verdict`, `--reviewer` | `0` = recorded |
| Verify MR merge readiness | `rein verdict check` | `--mr`, `--sha`, `--tier` | `0` = pass, `1` = fail |
| Render human approval prompt | `rein approve prompt` | `--mr`, `--sha` | `0` = printed JSON |
| Run secondary advisor model | `rein advise <role>` | `<role>`, `<dir>`, `<task>`, `<out>` | `0` = finished |
| Run E2E test via Orca browser | `rein e2e test` or `run` | `<url-or-spec>`, `--out`, `--json` | `0` = pass, `1` = fail |
| Lint task spec before launch | `rein spec check` | `<spec>`, `<contract>` | `0` = pass, `1` = lint fail |

---

## 2. Common AI Operational Recipes

### Recipe A: Setting up a Worker Task (Coordinator)
```sh
# 1. Create contract with allowlist
rein contract new --name task-1 --run-dir ~/runs/sprint-1 \
  --allow 'internal/auth/**,cmd/app/**' --scope S1,S2 --report-path ~/runs/sprint-1/reports/task-1.md

# 2. Install guard hooks into worktree
rein hooks install task-1 --vendors claude,codex,agy

# 3. Write OS sandbox profile
rein sandbox task-1
```

### Recipe B: Pre-Done Verification (Worker)
Before sending `worker_done`, verify your own worktree:
```sh
rein drift task-1 --expect-guard claude --claimed-files internal/auth/login.go
```
- If exit code is `1`, read the `DRIFT` lines and fix out-of-scope edits or missing scope evidence.

### Recipe C: Reviewing an MR (Reviewer)
```sh
# 1. Generate standard Review Report template bound to Head SHA
rein verdict template --mr 10 --sha $(git rev-parse HEAD) --reviewer codex:gpt-5 --worker claude:sonnet > report.md

# 2. Inspect code, run tests, fill report.md

# 3. Record review verdict in ledger
rein verdict record --mr 10 --sha $(git rev-parse HEAD) --verdict APPROVE --reviewer codex:gpt-5 --worker claude:sonnet
```

### Recipe D: E2E Verification & Board-PDF (Tester / Coder)
```sh
# Smoke test a route and automatically build executive PDF report
rein e2e test http://localhost:3000 --out report/e2e
```

---

## 3. Full Reference
Consult the complete 16-command manual at:
[docs/CLI_REFERENCE.md](file:///Users/voravit.l/dev/rein/docs/CLI_REFERENCE.md)
