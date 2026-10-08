# Project pack: where a project's own rules live

The playbook, templates and scripts in this skill are project-neutral. Everything that belongs to one project (its
stack, gate commands, live services, owner rules, deploy notes) lives in a **project pack**: a local directory that
never goes into a public repository. The project's rein profile points at it:

```json
// ~/.config/rein/profiles/myapp.json
{ "name": "myapp", "worktree_root": "~/worktrees/myapp", "pack": "~/.config/rein/packs/myapp", "...": "..." }
```

rein itself ignores `pack`; the coordinator reads it at the start of a run and puts `PIPELINE_PACK=<absolute pack dir>`
in front of each `scripts/advise.sh` call, which appends the pack's advisor rules (and refuses to run without them
unless `ADVISE_NO_PACK=1`).

## Layout
| File | Used by | Contents |
|---|---|---|
| `PACK.md` | coordinator, steward (read before acting) | repo path and Orca repo id; stack; **gates** (exact commands, what "green" means, known load flakes, exit codes); **safety** (live containers, DB, ports, owner-only scripts, the read-only exceptions); isolated-stack recipe for testing merged main; deploy notes to carry in MR text; standing owner rules (for example "docs-only MRs may be merged after the checks") |
| `worker-rules.md` | pasted into every spec under the generic worker rules | gate commands for workers, protected resources, stack-specific build/test rules, shared files to keep small |
| `review-rules.md` | pasted into every review under the generic review rules | how the reviewer runs the gates safely, the one read-only exception (if any) |
| `advisor-rules.md` | appended by `advise.sh` | the protected resources an advisor must never touch |
| `gates/` | everyone | gate scripts: an isolated backend suite (throwaway DB), a frontend gate, a guarded e2e runner |
| `stack/` | steward `stack-test` | templates for an isolated copy of the app on other ports |

Gate scripts that drive docker usually must run outside the Claude Code sandbox: list them in the profile's
`sandbox_excluded_commands` with their absolute path (`/abs/pack/gates/backend-test.sh *`).

## Writing gate scripts that a coordinator can trust
- **Fail closed.** Exit nonzero when any step fails, when zero tests ran, or when a precondition cannot be checked.
  The coordinator reads exit codes, not summaries.
- **Never touch the live stack.** Use a throwaway database on its own network, other ports, a lock per port.
- **Prove it.** A guarded e2e runner lists the selected specs first (filters can match more than you think), refuses
  anything else, and compares live-DB counters before and after.
- **Stop only what you started.** Clean up your own processes and containers on exit.

Start from `examples/pack/` in the rein repository.
