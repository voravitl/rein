# <project> pack

Local only: never commit this pack to a public repository. The rein profile `~/.config/rein/profiles/<project>.json`
points at it with `"pack": "~/.config/rein/packs/<project>"`.

## Project
- Repo: `~/src/<project>`; Orca repo id: `<id>`; worker worktrees: `~/worktrees/<project>/<task>`.
- Stack: <languages, frameworks, database>.
- Forge: GitLab (`glab`); MR pipelines: <yes/no>.

## Safety (put these into every spec through worker-rules.md)
- Live stack: <containers / compose project>, database <port>, app ports <ports>. Off limits for workers, reviewers
  and advisors. The profile's `protect_container_prefixes`, `protect_ports`, `deny_commands` enforce it.
- Owner-only scripts: <release, seed>.
- Read-only exceptions (if any): <e.g. the counter query gates/e2e-guard.sh runs before and after e2e>.

## Gates (exit code is the verdict)
| Gate | Command | Green means | Known flakes |
|---|---|---|---|
| Backend suite | `<pack>/gates/backend-test.sh <worktree> <name> ["filter"]` | 100% pass, >0 tests | <load timeouts: rerun alone> |
| Frontend | `<pack>/gates/frontend-gate.sh <worktree> <name>` | tests, type check, build all pass | |
| e2e (one spec) | `<pack>/gates/e2e-guard.sh <worktree> <spec>` | the listed spec only, live DB unchanged | |

## Isolated stack (test merged main)
1. Scratch worktree of `origin/main`.
2. Start the app under its own project name and its own ports, with a read-only copy of the data.
3. Smoke checks, then the full e2e against the isolated ports.
4. Teardown is mandatory; prove nothing of the isolated stack is left.

## Deploy notes to carry in MR text
- <migrations that need extensions or rights, feature switches, rollout scheduling>

## Standing owner rules
- <e.g. "docs-only MRs (doc files and their images only, not AGENTS.md/CLAUDE.md) may be merged after the checks; a
  release or deploy always needs an explicit yes">
