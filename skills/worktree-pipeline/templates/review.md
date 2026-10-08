# Review <ID> round <k> (<model>): <task> — your review short name is `review-<id>-r<k>`

Your worktree `<path>` is detached at `<head>` (branch `<branch>`, based on origin/main `<base>`). Commits under review: `git log --oneline <from>..<head>`.
Read first: the task `<run>/specs/<id>.md`; the coordinator's answers `<run>/specs/<id>-answers.md` (if any); the design sections; the previous review `<run>/reports/review-<id>-r<k-1>.md` and the fix spec (for delta rounds); the worker report `<run>/reports/<id>.md` (do NOT trust it — verify every claim in the code).

**Review:**
1. Each scope item / previous finding: done / partly / not done, with file:line evidence.
2. Independent audit of the risky invariant(s): <e.g. a new field never widens access; test fixtures match the real API shapes per endpoint and role>.
3. Security/bypass attempts specific to this change: <list>.
4. Tests present, meaningful, not vacuous; re-run the red check yourself for at least <n> key rules and report each.
5. Out-of-scope changes and regressions in other screens/services.
6. Gates: <exact commands>; e2e only the way the project pack allows.
Stopping rule: APPROVE when what remains is theoretical or nits.

<paste templates/review-rules.md here>
