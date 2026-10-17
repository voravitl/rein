# Migration: hunter DRAFT → APPROVED (report-only) — ต้องทำยังไงต่อ

- **Version:** 2026-10-14 · สถานะ: `agents/rein-bug-hunter.md` = APPROVED (report-only, supervised)
- เอกสารนี้คือแผนขั้นต่อไปหลัง owner อนุมัติ — เรียงตาม safety + dependency; แต่ละขั้นมีเงื่อนไขผ่าน/หยุดชัดเจน

## Local follow-up — 2026-10-16

Owner authorized further local bug fixes and goal implementation while preserving the uninstall. The opt-in consumer API, journal/checkpoint safety fixes and advisor prompt/version metadata are implemented with local regression tests. This adds source capability; it does not activate supervised mode, install hooks, prove live recovery, or connect the hunter registry to production. The daemon/restore/live-pilot boundaries below remain open. See the current update in `IMPLEMENTATION_BACKLOG.th.md` and `/tmp/runs/run1/reports/fix-orca-hardening.md` for evidence and remaining gates.

## 1. อนุมัติครั้งนี้ครอบคลุมอะไร (และไม่ครอบคลุมอะไร)

| อนุมัติแล้ว | ยังไม่อนุมัติ |
|---|---|
| ใช้ hunter workflow ได้แบบ report-only + มีคนคุม (เจอแล้วรายงาน ไม่แก้เอง) | supervised mode ทั้งชุด (supervisor daemon, loop skeleton) |
| อ้าง `agents/rein-bug-hunter.md` เป็น approved workflow ได้ | อ้าง registry (`internal/hunter/finding.go`) เป็น production API — ยังเป็น test-only library |
| รัน `advise.sh` review ผ่าน review receipt ตาม OMC contract | auto-fix, unattended runs, live pilot, push/merge/deploy |

## 2. เงื่อนไขที่อนุมัติตั้งอยู่บน (evidence — ตรวจซ้ำได้)

1. D5 integration: `TestWorkflow_CleanFixtureReportsNoConfirmedFindings` + `TestWorkflow_SeededDefectToVerifiedFixed` PASS (`internal/hunter/workflow_test.go`)
2. Live review 4 roles ผ่าน review receipt จริง (run `live-advise-20261014`): 3 `ok:true` + tracer `ok:false` แบบ fail-closed — ledger `~/.claude/omc/worktree-pipeline/model-ledger.jsonl`
3. Bypass F-LIVE-1..4 ปิดครบ + regression tests 4 ตัว + mutation 4 จุดแดงครบ (`internal/hunter/live_regression_test.go`)
4. Full suite: `go test ./...` 19 pkgs PASS + `test_advise_routing.py` 8 tests OK (docker `longrun-ci:v1`)

ถ้าข้อใดถูกแก้จนไม่จริงแล้ว (เช่น แก้ `finding.go` โดยไม่รัน tests) — สถานะ APPROVED หมดอายุจนกว่าจะ verify ใหม่

## 3. จุดอ่อนที่รับทราบไว้ (ไม่ปิดบัง — ต้องแก้ก่อนขยายขอบเขต)

- `VerifyFix` เช็ค green แค่ substring (`strings.Contains(greenEvidence, fixedRevision)` — `finding.go:519`) — ส่งแค่ revision string ก็ผ่าน → **ปิดแล้ว 2026-10-14 (slice G-GREEN):** บังคับโครงสร้าง `fixed=` + `FAIL@<baseline>` + `PASS@<fixed>` + `existing-pass` + `greenstruct_test.go` + mutation แดงครบ
- revision เป็น free text จาก caller ไม่ผูกกับ attempt records
- O-pin GAP: ledger ไม่มี OMC version/role digest (slice C6)
- kiro-parser GAP: `advise.sh:68` เก็บ output kiro-cli ปัจจุบันไม่ได้ (log 110 บรรทัด → OUT 0)
- `advise.sh` เรียก CLI ตรง (shell-direct) ไม่ใช่ orca-launch ตาม `launch:` ใน config — `route check` ไม่ได้ validate launch

## 4. ขั้นต่อไป (ตามลำดับ — ห้ามข้าม)

### ขั้นที่ 1: kiro-parser gap — ✅ ปิดแล้ว 2026-10-14 (slice G-KIRO)
- RED test `cmd/rein/kiro_parser_test.go` → แก้ `advise.sh:68` (strip preamble แทน `> ` gate) → GREEN + verify log จริง 74 บรรทัด + mutation แดงครบ
- หมายเหตุ: ถ้า kiro เปลี่ยน format อีก → ล็อก format ที่รองรับไว้ใน test ตัวนี้ก่อนแก้ script เสมอ

### ขั้นที่ 2: green-evidence structure — ✅ ปิดแล้ว 2026-10-14 (slice G-GREEN)
- RED test `internal/hunter/greenstruct_test.go` (6 เคส unstructured แดง) → แก้ `VerifyFix` ให้บังคับ `fixed=` + `FAIL@<baseline>` + `PASS@<fixed>` + `existing-pass` → hunter เขียว 3 รอบ + mutation แดงครบ

### ขั้นที่ 3: loop skeleton (daemon) — NEXT (ต้องมี supervised mode approve แยก)
- ตาม `docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md`; R2/R7/O1 พิสูจน์ได้เฉพาะเมื่อมี loop จริง

### ขั้นที่ 4: crash/restart/fault matrix + live pilot
- BLOCKED จนกว่า env + authorization พร้อม (owner ตัดสินใจแยก)

## 5. Rollback (ถอน approval)

1. `mv agents/rein-bug-hunter.md agents/rein-bug-hunter.draft.md` + เปลี่ยน header กลับเป็น DRAFT
2. ติ๊ก BACKLOG checklist ข้อ owner approval กลับเป็น `[ ]` พร้อมเหตุผล
3. ไม่มี code ต้อง revert (approval นี้เปลี่ยนแค่ docs + ชื่อไฟล์ — registry ยังเป็น test-only เหมือนเดิม)

## 6. คำสั่ง verify (ทั้งหมดใน docker — ไม่แตะ host)

```sh
git status --short
docker run --rm -v ~/dev/rein:/src:ro -v rein-gomod:/root/go -v rein-gocache:/tmp/gocache -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=auto -e GIT_CONFIG_GLOBAL=/dev/null longrun-ci:v1 sh -c 'cd /src && /usr/local/go/bin/gofmt -l internal/ cmd/ && go vet ./... && go test ./... -count=1'
grep -rn "rein-bug-hunter.draft" --include="*.md" --include="*.go" . ; echo "(empty=ไม่มีอ้างอิงชื่อเก่าค้าง)"
```
