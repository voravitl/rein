# Cross-provider continuation

Rein can carry a local schema-2 supervised task from one approved provider to another after a configured quota signal. The successor receives the same working tree, verified partial-work checkpoint, original objective, run/task identity, contract allow/deny paths and verification commands. It starts a new provider invocation; native conversation/session state is not portable.

A handoff is permitted only after the previous process group is proven gone, checkpoint identity verifies, the previous reservation is settled and quota cooldown is recorded. The supervisor then obtains a fresh routing decision within the approved attempt bound. Unknown liveness and changed evidence stop progression. The successor’s fallback prompt cannot replace the original objective. Receipt and input digests are checked before launch; source provider and harness are included in the receipt.

## Running with installed providers

Use the schema-2 plan format in [CLI reference](CLI_REFERENCE.md). Before starting, configure both real harnesses in the provider inventory/worker chain, install their contract hooks, and authorize their launch argv and quota pools in the plan. Each provider needs valid catalog/probe evidence and the existing routing quality/budget requirements. Do not seed invented quality or cost evidence to satisfy these gates.

For example, one task’s `launches` can use these literal forms (provider keys must match your configuration):

```json
{
  "primary": ["/absolute/bin/agy", "--model", "{model}", "--effort", "{effort}", "--print", "Fix the approved task and run its verification commands."],
  "backup": ["/absolute/bin/opencode2", "run", "--standalone", "-m", "{model}#{effort}", "Continue the approved task."]
}
```

Set `max_attempts` to at least 2 and `process_policy` to `non_detaching`. Include objective verification argv in `verify`, explicit budget/time limits and the exact contract/config digests. Keep all task work in its distinct contracted worktree. In the owner terminal:

```sh
rein supervisor validate --plan /absolute/approved-plan.json
rein run start /absolute/repo --run RUN --session SESSION --pid OWNER_PID --supervisor-plan /absolute/approved-plan.json
rein run allow /absolute/repo --task TASK --reason 'approved bounded provider continuation'
rein supervisor run --plan /absolute/approved-plan.json
```

A configured literal quota signal triggers recovery; arbitrary errors do not. Restart with the same plan resumes only from complete durable evidence and does not launch a duplicate writer. Completion waits for independent review and human approval. Remote Orca recovery remains unsupported because remote termination cannot yet be proven.

## Evidence and validation

Under the contract index, `routes/supervisor/<attempt>-handoff.json` carries the objective, source identity, changed-file list and checkpoint/execution digests. The successor’s execution record carries `handoff_path`, `handoff_digest` and `input_digest`. Checkpoint and verification records are revalidated on supervisor restart. Private receipts can contain task text; launch ledger notes retain only the input digest.

`TestCrossProviderHandoffAgyToOpenCode` runs the production supervisor through two separate protocol-faithful CLI fixtures, including discovery, quota interruption, argv-to-stdin transport, preservation of partial edits, independent verification commands, accounting, lease release and restart. A deliberately different backup prompt proves the original objective is retained. Other recovery tests reject modified checkpoints, execution records and receipts.

These process tests do not establish live model comprehension, actual provider exhaustion or live cost. A live acceptance run must use two authenticated real providers, retain the above evidence, show that the successor completed the original objective from partial work, and pass independent verification/review. No real-provider result is claimed by this change. Receipts produced by older Rein versions without an original objective fail closed and require reconciliation.
