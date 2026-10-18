# Todo — orca-goal-success (opencode session 2026-10-17)

1. [x] Read Principles section of poteto-mode (skill loaded; leaves read: fix-root-causes, sequence-verifiable-units, model-the-domain)

## Figure-it-out phases (verbatim, from playbooks/figure-it-out.md)
- [x] Phase A: Ground first, then commit. Predicate = the three-level def-of-done (D0 in-session repo green + defect fixes; D1 artifact usability; D2 owner-gated pilot/Orca decisions enumerated). Framed in trail rows `frame` + `baseline`, checkpoint given as this run's progress reports.
- [x] Phase B: Decompose into atomic, independently-landable units. Units U1(a–e)–U6 below; riskiest-unknown-first (the docker-red test). Verification harness = the two previously-red docker lanes + new unit seams, baseline captured pre-change. Design decision logged (`design` row); fan-out skipped with reason below.
- [x] Phase C: hypothesis → smallest change → measured on real artifacts (docker lanes, mutations, SIGABRT dump); one instrument-verified rejection (my own pipe-direction test bug) recorded and fixed. Verdicts under Phase C: VERIFIED where stated; NOT VERIFIED items named explicitly (live pilot, cross-model review of this work).
- [x] Phase D: `.audit/orca-goal-success.tsv` (28 rows at reply time); committed? NOT committed (standing no-commit constraint).
- [x] Phase E: whole checked vs the Phase A predicate; recurring correction (windows test-compile ungated) encoded as a ci.yml GOOS=windows vet step.

## Concrete units (Phase B design, executed under Phase C loop)

- [x] U1a swap `run.ProcStart` call sites in internal/routing to a package-level seam var (behavior-preserving; routing tests green)
- [x] U1b red lease tests: finished-before-pin recording + unchanged fail-closed for unknown procfs errors (RED observed, then fixed; first version hung on a pipe-direction bug in my own test — caught by goroutine dump)
- [x] U1c lease change: `ChildStartUnknown` state (struct, SetChildGroup, CompleteLocal); red → green, mutation-1 red/green pin
- [x] U1d graph evidence variant (`pid_start_unknown`) + graphLocalGone validation + routeLaunch copy + cmd/rein red/green tests; mutation-2 red/green pin
- [x] U1e re-verify: docker cmd/rein flake-lane ×3 straight green + docker full cmd/rein 5.1s + docker full routing 5.8s + host routing full 41.4s
- [x] U2 docs: lease/route citations refreshed (lease.go:97,166; route_launch.go:204-286 + lease.go:197-246 + settle.go:48); advise.sh citation re-verified; ARCHITECTURE §2 documents the new state; backlog line added; 2 report files extended; fresh snapshot git-status-after-opencode-prepin.txt (114 items)
- [x] U3 usability: built /tmp/runs/run1/rein-opencode; version/usage/hook lifecycle (contract → hooks install opencode → plugin files → uninstall clean, empty dirs remain by design) in disposable HOME; fail-closed UX verified twice
- [x] U4 Orca gate C: installed 1.4.224; 1.4.225/1.4.226 changelogs have no worker-stop-verdict; upstream in-flight PRs named (#14977 ptyStopVerdict "Not merged", #15105, #15244/15261/15235); BLOCKED stays honest
- [x] U5 aggregate gates: host go test ./... exit 0; docker longrun-ci:v1 full 21/21 exit 0; vet clean; gofmt clean; Windows cross-vet caught my missing !windows tag then clean; amd64 build OK; python pipeline 85 OK; host hook-isolation aggregate from codex preserved
- [x] U6 trail audit (28 rows), ci.yml gate encoded (GOOS=windows vet), reply with attention section
- [x] U7 census same-host verifier (Option A, user-approved): internal/orca/census.go + census_linux.go/census_darwin.go/census_unsupported.go; `rein orca census record --pid N --out F` / `census verify --in F` (exit 0 gone / 1 present / 2 unsupported-foreign); real-process tests: tree lifecycle, strict zombie(id stays present), residual spawn-after-record honest proof, decision table, missing-root fail-closed; mutation M1(zombie=gone) RED + M2(recycle check off) RED, restored GREEN; host 21/21 exit 0 (/tmp/census-gate-host.log)
- [x] U8 subject clarity (user-approved parallel thread): OrcaSubject composes `rein <name> · <agent>:<model>` for --task-title/--title + LaunchResult.Subject; red(nonexistent symbol)→green→mutation RED/green; stale key in cmd/rein/orca_flags_test.go updated by main thread; no attempt counter existed in scope — none fabricated

## Skip log
- skip: agent/arena fan-out for the design — precedent exists and the domain rules are already encoded in lease.go; multi-model arena would be ceremony (laziness-protocol)
- skip: `how` skill pass for this unit — subsystem was already read end-to-end this session across three verification rounds (route_launch → lease → graph_adapter → proc impls)
- skip: rebuilding a hand-made production plan.json for a disposable supervised run — the graph CLI tests drive the same cmdSupervisor code in-process including the patched path; a second hand-built fixture duplicates them with no new signal (sequence-verifiable-units)
- skip: cross-model review of this continuation — no independent reviewer (codex/claude subagent on another model family) is wired in this OpenCode session's tool set; the trail + mutation/dump evidence is what stands in for it; reviewer slot remains open for the owner
- [x] U9 release round (2026-10-18, owner-approved): commit 72941c1 + 3d090bd pushed to origin/main, tag v0.8.0 -> release workflow; pre-commit audit of all 115 items (binary excluded via .gitignore, no secrets, deletions intentional); local tags now consistent 0.8.0 across plugin/package manifests
- [x] U10 release incident + latch (2026-10-18): gitignore pattern ate 25 cmd/rein sources; tag rebuilt v0.8.0 at ec9cbe5, release live with assets, CI green; guard `scripts/preflight-push.sh` added — run before any future push
