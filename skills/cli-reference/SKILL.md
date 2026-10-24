---
name: cli-reference
description: "Complete CLI command reference and operational guide for rein. Use whenever you need to know how to invoke rein commands (config, contract, drift, hook, hooks, run, budget, task, ledger, providers, route, verdict, approve, advise, orca, e2e, spec, sandbox, checkpoint, hunter, supervisor, version), understand CLI flags, interpret exit codes, or find copy-paste operational recipes."
---

# `rein` CLI Command Reference & AI Guide

This skill provides AI models (coordinators, workers, reviewers, advisors) with immediate, authoritative usage instructions for the full `rein` command set.

---

## 1. Quick Command Dispatch Matrix

| When you need to... | Run this command | Key flags | Exit code |
|---|---|---|---|
| Set task boundary for worker | `rein contract new` | `--name`, `--allow`, `--scope`, `--run-dir` | `0` = ok |
| Write/inspect Crux provider config | `rein config init/check --dir DIR` | `--model`, `--quota` | `0` = ok, `1` = error, `2` = invalid flag |
| Inspect active task allowlist | `rein contract show <name>` | `<name>` | `0` = ok |
| Guard worker worktree | `rein hooks install <name>` / `rein hooks uninstall <name>` | `--vendors` | `0` = installed, `2` = refused |
| Inspect a PreToolUse hook event | `rein hook` | `--vendor` | `0` = allow, `1` = bad flag (deny via stderr) |
| Write OS sandbox profile | `rein sandbox <name>` | `<name>` | `0` = written |
| Check for scope drift / false claims | `rein drift <name>` | `--expect-guard`, `--claimed-files` | `0` = clean, `1` = drift |
| Start coordinator run | `rein run start [repo]` | `--run <name>`, `--profile` | `0` = ok |
| Audit coordinator worktrees | `rein run audit [repo]` | `--pinned sha` | `0` = ok, `1` = drift |
| Check token & cost budget | `rein budget check` | `--task`, `--run` | `0` = ok, `2` = hard cap |
| Check if worker is stuck/dead | `rein task status <task>` | `<task>` | String status |
| Record model spending/ledger | `rein ledger add` | `--task`, `--worker`, `--rounds` | `0` = recorded |
| Check provider fallback chains | `rein providers` | `--chain`, `--timeout` | `0` = ok |
| Pick task route + model automatically | `rein route auto` | `--task`, `--run`, `--profile-file`, `--worker-model` | `0` = receipt written, `1` = refused |
| Generate Review Report scaffold | `rein verdict template` | `--mr`, `--sha`, `--reviewer`, `--worker` | `0` = printed |
| Record code review verdict | `rein verdict record` | `--mr`, `--sha`, `--verdict`, `--reviewer` | `0` = recorded |
| Verify MR merge readiness | `rein verdict check` | `--mr`, `--sha`, `--tier` | `0` = pass, `1` = fail |
| Render terminal-only approval preview | `rein approve prompt` | `--mr`, `--sha` | `0` = JSON preview and terminal command; interactive approval disabled |
| Record human approval | `rein approve` | `--mr`, `--sha`, `--reason`, `--as-directed` (inside an AI session) | `0` = approved, `1` = refused |
| Run secondary advisor model | `rein advise <role>` | `<role>`, `<dir>`, `<task>`, `<out>` | `0` = finished |
| Run native Orca worker lifecycle | `rein orca <launch\|wait\|drain\|close\|tidy>` | `--run`, `--terminal`, `--worktree` | `0` = ok, `1` = error, `3` = dirty, `4` = unique commit |
| Run E2E test via Orca browser | `rein e2e test` or `run` | `<url-or-spec>`, `--out`, `--json` | `0` = pass, `1` = fail |
| Lint task spec before launch | `rein spec check` | `<spec>`, `<contract>` | `0` = pass, `1` = lint fail |
| Snapshot / restore a worktree safely | `rein checkpoint <finalize\|verify\|restore>` | `--task`, `--attempt`, `--run`, `--worktree` | `0` = success, `1` = refused |
| Evidence-based bug finding workflow | `rein hunter <report\|reproduce\|verify\|review\|approve>` | `--id`, `--repo`, `--attempt` | `0` = recorded, `1` = refused |
| Validate / run a bounded plan | `rein supervisor <validate\|run>` | `--plan`, `--invocation` | `0` = verified, `1` = blocked |
| Print binary version | `rein version` | — | `0` = ok |

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
Consult the complete command manual (full flags, exit codes, refusal codes, recipes) at:
[docs/CLI_REFERENCE.md](https://github.com/voravitl/rein/blob/main/docs/CLI_REFERENCE.md)
