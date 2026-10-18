# Crux OpenCode2 adapter

This adapter targets OpenCode **v2.0.32** plugin SDK. It exports `{ id, setup(ctx) }`, registers `ctx.session.hook('prompt', ...)` and `ctx.session.hook('context', ...)`, and stores only a Boolean preference in plugin-scoped `ctx.storage`. It does not use the OpenCode V1 event API or change the user prompt, tools, permissions, model or provider.

## Load and activate

Keep the complete Crux source tree together. From the repository root, create a project-local plugin link in an unused location:

```sh
mkdir -p .opencode/plugins
ln -s "$(pwd)/plugins/crux/adapters/opencode2" .opencode/plugins/crux
```

The directory's `index.js` is the V2 loader entry point. Do not copy that file alone: it imports the shared core from the same Crux tree. Start a fresh OpenCode2 session after adding the link. Send `$crux:crux-mode` as prompt text to activate that session; `$crux:crux-mode off` or `/crux-off` disables it. For CLI v2.0.32, send the prompt through stdin with no positional message: the CLI adds literal quotes around a positional argument containing spaces, which changes the text seen by the activation hook. For example:

```sh
printf '%s' '$crux:crux-mode อธิบายสถานะงาน' | opencode2 run --standalone
```

Merely loading the plugin does not activate Crux. Context injection persists through subsequent turns and context/compaction reconstruction while the SDK's session ID and plugin storage persist. A new session remains inactive. Do not claim that registration alone proves a running chat loaded the plugin.

Read the shared Thai communication skill and core runtime when injected. Use OpenCode2's exposed native tools and agent APIs, not Codex-only names. Discover actual model/provider capabilities before delegation; inherited permissions remain authoritative. Rein still owns routing receipts, contracts, retries and checkpoints; Orca transport is used only through verified Rein interfaces. When native delegation is absent, do bounded sequential work and disclose the missing independent lane.

## Evidence and limits

Behavior tests exercise the V2 SDK callback shapes, independent session IDs, persistent storage, context injection and off commands. An isolated native v2.0.32 private server loaded Crux with `plugin.list` reporting `state.status: active` for the exact adapter source. A subsequent native session probe also verified prompt-hook activation, isolation and deactivation without invoking a model; see the protocol below. The later model/context probe below adds narrower native evidence; provider switching and Orca recovery remain unverified.

API references: [V2 plugin documentation](https://dev.opencode.ai/v2/docs/build/plugins/), [session API at revision 843a6c5](https://github.com/anomalyco/opencode/blob/843a6c514e606b436a7fe713422ace0a93ad3d61/packages/plugin/src/promise/session.ts), [storage API](https://github.com/anomalyco/opencode/blob/843a6c514e606b436a7fe713422ace0a93ad3d61/packages/plugin/src/promise/storage.ts).

## ผลตรวจ prompt hook ผ่าน session จริง

วันที่ 18 ต.ค. 2026 ทดสอบกับ private server ของ v2.0.32 โดยใช้ XDG/TMP และ `OPENCODE_TEST_HOME` ใต้โฟลเดอร์ทดสอบ ไม่ใช้ daemon, credentials หรือ config ของผู้ใช้ โหลด Crux จาก source ที่กำหนดและตรวจว่า active ก่อนสร้าง session

1. อ่าน schema จาก `/openapi.json` ของ server เดียวกัน แล้วสร้าง session A และ B ด้วย `POST /api/session` และ `location.directory` ของโครงการทดสอบ
2. ส่ง `{ "text": "$crux:crux-mode", "resume": false }` ไปที่ `POST /api/session/SESSION_ID/prompt` ของ A แล้วตรวจ storage ของ Crux จากฐานข้อมูลชั่วคราวแบบ read-only พบ `session:A:active` เป็น `true`
3. ส่งข้อความธรรมดาให้ B ด้วย `resume:false` พบว่าไม่มี active key ของ B และ A ยัง active
4. ส่ง `$crux:crux-mode off` ให้ A ด้วย `resume:false` พบว่า active key ถูกลบ และข้อความคำสั่งยังคงเดิม
5. ตรวจ `session.get` ของทั้งคู่: cost และ input/output/reasoning tokens เป็นศูนย์ แล้วปิด private server ด้วย stdin EOF ได้ exit 0 พร้อมลบพื้นที่ทดสอบ

ผลนี้พิสูจน์ว่า prompt callback ของ plugin ถูกเรียกจาก session API จริง ไม่ใช่เพียง mock SDK หรือสถานะ loader การอ่าน storage ใช้ตรวจหลักฐานเฉพาะการทดสอบ ไม่ใช่ interface ที่ plugin ต้องผูกกับโครงสร้างฐานข้อมูลภายใน

`resume:false` ไม่เริ่ม agent loop จึงไม่พิสูจน์ context hook ส่วน `GET /api/session/SESSION_ID/context` อ่านประวัติที่จัดเก็บและไม่ได้เรียก hook นั้น การพิสูจน์ context ต้องผ่านเส้นทางที่เลือก model/connection ได้จริง อย่าใช้การเรียก model ที่ไม่พร้อมแล้วอนุมานว่า context callback ทำงานไปแล้ว


## ผลตรวจ context และโมเดลจริง

วันที่ 18 ต.ค. 2026 ใช้ private server เดิมของ v2.0.32 กับข้อมูลและการยืนยันตัวตนของ CLI ผู้ใช้ตามปกติ โดยไม่คัดลอก credentials หรือแก้ config/hooks ของผู้ใช้ โครงการและ observer อยู่ในพื้นที่ชั่วคราว แต่ประวัติ session ที่สร้างอาจคงอยู่ในข้อมูล CLI ปกติ กำหนด session permissions ให้ปฏิเสธทุกอย่าง ยกเว้นอ่าน source ของ Crux และจำกัด agent ไว้สองขั้น

- `opencode-go/beam-free` ตอบจริงและอ่าน shared Thai skill แต่การตรวจเนื้อหาไม่ผ่านทั้งสองรอบ: รอบแรกใช้ศัพท์ไม่ชัดและปนหลักฐาน checkpoint กับการหยุด worker; รอบหลังยังเพิ่มเงื่อนไขที่ไม่มีหลักฐาน จึงไม่นับว่า Thai smoke ผ่าน
- รอบแรกที่ใช้ CLI positional prompt ไม่พบ active key และ context ที่คาดไว้ ตรวจข้อความใน session ที่เป็นเจ้าของแล้วพบเครื่องหมาย `"` ที่ CLI เติมไว้หน้าคำสั่ง จึงไม่ผ่าน parser ที่ตรวจต้นข้อความ ต่อมาทดสอบ native CLI แบบแยกข้อมูลผู้ใช้พบว่า stdin รักษา prompt ตรงทุก byte และเปิดโหมดได้ ส่วน positional argument ไม่ตรงและไม่เปิดโหมด
- เมื่อเปิดโหมดด้วย `POST /api/session/SESSION_ID/prompt` พร้อม `resume:false` พบ active key เป็น `true` แล้วเริ่ม native model path: observer ที่ลงทะเบียนหลัง production callback พบข้อความ `modeContext()` ตรงทุก byte ใน `event.system` ของ session นั้น นี่เป็นหลักฐาน native context hook ในเส้นทางนี้ ไม่ใช่เพียง fixture
- โมเดล `opencode-go/claude-haiku-5-5` หยุดที่ `provider.quota` / `Go usage limit exceeded` (monthly) หลัง context hook ทำงาน จึงไม่มีคำตอบจากโมเดลนี้ให้ตรวจภาษา

ปิด private server ด้วย stdin EOF ได้ exit 0 และลบพื้นที่ชั่วคราวแล้ว การยืนยัน context ข้างต้นใช้ observer ชั่วคราวที่ import production adapter ไม่ได้เปลี่ยน callback ใน source และไม่ได้พิสูจน์ว่าโมเดลทำตาม context หรือส่งต่องานข้าม provider ได้


### ยืนยันเส้นทาง CLI โดยไม่เรียกโมเดล

ทดสอบ CLI v2.0.32 กับ private server และ XDG/TMP/`OPENCODE_TEST_HOME` ชั่วคราว ส่ง prompt เดียวกันที่มีภาษาไทย ช่องว่าง newline เครื่องหมายคำพูด `$()` และ backticks ผ่าน positional argument เทียบกับ stdin อ่านเฉพาะ user message และ active key ของสอง session ที่สร้างเอง:

| ทางส่ง prompt | ข้อความตรงทุก byte | Crux active |
|---|---|---|
| positional argument หนึ่งตัว | ไม่ตรง | ไม่เปิด |
| stdin โดยไม่ใส่ positional message | ตรง | เปิด |

กำหนดชื่อ model ที่ไม่มีอยู่โดยตั้งใจเพื่อให้ทั้งสองคำสั่งหยุดก่อนเรียกโมเดล (CLI exit 1) จึงใช้ผลนี้พิสูจน์ transport และ prompt hook เท่านั้น ทั้งสอง session มี cost และ token ทุกชนิดเป็นศูนย์ ปิด server exit 0 และลบพื้นที่ชั่วคราวแล้ว ข้อความ task context ของ Rein ต้องผ่าน stdin ใน supervised OpenCode launch ด้วยเหตุผลเดียวกัน ไม่แก้ activation parser ให้ยอมรับข้อความที่ถูก quote หรือคำสั่งที่ซ่อนอยู่ในเนื้อหา
