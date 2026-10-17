# Architecture: Rein + Orca + OMC (v0.2)

- **Version:** 0.2 · baseline `2ccc0a0` · omc 5.7.1 · orca 1.4.224
- เอกสารนี้คือ design ของ supervised mode (ยังไม่ implement) + บันทึกของเดิมที่ reuse; อะไรที่เขียนว่า "มีแล้ว" ต้องมี file:line กำกับ

## 1. ของเดิมที่ reuse (ห้ามสร้างซ้ำ)

| ความสามารถ | ของเดิม | ไฟล์ |
|---|---|---|
| task contract + guard hook + drift judge | มีแล้ว | `internal/contract`, `internal/guard`, `internal/drift/drift.go:88` |
| ordered routing Prepare/Validate/Launch + cooldown/pool | มีแล้ว | `internal/routing/routing.go:236,301`, `cmd/rein/route_launch.go` |
| auto routing + evidence receipt + settle/hand-over | มีแล้ว | `internal/routing/{auto,receipt,settle}.go` |
| single-writer lease + single-use receipt | มีแล้ว | `internal/routing/lease.go:97,166` |
| Orca dispatch fencing (pending/dispatched, settle ปลด) | มีแล้ว | `cmd/rein/route_launch.go:204-286`, `internal/routing/lease.go:197-246`, `internal/routing/settle.go:48` |
| read-only advisor + ledger call accounting | มีแล้ว | `skills/worktree-pipeline/scripts/advise.sh:48-81` |
| coordinator run guard + tick + approvals | มีแล้ว (B0/B1 บางส่วน) | `internal/run/`, `internal/approval/`, `docs/adr/0002-run-guard-and-approvals.md` |
| probe retry จุดเดียว (Phase B slice แรก) | มีแล้ว | `internal/providers/providers.go` (`probeWithRetry`), `internal/routing/routing.go:294` |
| review report 6 หัวข้อ + verdict ledger + human approve | มีแล้ว | `AGENTS.md` §2, `internal/verdict` |

## 2. สิ่งที่เพิ่ม (เฉพาะที่ขาด)

1. **Supervisor (deterministic, ใน Rein):** event log append-only (persist-ก่อน-ack) + error classification (9 classes) + safe handoff state machine + checkpoint/restore + waiting states; ไม่เรียก LLM; idle loop ไม่ busy-loop; probe เพดานแยก
2. **Checkpoint format + restore verify:** manifest เทียบ hash (ไม่ใช้ reset --hard เป็น default)
3. **Bug hunter report-only:** finding registry/dedupe + test-only reproducer (contract แยก, isolated worktree) + runner-backed verification; reuse OMC roles + `advise.sh` + review pipeline เดิม
4. **Supervised-mode ownership contract:** ใคร dispatch/ack ต่อ run (ดู §4)
5. **Finished-without-pin local worker state (2026-10-17):** worker ที่จบภายในหน้าต่าง setpgid→birth-time read (linux จะรายงาน ErrNoProc กับ zombie) จดสถานะ `child_start_unknown`/`pid_start_unknown` อย่างชัดเจน ห้ามปลอมเลข start, การปลดยัง gate ด้วย proof ว่าทั้ง group ขาดจริง; เริ่มจากเทสพังใน docker (`TestGraphCLIRevalidatesCompletedEvidenceOnRestart`, `internal/routing/lease.go:303-318`, `cmd/rein/graph_adapter.go:124-138`)
6. **Census same-host verifier (2026-10-17, opt-in เรียกใช้เมื่อต้องการ):** Orca รุ่นที่ใช้งาน (≤ 1.4.224) ยังไม่รายงาน verdict ของการจบ worker อย่าง durable — ผู้ตรวจจึงได้พิสูจน์จาก ground truth ของ OS โดยตรง (`rein orca census record --pid N --out F` แล้ว `rein orca census verify --in F`): record ปักหลักฐานจริง (pid, start-marker, ppid) ของ tree ที่มีอยู่ตอน *scan เดียว* (ผ่าน `/proc` บน linux; `/bin/ps` + kern.proc.pid บน darwin; platform อื่น fail-closed exit 2), verify พิสูจน์ว่าตัวตนแต่ละอันหายไปจริง — pid reuse ไม่ลวงการนับ "หาย" เพราะ start marker ต้อง match ด้วย, zombie ถือว่า "ยัง present" เพื่อความเข้ม ไม่นับว่าหาย; snapshot พิสูจน์ได้เฉพาะเครื่อง/บูตเดียวกัน (hostname + boot id) ห้ามเอาไปใช้คนละเครื่อง เป็น fail-closed ตามหลัก; residual ที่ต้องพูดตามจริง: descendant ที่ spawn *หลัง* record หรือ setsid orphan ที่ re-parent ออกนอก tree จะหลุดเพราะการเดินจาก ppid (เทส `TestCensusResidualSpawnAfterRecord` ใน `internal/orca/census_test.go` พิสูจน์เคสนี้ให้เห็นของจริง) ผู้ใช้ยังต้องกำกับด้วยกติกาว่า worker ที่ถูก fence ไม่แตกแขนงหนีกลุ่ม
7. **Subject ชัดเจนเมื่อ launch (2026-10-17):** title ของ terminal/worker ใน Orca เดิมแสดงแต่ชื่องาน (จะรู้ว่า model อะไรก็ต้องเปิด spec) — ตอนนี้ทั้งสองเส้นทาง launch ใช้รูปแบบ `rein <name> · <agent>:<model>` ที่ `--task-title`/`--title` (`internal/orca/orca.go` OrcaSubject) และ `LaunchResult.Subject` คืนค่านั้นให้ journal/evidence ใช้ชุดเดียวกัน; ยังไม่ใส่เลข attempt เพราะตำแหน่ง launch ไม่มี field ตัวนั้นอยู่จริง ห้ามปลอม

## 3. สิ่งที่ชัดเจนว่าไม่ทำ

- orchestration framework ใหม่ (Orca คุม runtime อยู่แล้ว)
- ทีม LLM ใหม่แข่ง advisor/reviewer เดิม
- T3 code ใน scope นี้; paid overage; auto-fix default; `ralph/autopilot/team/ultragoal` ใน coordinator
- OMC memory เป็น authoritative state; advisor/memory override contract/budget/approval/evidence

## 4. Controller boundary (single-control-owner)

- Legacy mode (ของเดิม): coordinator dispatch เองผ่าน `worker-start`, waiter `ocloop2.py` ack `worker_done/escalation/question`, heartbeat ack ทุกนาที (`ocloop2.py:1-20`)
- Supervised mode (proposed): supervisor เป็นผู้ dispatch/ack เพียงชุดเดียวต่อ run; coordinator ส่ง "approved task graph + policy" แล้วถอยเป็น planner/approver; waiter เดิมต้องหยุด (ห้ามแย่ง consume/ack)
- เปลี่ยน mode ได้เมื่อไม่มี active work ค้างเท่านั้น (ดู `MIGRATION_V0_1_TO_V0_2.th.md`); rollback ต้องไม่ทำให้ worker สองชุดซ้อน
- Hooks ขัดกัน → fail closed พร้อมสาเหตุ ห้ามปิด guards

## 5. Data & state ownership

- Rein: event log, checkpoint, lease/receipt/hold/budget/ledger (durable, authoritative)
- Orca: dispatch/terminal/worktree lifecycle ที่สังเกตได้ (observed, ไม่ใช่ permission)
- OMC memory/notepad/wiki: pointers/summaries (untrusted task data; stale/missing ห้ามเปลี่ยน authoritative state)
- Worker output: input สำหรับตรวจ ไม่ใช่ authority อนุมัติสิทธิ์หรือยืนยันผล

## 6. Failure handling (ออกแบบ ไม่ใช่ของเดิม)

- launcher exit ≠ worker exit; terminal idle ≠ done; worker บอก done ≠ tests ผ่าน; parent ตาย ≠ descendants หยุด
- liveness พิสูจน์ไม่ได้ → BLOCKED_RECONCILIATION (ไม่เดาปล่อย lease/เปิด writer)
- PID reuse: เทียบ pid+start (`internal/run/proc_*.go` มี `ProcStart` อยู่แล้ว — reuse)
- ambiguous dispatch / lost ack → block ไม่ blind relaunch; ไม่ claim exactly-once โดยไม่มีหลักฐาน
