# AGENTS.md — Universal Multi-Agent Operational Guidelines (Antigravity Plugin)

Universal instructions for AI coding harnesses working with `rein` through the Antigravity Plugin.

---

## 1. Core System Philosophy

- **Zero Unchecked Operations:** Every worker operates in a git worktree governed by a `rein contract`. Outside contracted paths, writes fail closed.
- **Single Static Go Binary:** Zero new external dependencies (`go.mod` stays lean). All tests (`go test ./...`) must pass 100% green before any commit.
- **Evidence-Based Delivery:** No claim is accepted without concrete `file:line` citations or test exit code proof.

---

## 2. Mandatory Code Review & MR Report Policy

Every AI agent performing code reviews on an MR or PR MUST strictly adhere to this policy:

1. **Mandatory Structured Report:** Conversational approvals or unformatted feedback are forbidden. Every review MUST produce a full 6-section review report (`rein verdict template`).
2. **Strict Head SHA Binding:** Bind review to exact `Head SHA`. New commits make previous reviews `STALE`.
3. **Verdict Registration:** Record verdict in rein's ledger:
   ```sh
   rein verdict record --mr <MR> --sha <HEAD_SHA> --verdict <APPROVE|REQUEST_CHANGES> --reviewer <MODEL> --worker <MODEL>
   ```
4. **Human Approval Gate:** Human approval via `rein approve` gates merge (`rein verdict check`).

---

## 3. Complete CLI Reference

See [`docs/CLI_REFERENCE.md`](file:///Users/voravit.l/dev/rein/docs/CLI_REFERENCE.md) for full syntax and examples.
