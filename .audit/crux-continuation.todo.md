# Todo: crux-continuation (session pickup จาก Codex + เดินงาน audit ต่อ) — 2026-10-18

## Session pickup (playbook steps, verbatim)

1. [x] Read the project checkpoint first using Resume storage. A successful read gives the note and artifacts without requiring a path from the previous session. If the pointer is missing, locate the prior trail through a local transcript under Claude Code's per-project transcripts directory at `~/.claude/projects/<encoded-cwd>/*.jsonl` (where `<encoded-cwd>` is the workspace cwd with `/` → `-`. Do not glob across other directories under `~/.claude/projects/`, that crosses workspace boundaries and reads private chats from unrelated projects), a cloud-agent URL, or a pushed branch. An unavailable or incomplete checkpoint needs diagnosis rather than a silent fallback. For transcript fallback, read the metadata overview and last messages first, then scan back for the decision points. Parse a long transcript in a subagent and keep the reduced timeline in the main thread (the principle-guard-the-context-window skill).
   - checkpoint = conversation checkpoint + `.audit/handover-codex-crux-20261018.md` + codex rollout `~/.codex/sessions/2026/10/18/rollout-2026-10-18T06-57-02-01a14c4c-...jsonl`
2. [x] Reconstruct operational state. The branch and worktree, what already landed (`git log`, `git diff` against the base), the open todos, the decisions made. Use the prior trail to recover progress. For claims about human rulings, follow Claims about human decisions. An agent summary is not the original ruling.
   - HEAD `2b118c4` ทุก worktree; WIP uncommitted: main 63 (ก่อนเพิ่มไฟล์ .audit), crux 62, census-test 1
   - landed: census F-11 fix (main), Crux readiness 4 runtime, python 86 tests
3. [x] Diff done vs pending. Compare what shipped against what was planned, name the resume point, do not re-run the prior repro or redo completed work. Recheck claims that affect the remaining work without repeating completed investigations. Read artifacts linked by the resume note, including requested questions or checklists.
   - resume point: sync 2 ทาง → verify → audit F-01 เป็นต้นไป (audit §5 ลำดับ)
4. [x] Route the remaining work to the matching playbook and pick the verdict: continue the execution, ship a finished recommendation, ratify or override a prior conclusion, or postmortem a failed run. The pickup playbook ends here. The routed playbook owns the rest.
   - verdict: continue the execution; routed = Bug fix (audit F-01/F-02)
5. [x] Verify the inherited claims against the original goal on the real artifact (the principle-prove-it-works skill). A passing prior self-report is not the proof.
   - ตรวจ working copies เอง: diff 3 ไฟล์ครบ + grep `selectionFlags` (main=0, crux=3) + python 86 tests รันซ้ำ

## Task-specific

- [x] U1a sync crux → main: `cmd/rein/route_launch_input.go` (selectionFlags fix) + `cmd/rein/route_launch_input_test.go` — diff empty, shasum e6fdc576→2087fba3
- [x] U1b sync main → crux: `internal/orca/census_test.go` (FIFO fix) — diff empty
- [x] U2 verify U1: `go test ./cmd/rein ./internal/orca` ok ทั้งสอง worktree (100.7s/16.2s) + `go test -race ./internal/orca -run TestCensusResidualSpawnAfterRecord -count=3` ok 6.465s
- [x] W0 (คำสั่งเจ้าของ 22:40): ปิด/ลบ worktree ไม่ใช้ — 4 Orca fixtures ที่ `rein-runs/crux-orca-*/seed` (Orca ปฏิเสธ rm เพราะ main worktree; ลบ path แล้ว Orca ตัดออกจาก list 20→16) + `crux-census-test` worktree/branch (เนื้อหาซ้ำ main; branch ไม่มี commit เฉพาะ)
- [x] U3+U4 → รวมเข้า WS1 (2026-10-19, F-01/F-02) — ยังไม่แก้จริง; WS1 ถือ pending status
- [x] U5 gates: `go test ./...` (main) + `go vet ./...` — suite 21 ok exit=0; vet exit=0 (logs: opencode tmp `full-go-test-after-cleanup.log`, `vet-after-cleanup.log`)
- [x] U6 update handover file/สถานะ — handover อัปเดตแล้ว (อัปเดตอีกรอบหลัง WS จบ)
- [x] WS1 F-01+F-02 (HIGH, delegated): verdict revision freshness + effective latest verdict/reviewer — DONE (6 CheckVerdicts tests + 2 RevisionIdentitiesMatch tests; `go test ./internal/verdict` 3.3s, `-race` 4.5s; note: git patch-id ทน line shift, normalized-body proof เป็น conservative guard; พบ identity bug เพิ่ม → แก้แล้วด้านล่าง)
- [x] WS2 F-03 (HIGH, delegated): coordinator approval validation/persistence ผ่าน real hook entrypoint + owner binding — DONE (3 tests; guard 1.25s; binary harness: prefill → deny, canonical → allowed, approve → record)
- [x] WS3 F-04 (HIGH, delegated): hunter lifecycle CLI wiring + empty-registry journey ถึง owner closure — DONE (journey test; `internal/hunter` 22s; `cmd/rein -run Hunter` ok; gofmt/vet ok)
- [x] WS4 F-05/06/07 (delegated): npm tarball bootstrap ผ่าน verified release assets + wrapper self-recursion + child error exit status — DONE (node --test 3/3; offline tarball fixture ผ่าน file://; dash ok; install.ps1 Windows gap → follow-up)
- [x] WS5 F-08/09 (delegated): e2e screenshot path confinement + scenario deadline enforcement (owner ยืนยัน 2026-10-19: เก็บ engine ไว้ แก้ต่อ; skill ถอดไปอย่างเดียว) — DONE (traversal/symlink/deadline 7 regressions; e2e 13.5s; race ok)
- [x] WS6 F-10 (LOW, delegated): docs 3 ไฟล์ — status ให้ตรงของที่ ship จริง, แยก historical receipt จาก live verification — DONE (15+/11-; อ้าง file:line; docs ประวัติศาสตร์อื่นยังขัด → follow-up)
- [x] WS7 F-12 (MEDIUM, delegated): preflight-push.sh git-failure + ignored-file enumeration + fixture tests — DONE (6 fixtures; scripts suite 18/18; untracked=fatal ตาม fail-closed)
- [x] F-01 side fix (lead): revision identity คำนวณจาก SHA ที่ระบุ ไม่ใช่ HEAD — 6 จุด (RecordVerdict, verdict check/approve/summary, hook approval, tier inference) + regression 2 ตัว (record + CLI) fail-before→pass-after
- [x] U7a (lead): CI wiring — เพิ่ม `python3 -B -m unittest discover -s scripts -p 'test*.py'` + `node --test test/*.test.mjs` ใน `ci.yml`
- [x] U7b (lead): final gates (`go test -count=1 ./...` + `-race` + vet + gofmt + python/node), CLI repro F-01, trail rows, handover, รายงานไทย — DONE (ทุก gate เขียว; CLI: B exit 0, C exit 1; ยังไม่ commit ตามกติกาเจ้าของ)
- [x] Follow-up (Windows dropped): version/tag consistency อยู่ใน CI+release แล้ว (`scripts/check-version-consistency.sh`); ADR 0002 B2.2 text อยู่แล้ว; docs ประวัติศาสตร์ที่ยังบอก test-only ปรับแล้ว (`MIGRATION_HUNTER_APPROVED.th.md`, `MIGRATION_V0_1_TO_V0_2.th.md`); hunter usage อยู่ใน `docs/CLI_REFERENCE.md` + `main.go` help แล้ว

## Autonomous run (playbook steps, verbatim — ขับ remediation F-01..F-12, 2026-10-19)

**Exit predicate (checkable):** F-01–F-10 + F-12 แก้พร้อม regression test พิสูจน์ fail-before→pass-after; `env GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 go test -count=1 ./...` เขียวทุกแพ็กเกจ; `go test -race -count=1 ./...` เขียว; `go vet ./...` exit 0; python/node suites ที่ CI รัน เขียว; npm tarball bootstrap พิสูจน์ offline จาก file:// release fixture; CLI-level verdict repro (F-01) reject จริง; ไม่มี commit/push/PR (กติกาเจ้าของ)

1. [x] State the exit condition as a checkable predicate before the first iteration (tests green, repro fixed, all N PRs merged, pixel-diff zero).
   ↳ predicate ด้านบน; F-11 ปิดแล้ว, G-01 = safety boundary โดยเจตนา — ไม่นับใน predicate
2. [x] Pick the wake mechanism using Claude Code's `loop` skill (a built-in, not a pstack skill). An event to watch (CI, a merge, a ref advancing) gets a watcher subagent that wakes you on the event, with a long time-based heartbeat as fallback. No event gets a fixed-interval heartbeat sized to when the result is worth re-checking.
   ↳ ไม่มี external event (งาน = finding list รู้จุดจบ) → single continuous session; ไม่มี watcher/heartbeat; checkpoint ต่อ unit แทน
3. [ ] Each iteration makes the smallest change the evidence justifies, verifies it against the predicate, commits if it advanced, discards changes that didn't help. Belt-and-suspenders that "might help" gets reverted, not left to ride.
   Sequence the work via the **sequence-verifiable-units** principle skill, verifying each unit before the next instead of batching checks at the end.
   ↳ 1 finding = 1 unit; commit ไม่ได้ (กติกาเจ้าของ) — advance = fail-before→pass-after ใน working tree + trail row
4. [ ] Mid-run discoveries are yours. Address broken skills, related bugs, flaky verifiers, review noise, tooling failures, orphaned follow-ups, and fixable drift yourself via poteto-mode. Put out-of-band fixes in their own PR. Do not park reversible work for the human or use `AskUserQuestion`. Surface only irreversible actions, genuine product or preference calls no experiment can settle, or a real dead end. Keep the predicate as the main drive, and return to it after each side fix.
   ↳ PR ไม่ได้ (กติกางาน) — side fix เข้า working tree + trail row
5. [ ] Checkpoint every iteration via the **show-me-your-work** skill, a row for what changed and whether the predicate moved.
   ↳ trail = `.audit/crux-continuation.tsv`
6. [ ] Stop when the predicate is met. A plateau is not a stop, so keep going and pivot your approach to push past it. Surface a genuine dead end rather than spinning, and never relax the predicate to declare victory.

**Reply:** the exit condition, iterations run, what landed, what was discarded, final predicate state.

## Bug fix (playbook steps, verbatim — routed ให้ F-01/F-02)

1. [ ] Reproduce it yourself on the matching surface via the driver skill (Non-negotiables), even when a debug or instrumentation protocol says to ask the user to reproduce. Ask the user only with a stated, specific reason the control surface cannot reach the target, and only after driving it as far as it goes. If it won't reproduce directly, synthesize the trigger, tighten conditions, or instrument until it fires.
2. [ ] Binary-search the cause. Form the candidate hypotheses, then rule them out until one survives. Seed them with `how` over the affected subsystem and the **why** skill for regression history. Each pass, take the split that cuts the most remaining problem space, get runtime evidence, eliminate. When program state is unclear, add instrumentation or logging and read it as the code runs. Don't guess. Drive a long or stubborn hunt with Claude Code's `loop` skill. Confirm the surviving *mechanism* with runtime evidence before the step-3 architect/interrogate fan-out.
3. [ ] Plan the fix. If it crosses a function boundary, `architect` first. Delegate implementation to a subagent using your configured bug-fix model (default in poteto-mode's Models section) with a specific scope.
4. [ ] Verify on the same surface. The original repro now passes. "Inconclusive" or wrong-surface is not a pass. Flag it. Unit tests show branch behavior, not bug absence.
5. [ ] Stage the commits so the failing repro lands before the fix in git history. See the **tdd** skill for the failing-test-first cadence when the bug has a cheap local test path. Skip it when the test would be expensive, integration-heavy, or unclear.
   - skip: เจ้าของงานห้าม commit จนกว่าจะสั่ง (กติกางานนี้) — เก็บเป็น WIP + tests พร้อม commit
6. [ ] Run **Opening a PR**.
   - skip: เจ้าของงานห้ามเปิด PR จนกว่าจะสั่ง
