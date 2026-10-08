# Task <ID>: <one-line goal> (GitLab issue #<n>) — short name for gates: `<name>`

Your worktree is a fresh branch from origin/main <sha> (<what main already contains that matters>). Read `AGENTS.md` and <design docs + sections>. Documents may be Thai; code, comments, commits and tests are English.

## Owner decisions (<date>)
- <id>: <choice> — <one line of what it means for the code>

## Scope
- **S1** <deliverable with exact files/endpoints/behaviour; quote contracts (DTO shapes, error codes) from the design>
- **S2** ...
(Scope ids S1..Sn are the contract's `--scope`; the report needs one section per id with status done / partly / not done.)

<paste `rein contract show <name>` here: the machine-checked ownership, never-edit list, scope ids and report path>

## Not in scope
- <later MRs; things the worker must not start>

## Tests
- <named rows from the design>; every new test gets a red check (break the rule, see it fail for the expected reason, restore) listed in the report.

<paste templates/worker-rules.md here, with Ownership filled in and the gate commands for this repo>

## Report
`<run>/reports/<name>.md`: per scope item → files → tests → red check; final gate numbers; deviations with reasons; open questions. Then send worker_done with `--report-path` and `--files-modified` (exactly once; heartbeat every 5 min while working).
