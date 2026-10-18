# Wiring decision: event log / checkpoint / classify จะต่อ supervisor loop หรือคง test-only (v0.2)

> **สถานะปัจจุบัน (2026-10-19):** decision ยังใช้ (ทาง B) และ library ถูกใช้โดย production CLI แล้ว (`rein checkpoint`, `rein supervisor`, `rein hunter`) ข้อความ “test-only” ด้านล่างเป็นสถานะ historical; remote Orca recovery ยังคง fence ตาม `docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md`

- **Status:** DECIDED — owner เลือกทาง B (ต่อ supervisor loop) เมื่อ 2026-10-13 · design หลักย้ายไป `docs/SUPERVISED_CONTROLLER_BOUNDARY.th.md` (slice C2) แล้ว เอกสารนี้เก็บประวัติการตัดสิน
- **Baseline:** HEAD `2ccc0a0`; B2 (`classify_event.go`), B3 (`event.go`), B4 (`checkpoint.go`), B5 (`fault_injection_test.go`) = library + tests เขียวใน docker ทั้งหมด, grep ยืนยันไม่มี production caller (มีแต่ในตัวเองกับ test) ยกเว้น `ProbeWithRetry` ที่ต่อเข้า `Prepare()` แล้ว (`routing.go:294`)
- เอกสารนี้ไม่เปลี่ยน code, ไม่เปิด supervised mode, ไม่แตะ production path

## ทางเลือก

### A. คงเป็น test-only contract (แนะนำสำหรับตอนนี้)
- event log / checkpoint / classify อยู่เป็น pure library + regression tests ที่ล็อกพฤติกรรม fail-closed
- supervisor loop ยังไม่เกิด — coordinator เดิน manual handover เดิม (`SKILL.md` §1 quota fallback: checkpoint ด้วยมือ → `route cooldown` → reinstall hooks → re-prepare)
- ข้อดี: ไม่เพิ่ม control plane ที่สอง, ไม่เสี่ยงแย่ง dispatch/ack กับ `ocloop2.py`, ไม่ต้องหยุด waiter เดิม
- ข้อเสีย: coordinator หมด quota เอง = ระบบหยุด (ตามที่ PRD ยอมรับไว้)
- งานที่ต้องทำ: เกือบศูนย์ — แค่กันไม่ให้ใครอ้างว่า library = supervisor (docs ปัจจุบันทำแล้ว: BACKLOG NEXT + TEST_PLAN PARTIAL + PRD §12)

### B. ต่อเข้า supervisor loop (deferred — ต้องมี design slice แยก)
- ต้องออกแบบก่อน implement (ห้ามเริ่ม loop ในรอบนี้):
  1. controller boundary: supervisor เป็นผู้ dispatch/ack เพียงชุดเดียวต่อ run; `ocloop2.py` ต้องหยุด (ห้ามแย่ง consume/ack `worker_done/escalation/question`) — ดู `MIGRATION_V0_1_TO_V0_2.th.md` §1–§2
  2. event consumer: ใครเรียก `Record` (Orca check output → classify → record), persist-ก่อน-ack ที่จุดไหน, redelivery dedupe ที่จุดไหน
  3. checkpoint trigger: finalize ตอนไหน (writer stopped proof — ไม่ใช่ launcher exit), restore-into-worktree จริง (R8 ปัจจุบันมีแค่ manifest verify)
  4. idle loop: ไม่เรียก LLM, probe เพดานแยก, ทุก pool หมด → พักไม่วน (R9 ปัจจุบันมีแค่ unit)
  5. mode switch: เปลี่ยนได้เมื่อไม่มี active work ค้างเท่านั้น + rollback ไม่ให้ worker ซ้อน (MIGRATION §2–§3)
- ข้อดี: coordinator หมด quota แล้วยังเดินตาม approved graph ได้
- ข้อเสีย: เพิ่ม control plane ที่ต้องพิสูจน์ single-control-owner + crash/restart matrix (R6/R7) — งานใหญ่กว่า B2–B5 รวมกัน
- งานที่ต้องทำ: design slice แยก (controller boundary + consumer + trigger + loop budget) พร้อม TEST_PLAN R2/R6/R7 ก่อนเขียน code

## คำแนะนำ
- เลือก A ตอนนี้ (test-only contract + manual handover เดิมที่พิสูจน์แล้วใน parent run 2026-10-09) — B ยังไม่คุ้มจนกว่า coordinator-quota outage จะเป็นปัญหาจริงที่วัดได้
- สัญญาณที่ควรกลับมาทบทวน: coordinator หมด quota กลาง run เกิน 2 ครั้ง/ไตรมาส, หรือ pilot ต้องการ unattended recovery จริง (Phase E authorization)
- ไม่ว่าเลือกทางไหน: ห้ามอ้างว่า simulated quota event = real exhaustion; ห้ามอ้าง live Orca/OMC compatibility จาก fake tests อย่างเดียว (TEST_PLAN ย้ำไว้แล้ว)

## ถ้า owner เลือก B — slice ถัดไป (ไม่เริ่มจนกว่า owner สั่ง)
1. design: controller boundary doc (ใคร dispatch/ack, หยุด waiter อย่างไร, rollback อย่างไร) — docs only
2. consumer test: Orca check JSON fixture → classify → record → derive (fake, ประกาศว่าจำลอง) — test-only
3. checkpoint restore test: finalize → แตก worktree ใหม่ → restore → verify manifest — test-only
4. แล้วค่อย loop skeleton (idle no-LLM + pool-exhausted park) พร้อม R6/R7 matrix
