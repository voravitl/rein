# Crux สำหรับ Hermes Agent

ใช้ skills กลางชุดเดียวกับ harness อื่น โดยอ่าน [ทักษะการสื่อสารภาษาไทย](../../skills/thai-communication/SKILL.md) และ [โหมดหลัก](../../skills/crux-mode/SKILL.md) ก่อนทำงาน

## เริ่มใช้งาน

Hermes Agent โหลด plugin จาก `~/.hermes/plugins/<ชื่อ>/` (หรือ project plugin ภายใต้ `<worktree>/.hermes/plugins/<ชื่อ>/` เมื่อเปิด `HERMES_ENABLE_PROJECT_PLUGINS=1`):

```sh
ln -s "<crux-root>/adapters/hermes" ~/.hermes/plugins/crux
hermes plugins enable crux
```

ค่า `plugins.enabled` เป็น allow-list: ต้องมี `crux` อยู่ในนั้น Hermes ถึงจะโหลด plugin ใช้ `hermes plugins enable crux` เพื่อเพิ่มชื่อโดยรักษา plugin เดิม สำหรับ task ที่ใช้ rein ให้เตรียมตามวิธีด้านล่างก่อนเริ่ม worker

### ใช้ Crux กับ rein ใน task เดียวกัน

ผู้ประสานงานต้องติดตั้ง Crux ใน Hermes home ของ task เพราะเมื่อกำหนด `HERMES_HOME` แล้ว Hermes จะไม่ค้น plugin จาก home หลัก:

```sh
mkdir -p "<worktree>/.hermes/plugins"
ln -s "<crux-root>/adapters/hermes" "<worktree>/.hermes/plugins/crux"
```

ก่อน `rein hooks install <task> --vendors hermes` ให้เตรียม `<worktree>/.hermes/config.yaml` เป็น template นี้ โดยใช้เฉพาะ task home ใหม่ที่ยังไม่มี config หรือแทนที่ config ที่ rein สร้างไว้เองจากผู้ประสานงาน:

```yaml
plugins:
  enabled:
    - rein-guard
    - crux
```

installer รองรับ template นี้แบบตรงตัว รวมถึง template ที่มีเฉพาะ `crux` โดยเพิ่ม `rein-guard` ให้ รองรับการติดตั้งซ้ำโดยรักษา config เดิม และเมื่อถอน guard จะเหลือเฉพาะ `crux` ไม่แก้ plugin หรือ state ของ Crux ส่วน config ที่มีค่าอื่นจะถูกปฏิเสธโดยเก็บไฟล์เดิมไว้ ห้ามเขียนทับ config ของผู้ใช้เพื่อให้ผ่านการตรวจ

เริ่ม Hermes จาก `<worktree>` ด้วย `HERMES_HOME=<worktree>/.hermes` และ `HERMES_ENABLE_PROJECT_PLUGINS=1` ตาม launch line ของ rein ห้ามให้ worker แก้ config นี้หรือเรียก `hermes config set plugins.enabled` เพื่อแทนที่ allow-list เพราะ config เป็นส่วนหนึ่งของ guard ที่ป้องกันการแก้ไข

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
