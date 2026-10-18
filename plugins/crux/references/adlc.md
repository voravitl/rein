# ADLC with Rein and Orca

Crux coordinates the engineering workflow: clarify the objective and acceptance evidence, choose a design when needed, implement in scope, independently review the exact revision, verify behavior, and deliver through the project's release process. These are guidance stages, not a new Rein task-state machine. Skip irrelevant ceremony for a small task.

## Existing integration points

Use the repository's `docs/CLI_REFERENCE.md` and installed command help for current syntax. These commands exist in Rein; listing them does not prove a live provider connection.

| Boundary | Existing Rein interface | Required evidence |
| --- | --- | --- |
| Task scope | `rein contract new`, `rein spec check` | Exact contract/spec and contracted worktree |
| Controller ownership | `rein run start` and the documented run lifecycle | Current controller identity, not a copied session label |
| Provider selection | `rein route discover`, `rein route auto`, `rein route launch` | Fresh routing receipt and launch metadata, plus the separately supplied authorized launch template |
| Worker transport | `rein orca launch`, `rein orca wait`, `rein orca drain` | Exact run/task/dispatch/worker identity |
| Transferable files | `rein checkpoint finalize`, `verify`, `restore` | Verified checkpoint digest and contract/run identity |
| Attempt accounting | `rein route settle`, `rein ledger` | Required termination and accounting evidence |
| Revision review | `rein verdict record`, `rein verdict check`, `rein approve` | Exact HEAD and the project's explicit approval gate |

Follow each interface's prerequisites. Do not run `route auto` and an unrelated direct launcher as if they consumed the same routing receipt. A controller owns launch integration; a child returns its result to the controller instead of claiming authority to launch its replacement.

Orca `worker_handle` and `terminal_handle` are distinct. Structured workers may have no PTY; terminal read/send is not a universal worker protocol. Preserve `task_id`, `dispatch_id`, worktree and available handles from real launch receipts rather than reconstructing them from names.

## Execution lanes

Choose one supported lane before dispatch. Native bounded delegates use the host collaboration surface. Rein schema 2 graphs execute local non-detaching providers with task launch templates. Direct `rein orca launch` is a separate transport/worktree lane and does not consume a `route auto` receipt. `route launch` may accept only the specific transports/harnesses its current guard allows; supervised graphs reject remote Orca, and routed Codex/Kiro/OpenCode require direct shell. Never combine these paths by assumption.

## Provider handoff

1. Capture the actual completed work, pending acceptance criteria and verification gaps.
2. Finalize/verify the current attempt's checkpoint using the existing Rein boundary. Inspect unsupported Git states instead of forcing a restore.
3. Preserve the old worker's lease until the required liveness, termination and accounting evidence permits settlement. Timeout is not release.
4. Give the next controller/worker the [context brief](handoff.md) plus references to existing scope and runtime receipts.
5. Obtain a fresh eligible routing receipt for the next attempt. The receiving adapter must actually consume the supplied context; verify this in its output and artifacts.
6. Reverify the resulting exact revision. Earlier review of a different HEAD is stale.

Rein already emits a bounded continuation receipt in `cmd/rein/graph_recovery.go`. It contains `run`, `task`, `previous_attempt`, `checkpoint_digest`, `execution_digest`, `progress` and `next_action`, then validates its digest and exports `REIN_HANDOFF_PATH`/`REIN_HANDOFF_SHA256`. Use that receipt; Crux's prose brief supplements it and does not replace its validation.

Current graph execution/recovery is documented for local non-detaching providers. Remote Orca recovery remains fenced. A checkpoint transfers files and metadata; it does not restore an arbitrary provider's native conversation. Do not remove these restrictions through workflow instructions.

## Evidence needed before claiming interoperability

A real pilot must show one provider completing a bounded slice, a verified checkpoint and settled attempt, another eligible provider demonstrably receiving the continuation context, and final independent behavior verification. Record the actual harnesses, models, receipts and skipped checks. Until that pilot succeeds, report multi-provider continuation as unverified.
