### Orchestrate with Rein

Use this playbook when a program needs multiple bounded work units, verified
handoffs and durable runtime evidence. A task that fits one session stays in
its normal playbook. Crux authors briefs and interprets evidence; Rein owns
dispatch, routing receipts, retries, leases, checkpoints and approval gates.

Read the shared runtime guide and the checked-out repository's
`docs/CLI_REFERENCE.md` sections on routing, run ownership, checkpoints and
bounded supervisor before issuing commands. Its current implementation is the
source of truth; do not invent flags or treat these examples as authorization.
[Rein CLI reference](https://github.com/voravitl/rein/blob/main/docs/CLI_REFERENCE.md)
contains the maintained command contract.

1. **Frame the work.** Record the user's outcome, non-goals, acceptance commands,
   budget, stop condition and dependency order. Give each unit its own worktree
   and contract. Its brief names owned paths, prohibited paths, exact revision,
   predecessor evidence, verification and the expected report. Workers preserve
   concurrent edits and do not recursively orchestrate.
2. **Prove ownership.** Inspect current run and contract identity before any
   mutation. Install the selected harness's task guards with
   `rein hooks install <task>` before taking the current contract hash. Unknown
   ownership, an occupied worktree or an incompatible run is a blocker; a chat
   restart, session label or Crux mode flag never grants takeover authority.
3. **Choose the supported runtime.** Schema 1 supervisor plans monitor already
   evidenced Orca dispatches; they do not launch workers or release remote
   leases. Schema 2 task graphs execute explicitly approved local,
   non-detaching shell providers only. Schema 3 graphs extend this with
   lightweight action nodes, explicit execution bounds, and dependency-guided
   worker integration via `rein dag compile` and `rein dag integrate`. Prepare
   a bounded plan against the actual schema, then run `rein supervisor validate --plan <plan.json>`.
   Only with existing authorization, bind that exact plan through
   `rein run start <repo> --run <run> --session <session> --pid <owner-pid> --supervisor-plan <plan.json>`
   and execute `rein supervisor run --plan <plan.json>`. Existing runs cannot
   be silently converted into supervised ownership.
4. **Use Rein selection and launch.** Where manual routing is the authorized
   lane, `rein route auto` takes the task profile and returns a bound decision.
   The decision supplies provider/agent/model/effort metadata, not argv. Use the separately authorized provider launch template, then let `rein route launch` validate its actual identity. Direct `rein orca launch` is a separate lane and does not consume this receipt. A receipt
   permits one execution; stale evidence, changed configuration or a retry
   requires a new Rein decision. Crux's preferred role/model mapping cannot
   override Rein's selection policy, funding, lease or guard checks.
5. **Carry forward existing evidence.** Carry contract/worktree/revision identity,
   current attempt, objective results and unresolved gaps in the next brief.
   Schema 2 local recovery passes its existing hashed continuation receipt as
   `REIN_HANDOFF_PATH` and `REIN_HANDOFF_SHA256`. The approved cooperative
   launcher must read and validate it. Do not create a competing Crux handoff
   state format or claim this restores a provider's native conversation.
6. **Reconcile before continuation.** Read Rein's recorded process/dispatch,
   checkpoint, verification and accounting evidence. Native collaboration
   notifications and Orca terminal visibility are observations, not completion
   receipts. Ambiguous liveness, remote termination or accounting preserves
   the fence. Never redispatch through an upstream coordinator to bypass it.
7. **Verify and review.** Run the unit's objective checks on its exact output
   revision. Separate source inspection, local checks, CI and deployed/runtime
   proof. Record independent review against the current head with Rein's
   verdict mechanism. `WAITING_HUMAN` is not approval; successful process exit,
   green CI and a reviewer recommendation do not replace `rein approve` or
   `rein verdict check` before a gated merge.
8. **Close with evidence.** Report the outcome against the acceptance criteria,
   worktrees and revisions, completed and blocked units, exact verification,
   receipt/checkpoint pointers and the remaining authorized next action.
   Preserve Rein's durable evidence for recovery and review.

Do not create an `orch` runtime, a separate `units.tsv`/`ledger.tsv` authority,
independent inbox acknowledgements, a second retry controller or a background
scheduler. The removed upstream orchestration helper is not a fallback for a
Rein refusal. Other harness/provider execution and remote Orca graph recovery
remain unverified or unavailable unless the actual runtime proves support.
