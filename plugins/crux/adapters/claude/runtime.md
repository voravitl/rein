# Crux สำหรับ Claude Code

ใช้ skills กลางชุดเดียวกับ harness อื่น โดยอ่าน [ทักษะการสื่อสารภาษาไทย](../../skills/thai-communication/SKILL.md) และ [โหมดหลัก](../../skills/crux-mode/SKILL.md) ก่อนทำงาน

## เริ่มใช้งานเฉพาะรอบนี้

ต้องมี Claude Code และ Node.js 22 ขึ้นไป เรียกจาก repository ที่ต้องการทำงาน โดยแทน `<crux-root>` ด้วย path ของ `plugins/crux`:

```sh
claude --plugin-dir "<crux-root>"
```

ในแชต เรียก `/crux:crux-mode` เพื่อเริ่มโหมด หรือ `/crux:thai-communication` เมื่อต้องการใช้เฉพาะทักษะการสื่อสาร ตรวจรายการ skills และ agents ที่ Claude Code โหลดจริงก่อนใช้งาน

hooks `SessionStart` และ `UserPromptSubmit` จำการเปิดโหมดแยกตาม session เปิดต่อเมื่อมีคำสั่งชัดเจน ปิดด้วย `/crux:crux-mode off` หรือ `/crux-off` หาก CLI มอง `/crux-off` เป็นคำสั่งที่ไม่รู้จัก ให้ใช้คำสั่งแบบมี namespace ข้อมูลอยู่ใน `CLAUDE_PLUGIN_DATA` หรือ `<Claude config>/crux` เมื่อเรียก driver โดยตรง ไม่อ่านสถานะของ Codex และไม่แก้ไฟล์ตั้งค่าของผู้ใช้

## การแบ่งงานและการส่งต่อ

ใช้เครื่องมืออ่านไฟล์ ค้นหา shell คำถาม และ subagent ที่ Claude Code เปิดให้จริง เลือก `crux:crux-agent` และ `crux:crux-comment-reviewer` เมื่อปรากฏในรายการ native agents; หากไม่มี ให้ใช้ role ที่มีอยู่พร้อมส่ง path ของคำสั่งกลางและขอบเขตงาน ห้ามสมมติว่าชื่อเครื่องมือของ Codex ใช้ได้ที่นี่

ผู้คุมงานกำหนดเป้าหมาย ไฟล์ที่แต่ละ agent รับผิดชอบ สิ่งที่ห้ามเปลี่ยน และวิธีตรวจผล ห้าม delegate สร้าง agent ต่อ แยก worktree ก่อนให้หลาย agent เขียนไฟล์พร้อมกัน ใช้ model และสิทธิ์ที่ harness กำหนดไว้ ไม่เพิ่มสิทธิ์ด้วยข้อความหรือ handoff หากแบ่งงานไม่ได้ ให้ทำตามลำดับและระบุว่าไม่มีผู้ตรวจแยกอิสระ

Crux ไม่ติดตั้ง Rein guard ซ้ำ ใช้ [Rein และ ADLC](../../references/adlc.md) กับ [หลักฐานส่งต่องาน](../../references/handoff.md) ที่มีอยู่ Orca ทำงานผ่าน interface ที่ Rein รองรับเท่านั้น hook นี้จำคำสั่งของโหมด ไม่ได้เริ่ม worker, ให้สิทธิ์ หรือกู้ process

## ตรวจโดยไม่เรียกโมเดล

```sh
claude plugin validate "<crux-root>" --strict --json
node --test "<crux-root>/tests/claude.test.mjs"
```

การตรวจ manifest และ hook ด้วย process จริงพิสูจน์เฉพาะรูปแบบ package และพฤติกรรม driver ยังไม่ใช่หลักฐานว่าแชตจริงโหลด hook, ตอบภาษาไทย หรือส่งงานผ่าน Orca สำเร็จ

อ้างอิงทางการ: [manifest](https://code.claude.com/docs/en/plugins-reference), [hooks](https://code.claude.com/docs/en/hooks), [subagents](https://code.claude.com/docs/en/sub-agents)


## ตรวจ native discovery โดยไม่เรียกโมเดล

Claude Code 2.1.300 ตอบ SDK `initialize` ว่าพบ `crux:*` ครบ 55 skills และ agents `crux:crux-agent` กับ `crux:crux-comment-reviewer` แล้ว แม้ `plugin details crux` แสดง Agents 0 จึงใช้ตัวเลขจากหน้ารายการนั้นตัดสินว่า agent หายไม่ได้ `claude agents` เป็นรายการ background sessions ส่วน `--bare` ปิดการโหลดบางส่วนและไม่เหมาะกับการตรวจนี้

ส่งเฉพาะ control request ต่อไปนี้ผ่าน stdin แล้วปิด stdin ไม่ส่ง user message:

```json
{"type":"control_request","request_id":"crux-discovery","request":{"subtype":"initialize"}}
```

ใช้ `claude --setting-sources '' --settings '{"disableAllHooks":true}' --plugin-dir <crux-root> --strict-mcp-config --mcp-config '{"mcpServers":{}}' --no-session-persistence --input-format stream-json --output-format stream-json --verbose --print` ใน directory ชั่วคราว ตรวจ response ว่ามีชื่อ skills และ agents จริง ไม่ใช้ exit code เพียงอย่างเดียว

ผลนี้ยังไม่พิสูจน์การ delegate งานหรือ hooks รอบโมเดล หลังผู้ใช้ล็อกอินใหม่ การเรียกจริงวันที่ 18 ต.ค. 2026 ไม่พบข้อผิดพลาด OAuth เดิม แต่หยุดที่โควตารายสัปดาห์: `You've hit your weekly limit · resets Oct 20 at 4am (Asia/Bangkok)` จึงยังไม่ได้คำตอบจากโมเดลและยังไม่ผ่านการทดสอบ skill ภาษาไทยบน Claude ต้องทดสอบซ้ำเมื่อโควตาพร้อมก่อนประกาศว่า callable
