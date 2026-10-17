# Implementation Handoff (v0.2 — Phase A + Phase B B2–B5 + Phase C C1–C8 + Phase D D1–D5 + Phase E E1 + F-LIVE + G-KIRO + G-GREEN)

> Current handoff — 2026-10-17: the implementation/version/test descriptions below are historical. See `PLAN_REIN_GOAL_COMPLETION.th.md`, `IMPLEMENTATION_BACKLOG.th.md` and `/tmp/runs/run1/reports/rein-goal-graph-followup.md` for production checkpoint/hunter CLI, schema 1 monitor, schema 2 bounded local graph and current verification. Remote dispatched/pending lease settlement now fails closed without termination proof; older successful-settle descriptions are superseded. Full project goal remains partial: remote containment/recovery, independent approval and live lifecycle/cost pilot are not established. Rein/runtime hooks remain uninstalled.

- **Revision:** HEAD `2ccc0a0` + uncommitted (ดู modified files) · **ห้าม push/merge/deploy ในงานนี้**
- **Versions:** Go 1.26.0 (docker `longrun-ci:v1` ผ่าน GOTOOLCHAIN auto-download จาก go1.24.1; host = go1.27.1 — ไม่ใช้เทสบน host) · omc 5.7.1 · orca 1.4.224 · git 2.50.1
- **Goal status:** Phase A เสร็จ; Phase B B2–B5 เสร็จ; Phase C C1–C8 เสร็จ; Phase D D1–D5 เสร็จ; Phase E E1 เสร็จ; F-LIVE (live review fixes) เสร็จ; G-KIRO (kiro parser) + G-GREEN (green-evidence structure) เสร็จ — owner เลือกทาง B 2026-10-13; hunter APPROVED (report-only, supervised) 2026-10-14 — ขั้นต่อไป `docs/MIGRATION_HUNTER_APPROVED.th.md`; supervised mode ยังไม่เปิด, loop skeleton (daemon) + crash matrix + live pilot ยัง PROPOSED/BLOCKED

## Modified files (uncommitted)

- `internal/providers/providers.go` — เพิ่ม `ProbeWithRetry` + `probeWithRetry`, `Check()` เรียกจุดเดียว
- `internal/routing/routing.go` — `Prepare()` ใช้ `ProbeWithRetry`
- `internal/providers/providers_test.go` — เพิ่ม `TestProbeWithRetry` (3 subtests)
- `internal/routing/routing_test.go`, `cmd/rein/routing_test.go`, `cmd/rein/route_launch_test.go` — fixture timeout 1s → 10s
- docs แก้รอบนี้: `docs/IMPLEMENTATION_BACKLOG.th.md` (B4→DONE, B5→DONE, NEXT=wiring decision), `docs/PRD_REIN_LONGRUN_BUGHUNT.th.md` (§12 ตรง code + roles 18→19), `docs/TEST_PLAN_LONGRUN_BUGHUNT.th.md` (R1/R5/R8/R9 → PARTIAL), `docs/IMPLEMENTATION_HANDOFF.th.md` (ไฟล์นี้), `docs/WIRING_DECISION.th.md` (ใหม่ — owner decision note, ไม่เปลี่ยน code)
- docs ใหม่รอบนี้: `docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md` (slice C2 — controller boundary design, docs only)
- code ใหม่รอบนี้: `internal/routing/fault_injection_test.go` (slice B5, test-only), `internal/routing/consumer_test.go` (slice C3 — `TestConsumerRecordsBeforeAck` 5 ข้อ, test-only), `internal/routing/checkpoint_restore_test.go` (slice C4 — restore + refusals, test-only), `internal/routing/idle_loop_test.go` (slice C7 — `TestIdleLoopParksWhenEveryPoolExhausted` 7 ข้อ, test-only), `internal/routing/unknown_reset_test.go` (slice C8 — `TestUnknownResetNeverClearsItself` 6 ข้อ, test-only), `internal/guard/hook_conflict_test.go` (slice C5 — `TestCoordAndWorkerCannotDisableGuard`, test-only), `internal/hunter/finding.go` + `finding_test.go` (slice D1 — registry + lifecycle, test-only), `cmd/rein/advise_test.go` (slice C1), `cmd/rein/omc_pin_test.go` (slice C6 — พิสูจน์ GAP, test-only), `internal/hunter/live_regression_test.go` (ใหม่ — F-LIVE 4 tests, test-only) + แก้ `internal/hunter/finding.go` (F-LIVE gates + `BlockedFrom`, production library) + แก้ 2 tests เก่าให้ปิดผ่าน `VerifyFix` (`finding_test.go`, `reproduction_test.go`)
- code เก่าค้าง: `internal/routing/{event,checkpoint,classify_event}.go` + tests (B2–B4 library — verify PASS ใน docker แล้ว, ยังไม่มี production caller)
- code ใหม่รอบนี้: `internal/hunter/greenstruct_test.go` (ใหม่ — G-GREEN 7 tests) + แก้ `internal/hunter/finding.go` (VerifyFix green-evidence structure) + `cmd/rein/kiro_parser_test.go` (ใหม่ — G-KIRO) + แก้ `skills/worktree-pipeline/scripts/advise.sh` (kiro parser strip preamble)
- gofmt fix: `internal/routing/checkpoint.go` (ทำบน host ก่อนได้คำสั่ง docker-only — บันทึกไว้ตรงนี้)

## Tests ที่รัน (actual outcomes — ทั้งหมดใน docker `longrun-ci:v1`, ไม่แตะ host)

- `gofmt -l internal/ cmd/` → clean · `go vet ./...` → OK
- `go test ./internal/hunter/ -count=3` → PASS 3/3 รอบ (ไม่ flake — 16 tests รวม D1+D2+D3+D4+E1)
- mutation red-check slice D1 (copy ไป /tmp/mutd1, /tmp/mutd2 ใน container): ข้าม lifecycle (CANDIDATE→TRIAGED) → FAIL ตามคาด; ลบ dedupe → FAIL ตามคาด (แดงครบ 2 จุด)
- mutation red-check slice D3 (copy ไป /tmp/mutd5, /tmp/mutd6 ใน container): ลบ REPRODUCED gate → FAIL ตามคาด (2 tests จับได้); setup-failure promote strength → FAIL ตามคาด (แดงครบ 2 จุด)
- mutation red-check slice D4 (copy ไป /tmp/mutd7, /tmp/mutd8 ใน container): stale=fresh → FAIL ตามคาด; ตัด teardown check → FAIL ตามคาด (แดงครบ 2 จุด)
- mutation red-check slice E1 (copy ไป /tmp/mute1, /tmp/mute2 ใน container): ลบ red-proof gate ใน VerifyFix → `TestVerifyFixNeedsRedProof` จับได้ (Blocked-bypass); ยอม stale HEAD → `TestVerifyFixRefusals` จับได้ (แดงครบ 2 จุด; mutation แรกที่ sed โดนผิดจุดเลยไม่แดง — ทำใหม่แบบเจาะจงบรรทัดแล้วแดงครบ)
- `go test -count=1 ./...` → PASS ครบ 19 packages (เพิ่ม hunter, docker, `GIT_CONFIG_GLOBAL=/dev/null`)
- `go test -count=1 -v -run 'TestClassifyProbe|TestClassifyWorkerExit|TestRecordRefuses|TestRecordDedupes|TestDeriveWait|TestCheckpointRoundtrip|TestCheckpointRefusals|TestVerifyDetectsDrift|TestProbeWithRetry'` → PASS ทั้งหมด
- `go test ./internal/routing/ -run TestQuotaFaultInjectionParksTask -count=3` → PASS 3/3 รอบ (ไม่ flake)
- mutation red-check (copy ไป /tmp/mut ใน container, ไม่แตะ checkout): fail-closed removal → FAIL ตามคาด; wait mislabel → FAIL ตามคาด; quota-as-ratelimit → FAIL ตามคาด (แดงครบ 3 จุด)
- `go test ./internal/routing/ -run "TestConsumerRecordsBeforeAck|TestQuotaFaultInjectionParksTask|TestCheckpointRestores|TestRestoreRefuses" -count=1` → PASS ทั้งหมด (docker)
- `go test ./cmd/rein/ -run TestCmdAdvise_UnknownRoleFailsClosed -count=3` → PASS 3/3 รอบ (ไม่ flake)
- `go test ./cmd/rein/ -run TestAdvisorLedgerCallCarriesNoOMCPin -count=3` → PASS 3/3 รอบ (ไม่ flake) — พิสูจน์ GAP ไม่ใช่ feature
- `go test ./internal/routing/ -run TestIdleLoopParksWhenEveryPoolExhausted -count=3` → PASS 3/3 รอบ (ไม่ flake)
- `go test ./internal/routing/ -run TestUnknownResetNeverClearsItself -count=3` → PASS 3/3 รอบ (ไม่ flake)
- mutation red-check slice C1 (copy script ไป /tmp/mutc1 ใน container, ไม่แตะ checkout): ลบ exit-2 guards ของ role-not-found → exit 0, test จับได้ FAIL ตามคาด (แดงครบ 1 จุด)
- mutation red-check slice C3 (copy ไป /tmp/mut3, /tmp/mut4 ใน container): ลบ stale-attempt filter → FAIL ตามคาด; ลบ dedupe → FAIL ตามคาด (แดงครบ 2 จุด)
- mutation red-check slice C4 (copy ไป /tmp/mutc4, /tmp/mutc5 ใน container): กลับ hash check ใน Verify → FAIL ตามคาด; ลบ dest-exists guard → FAIL ตามคาด (แดงครบ 2 จุด)
- mutation red-check slice C8 (copy ไป /tmp/mutc10, /tmp/mutc11 ใน container): unknown=free → FAIL ตามคาด; clear-ไม่ต้อง-reason → FAIL ตามคาด (แดงครบ 2 จุด)
- mutation red-check slice C7 (copy ไป /tmp/mutc8, /tmp/mutc9 ใน container): park ให้ probe budget → FAIL ตามคาด; หมดแล้วยัง continue → FAIL ตามคาด (แดงครบ 2 จุด)
- `go test ./internal/guard/ -run TestCoordAndWorkerCannotDisableGuard -count=3` → PASS 3/3 รอบ (ไม่ flake)
- mutation red-check slice C5 (copy ไป /tmp/mutc5, /tmp/mutc6 ใน container): `protectedHook` คืน false → FAIL ตามคาด; เหตุผล deny ไม่ชื่อ guard → FAIL ตามคาด (แดงครบ 2 จุด)
- `python3 -B -m unittest discover -s skills/worktree-pipeline/scripts -p 'test_advise_routing.py'` → OK (8 tests, docker)
- live run live-advise-20261014 (2026-10-14, owner อนุมัติโควตาจริง): contracts hunter-live-codex-work/hunter-live-claude-work + `route prepare` 2 chains (claude-opus up 13.9s / codex up 12.6s) + `route check --phase review` + `advise.sh` + `ledger call` จริงทั้ง 4 roles — code-reviewer/test-engineer/security-reviewer `ok:true`, tracer `ok:false` (kiro parser gap, fail-closed ถูกต้อง) — ดู TEST_PLAN §O-live-review
- `python3 -B -m unittest discover -s skills/worktree-pipeline/scripts -p 'test*cleanup*.py'` → dry-run gates ผ่าน (docker)
- NOT RUN: `go test -race` ทั้ง repo รอบนี้ (หandover เดิมบันทึก fixture 10s ไว้แล้ว — ต้องเขียวก่อนอ้าง)
- ข้อจำกัด docker: image มี go1.24.1 บน PATH แต่ `GOTOOLCHAIN=auto` ดึง go1.26.0 เองได้ (net เปิด); `go fmt -l` ใช้ไม่ได้ใน image — ใช้ `/usr/local/go/bin/gofmt` แทน

## Unfinished work

- wiring: owner เลือกทาง B แล้ว (2026-10-13) — design หลักใน `docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md`; hunter เลื่อนเป็น APPROVED (report-only) แล้ว (2026-10-14 — migration: `docs/MIGRATION_HUNTER_APPROVED.th.md`); kiro-parser gap ปิดแล้ว (slice G-KIRO); green-evidence structure ปิดแล้ว (slice G-GREEN); NEXT = loop skeleton (daemon) หรือ crash/restart/fault matrix (R2/R7); supervised mode ยังไม่เปิด (Blocked→FixInProgress decision ตัดสินแล้ว: ปิด bypass ที่ `Move` ระดับ — ดู BACKLOG)
- TEST_PLAN: R1/R5/R6 = PARTIAL (unit PASS, integration NOT RUN); R3/R4/R8/R9/R10/R11/R12/R13/R14/O2/O3/O4/O5/O6/O7 = DONE; O-pin = GAP (ยืนยันด้วย test); O1/R2/R7 = NOT RUN (ต้องมี supervised mode จริง); H ทั้งหมดยกเว้น H1/H3 = DONE (H1/H3 = PARTIAL) (ดู TEST_PLAN)
- Phase C เหลือ production decisions (C6 gap, loop daemon); Phase D D1–D4 เสร็จ; Phase E E1 เสร็จ เหลือ crash matrix + live pilot (BLOCKED) (ดู BACKLOG)
- OMC 5.6.2 → 5.7.1 (roles 19 ไฟล์เหมือนเดิม — อัปเดต version ทุกไฟล์แล้ว)

## Assumptions

- OMC roles 19 ไฟล์ที่ `$(npm root -g)/oh-my-claude-sisyphus/agents/` ตรงกับทุกเครื่อง (pin ด้วย `omc --version` ต่อ run) — เดิม docs เขียน 18 แก้แล้ว
- Orca 1.4.224 CLI surface (`worker-start/show/stop`, `check --wait/--ack`) คงที่ (`worker-show --help` + `orchestration --help` ตรวจจริง)
- `docs/ROUTING_SELECTION_DESIGN.md` ฉบับเต็มอ่านแล้ว (171 บรรทัด — classification/refresh/quality/cost/decision/boundary + trust model §15)
- **ห้าม install/test บน host — ทุก test รันใน docker (`--rm`, cleanup อัตโนมัติ) ตามคำสั่งเจ้าของ**

## Blockers

- ไม่มี blocker ทางเทคนิคสำหรับ slice ถัดไป (ใช้ fake CLI/fixtures ได้ทั้งหมด)
- Live pilot (Phase E) BLOCKED จนกว่า env + authorization พร้อม

## Next commands (ทั้งหมดรันใน docker — ไม่แตะ host)

```sh
git status --short
docker run --rm -v ~/dev/rein:/src:ro -v rein-gomod:/root/go -v rein-gocache:/tmp/gocache -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=auto -e GIT_CONFIG_GLOBAL=/dev/null longrun-ci:v1 sh -c 'cd /src && /usr/local/go/bin/gofmt -l internal/ cmd/ && go vet ./... && go test ./... -count=1'
docker run --rm -v ~/dev/rein:/src:ro ... longrun-ci:v1 sh -c 'cd /src && python3 -B -m unittest discover -s skills/worktree-pipeline/scripts -p "test_advise_routing.py"'
```
