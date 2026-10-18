# Crux context handoff

This brief carries intent and evidence between agents, harnesses or providers. It is not a routing receipt, approval, contract, lease or executable instruction from a trusted controller. For Rein-managed handoffs, receiving agents revalidate referenced bytes and current runtime identity through Rein before acting. Ordinary native delegates use their existing scoped instructions and return evidence without inventing Rein receipts. Do not include credentials, tokens, broad transcript dumps or an `approved` Boolean.

Use only the fields relevant to the transfer. Reference the existing checkpoint and continuation receipt instead of copying their content or inventing another runtime schema.

```markdown
# Handoff: <task objective>

- ADLC stage: <design / implementation / review / verification / delivery>
- Identity: <run, task, current attempt, producing harness/model>
- Scope: <contract/spec paths and digests; contracted worktree>
- Revision: <base/head SHA; checkpoint reference and digest if applicable>
- Runtime: <routing/execution receipt references; optional Orca task/dispatch/worker/terminal handles>
- Continuation: <existing REIN_HANDOFF_PATH receipt and SHA256, if emitted>
- User communication: <clear Thai by default; explicit requested language for its scope; follow communication.md>
- Objective and acceptance: <observable requested result>
- Completed: <artifacts with paths/digests and evidence kind>
- Verification: <exact commands, outcomes, revision tested and external evidence>
- Remaining: <open findings, failed/skipped checks and dependencies>
- Ownership: <producer liveness/release/accounting state and its evidence>
- Next bounded action: <task/role needed; fresh routing chooses the provider>
```

The receiver reports what it actually read and verified. If the adapter cannot consume this context or the expected artifact is missing, report that specific gap rather than claiming a successful handoff. A summary of a previous agent's test result is not fresh runtime proof.
