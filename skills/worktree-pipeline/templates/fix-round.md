# <ID> fix round <k> (issue #<n>) — on top of <head sha> (same worktree, do not rebase)

The review gate (<model>) reviewed <range>: `<run>/reports/review-<id>-r<k>.md` — <VERDICT> with <counts>. Read the whole file and its evidence. Fix in NEW commit(s):
1. **<finding id> — coordinator decision: <the one way you want it fixed, with the design/backend reference>.** <exact behaviour + the integrated test scenario that proves it>
2. **<finding id>:** <fix + test>
3. NITs: fix <ids> (small); leave <ids> because <reason> and say so.
4. A red check for every new or changed test (one mutation, one run, restore); list the results.
Gates (paste summary lines): <the project pack's gates, e.g. backend suite / frontend tests / type check / build / guarded e2e>. Add a "Fix round <k>" section to the report (finding → commit → test → red check). Then send worker_done (do not forget it) with `--report-path` and `--files-modified`. All earlier rules apply.
<If the gate failed: state the failing test, the root cause you found (e.g. a fixture shape the backend can never send, with file:line), and forbid weakening assertions.>
