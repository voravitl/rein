# Controller boundary: supervised mode dispatch/ack ownership (v0.2 design)

- **Status:** DESIGN (docs only — ไม่เปลี่ยน code, ไม่เปิด supervised mode, ไม่แตะ production path)
- **Baseline:** HEAD `2ccc0a0` · อ่านจริง: `internal/run/run.go` (Marker/Start/Resume/End), `cmd/rein/run.go` (tick entry), `internal/run/tick.go`, `skills/worktree-pipeline/scripts/ocloop2.py`, `internal/routing/lease.go`, `cmd/rein/route_launch.go`, `docs/MIGRATION_V0_1_TO_V0_2.th.md`
- ขอบเขต: ตัดสินว่า "ใคร dispatch/ack" ในแต่ละ mode + เงื่อนไขเปลี่ยน mode + rollback — ส่วน consumer/trigger/loop budget อยู่ slice ถัดไป (ดู WIRING_DECISION §B)

## 1. ของเดิมที่ reuse (ห้ามสร้างซ้ำ)

| ความสามารถ | ของเดิม | ไฟล์:บรรทัด |
|---|---|---|
| run marker + owner liveness (pid+start, session rebind) | มีแล้ว | `internal/run/run.go:58-78` (Marker), `:334-350` (OwnerAlive), `:402-450` (Start), `:491-509` (Resume) |
| marker ห้ามซ้อน (exclusive create) | มีแล้ว | `run.go:414-422` (`ErrActive`), `:293-` (writeMarker exclusive) |
| waiter consume/ack ฝั่ง legacy | มีแล้ว | `ocloop2.py:1-41` (ack heartbeat-only, พิมพ์ deliveryId ของ real batch ให้คน ack เอง) |
| Orca dispatch fencing (pending → dispatched, settle ปลด) | มีแล้ว | `route_launch.go:117-154`, `lease.go:179-189` |
| single-writer lease (process identity, unknown = block) | มีแล้ว | `lease.go:41-69` (holderBlocks), `:105-142` (AcquireLease), `:192-224` (EndLease) |
| single-use receipt | มีแล้ว | `lease.go:149-168` (ConsumeReceipt) |
| tick single-flight (flock, stale → deny spawn) | มีแล้ว | `internal/run/tick.go`, `cmd/rein/run.go:103-107` |
| mode switch / rollback หลักการ | มีแล้ว (design) | `MIGRATION_V0_1_TO_V0_2.th.md` §1–§3 |

## 2. ผู้ dispatch/ack ในแต่ละ mode (ตัดสิน)

| | Legacy (ของเดิม — ไม่เปลี่ยน) | Supervised (proposed) |
|---|---|---|
| dispatch worker | coordinator ผ่าน `worker-start` / preamble route | **supervisor ชุดเดียวต่อ run** (เรียก `route launch` เดิม — ไม่สร้าง launcher ใหม่) |
| ack `worker_done/escalation/question` | waiter `ocloop2.py` (heartbeat ack ทุกนาที; real batch พิมพ์ deliveryId ให้ coordinator ack เอง) | **supervisor** (consume → `Record` event ก่อน → ค่อย ack; redelivery dedupe ด้วย EventID) |
| ack heartbeat-only | `ocloop2.py` | supervisor (กฎเดิม: heartbeat-only ack ได้, real batch ห้ามกลืน) |
| review/advisor | coordinator ผ่าน `advise.sh` (เส้นเดิม ไม่เปลี่ยน) | เหมือนเดิม — supervisor ไม่เรียก advisor เอง (idle loop ห้ามเรียก LLM) |
| merge/deploy/release | human เท่านั้น | เหมือนเดิม |
| budget/approve/policy | owner | เหมือนเดิม — supervisor อ่านได้อย่างเดียว |

**Single-control-owner rule:** ต่อ run มีผู้ dispatch/ack เพียงชุดเดียว ห้ามมีช่วงที่ coordinator loop เดิม + supervisor ทำงานพร้อมกัน (ดู §4)

## 3. ทำไม supervisor ต้องอยู่นอก coordinator session

- owner ของ run คือ coordinator session (`SessionID` + `PID` + `StartTime` ใน marker) — ถ้า coordinator หมด quota / process ตาย ระบบเดิมหยุด (`ErrActive` pending, hook เงียบ)
- supervisor จึงต้องเป็น **process แยก** (ไม่ใช่ subagent ไม่ใช่ thread ใน coordinator session) ที่อ่าน marker + event log + lease/receipt เดิมได้โดยไม่ต้องมี coordinator alive
- supervisor ไม่ผูกกับ `SessionID` ของ coordinator: มันอ่าน `Marker.Run/Root/PID` เพื่อรู้ว่า run ไหน + coordinator ยัง alive ไหม (ใช้ `OwnerAlive()` เดิม — `run.go:334`) แต่ไม่ต้องเป็น owner เอง
- การกระทำของ supervisor จำกัดเฉพาะที่ approved task graph + policy อนุญาต (ดู §5); เรื่องใหม่เกินแผน → `WAITING_HUMAN` ไม่ขยาย scope เอง

## 4. เงื่อนไขเปลี่ยน mode (fail-closed ทั้งหมด)

**Legacy → Supervised (เปิด):**
1. `orca_cleanup.py` + `cleanup_worktrees.py` ผ่าน — ไม่มี live worker/terminal/worktree ค้าง (ขั้นตอนเดิม `SKILL.md` §8)
2. `route holds` ว่าง + `route status` ไม่มี pool ค้างที่ต้อง reconcile
3. `rein run audit` ไม่มี COORDINATOR_DRIFT ค้าง
4. coordinator หยุด waiter (`ocloop2.py`) ก่อน — verify ด้วย process check ว่าไม่มี waiter ของ run นี้เหลือ (ห้ามมีช่วงทับซ้อน)
5. เปิด supervised flag (proposed — **ห้าม default on** จนกว่า Phase B–C เสร็จ + owner อนุมัติ) + supervisor อ่าน approved task graph + policy ที่บันทึกไว้แล้วเท่านั้น

**Supervised → Legacy (rollback):**
1. หยุด supervisor ก่อน (verify process gone — ไม่ใช่แค่ส่ง signal)
2. verify ไม่มี writer/lease/hold/event-unacked ค้าง (`TaskBusy` ว่าง, holds ว่าง, event log ไม่มี batch ที่ record แล้วแต่ยังไม่ ack)
3. coordinator กลับมา dispatch เอง + restart waiter
4. ห้ามมีช่วงที่ทั้ง supervisor และ coordinator dispatch พร้อมกัน (single-control-owner)

**ตรวจจับ violation:** ถ้าพบ dispatch จากสอง controller ใน run เดียวกัน (lease DecisionID ใหม่ซ้อนขณะ supervisor active, หรือ waiter ack ข้าม supervisor) → หยุดทั้งคู่, event เป็น `BLOCKED_RECONCILIATION`, รอ owner — ห้ามแก้ด้วยการปิด guard ฝั่งใดฝั่งหนึ่ง

## 5. อำนาจของ supervisor (allowlist — นอกนี้ห้าม)

ทำได้:
- อ่าน: marker, event log, checkpoint, lease/receipt/hold, budget/ledger, cooldowns, approved task graph + policy
- เขียน: **event log ของตัวเองเท่านั้น** (`Record` — append-only, persist-ก่อน-ack) + checkpoint finalize ตอน safe boundary
- เรียก: `route launch` (ผ่าน receipt เดิม — ไม่ bypass Prepare/Validate), `route cooldown/clear` (ด้วย observed evidence — ไม่เดา reset), `route settle` (เมื่อ worker จบพิสูจน์ได้), probe ภายใต้เพดานแยก
- ack: Orca delivery ที่ตัวเอง consume (หลัง `Record` สำเร็จ — redelivery dedupe ด้วย EventID)

ทำไม่ได้ (fail closed + event เหตุผล):
- แก้ owner policy, marker (ยกเว้น field ที่ schema ใหม่กำหนดชัด + owner อนุมัติ), supervisor journal ของ run อื่น, receipts/leases ของ task อื่น, authoritative budget state, finalized verification evidence
- เปิด writer ใหม่ขณะ `TaskBusy(task) != ""` (liveness พิสูจน์ไม่ได้ → `BLOCKED_RECONCILIATION`)
- reuse receipt (`ConsumeReceipt` เดิมกันอยู่ — `lease.go:149`), auto-unlock stale lock (policy ให้ owner reconcile — `route reconcile` user-only)
- เปิด paid overage, ปิด guardrails, push/merge/deploy, ใช้ production accounts/data
- เรียก LLM ใดๆ ใน idle loop (รวม advisor — supervisor ไม่เรียก `advise.sh`)

## 6. สิ่งที่ slice นี้ยังไม่ตัดสิน (slice ถัดไป)

- consumer: Orca check JSON → classify → `Record` → ack ลำดับไหน + redelivery dedupe ตรงไหน (test-only slice)
- checkpoint trigger: finalize ตอนไหน (writer-stopped proof ไม่ใช่ launcher exit) + restore-into-worktree จริง (test-only slice)
- idle loop budget: รอบเวลาเท่าไร, probe เพดานแยกเท่าไร, ทุก pool หมด → park อย่างไร (loop skeleton slice + R6/R7 matrix)
- schema: marker/supervised-flag/event-log path ใหม่เป็น additive-only (config เก่าต้องใช้ต่อได้ — MIGRATION §4)
