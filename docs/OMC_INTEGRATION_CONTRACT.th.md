# OMC Integration Contract (v0.2)

- **Version:** 0.2 · OMC `oh-my-claude-sisyphus@5.7.1` (ตรวจจริง `npm ls -g`, roles 19 ไฟล์)
- สัญญานี้ผูก role **prompts** ของ OMC กับ execution harness ของ Rein ไม่ใช่การรองรับ OMC runtime ทั้งชุด

## 1. Role resolution / version pinning

- ลำดับค้นหา role (ของเดิม `advise.sh:27-30`): `skills/worktree-pipeline/templates/roles/<role>.md` ก่อน แล้วค่อย `$(npm root -g)/oh-my-claude-sisyphus/agents/<role>.md`; ไม่พบ → exit 2 fail closed
- Roles ที่ reuse (ตรวจไฟล์จริง): `critic`, `tracer`, `code-reviewer`, `security-reviewer`, `test-engineer`, `architect` (+ pipeline roles `blast-radius`, `claim-auditor` ใน `templates/roles/`)
- Pin (C6 implemented): ทุก advisor call บันทึก `role_source`, `omc_version`, `role_digest` ใน ledger โดย OMC version อ่านจาก `package.json` ของ installation เดียวกับ role ที่เลือกจริง (resolve symlink); ไม่ใช้ `omc --version` จาก global CLI ที่อาจเป็นคนละ installation และหากอ่าน version ไม่ได้จะ exit 2 ก่อน route check/provider
- `role_digest` คือ SHA256 ของ role text ที่โหลดและส่งใน prompt จริง หลังตัด YAML frontmatter และ trailing newlines ตาม shell เดิม; role text เปลี่ยน → digest เปลี่ยน ใช้แยกหลักฐานข้าม prompt/version ได้ แต่ยังไม่มี automatic evidence invalidation
- Pipeline-local roles บันทึก `role_source=pipeline`, `omc_version=unknown` อย่างชัดเจน และ digest ของ text ที่ใช้ โดยไม่ต้องติดตั้ง OMC; ledger rows เดิมที่ไม่มี metadata ยังอ่านได้
- ไม่ hardcode role→provider/model ใดๆ (เลือกผ่าน `route auto --phase review` + `ADVISE_PROVIDER/MODEL/EFFORT`)

## 2. Execution paths (แยก 5 เส้น — ห้ามบังคับผ่าน launcher เดียวโดยไม่ตรวจ contract)

| เส้น | วิธี | หลักฐาน |
|---|---|---|
| coding worker | `route launch` (worker receipt) + Orca worker-start / shell preamble | `route_launch.go:47-79`, `SKILL.md` §2 |
| read-only advisor | `rein advise` / `advise.sh` + review receipt (`route check --phase review`) | `advise.sh:48-50`, `route_launch.go:48-50` (review receipt ห้าม launch implementation) |
| formal reviewer | review worktree แยก + review report 6 หัวข้อ + `verdict record` | `AGENTS.md` §2, `SKILL.md` §4 |
| test-only reproducer | production `hunter reproduce`: per-attempt contract/index อนุญาต `hunter-tests/**`, `evidence/**` ใน disposable clone; ตรวจ source/Git/mode/symlink/contract หลังรัน ปฏิเสธ scope violation; เป็น trusted-command integrity boundary ไม่ใช่ OS sandbox | `internal/hunter/execute.go`, `internal/hunter/scope.go` |
| OMC native subagent | ใช้ได้เฉพาะคำถามที่ไม่แตะ repo (`omc ask` รัน codex/agy แบบไม่มี sandbox — `omc-toolkit.md:38`); ห้ามใช้แทน advisor ที่ต้องอ่าน code | `omc-toolkit.md:38-39` (+ `omc ask antigravity` hangs — upstream antigravity-cli#76) |

## 3. State ownership / memory boundaries

- OMC memory/notepad/wiki = pointers/summaries; recovery อ่าน Rein durable state (event log/checkpoint/lease/receipt) ได้โดยไม่รอ OMC memory
- stale/missing memory ห้าม resume ผิด revision; OMC summary ไม่ใช่หลักฐานว่า test/review ผ่าน
- ไม่อนุญาต `omc ralph verify` เป็น gate (baseline เทียบผิด — `omc-toolkit.md:55`); gate คือ pack scripts + `rein drift` + review

## 4. Hooks conflicts

- ห้ามเปิด `ralph/autopilot/team/ultragoal` ใน coordinator session (arm `git-guardrails` + budget hooks — `omc-toolkit.md:56`); Orca คือ orchestrator
- เจอ `git-guardrails` block → หยุด + รายงาน `DECISIONS NEEDED` ห้ามตั้ง `OMC_GIT_GUARDRAILS=0` เอง (`agents/orca-swarm.md:42`)
- hook/policy ขัดกัน → fail closed พร้อมสาเหตุ

## 5. Usage accounting (transitive)

- ทุก advisor call บันทึก `ledger call` (`advise.sh:78-81`); tokens มาจาก codex footer, credits จาก kiro log; unknown = unknown ไม่ใช่ 0
- worker/reviewer/probe/repair/reproducer ทุก call ต้องมี attempt + บันทึก charge (`CLI_REFERENCE.md` §15 evidence flow)
- nested/subagent calls ที่มองไม่เห็น → ระบุข้อจำกัดในรายงาน run; ปฏิเสธ mode ที่ต้อง hard bound แต่บังคับไม่ได้

## 6. Capability failures / fallback (ไม่ข้าม policy)

- provider ล้ม → รายงาน ไม่สลับ vendor ของ review เงียบๆ (`omc-toolkit.md:40`)
- unsupported/missing role → fail closed (exit 2) ห้าม fallback เป็น write-capable worker
- optional advisor ไม่พร้อม ≠ review ผ่าน; required reviewer ไม่พร้อม → WAITING_REVIEW
- agy exit 3 / 429 → ย้าย task ไป codex ตาม routing table (`SKILL.md` §1) ไม่ใช่การเดา reset
