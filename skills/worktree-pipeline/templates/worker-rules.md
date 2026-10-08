## Rules for every worker (paste into the spec; adjust Ownership)
- Repository: <project, stack>. Read `AGENTS.md` and the design documents named in the spec. Code, comments, commits and tests are English.
- **Ownership:** edit ONLY what your task needs, inside YOUR worktree (the machine-checked block above is the hard limit). Other workers change other areas at the same time: <list them and their files>. Keep changes to shared files small and local so the later rebase is easy.
- **Do not edit:** design documents (except where your task says so), version files, release scripts, and anything in the never-edit list above.
- **Git:**
  - Use Conventional Commits that end with the issue reference.
  - NEVER push, open a merge request, comment on the forge, or rebase/reset other branches.
  - Never commit build output, dependency folders or logs.
- **The running application is OFF LIMITS:** its containers, volumes, images, database and ports (the project rules below name them). Never run `docker volume prune` or `docker system prune`.
- **Build and test only through the project's gate commands** (below). Never run two full suites at the same time. The full suite must be 100% green at the end.
- **Dependencies:** no new dependencies and no package installs unless the spec says so.
- **Quality bar:**
  - no TODO/FIXME, no skipped or only tests, no stubbed branches;
  - every new test gets a red check: break the rule it guards, see it fail for the expected reason, then restore. List each one in the report;
  - match the surrounding style.
- **When the design is unclear:** use the preamble's `ask` command with the options you see; do not guess.
- **Report:**
  - write `<run>/reports/<short name>.md`: one section per scope id (`S1`, `S2`, ...) with status `done` / `partly` / `not done` and evidence (files → tests → red check); final numbers; deviations with reasons; open questions;
  - then send worker_done with `--report-path` and `--files-modified`;
  - send a heartbeat every 5 minutes while working.
- **Review gate:** a separate read-only reviewer. Its findings come back to you as a follow-up task in this terminal.

<paste the project pack's worker-rules.md here: gate commands, protected resources, stack-specific rules>
