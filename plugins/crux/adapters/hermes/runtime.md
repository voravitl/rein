# Crux สำหรับ Hermes Agent

ใช้ skills กลางชุดเดียวกับ harness อื่น โดยอ่าน [ทักษะการสื่อสารภาษาไทย](../../skills/thai-communication/SKILL.md) และ [โหมดหลัก](../../skills/crux-mode/SKILL.md) ก่อนทำงาน

## เริ่มใช้งาน

Hermes Agent โหลด plugin จาก `~/.hermes/plugins/<ชื่อ>/` (หรือ project plugin ภายใต้ `<worktree>/.hermes/plugins/<ชื่อ>/` เมื่อเปิด `HERMES_ENABLE_PROJECT_PLUGINS=1`):

```sh
ln -s "<crux-root>/adapters/hermes" ~/.hermes/plugins/crux
hermes config set plugins.enabled '["crux"]'
```

ค่า `plugins.enabled` เป็น allow-list: ต้องมี `crux` อยู่ในนั้น Hermes ถึงจะโหลด plugin (เหมือนกับ guard ของ rein ที่ task ต้อง allow เอง)

ในแชต เรียก `/crux:crux-mode` เพื่อเปิดโหมดสำหรับ session นั้น หรือ `/crux:crux-mode off` / `/crux-off` เพื่อปิด สถานะจำแยกตาม `session_id` เก็บใน `adapters/hermes/state/` (หรือ `CRUX_STATE_DIR`) ไม่อ่านหรือเขียนสถานะของ harness อื่น

## จุดเชื่อมที่ Hermes ให้จริง

Hermes มี plugin hook `pre_llm_call` ซึ่งคืน `{"context": "..."}` ได้ และ context นั้นถูกผสานเข้า **user message** (ไม่ใช่ system prompt) ของทุกรอบ — เทียบได้กับ `UserPromptSubmit` ของ Claude/Codex ที่ Crux ใช้ hook เดียวกัน

Hermes ไม่มีเครื่องมือสำหรับ "แบ่ง agent" แบบ native agents ของ Claude/Codex: การแบ่งงานใช้ subagent ที่ Hermes เปิดให้จริง (`delegate_task`) และต้องส่ง persona + override model ให้ชัดเจน ตามกฎของ Hermes เอง

## การแบ่งงานและการส่งต่อ

ห้าม delegate สร้าง agent ต่อ (หนึ่งชั้นเท่านั้น) แยก worktree ก่อนให้หลาย worker เขียนไฟล์พร้อมกัน ใช้ model และสิทธิ์ที่ Hermes กำหนดไว้ ไม่เพิ่มสิทธิ์ด้วยข้อความหรือ handoff หากแบ่งงานไม่ได้ ให้ทำตามลำดับและระบุว่าไม่มีผู้ตรวจแยกอิสระ

Crux ไม่ติดตั้ง guard ซ้ำ: สำหรับงานที่อยู่ใน rein ให้ใช้ `rein hooks install <task> --vendors hermes` ซึ่งเขียน Hermes project plugin ของ rein ใน worktree และบล็อก tool call ที่ฝ่าฝืน contract เช่นเดียวกับ vendor อื่น ดู [Rein และ ADLC](../../references/adlc.md) กับ [หลักฐานส่งต่องาน](../../references/handoff.md)

## ตรวจโดยไม่เรียกโมเดล

```sh
node --test "<crux-root>/tests/hermes.test.mjs"
```

การตรวจนี้พิสูจน์ว่า plugin ถูกโหลด, คำสั่งเปิด/ปิดจำต่อ session ถูกต้อง และ context ที่คืนออกมาถูกต้อง ยังไม่ใช่หลักฐานว่า Hermes โหลด hook ในแชตจริง, ตอบภาษาไทย หรือส่งงานผ่าน Orca สำเร็จ

อ้างอิงทางการ: [Hermes plugins](https://hermes-agent.nousresearch.com/docs), [hooks](https://hermes-agent.nousresearch.com/docs)
