# Handover ให้ Codex: งานเชื่อม Crux ↔ Rein ↔ Orca (2026-10-18)

## 0. ตัวตนของงานส่งต่อนี้

- **ผู้รับ**: Codex session เดิม `01a14c4c-2c25-79e3-9a03-a223f1e084dc` (TUI terminal `term_8e6d6bee-bf53-4d8e-8bef-63fcaaa1e82b`, cwd `/Users/voravit.l/dev/rein`, branch `main`)
- **ผู้ส่ง**: OpenCode (session ใน Orca worktree เดียวกัน `308dbc44-3051-4ebb-ae2d-6844604b892e::/Users/voravit.l/dev/rein`)
- **เวลาส่งต่อ**: 18 ตุลาคม 2026 22:29 น. Asia/Bangkok
- **เหตุผล**: Codex ทั้ง workspace ติดเครดิต (`codex_error_info: usage_limit_exceeded`, `rate_limit_reached_type: workspace_owner_credits_depleted`, balance 0) — เจ้าของงานต้องเติมเครดิตแล้วพิมพ์ `/goal resume`; งานทั้งหมดในไฟล์นี้รออยู่ในคิว
- **ความคืบหน้า (19 ต.ค. release)**: Crux + Rein **released v0.9.0**; GitHub release มี 8 assets; `rein-darwin-arm64 version` = `rein 0.9.0` และ SHA256SUMS ตรง; audit remediation F-01–F-12 ปิด; final gates ครบ (Go full/race/vet/gofmt, node/python/scripts/npm, preflight/version/crux tarball); npm ยังไม่ publish (unauthenticated)

## 1. Goal ที่ต้องทำต่อ (คำสั่งเจ้าของงาน)

> "ทำให้ทั้งหมดเชื่อมเข้าหากัน ไปออกแบบและวางแผนมา และ output ที่สื่อสารกับ user ต้องเข้าใจง่าย อธิบายภาษาไทย รู้เรื่อง แก้ไขมาด้วย"

ตีความ: เป้าหมายคือทำให้ Crux ↔ Rein ↔ Orca เชื่อมกันครบ 4 harness (Claude Code, Codex, Antigravity, OpenCode2) — ออกแบบ/วางแผน/แก้โค้ด/พิสูจน์ด้วยหลักฐาน และ **ทุกคำตอบถึงเจ้าของงานต้องเป็นภาษาไทยที่อ่านรู้เรื่อง**

## 2. งานที่ทำเสร็จแล้วก่อน Codex ติดเครดิต (มีหลักฐาน)

| งาน | หลักฐาน | สถานะ |
|---|---|---|
| Crux readiness 4 runtime (Codex 55 skills/hooks untrusted, Claude 55 skills + 2 agents, agy เลือก Crux ได้, OpenCode2 โหลด adapter) | `/Users/voravit.l/dev/rein-runs/crux/native-all-readiness.json`, `harness-acceptance.json`, `native-all-readiness-receipt.json` | เสร็จ (metadata-level) |
| census F-11 fix (FIFO trigger + cleanup defer) | อยู่ใน **main** `internal/orca/census_test.go` (mtime 21:28); cleanup หลักฐาน `/Users/voravit.l/dev/rein-runs/crux/census-failed-test-cleanup.json` (shell ที่ leak จากรอบก่อนถูก SIGTERM แล้ว, PID 49827) | เสร็จใน main |
| Go full suite หลัง census fix | `/Users/voravit.l/dev/rein-runs/crux/full-go-test-native-quota-fixed.log` — 21 packages `ok` ทุกตัว | exit 0 |
| Python tests ของ plugins/crux | รันซ้ำโดยผู้ส่งวันนี้: `python3 -B -m unittest discover -s plugins/crux/tests -p 'test*.py'` ที่ `/Users/voravit.l/dev/rein-worktrees/crux` → `Ran 86 tests`, `OK` (10.160s) | PASS |
| `go test ./cmd/rein` หลัง native selection fix | ok 2.682s / 3.057s (ใน rollout ของ Codex รอบ 21:40) | PASS |

## 3. งานค้างด่วนที่สุด: sync 2 ทาง (ยังไม่ commit — ตรวจซ้ำโดยผู้ส่งวันนี้)

HEAD ของทุก worktree = `2b118c4` เท่ากันหมด แต่ WIP ยัง uncommitted และ**ไม่ตรงกัน 2 ทาง**:

| ทิศทาง | ไฟล์ | หลักฐาน | ต้องทำ |
|---|---|---|---|
| **crux → main** | `cmd/rein/route_launch_input.go` | crux (mtime 21:40) มี `selectionFlags` 3 จุด — รับ `claude --agent/--plugin-dir`, `agy --agent`; main (mtime 08:13) grep = **0 จุด** | พา fix เข้า main |
| **crux → main** | `cmd/rein/route_launch_input_test.go` | crux มี `TestGraphPromptSlotNativeCruxSelection` + `TestGraphCLIPreservesApprovedCruxAgent` (ยืนยัน argv จริง + negative cases), main ไม่มี | พา tests เข้า main ด้วยกัน |
| **main → crux** | `internal/orca/census_test.go` | main (mtime 21:28) ใช้ `syscall.Mkfifo` + `defer` cleanup ก่อน fatal assertion (บรรทัด ~245-273); crux (mtime 07:33) ยังเป็น `os.WriteFile` trigger แบบเก่า | พา F-11 fix เข้า crux |

**หมายเหตุ**: ทั้งสองทางเป็น *uncommitted WIP* — ห้าม `git checkout <file>` ข้าม worktree เด็ดขาด (จะทับงานที่ยังไม่ commit) ให้ `git diff` เทียบแล้ว apply เฉพาะ hunk

## 4. บริบท git ที่ต้องรู้

- HEAD `2b118c4` = "chore(audit): trail U10-U12 (preflight latch, lease window fix, final release)" — main sync กับ `origin/main` และ push แล้ว
- WIP uncommitted: **main 63 items**, **feat/crux 62 items**, **fix/crux-census-test 1 item** (`M internal/orca/census_test.go`)
- Worktree ที่เหลือ (หลัง cleanup 18 ต.ค. 23:00):
  - `/Users/voravit.l/dev/rein` — branch `main` (ที่อยู่ปัจจุบันของ Codex)
  - `/Users/voravit.l/dev/rein-worktrees/crux` — branch `feat/crux` (ถูก sync เข้า main แล้วในรอบนี้)
  - (`crux-census-test` ถูกปิด/ลบตามคำสั่งเจ้าของ — เนื้อหาซ้ำ main ทั้งหมด)
- **กติกาเดิมของงานนี้: ห้าม commit / push / เปิด PR จนกว่าเจ้าของจะสั่ง**

## 5. Audit ค้าง: REQUEST_CHANGES (12 findings, HIGH 7)

- รายงานเต็ม (สำเนา): [`.audit/rein-project-audit-20261018-859a94a.md`](rein-project-audit-20261018-859a94a.md) — ตรวจที่ Head `859a94a1e942507a45d02562ef729ff5b4416316`
- แก้แล้วเฉพาะ **F-11** (census test) และอยู่ใน main เท่านั้น (ดูข้อ 3)
- ลำดับ remediation จากรายงาน §5 (ทำตามนี้):
  1. **F-01/F-02** — verdict freshness: commit ใหม่รับ verdict เก่า (main-file intersection ว่าง = ผ่าน) และ REQUEST_CHANGES ไม่ revoke APPROVE ของ reviewer คนเดิม — `internal/verdict/verdict.go:107-114`, `internal/verdict/check.go:199-205`
  2. **F-03** — coordinator approval bypass validation/recording — `internal/guard/coordinator.go:64-68,134-135`
  3. **F-04** — hunter lifecycle ไปไม่ถึง closure ผ่าน production CLI — `cmd/rein/hunter.go:25-139`, `internal/hunter/finding.go:191-209,403-417`
  4. **F-05/F-06/F-07** — npm tarball ขาด native binary/source, wrapper เรียกตัวเองซ้ำ, launch error คืน exit 0 — `package.json:9-20`, `bin/rein.js:15-20,29-32,53,57`
  5. **F-08/F-09** — E2E screenshot หลุด evidence dir (`../outside.png`), scenario timeout ไม่ถูกบังคับ — `internal/e2e/runner.go:211-217`, `spec.go:30`
  6. **F-10** — doc ปัจจุบันขัดกันเรื่องความสามารถที่ ship — `docs/ARCHITECTURE_REIN_ORCA_OMC.th.md:4` ฯลฯ
  7. **F-12** — `scripts/preflight-push.sh:15-18,25,36` คืน 0 ทั้งที่ Git fail/มี ignored source
- **G-01 (Orca recovery fence)**: ห้ามแตะ — เป็น safety boundary โดยเจตนา (`cmd/rein/route_launch.go:72-74`, `graph_adapter.go:367-384`)

### 5.1 Follow-ups หลัง remediation (ไม่ block audit)

- **F-01 side fix (lead, 2026-10-19)**: revision identity คำนวณจาก SHA ที่ระบุ ไม่ใช่ `HEAD` — 6 จุด (`RecordVerdict`, `verdict check/approve/summary`, hook approval, tier inference) + regression 2 ตัว (`internal/verdict/record_identity_test.go`, `cmd/rein/verdict_identity_test.go`) พิสูจน์ fail-before→pass-after
- `scripts/install.ps1` ยังไม่มี release download → Windows native bootstrap จาก npm tarball ยังไม่ครบ (script อื่น ๆ รายงาน error ตรงแล้ว)
- version ใน `package.json` กับ `.claude-plugin/plugin.json` ต้องตรงกันก่อน publish (download URL ผูกกับ version)
- `docs/adr/0002-run-guard-and-approvals.md:171-172` ยังบรรยายกฎ file-intersection เก่า; implementation เข้มกว่า (normalized body proof) — ควร amend
- docs ประวัติศาสตร์ที่ยังบอก test-only/proposed: `MIGRATION_HUNTER_APPROVED.th.md`, `MIGRATION_V0_1_TO_V0_2.th.md`, `TEST_PLAN_LONGRUN_BUGHUNT.th.md`, `WIRING_DECISION.th.md`, `IMPLEMENTATION_BACKLOG.th.md`
- hunter usage ใน `docs/CLI_REFERENCE.md` + `cmd/rein/main.go` help ยังไม่รวม `triage|authorize-fix|start-fix`
- `scripts/preflight-push.sh`: untracked source = fatal (fail-closed) → latch จะแดงจนกว่า WIP จะถูก commit; เป็นเจตนา ไม่ใช่บั๊ก
- WS4/WS7 tests ถูก wire เข้า CI แล้ว (`.github/workflows/ci.yml`)

## 6. เป้าหมายถัดไปของโปรแกรม (จาก `plugins/crux/references/integration-plan.md` §"จุดตรวจล่าสุด")

1. **ปิดการส่งงานอัตโนมัติ AGY → Codex** — ให้ reviewer อิสระตรวจ revision ที่แก้แล้ว (ไม่ใช้ calibration suite v1)
2. **ใช้ production supervisor graph ผ่าน Orca terminal** — พิสูจน์โมเดลจริง, handoff ข้าม provider, crash recovery, cancellation/descendants
3. **ปิดเกณฑ์ทั้ง 4 harness** — activation/continuation/off/guard denial/handoff/ภาษาไทย ต้องมีหลักฐานรายตัว (ตารางรับรอง §"ตารางรับรองความสามารถต่อ harness")
4. **เชื่อม native Orca lifecycle** — ownership กับหลักฐาน stop/settle ก่อนปลด lease; แยก local กับ federation

## 7. ที่ติด external (ห้ามพยายามฝ่า)

- **Codex credits**: หมดทั้ง workspace — ปิดทุก turn จนกว่าเจ้าของเติมเงิน (สาเหตุของ handover นี้)
- **Orca quota หลักฐานเก่า/ไม่มีข้อมูล**: การเปิดรอบโมเดลจริงข้าม provider ตามนโยบายเดิมยังทำไม่ได้ — รอหลักฐาน quota/บัญชีที่ใช้ได้จริงรอบใหม่
- **Claude weekly quota**: รีเซ็ต 20 ต.ค. 2026 04:00 น. Asia/Bangkok
- **OpenCode2 Haiku monthly quota**: เต็ม

## 8. แหล่งหลักฐานที่มีอยู่แล้ว

- `/Users/voravit.l/dev/rein-runs/crux/` — reports/logs ของ Codex รอบล่าสุด (readiness, acceptance, go test logs, census cleanup)
- `.audit/orca-goal-success.todo.md` + `.audit/orca-goal-success.tsv` — trail U1-U12 ของ session ก่อน (release v0.8.0; เป็นประวัติ ไม่ใช่สถานะปัจจุบัน)
- Rollout ของ Codex เอง: `~/.codex/sessions/2026/10/18/rollout-2026-10-18T06-57-02-01a14c4c-2c25-79e3-9a03-a223f1e084dc.jsonl`
- Rollout ของ subagent ที่ตายเพราะ credits: `rollout-2026-10-18T21-40-44-01a14f75-38cc-7313-97a8-434aec59430b.jsonl` (`/root/native_selection_review`)

## 9. กติกาการรายงานและลงมือ (คงเดิมจาก session ก่อน)

1. ทุกคำตอบถึงเจ้าของงานเป็น**ภาษาไทยอ่านง่าย** — ระบุว่าเสร็จอะไร ติดอะไร ต้องทำอะไรต่อ โดยไม่ให้เจ้าของต้องอ่าน log เอง
2. ทุกข้ออ้างต้องมีหลักฐาน `file:line` หรือ exit code — ห้ามสรุปเกินหลักฐาน
3. ห้าม commit / push / เปิด PR จนกว่าเจ้าของจะสั่ง
4. แก้ที่เจ้าของความสามารถ: adapter → `plugins/crux/adapters/`, lease → Rein, transport → Orca integration
5. รัน gates ก่อนรายงาน: `go test ./...` (เมื่อแก้ Go) + node tests + python tests ของ crux + `go vet ./...`

## 10. ลำดับเริ่มงานที่แนะนำ (หลัง `/goal resume`)

1. อ่านไฟล์นี้ + `.audit/rein-project-audit-20261018-859a94a.md` §5
2. Sync 2 ทางตามข้อ 3 แล้วรัน `go test ./...` ที่ main + `go test ./cmd/rein ./internal/orca` ที่ crux ยืนยันทั้งคู่เขียว
3. รายงานเจ้าของงาน 5-8 บรรทัดเป็นภาษาไทย: ทำอะไรไป / ผล test / เหลืออะไร แล้วรอคำสั่งก่อนหยิบ F-01
4. ถ้าเจ้าของสั่งให้เดินหน้าแก้ audit: ลุยตามลำดับ §5 ข้อ 5 (F-01/F-02 ก่อน)
