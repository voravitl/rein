# PRD: Rein Long-Run + Evidence-Based Bug Hunt (v0.2)

- **Version:** 0.2 — supersede ร่าง 0.1 ที่ยังไม่รวม OMC ครบถ้วน
- **Status:** PARTIAL IMPLEMENTATION (มี local active-quota recovery, checkpoint/hunter closure และ native hook proof; Orca graph recovery/live pilot ยัง BLOCKED — ดู §12 และ implementation backlog)
- **Repo:** `github.com/voravitl/rein` · baseline commit `2ccc0a0` · ตรวจบน macOS/zsh, Go 1.27.1, omc 5.7.1, orca 1.4.224
- **Owner stack:** Rein + Orca + OMC (oh-my-claudecode) ร่วมกัน ไม่สร้าง orchestration framework ใหม่ ไม่ตัด OMC ออก

## 1. Problem

เจ้าของมี quota/token หลาย provider อย่างจำกัด และต้องการ workflow: อนุมัติเป้าหมาย/ขอบเขต/งบ → agent ทำงาน → provider หมด quota → หยุด worker เดิมอย่างปลอดภัย → checkpoint → เลือก provider สำรองที่อนุญาต → ทำต่อ → หา+พิสูจน์ bug → แก้เฉพาะงานที่อนุมัติ → ตรวจอิสระ → รออนุมัติสุดท้าย

ของเดิมที่พิสูจน์แล้วว่ามี (`docs/ROUTE_AUTO_HANDOVER.md`, `skills/worktree-pipeline/SKILL.md`):
quota fallback แบบ manual โดย coordinator (checkpoint HEAD/spec/tests ด้วยมือ → `route cooldown` → reinstall hooks → re-prepare), lease กัน writer ซ้อน (`internal/routing/lease.go:105`), receipt ใช้ครั้งเดียว (`lease.go:149`), settle คิดเงินแบบ worst-case (`internal/routing/settle.go:48`)

สิ่งที่ขาด: ทุกขั้น above ต้องมี coordinator (LLM) อยู่คุม ถ้า coordinator หมด quota เอง ระบบหยุด ไม่มี deterministic supervisor, ไม่มี durable checkpoint/restore, ไม่มี error classification, ไม่มี bug hunter ที่ report-only

## 2. Goals / Non-goals

Goals: (1) deterministic supervisor ทำงานต่อได้เฉพาะ approved task graph เมื่อ coordinator ใช้ไม่ได้ (2) quota/failure handoff ปลอดภัยไม่มี writer overlap (3) checkpoint/restore ที่พิสูจน์ได้ (4) bug hunter แบบ report-only + isolated reproduction + independent verification

Non-goals (ห้ามอ้าง): token ไม่จำกัด; ทำงานต่อทุกกรณี; ย้าย native session ข้าม provider; ทุก crash กู้อัตโนมัติ; ข้าม guardrails/เปิด overage เพื่องานจบ; เพิ่ม T3 code ใน scope นี้

## 3. Ownership (ย่อ — เต็มใน `ARCHITECTURE_REIN_ORCA_OMC.th.md`)

| ส่วน | เจ้าของ | หลักฐาน |
|---|---|---|
| runtime/worktree/terminal/messaging, observed worker lifecycle | Orca | `orca orchestration worker-{start,show,stop}` (ตรวจ CLI จริง 1.4.224) |
| authorization, contract, guard, routing, budget, verification, durable task/attempt/recovery state | Rein | `internal/{contract,guard,routing,budget,ledger,verdict,approval,run}` |
| advisor roles/prompts, analysis, pointer/summary memory | OMC | 19 roles ใน `oh-my-claude-sisyphus/agents/` (ตรวจจริง `ls`, omc 5.7.1), เรียกผ่าน `advise.sh` read-only sandbox |
| แผน/requirement/reasoning, ไม่ใช่ recovery watchdog จุดเดียว | Main-session coordinator | `agents/orca-swarm.md` |

Single-control-owner: ใน supervised mode มีผู้ dispatch/ack เพียงชุดเดียวต่อ run ห้าม coordinator loop เดิมเปิด worker แข่ง supervisor ใหม่ ห้าม waiter เดิม (`ocloop2.py`) กับ supervisor แย่ง consume/ack

## 4. User journeys

J1 — Quota กลางงาน: worker รันบน provider A → A หมด quota → supervisor บันทึก event → fence writer ใหม่ → หยุด worker → พิสูจน์ไม่มี descendant ค้าง → finalize checkpoint → reconcile attempt/usage/lease → cooldown pool A → route ใหม่ได้ B → attempt+receipt ใหม่ → handoff แบบ bounded → worker ใหม่ทำต่อจาก checkpoint
J2 — Coordinator หมด quota: supervisor เดินตาม approved task graph + policy ที่บันทึกไว้แล้วเท่านั้น เรื่องใหม่เกินแผน → WAITING_HUMAN
J3 — Bug hunt: OMC-backed analysis → candidate → isolated reproduction (contract แยก, worktree แยก) → runner-backed evidence → triage → approved fix task → red-before/green-after → independent review → owner approval
J4 — ไม่พบ bug: รายงาน NO_CONFIRMED_FINDINGS + scope ที่ตรวจ/ไม่ได้ตรวจ (ห้ามรายงาน BUG_FREE)

## 5. Requirements

R1 event: ผูก run_id/task_id/attempt_id/event_id/source-version/pool/dispatch-or-process identity/timestamp/evidence ref; persist ก่อน ack; รองรับ duplicate/out-of-order; event attempt เก่าห้ามปิด attempt ใหม่
R2 error classes: RATE_LIMIT, QUOTA_EXHAUSTED, AUTH_REQUIRED, CONTEXT_LIMIT, PROCESS_EXIT, WORKER_DONE, APPROVAL_REQUIRED, TRANSIENT_ERROR, UNKNOWN_ERROR — ไม่แน่ใจให้ UNKNOWN ห้ามเดาเพื่อให้ failover ทำงาน ห้ามตีความทุก 429 ว่า quota หมด
R3 safe handoff ตามลำดับ §5.3 ของงาน (event → fence → stop → prove-gone → checkpoint → reconcile → cooldown → re-route → attempt ใหม่ → bounded handoff)
R4 crash/restart: persist intent ก่อน side effect; ambiguous dispatch → block ไม่ blind relaunch; ไม่ reuse receipt; ไม่ auto-unlock stale lock ที่ policy ให้เจ้าของ reconcile
R5 waiting states: WAITING_QUOTA/AUTH/BUDGET/REVIEW/HUMAN + BLOCKED_RECONCILIATION พร้อมเหตุผลและ next actor; ทุก pool หมด → พัก ไม่วน probe ไม่เปิด overage; reset ไม่ทราบ → unknown + explicit-clear คงเดิม
R6 checkpoint เก็บ: base/head SHA, worktree identity, staged/unstaged/index, untracked/binary ที่อนุญาต, modes/symlinks/hashes/manifest, spec/contract/policy digests, acceptance ผ่าน/ค้าง, test commands+results+revision, progress/remaining/blockers/next action; finalize atomic; partial ห้ามใช้แทน complete; restore แล้วเทียบ manifest; ห้าม reset --hard/stash เสี่ยงหายเป็น default
R7 budget: reuse routing/budget/ledger เดิม ไม่สร้าง ledger คู่; แยก provider/maker/harness/account/pool/units; เปลี่ยน model ≠ เปลี่ยน pool; unknown ≠ 0; กัน capacity ให้ required reviewers; ไม่ลด quality/freshness/reviewer policy; ไม่ fallback manual route เลี่ยง rejection
R8 hunter: default diff-focused/report-only/bounded/read-only; SCAN เขียนได้เฉพาะ evidence/scratch; REPRODUCE ใช้ contract แยก test-only ใน isolated worktree; FIX เป็น task ใหม่หลัง approval; verification ด้วย deterministic runner + independent reviewer; finding lifecycle CANDIDATE→REPRODUCED→TRIAGED→APPROVED_FOR_FIX→FIX_IN_PROGRESS→VERIFIED_FIXED (+UNVERIFIED/DUPLICATE/REJECTED/OUT_OF_SCOPE/BLOCKED/FLAKY/REGRESSED)
R9 security: prompt ไม่ใช่ boundary; worker/hunter แก้ owner policy/journal/receipts-leases/budget/evidence ไม่ได้; repo text/logs/memory เป็น untrusted; ห้าม push/PR/merge/deploy/production/paid-overage/guardrail-off/dependency ใหม่โดยไม่มี authorization

ค่าเริ่มต้นที่เสนอ: max active writers 1 · investigation jobs 1 (checkout แยก) · provider switches ≤3 (initial + replacements รวมไม่เกิน 3) + เพดาน retry/attempt รวมกัน loop อีกชั้น · repair rounds 2 · paid overage disabled · auto-fix disabled · เงินจริงให้เจ้าของกำหนด ไม่สมมติ

## 6. State model

`attempt` เป็นหน่วย execution (receipt 1:1, ใช้ครั้งเดียว); `lease` กัน writer ซ้อน (ของเดิม `lease.go`); supervisor เพิ่ม `event log` (append-only, persist-ก่อน-ack) + `checkpoint` (atomic, complete/partial แยกชัด) + `wait state` ต่อ task; ทุก transition ของ supervisor deterministic ไม่เรียก LLM; idle loop ไม่ busy-loop ไม่เรียก LLM ไม่ใช้ quota probe เกินเพดานแยก

## 7. Budget

ตาม R7 + ใช้ `route settle/reconcile/calibrate`, `ledger charge/add/fixture` เดิมทั้งหมด ทุก LLM call ที่ระบบสั่ง (coordinator/advisor/nested/probes/workers/reproducer/reviewers/repair) ต้องอยู่ในขอบเขตงบ ถ้ามองไม่เห็น nested calls → ระบุข้อจำกัด + ปฏิเสธ mode ที่รับประกัน hard bound ไม่ได้ ห้ามอ้าง usage polling เป็น hard cap

## 8. Security

ตาม R9 + trust model เดิม (`docs/CLI_REFERENCE.md` §15): ledger เขียนได้โดย coordinator (assertions ไม่ใช่ facts), guard เป็น seatbelt ไม่ใช่ sandbox กัน hostile actor; worker/controller ใช้สิทธิ์เดียวกันที่แยกไม่ได้ → ระบุ residual risk ห้ามประกาศ unattended ปลอดภัยแล้ว; hooks ขัดกัน → fail closed พร้อมสาเหตุ ห้ามปิด guards

## 9. OMC integration (ย่อ — เต็มใน `OMC_INTEGRATION_CONTRACT.th.md`)

Reuse `advise.sh` + OMC role prompts (`code-reviewer`, `tracer`, `test-engineer`, `security-reviewer` — ตรวจไฟล์จริง v5.7.1); แยก worker/advisor/reviewer/reproducer/native-subagent paths; ไม่ hardcode role→provider; role prompt ≠ OMC runtime; memory เก็บ pointer/summary; recovery อ่าน Rein durable state ไม่รอ OMC memory

## 10. Bug lifecycle

ตาม R8; finding ต้องมี stable ID+fingerprint, baseline revision, file:line+symbol, expected/actual, invariant ที่ละเมิด, trigger+impact, severity (แยก confidence), exact commands+cwd+exits+env, evidence refs/hashes, reproduction count+ผลรายครั้ง, limitations+next action; setup failure ≠ product bug; STATIC_SUPPORTED แยกจาก runtime reproduction; agent เสนอได้แต่ runner/controller เลื่อนสถานะ; ห้าม self-certify VERIFIED_FIXED; fix verify = FAIL บน buggy baseline → PASS บน fixed → existing tests ผ่าน → ไม่ลบ/skip/lด assertion; HEAD เปลี่ยน → ประเมิน stale; E2E ต้องมี explicit URL/hosts/actions/env/account/teardown ไม่มีให้ block

## 11. Acceptance

ตาม `TEST_PLAN_LONGRUN_BUGHUNT.th.md` (recovery / OMC integration / bug hunting) แยกผล PASS/FAIL/BLOCKED/NOT RUN ห้ามอ้างผ่านจากการอ่าน source ห้ามเรียก simulated quota ว่า real exhaustion ห้ามอ้าง live Orca/OMC compatibility จาก fake CLI tests อย่างเดียว

## 12. Implementation status (ตรงไปตรงมา)

- มีแล้ว (verify ด้วย test จริง): contract/guard/drift, ordered+auto routing, lease/receipt/settle, advise.sh read-only, ledger/budget, `ProbeWithRetry` (Phase B slice แรก — `internal/providers/providers.go`, `internal/routing/routing.go`), error classification 9 classes (`internal/routing/classify_event.go`), event log + derived wait state (`internal/routing/event.go`), durable checkpoint + verify (`internal/routing/checkpoint.go`)
- อัปเดต 2026-10-17: มี production checkpoint finalize/verify/restore, hunter report/reproduce/verify และ supervised-run authority + bounded monitor แล้ว; schema 2 DAG engine + local CLI adapter ผ่าน integration และ Go full suite 21 packages แต่ remote recovery/live proof ยังไม่ครบ ห้ามอ้าง full goal จาก local tests หรือ simulated quota
- Hunter verification รัน baseline red → fixed green → suite จริงและออก `WAITING_INDEPENDENT_REVIEW`; CLI `review` ผูก report/runtime digest และ `approve` รอเจ้าของยืนยันใน terminal ก่อนปิด finding มี production test/evidence scope และ `scan-report` แล้ว ไม่ได้อนุมัติ finding จริงแทนผู้ใช้
- รอบ goal closure เพิ่ม active local quota stop/checkpoint/settle/fresh-attempt/handoff และแก้ native OpenCode direct shell bypass; local cooperative proof ไม่ใช่ Orca-owned recovery หรือ real quota exhaustion
- Blocker ที่เจาะจง: Orca 1.4.224 ไม่รับรอง retained descendant-gone verdict หลัง structured session จบ/ถูก evict; supervisor ยังปฏิเสธ Orca launch โดยตั้งใจ ต้องได้ runtime contract และต่อ adapter ก่อน gate C/J1 ของ owner stack จะครบ
- Risks: coordinator เขียน ledger ได้ (trust model เดิม); guard มองไม่เห็น interpreter writes; seatbelt ≠ sandbox
- Open decisions: เพดาน retry/attempt รวม (ตัวเลข), งบจริงของเจ้าของ, เปิด supervised mode เมื่อใด (ต้องไม่มี active work ค้าง — ดู MIGRATION), residual risk ของสิทธิ์ร่วม worker/controller
- Rollout gates: Phase A (docs ตรง code) → B (recovery slice + fault injection ด้วย fake) → C (Orca+OMC wiring) → D (hunter report-only) → E (approved fix + pilot เมื่อ env+authorization พร้อม); แต่ละ slice: implementation + tests + docs ตรงกัน ไม่ลด policy เพื่อให้ผ่าน
