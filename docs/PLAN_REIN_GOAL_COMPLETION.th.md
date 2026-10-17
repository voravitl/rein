# แผนปิด goal Rein: supervisor, recovery, checkpoint และ hunter

วันที่: 2026-10-16 · baseline `2ccc0a057074d56076a7ab9cf7fe22a32c4da237` + working tree ที่มีอยู่

## ผลลัพธ์และขอบเขต

เปลี่ยนส่วนที่มีเฉพาะ helper/tests ให้เป็น capability ที่เรียกผ่าน CLI ได้ พร้อมหลักฐาน local integration จริง โดย reuse contracts, routing receipts, leases, budget และ run marker เดิม ไม่ติดตั้ง Rein กลับ ไม่เปิด hooks ไม่เรียก paid provider และไม่แตะ live workers ของผู้ใช้

คำว่า DONE ต้องแยก implementation/local proof ออกจาก live lifecycle proof เสมอ ไฟล์ JSON, PID, marker หรือ boolean ที่ agent เขียนขึ้นไม่ใช่สิทธิ์ takeover coordinator

## ลำดับงานและ acceptance

1. **P1 — Production checkpoint/restore** (implementation)
   - จัดเก็บเนื้อหา snapshot ที่ตรวจ hash ได้ก่อนคืน Complete เพื่อไม่พึ่งไฟล์ต้นทางที่อาจเปลี่ยน/หาย
   - restore ไป destination ใหม่เท่านั้น; รักษา HEAD/base, staged/unstaged/untracked, binary, executable และ symlink สำหรับสถานะที่ manifest รองรับ
   - ปฏิเสธ corrupt/missing data, path traversal, symlink-parent escape, destination เดิม และ unsupported states โดยไม่เขียนทับ source/evidence
   - เพิ่ม `rein checkpoint finalize|verify|restore` + CLI tests; restore ไม่เปลี่ยน contract, attempt, receipt หรือ marker เดิม
2. **P2 — Production report-only hunter** (ทำขนานกับ P1)
   - ต่อ `hunter report|get|list` กับ registry เดิม และ serialize concurrent updates
   - เพิ่ม local isolated test reproduction ที่ผูก exact commit/attempt และบันทึกผลจาก execution จริง ไม่รับข้อความ green เป็น execution proof
   - ไม่มี auto-fix/approval/merge path; setup failure ไม่ใช่ reproduced; current revision และหลักฐานต้องตรวจได้
3. **P3 — Bounded supervisor control** (หลังสัญญา ownership ชัดเจน)
   - เพิ่ม explicit supervised-run authorization ผูก digest ของ approved plan ตอนสร้าง run ใหม่; legacy mode เดิมไม่เปลี่ยน ไม่ adopt run เดิมโดยอัตโนมัติ
   - controller lock ร่วมกับ Rein launch/waiter paths; process identity ตรวจจริง; coordinator marker ไม่ถูก Resume/rebind
   - bounded separate process: load approved graph, validate marker/plan/contracts, persist intent ก่อน external action, persist event ก่อน ACK, durable replay/dedupe, stale-attempt isolation
   - crash window/unknown liveness/receipt mismatch -> BLOCKED_RECONCILIATION; ไม่ dispatch ซ้ำ ไม่ settle จาก completion text อย่างเดียว; idle ไม่มี LLM calls
   - recovery/next-attempt ต้องผ่าน fence/worker-gone/checkpoint/usage+lease reconciliation ก่อน route launch เดิม; หากพิสูจน์ไม่ได้ให้ park พร้อมเหตุผล
4. **P4 — Integration, review และ docs**
   - regression red/green ที่เปลี่ยน behavior; ใช้ local real Git repos และ fake provider executables เพื่อทดสอบ production CLI โดยไม่ใช้ credits
   - Go full suite, vet, Python tests, format/diff/shell checks; independent read-only review จุด authority, crash และ filesystem boundaries
   - อัปเดต backlog/CLI/report และระบุส่วนที่ยังไม่ผ่าน ห้ามเรียกว่าปิด full goal หากยัง unwired
5. **P5 — Live pilot (แยกจาก local implementation)**
   - ต้องมี disposable environment/worker identity/budget และการอนุญาต live ที่ชัดเจนก่อนใช้งาน
   - ไม่ถือว่าคำสั่งพัฒนา source อนุญาตให้ติดตั้ง hooks กลับหรือใช้ worker/session ปัจจุบันทำ destructive lifecycle test

## Caller / compatibility map

- checkpoint: `internal/routing/checkpoint.go`, restore fixtures, new checkpoint CLI; preserve existing Finalize/Load/Verify signatures unless additive API needed
- hunter: `internal/hunter/finding.go`, registry tests, new CLI; existing report-only callers remain compatible
- supervisor: `internal/run` authorization/locks, `internal/routing/consumer.go`, `cmd/rein/route_launch.go`, legacy `ocloop2.py`, new supervisor CLI/package; no hidden OMX state writes
- wiring: `cmd/rein/main.go` command dispatch/help, docs/CLI_REFERENCE.md, IMPLEMENTATION_BACKLOG.th.md, requested report `/tmp/runs/run1/reports/fix-orca-hardening.md`

## Rollback และ stop condition

ไม่เปลี่ยน branch/commit/install/global config ไม่ลบ evidence หรือ lock inode เก่า destination ที่ restore ไม่สำเร็จต้องระบุชัดว่าข้อมูลใดสร้างโดย operation นี้ ส่วนที่มีอยู่ก่อนต้องไม่ถูกลบ

จบรอบเมื่อ CLI slices และ integration gates ผ่านพร้อมรายงานสถานะจริง หาก live authority/ownership proof ไม่ครบ ปิดเฉพาะ safe branch แล้วบันทึก blocker โดยไม่ bypass guard หรือลดเงื่อนไข safety

## ผล implementation ในรอบนี้

- P1/P2 มี production API+CLI พร้อม local regression/integration; original source/evidence และ installation state ไม่ถูกแทนที่
- P3 ทำ authority + bounded monitor + legacy exclusion + exact dispatch receipt capture แล้ว; initial controlled launch/graph progression/recovery ยังเปิดอยู่ ห้ามเรียกว่า autonomous supervisor ครบ
- P4 มี independent storage/control review และaggregate final gate ผ่าน: Go 21 packages, Python 85 tests, vet/format/diff/shell checks; หลักฐาน ใน `/tmp/runs/run1/reports/rein-goal-completion-implementation.md`
- P5 live pilot ยัง NOT RUN; ไม่ใช้เครดิต/worker จริง ไม่ติดตั้ง hooks กลับ

## งานต่อเนื่อง 2026-10-17 — ปิด wiring และค้นหา bug เพิ่ม

สถานะข้างต้นเป็นผลรอบก่อน ไม่ใช่คำยืนยันว่า graph execution ใช้งานครบแล้ว รอบนี้ต้องต่อ schema 2 ผ่าน production CLI ก่อนปิด P3/P4 โดยคง schema 1 monitoring เดิม

1. ต่อ PrepareAuto → receipt-selected launch → observed process completion → checkpoint → usage settlement → objective verification เข้ากับ durable DAG; ทุก retry ต้อง prepare ใหม่ และไม่ทำ side effect ซ้ำหลัง crash ที่ยังพิสูจน์ผลไม่ได้
2. ปิด legacy route mutation ที่ข้าม controller และป้องกัน settlement ลบ dispatched/pending lease โดยไม่มีหลักฐาน worker หยุด; event completion เพียงอย่างเดียวไม่ใช่หลักฐานดังกล่าว
3. แก้และตรวจซ้ำ bug ที่พบเพิ่ม: malformed Git tree restore escape, ledger durability ก่อนปล่อย budget และ hunter text-only closure; เก็บ regression red/green และ runtime verification ที่ยังรอ independent review
4. ใช้ fake executable กับ local Git repos สำหรับ CLI integration; unsupported remote descendant/liveness proof ต้อง park โดยไม่ปล่อย hold หรือสร้างหลักฐานสำเร็จเอง
5. รัน aggregate checks หลังทุก lane หยุดแก้ไฟล์แล้ว และอัปเดตรายงานพร้อม remaining gaps; full live pilot ยังไม่อยู่ใน local proof

ขอบเขตไฟล์เพิ่ม: `internal/supervisor/*`, `cmd/rein/supervisor*.go`, controlled route launcher, route mutation guards, `internal/routing/lease.go`/`settle.go`/`consumer.go`, `internal/hunter/verify.go` และ tests ที่เรียก path เหล่านี้ ไม่เพิ่ม dependency และไม่เปลี่ยน installation/global hooks

ผลปิดรอบ local: graph CLI wiring พร้อม actual fake-harness/Git integration และ independent replay review; Go full suite 21 packages, Python 85 tests, vet/format/diff/shell checks ผ่าน รายงาน `/tmp/runs/run1/reports/rein-goal-graph-followup.md` ส่วน remote recovery, detached-writer containment, independent approval และ live pilot ยังคง PARTIAL/NOT RUN

คำสั่งเพิ่มเติมเรื่อง hook: พบ OpenCode project autoload เรียก binary ด้วย absolute path แม้ PATH ถอนแล้ว จึงถอด plugin ที่ตกค้างพร้อม backup และตรวจ config/plugin ของทุก harness ที่พบ รายงาน `/tmp/runs/run1/reports/hook-audit-all-tools.md`; ไม่ปิด hook ของเจ้าอื่นหรือ restart session ผู้ใช้ ผู้ใช้ยืนยันภายหลังว่าจัดการเฉพาะ Rein

## Hook lifecycle follow-up — Rein only (2026-10-17)

ผู้ใช้ยืนยันให้จัดการเฉพาะ Rein และเก็บ hooks ของเจ้าอื่นทั้งหมด เป้าหมายคือ install → invoke → uninstall → reinstall ที่ใช้งานได้จริง ไม่ใช่ข้อความ success หรือการลบ PATH อย่างเดียว

1. เพิ่ม task-scoped `rein hooks uninstall` ให้ลบเฉพาะ entry/artifact ที่พิสูจน์ว่า Rein สร้าง เก็บ sibling hooks, namespace, settings และ metadata ของเจ้าอื่น พร้อมปฏิเสธ symlink/foreign collision
2. ล็อก regression ก่อนแก้ lifecycle: repeated install/uninstall, mixed owners, generated OpenCode plugin execution, stale registration และ errors ที่ต้องไม่รายงาน success
3. แก้ harness installer ให้เรียก CLI จริงหรือ fail พร้อมเหตุผล ไม่ใช้ `|| true` กลบข้อผิดพลาด ไม่สร้าง global link ไป task-bound hook ของ worktree อื่น
4. ทดสอบใน disposable HOME/repository เท่านั้น แล้วตรวจ runtime registrations ของผู้ใช้ยังไม่มี Rein; ไม่เปิด Rein กลับใน session จริง ไม่แก้ hook เจ้าของอื่น
5. เก็บผล local execution, platform/runtime gaps และคำสั่งใช้งานใน CLI/setup/report ก่อนปิดงาน

ผล hook lifecycle: เพิ่ม uninstall CLI + generated OpenCode metadata + strict ownership/conflict handling; แก้ false-success ของ installer และกรณี hook ถูกเติม comment/ย้าย event/ซ้ำ namespace ผ่าน red→green regression และ independent re-review แล้ว actual built binary ผ่านสองรอบ install/invoke/uninstall/reinstall ครบ 5 vendors ใน disposable worktrees, installer 12 tests และ final hook race/vet ผ่าน ไม่มีการติดตั้งกลับใน harness จริงของผู้ใช้ รายละเอียด `/tmp/runs/run1/reports/rein-hook-lifecycle.md`; live native loader/session unload ยัง NOT VERIFIED

## Goal closure continuation — 2026-10-17

ขอบเขตรอบนี้: แก้ active quota ที่ worker ยังไม่ exit, ตรวจ checkpoint/settlement แล้วส่ง bounded handoff ให้ attempt ใหม่, บังคับ test-only reproduction ผ่าน production path, เพิ่ม independent review + owner acceptance และ scan coverage report; แก้ native OpenCode direct shell hook และ Orca structured-worker identity ที่พบจากการตรวจจริง

Caller map: `route_launch.go` → graph process/recovery/adapter → supervisor retry state/tests; hunter execute/scope → finding outcome → scan-report/review/approve CLI/tests; hook generator → plugin callbacks/lifecycle mocks/native runtime; Orca LaunchResult → CLI/JSON/launch fixtures. ไม่เปลี่ยน foreign hooks, global registration, dependencies หรือ existing user sessions

Acceptance: targeted red/green + aggregate tests/vet, native OpenCode install/allow/deny/uninstall/reinstall ใน disposable environment; ตรวจ owner architecture แยก local cooperative proof จาก Orca-owned recovery ซึ่งต้องมี exact stop/descendant-gone contract ห้ามเติม boolean แล้วเรียกว่าพิสูจน์ได้

ผลตรวจ native hook รอบล่าสุด: OpenCode2 2.0.32 โหลด plugin จริงและบังคับทั้ง tool/direct shell callbacks ผ่าน install/uninstall/reinstall ใน disposable server แล้ว; การ unload ใน session ผู้ใช้เดิมยังไม่ตรวจ ไม่เหมารวม native callback ของ vendor อื่นจาก Node mocks ดู `/tmp/runs/run1/reports/native-hook-runtime.md`

ผล review recovery: bounded local cooperative path ผ่าน independent review; full PRD ยัง BLOCKED เพราะ Orca-owned graph recovery ยังปฏิเสธ launch และ runtime ยังไม่มี durable descendant termination verdict ที่ครอบคลุม session eviction รายงานปิดรอบและ aggregate checks: `/tmp/runs/run1/reports/rein-goal-final.md`
