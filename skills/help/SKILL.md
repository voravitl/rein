---
name: help
description: "Interactive help, architecture guide, cross-vendor agent workflows, and troubleshooting manual for rein (Deterministic Multi-Agent Worktree Guardrails, Contract Locking, and Orca Orchestration). Use whenever asking how to use rein, how contracts/drift/verdicts work, how to coordinate Orca workers, manage vendor quotas (Claude/Codex/Antigravity/Kiro), or troubleshoot gate failures."
---

<!-- argument-hint: [topic | command | workflow | quota | orca | gates] -->

# `rein` Interactive Help & Operational Manual

`rein` is a deterministic supervisor and guardrail engine for AI coding harnesses (**Claude Code**, **OpenAI Codex**, **Google Antigravity**, **AWS Kiro**, **OpenCode**, **Hermes Agent**). It ensures zero unchecked operations by binding workers to git worktree contracts, tracking multi-model telemetry, and natively coordinating parallel Orca workers.

---

## 1. Core Mental Model (The 8-Stage Loop)

```text
[Task / Issue]
       │
       ▼
 1. Contract Locking (`rein contract new`)  ──► Restricts writes to scoped filepaths
       │
       ▼
 2. Hook & Sandbox (`rein hooks install`)   ──► PreToolUse guard blocks out-of-scope edits
       │
       ▼
 3. Native Orca Dispatch (`rein orca launch`) ──► Starts worker in clean worktree
       │
       ▼
 4. Bounded Execution & Heartbeats          ──► AI works; sends periodic heartbeats
       │
       ▼
 5. Anti-Drift Audit (`rein drift <task>`)   ──► Proves changes match claims & contract
       │
       ▼
 6. Cross-Vendor Review (`rein verdict record`) ──► Independent maker review report
       │
       ▼
 7. Human Approval Gate (`rein approve`)     ──► Explicit human sign-off required
       │
       ▼
 8. Safe Cleanup (`rein orca close`)         ──► Verifies clean tree & anchored commits
```

---

## 2. Topic & Guide Index

| Topic | When to Use | Key Command / Reference |
|---|---|---|
| **Task Contracts** | Restricting what files a worker can modify | `rein contract new --name T --scope S --allow 'path/**'` |
| **Anti-Drift Verification** | Verifying claims vs git diff before saying done | `rein drift <name> [--expect-guard <vendor>]` |
| **Native Orca Launch** | Starting workers in Orca with automatic worktree setup | `rein orca launch <name> <base> <spec> <agent> [--model M]` |
| **Orca Waiter & Drain** | Waiting for `worker_done` without swallowing signals | `rein orca wait` followed by `rein orca drain` |
| **Worker Close & Safety** | Releasing dispatch, removing worktree, keeping commits | `rein orca close --worktree <wt> [--terminal H]` |
| **Quota Management** | Checking limits across Claude, Codex, AGY, Kiro | `rein providers` and `rein route auto` |
| **Multi-Model Reviews** | Gating MRs with independent vendor reviews | `rein verdict template` & `rein verdict record` |
| **Human Approval** | Final merge authorization | `rein approve --mr N --sha <SHA>` |

---

## 3. Native Orca Orchestration Cheatsheet

`rein orca` integrates directly with Orca IDE without external shell/python scripts:

### 3.1 Spawning a Worker
```sh
# Launch an Antigravity worker (Gemini 3.8 flash high, auto sandbox headers)
rein orca launch fix-auth main specs/fix-auth.md agy

# Launch a Kiro worker (company-paid credits, Claude Sonnet 5.5)
rein orca launch backend-sync main specs/backend.md kiro --model claude-sonnet-5.5 --effort high

# Launch a supervised Codex worker
rein orca launch review-gate main specs/review.md codex --model gpt-6.1-sol
```

### 3.2 Supervised Waiting & Clean Draining
```sh
# 1. Wait in background for signal (worker_done, escalation, question)
rein orca wait --terminal "$ORCA_COORD" --timeout-ms 600000

# 2. Drain pending deliveries (counts heartbeats, prints reports, ACKs delivery)
rein orca drain --terminal "$ORCA_COORD"
```

### 3.3 Safe Cleanup (Zero Data Loss)
```sh
# Closes worker, releases dispatch, removes worktree
rein orca close --worktree ~/orca/workspaces/rein/fix-auth --terminal term_123 --dispatch disp_456

# If it exits with refusal (unique commits not in other refs):
# 1. Anchor commits: git branch keep/fix-auth <HEAD_SHA>
# 2. Re-run close command
```

---

## 4. Cross-Vendor Model & Quota Matrix

| Harness | Primary Model | Quota Unit | Best For | Quota Exhaustion Behavior |
|---|---|---|---|---|
| **Claude Code** | `claude-sonnet-5-5` / `claude-opus-5-5` | Subscription % | Coordinator session only | 429 / Rate Limit -> Keep in main session |
| **Codex** | `gpt-6.1-sol` | API / Credits | Independent review gate, backend | 429 / Out of credits -> Fallback to Kiro |
| **Antigravity** | `gemini-3.8-flash-high` | Gemini Requests | Docs, frontend, fact-checking | Exit 3 / RESOURCE_EXHAUSTED -> Fallback |
| **Kiro** | `claude-sonnet-5.5` / `claude-opus-5.5` | AWS IAM Credits | Heavy implementation, risky review | Monthly limit reached -> Cool down 2h |

> **Hard Invariant**: **Writer is NEVER the reviewer**. A change written by Kiro/Claude (Anthropic) must be reviewed by Codex (OpenAI). A change written by Codex must be reviewed by Kiro-Opus or Claude-Opus.

---

## 5. Troubleshooting & Exit Code Reference

| Exit Code | Meaning | Remediation |
|---|---|---|
| `0` | Success / Clean | Proceed to next pipeline stage |
| `1` | General Error / Drift | Check stderr; compare git diff against contract scope |
| `2` | Argument / File Error | Check command syntax via `rein <cmd> --help` |
| `3` | **Refused: Dirty Tree** | Worker left uncommitted files. Commit or discard before `rein orca close` |
| `4` | **Refused: Unique Commits** | HEAD commits exist only on this branch. Run `git branch keep/<name> <SHA>` before closing |

---

## 6. Full CLI Reference

For detailed flags and low-level options of all subcommands, consult [`docs/CLI_REFERENCE.md`](docs/CLI_REFERENCE.md) or invoke `rein:cli-reference`.
