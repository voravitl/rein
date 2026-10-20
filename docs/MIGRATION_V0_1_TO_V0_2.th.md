# Migration v0.1 → v0.2 (v0.2)

> **สถานะปัจจุบัน (2026-10-19):** เอกสารนี้เป็นประวัติการออกแบบ v0.2 ข้อความ “proposed/ยังไม่ implement” ด้านล่างไม่ใช่สถานะปัจจุบัน — supervisor/checkpoint/hunter CLI ถูก implement และมี regression tests แล้ว ส่วน remote Orca recovery ยังคง fence; ดู `docs/ROADMAP.md` และ `docs/CLI_REFERENCE.md`

- **Version:** 0.2 · legacy coordinator mode (ของเดิม) ↔ supervised mode (proposed, ยังไม่ implement)
- หลัก: เปลี่ยน mode ได้เมื่อไม่มี active work ค้างเท่านั้น; rollback ห้ามทำให้ worker สองชุดซ้อน; config/schema เก่าต้องใช้ต่อได้

## 1. ผู้ dispatch/ack ในแต่ละ mode

| | Legacy (มีแล้ว) | Supervised (proposed) |
|---|---|---|
| dispatch worker | coordinator (`worker-start`) | supervisor ชุดเดียวต่อ run |
| ack `worker_done/escalation/question` | waiter `ocloop2.py` (heartbeat ack ทุกนาที) | supervisor |
| review/advisor | coordinator ผ่าน `advise.sh` | เหมือนเดิม (ไม่เปลี่ยนเส้น) |
| merge/deploy | human เท่านั้น | เหมือนเดิม |

## 2. วิธีเปลี่ยน mode (ไม่มี active work ค้าง)

1. `orca_cleanup.py` + `cleanup_worktrees.py` ผ่าน (ไม่มี live worker/terminal/worktree ค้าง) — ขั้นตอนเดิม `SKILL.md` §8
2. `route holds` ว่าง + `route status` ไม่มี pool ค้างที่ต้อง reconcile
3. เปิด supervised mode (flag ใหม่ — **ห้าม default on** จนกว่า Phase B–C เสร็จ + เจ้าของอนุมัติ)
4. supervisor อ่าน approved task graph + policy ที่บันทึกไว้แล้วเท่านั้น (เรื่องใหม่เกินแผน → WAITING_HUMAN)

## 3. Rollback

- หยุด supervisor ก่อน → verify ไม่มี writer/lease/hold ค้าง → coordinator กลับมา dispatch เอง
- ห้ามมีช่วงที่ทั้ง supervisor และ coordinator dispatch พร้อมกัน (single-control-owner)
- event log + checkpoint ที่ supervisor เขียนต้องอ่านได้จาก legacy mode (ไม่ทิ้ง state ที่อีก mode อ่านไม่ออก)

## 4. Config/schema compatibility

- `fallback-chain.json`, profile `selection`, contract schema, ledger rows: ไม่เปลี่ยนความหมายใน v0.2 (เพิ่มเติมได้อย่างเดียว)
- `examples/longrun.policy.draft.yaml` เป็น **draft** — `enabled: false` default, required owner fields ว่างไว้, แยก supported/proposed ชัด, ห้ามอ้างว่า CLI ปัจจุบันอ่านได้
- `agents/rein-bug-hunter.md` **APPROVED (report-only, supervised)** ตั้งแต่ 2026-10-14 — เลื่อนจาก .draft หลัง integration test (D5) + live review 4 roles + ปิด bypass F-LIVE-1..4 + owner อนุมัติ; ขั้นต่อไป `docs/MIGRATION_HUNTER_APPROVED.th.md`; ยังห้ามอ้างว่าเป็น production API หรือใช้แบบ unattended

## 5. สิ่งที่เป็น draft (ห้ามเปิดใช้จริง)

- supervised mode ทั้งชุด (supervisor/event/checkpoint/policy ใหม่ + hunter production wiring ตาม `cmd/rein/hunter.go` — supervised mode แบบ live และ remote recovery ยังคงถูก fence)
- เปิดได้เมื่อ: Phase B–D เสร็จตาม TEST_PLAN + live pilot ได้รับ authorization (Phase E)
