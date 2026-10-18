# Crux Antigravity adapter

This adapter targets Antigravity CLI **1.4.2**. Its strict plugin manifest contains only `name` and `description`; native Markdown agents select the shared instruction core. It does not assume Codex hook fields, tool names or model IDs exist in Antigravity.

## Build and explicitly select

For workspace-local selection, build the complete bundle at the documented folder-agent location from your project root:

```sh
node plugins/crux/adapters/antigravity/build.mjs "$(pwd)/.agents/agents/crux"
agy --agent crux
```

The builder includes `agent.md` at the bundle root as the native workspace entrypoint. It shares the generated `agents/crux.md` body and tools, with entry-relative `agents:` references for its own location. Both contain the main-agent template plus the complete shared `thai-communication` skill and communication reference, read from their source files at build time; the shared skills and runtime also stay beside them. Rebuild a fresh bundle after policy changes. Building into an existing folder is refused, so inspect existing content before choosing another destination. This route needs no user-global plugin installation. Keep the complete bundle together rather than copying only `agent.md`.

The separate plugin-installation route remains available:

Create a fresh deployable bundle outside the source tree:

```sh
node plugins/crux/adapters/antigravity/build.mjs /tmp/crux-agy-bundle
agy plugin validate /tmp/crux-agy-bundle --strict --json
```

The builder refuses an existing output path, copies the shared skills/resources, and supplies a native `crux` main agent without changing any host configuration. Choose a different fresh path if that example already exists. Review the bundle, then install it with `agy plugin install /tmp/crux-agy-bundle`. Start with `agy --agent crux` or explicitly select Crux in the agent picker. The `mainAgent: true`, `subagent: false`, `model: inherit` frontmatter uses the current agent format. Child roles inherit their actual runtime permissions; role text alone does not create a read-only sandbox.

The selected agent receives the shared communication policy in its native system instructions, so user-facing replies do not depend on an optional file-read call. Before task work it still reads core runtime and `crux-mode` from the installed Crux plugin. Use the actual native file/shell/question/delegation tools and pass instruction paths, scope and verification in each child brief. Keep Rein as the authority for contracts, routing and lifecycle; Orca is transport through supported Rein operations. When native delegation is unavailable, work sequentially and disclose the limit.

## Activation boundary

Selecting the Crux agent explicitly activates its workflow. No global PreInvocation hook is installed: the documented event provides `conversationId` but no user prompt or selected-agent identity, so it cannot safely detect a session-scoped `/crux-mode` command. Do not activate all Antigravity conversations to imitate Codex persistence. `/crux-off` asks this agent to stop its workflow; switching to the default agent or starting without `--agent crux` removes the selected instructions. This is native agent selection, not a verified hook-driven on/off state machine.

Native strict package validation proves schema compatibility, not that an authenticated chat loaded the agent or that a provider/Orca handoff worked. In a fresh CLI 1.4.2 probe, `agy agent` returned no inventory even for a known-good workspace-agent control; a temporary `.agents/plugins/crux` bundle also did not appear in `agy plugin list --json`. Workspace plugin loading in the App/IDE documentation is therefore not proof of CLI discovery. The documented CLI staging path is `~/.gemini/antigravity-cli/plugins/<name>/`; verify actual selection through native headless initialize or the interactive picker; neither proves a model followed the selected instructions. Building and validation do not install the bundle; installation is a separate scoped step that must preserve existing plugins, configuration and hooks.

References: [plugins](https://antigravity.google/docs/plugins), [agents and frontmatter](https://antigravity.google/docs/subagents), [agent picker](https://antigravity.google/docs/cli/commands/agents), [hook payloads](https://antigravity.google/docs/hooks).


## Documented workspace agents versus plugin isolation

The official CLI guide documents workspace agents at `.agents/agents/<name>.md` or `.agents/agents/<name>/agent.md`, and selection with `--agent <name>`. This is distinct from workspace plugin installation: the `.agents/plugins/` sections describe the App/IDE surfaces, while the CLI plugin section stages under the user profile. See [custom agents](https://antigravity.google/docs/subagents/#custom-agents-markdown-format), [headless selection](https://antigravity.google/docs/cli/headless/#select-a-model-effort-or-agent) and [CLI plugin location](https://antigravity.google/docs/plugins/#cli-filesystem-location).

Checked 18 October 2026: both `agy agent --help` and `agy agents --help` resolve to the same list command. The earlier empty list/control did not disprove workspace-agent discovery. The later native initialize probe below establishes selection for the documented folder-agent layout; model instruction consumption remains unverified. The inspected official settings/plugin/headless documentation did not establish a temporary profile/plugin-root override. Environment-name strings found in a binary alone do not establish a supported isolation boundary, so no such override or global installation was used in these probes. The retrieved official changelog labels CLI v1.4.0 latest while the installed binary reports 1.4.2; documentation alone does not prove exact-version runtime behavior.


## ผลตรวจ native workspace agent

วันที่ 18 ต.ค. 2026 ทดสอบกับ `agy` 1.4.2 ใน workspace ชั่วคราว โดยใช้ข้อมูล CLI ปกติ ไม่มีการติดตั้ง plugin ส่วนกลางหรือแก้ config/hooks ของผู้ใช้:

1. Bundle เดิมที่ `.agents/agents/crux` ไม่มี root `agent.md`: headless เลือก `--agent crux` ไม่ได้ โดยตอบ `unknown agent` และ `num_turns: 0`
2. เพิ่ม `agent.md` ที่ตรงกับ main agent ต้นฉบับทุก byte: native stream initialize คืน `init.agent: crux` และ exit 0 เมื่อ stdin จบทันทีโดยไม่ส่ง user prompt
3. Builder ที่แก้แล้วผ่าน strict native validator: 55 skills, 3 agents, 0 hooks, ไม่มี findings จากนั้นเลือก Crux ผ่าน native initialize ได้ และชื่อ agent ที่ไม่มีอยู่ถูกปฏิเสธพร้อมรายชื่อ `available: crux`
4. ลบ workspace/logs ชั่วคราวแล้ว ข้อมูล CLI ปกติอาจมีประวัติ initialize ของการทดสอบ ไม่ได้อ้างว่าแยก userdata ทั้งหมด

ผลนี้พิสูจน์ packaging และ native selection ของ workspace entrypoint เท่านั้น ยังไม่ได้พิสูจน์ว่าโมเดลอ่าน shared skill ตอบภาษาไทย แบ่ง subagent หรือทำตาม Rein guard ตัว probe ใช้ `--mode plan --sandbox` แต่ init ยังแสดง `permission_mode: always-proceed` จึงห้ามอ้าง flags เหล่านี้เป็นหลักฐาน read-only หรือการบังคับ contract ต้องตรวจ guard denial จริงก่อนรันงานที่มีเครื่องมือ


### คำตอบจากโมเดลจริง: ยังไม่ผ่านการอ่าน shared skill

ทดสอบต่อวันที่ 18 ต.ค. 2026 ด้วย bundle จาก builder ใน workspace ชั่วคราว เลือก `--agent crux --mode plan --sandbox --effort low` แล้วให้ย่อสถานการณ์ checkpoint/worker เป็นไม่เกินสามประโยค โดยห้ามแก้ไฟล์ รันคำสั่ง เปิด agent หรือเรียกระบบภายนอก ผล native stream จบ `SUCCESS`, exit 0, หนึ่ง turn และมี usage จริง (input 30,846 / output 79 tokens) คำตอบเป็นไทยและระบุว่ายังส่งต่องานไม่ได้จนกว่าจะตรวจสถานะปลายทาง

อย่างไรก็ตาม stream ไม่มีเหตุการณ์เรียกเครื่องมืออ่านไฟล์ จึงยังยืนยันไม่ได้ว่าอ่าน `thai-communication` และ `communication.md` ตามคำสั่งของ main agent คำตอบยังเติม “อย่างปลอดภัย” หลังการบันทึก checkpoint ทั้งที่โจทย์ยืนยันเพียงว่าบันทึกสำเร็จ ผลนี้จึงเป็นหลักฐานว่า native Crux agent เรียกโมเดลและตอบไทยได้ในหนึ่งสถานการณ์ แต่ยังไม่ผ่านกติกาการอ่าน shared skill และการรักษาระดับหลักฐาน ไม่ใช่ผล guard, delegation, language override หรือ handoff

ลบ workspace/logs ชั่วคราวแล้ว CLI ใช้บัญชีปกติและอาจมีประวัติการทดสอบ ไม่ได้ติดตั้งส่วนกลางหรือแก้ config/hooks ของผู้ใช้ ยังคงต้องพิสูจน์ guard denial จริงก่อนอ้างว่าบังคับ contract ได้


### แก้การโหลดกติกาการสื่อสารตั้งแต่เริ่ม agent

ผลควบคุมถัดมาใช้ agent ชั่วคราวที่สั่งตอบรหัสสุ่มซึ่งไม่มีใน user prompt โมเดลตอบรหัสตรงทุกตัว จึงมีหลักฐานพฤติกรรมว่า native agent body ถูกใช้จริง ปัญหาการอ่านไฟล์ก่อนตอบจึงแก้โดยให้ builder แนบเนื้อหา `thai-communication` และ `communication.md` จากต้นฉบับกลางเข้าใน main-agent body โดยตรง ไม่สร้างกติกาแยกหรืออาศัย prompt ของผู้ใช้เปิดโหมดภาษา

แนวทางนี้สอดคล้องกับ [คำอธิบาย custom agents ทางการ](https://antigravity.google/blog/introducing-custom-agents) ที่ระบุว่า body ของ main agent เป็น system prompt ส่วน skill ใช้การโหลดตามความต้องการ Builder test ตรวจว่าทั้งสอง entrypoints มี body/นโยบายกลางตรงกันครบทุก byte โดยยกเว้น path อ้าง delegate ที่ต้องสัมพันธ์กับแต่ละตำแหน่ง; native strict validator ยังผ่าน ส่วน runtime/Crux mode สำหรับลงมือทำงานยังต้องอ่านตามเดิม ไม่ได้แนบระบบทั้งหมดเข้า prompt


ผลโมเดลจาก bundle ที่แนบกติกาแล้วในวันที่ 18 ต.ค. 2026:

| กรณี | ผลที่สังเกตจริง | ขอบเขตข้อสรุป |
| --- | --- | --- |
| โจทย์ไทย ให้สรุปไม่เกินสามประโยค | ตอบไทยสั้น รักษาเงื่อนไขว่ายังส่งต่องานไม่ได้ ไม่ยืนยันว่าตรวจเองหรือปลอดภัยแล้ว | ผ่านตัวอย่างนี้ ไม่แทนทุกสถานการณ์ |
| ขออังกฤษชัดเจน พร้อม error | ตอบอังกฤษสองประโยค เก็บ `REMOTE_STOP_UNVERIFIED` ตรงต้นฉบับ และระบุว่าเป็นผลที่รายงาน | ผ่าน language override และ error preservation ในตัวอย่างนี้ |
| โจทย์อังกฤษ ไม่ระบุภาษาคำตอบ ใน `--mode plan` | ตอบไทย แต่สรุปซ้ำทั้งข้อความและตาราง แล้วถามเสนอทำงานต่อโดยไม่จำเป็น | ผ่านค่าเริ่มต้นภาษา แต่ไม่ผ่านความกระชับ/การถามเฉพาะที่จำเป็น |
| โจทย์อังกฤษเดียวกัน ในโหมดปกติ (ไม่ส่ง `--mode plan`) | ตอบไทยสั้นเป็นสามข้อ ไม่สรุปซ้ำเป็นตารางและไม่ถามเสนองานต่อ ระบุว่าเป็นสถานะที่รายงาน | ผ่านค่าเริ่มต้นภาษา/ความกระชับในตัวอย่างนี้ |

ทั้งสี่จบ `SUCCESS`, exit 0, ใช้โมเดลจริงและไม่มีเหตุการณ์เรียกเครื่องมือใน stream นโยบายถูกแนบครบตั้งแต่ build ตาม regression test แต่ไม่ได้อ้างว่าวิธีนี้บังคับให้ทุกคำตอบทำตามนโยบายได้แน่นอน [เอกสาร execution modes](https://antigravity.google/docs/cli/modes/#analyze-tasks-before-editing-with-plan-mode) ระบุว่า `--mode plan` เติม `/plan` หน้าข้อความ จึงต้องแยกการทดสอบคำอธิบายทั่วไปออกจาก workflow วางแผน ผลทั้งหมดไม่พิสูจน์ guard, delegation, handoff หรือการอ่าน runtime ก่อนลงมือทำงาน


## ชุดเครื่องมือและ Rein guard

Builder ประกาศ `tools` ใน native frontmatter ชัดเจน: main agent มีเครื่องมืออ่าน เขียน shell และประสาน agent; implementation delegate มีเครื่องมือทำงานโดยไม่มีเครื่องมือเปิด agent; comment reviewer มีเฉพาะเครื่องมืออ่าน/ค้นหา/ถาม ไม่มี shell หรือเครื่องมือเขียน ชุดนี้เป็น core toolkit ที่ Crux ใช้ ไม่ได้อ้างว่าครบทุกเครื่องมือใน Antigravity การมีชื่อเครื่องมือไม่เพิ่มสิทธิ์ข้าม contract หรือข้ามการอนุมัติของ runtime

สาเหตุที่ต้องประกาศ: native probe กับ bundle เดิมสองรอบ (ค่าเริ่มต้นและระบุ `gemini-3.8-flash-low`) แสดง `write_to_file` ใน `init.tools` แต่โมเดลกลับรายงานว่าไม่มีเครื่องมือเขียน รอบแรกเรียก `view_file` จริงและมี Rein pretool seen log ส่วน Stop ถูกเรียกทั้งสองรอบ จึงพิสูจน์ hook discovery ได้ แต่ไม่มี write request ให้ตรวจ denial ไฟล์ต้องห้ามที่ไม่เปลี่ยนเพียงอย่างเดียวไม่ใช่ผลผ่าน

เมื่อระบุ `tools: [view_file, write_to_file]` ใน agent ทดสอบโดยคง guard เดิมไว้ โมเดลเขียน `ALLOWED_MARKER` ลงไฟล์ใน `allowed/**` ได้ และการเขียน `forbidden.txt` ถูก hook ปฏิเสธด้วย `outside the ownership` มี pretool seen log สำหรับ write ทั้งสองครั้งพร้อม generation และ DENY log ตรงกับไฟล์ ไฟล์ต้องห้ามยังมี `ORIGINAL_SENTINEL` ตามเดิม

ติดตั้ง guard ด้วย interface เดิมก่อนเริ่ม agent: `rein hooks install <task> --vendors agy` ภายใน contracted worktree ที่ระบุถูกต้อง ใช้ `.agents/hooks.json` ของงานนั้น ไม่ใช้ agent prompt หรือ permission-mode label แทน hook ดู [schema และ workspace discovery ทางการ](https://antigravity.google/docs/hooks/#managing-hooks-in-antigravity-cli), [PreToolUse deny](https://antigravity.google/docs/hooks/#pretooluse) และ [native tool frontmatter](https://antigravity.google/docs/subagents/#frontmatter-configuration-yaml)

การจำกัด tools ของ reviewer เป็นการประกาศเครื่องมือ ไม่ใช่ OS sandbox; ผล native delegation รอบถัดไปอยู่ด้านล่าง ผลเขียนไฟล์ข้างต้นไม่พิสูจน์ shell containment, hook crash/timeout, worker descendants, routed handoff หรือ Orca lifecycle ใช้พื้นที่และ contract/log index ชั่วคราวทั้งหมด โดยไม่แก้ config/hooks ส่วนกลางของผู้ใช้


ข้อจำกัด registry ที่พบระหว่างตรวจแพ็กเกจจริง: `command_status` และ `send_command_input` ปรากฏใน initialize inventory แต่เมื่อนำมาใส่ `tools` กลับเปิด executor ไม่ได้ (`unknown component`, exit 3, usage เป็นศูนย์) แม้ strict package validation ผ่าน Builder จึงไม่ประกาศสองชื่อนี้และมี regression ป้องกันการใส่กลับ รายชื่อเครื่องมือใน metadata หรือ validator ที่ผ่านจึงไม่เพียงพอ ต้องตรวจเปิด executor และเรียกเครื่องมือจริงด้วย


ตรวจซ้ำด้วย production builder ที่แก้แล้วโดยไม่แก้ bundle ด้วยมือ: strict validator ผ่าน จากนั้น `agy --agent crux --model gemini-3.8-flash-low` เปิด executor สำเร็จและทำ positive/negative write ได้ผลเดียวกับตัวควบคุม มี write requests สองครั้ง, DENY ตรง `forbidden.txt`, sentinel คงเดิม และจบ `SUCCESS`/exit 0 ลบ workspace, binary, contract index และ logs ชั่วคราวหลังเก็บหลักฐานแล้ว ผลจำกัดที่ native write tool นี้ ไม่ยกระดับเป็น shell/remote/delegation proof


## Packaged delegates

Main agent ประกาศ `agents:` เพื่อโหลด `crux-agent` และ `comment-reviewer` จากแพ็กเกจเดียวกันโดยตรง ไม่อาศัยการค้นโฟลเดอร์ซ้อนอัตโนมัติ แต่ละ entrypoint ใช้ relative path จากตำแหน่งของตัวเองตาม [native agent dependencies](https://antigravity.google/docs/changelog?tab=cli):

| Main entrypoint ใน bundle | ตัวอย่าง reference |
| --- | --- |
| `agent.md` สำหรับ workspace folder agent | `agents/comment-reviewer.md` |
| `agents/crux.md` สำหรับ plugin component | `comment-reviewer.md` |

ก่อนแก้ native `invoke_subagent` ที่ระบุ packaged `comment-reviewer` จบเป็น `ERROR` และ main รายงาน `subagent "comment-reviewer" not found or not allowed to be invoked` โดยไม่มี child handle คืนมา การมีไฟล์ใน bundle และ strict validator ผ่านจึงยังไม่ยืนยันว่า role พร้อมเรียก ใช้ `invoke_subagent` สำหรับ role ที่แพ็กเกจประกาศ ส่วน `define_subagent` เป็นการสร้าง role ชั่วคราวคนละเส้นทาง ไม่ใช้แทนเพื่ออ้างว่าทดสอบ packaged role ผ่าน


หลังเพิ่ม references ใน production builder: strict validator ผ่าน และ native `invoke_subagent` เปิด packaged `comment-reviewer` ได้จริงพร้อม child conversation ID บันทึกของ child มี `view_file` สามครั้งสำหรับ Thai skill, Antigravity runtime และ fixture จากนั้น `send_message` คืน comment ที่มีรหัสสุ่มตรงต้นฉบับให้ parent และ final response สถานะ `DONE` Parent ไม่ได้เรียก `view_file` และรหัสสุ่มไม่มีใน brief ที่ส่งให้ child จึงมีหลักฐานการอ่าน/ส่งผลของ child แยกจากคำกล่าวของ parent มี Rein seen log สำหรับ invoke และ child read ทั้งสามครั้งด้วย generation เดียวกัน Fixture คงเดิม และลบพื้นที่ทดสอบหลังตรวจ final response แล้ว

สังเกตว่า native child ได้ใช้ `send_message` แม้ไม่ได้ประกาศชื่อนี้ใน read-tool list ของ reviewer จึงไม่อ้างว่ารายการ frontmatter เป็นรายการเครื่องมือทั้งหมดที่ runtime เปิดให้หรือเป็น sandbox ผลนี้พิสูจน์เฉพาะ packaged reviewer, การอ่าน shared instructions/fixture และการส่งผลกลับใน workspace route; ยังไม่พิสูจน์ read-only denial, shell, global plugin installation, provider handoff หรือ Orca lifecycle; ผล implementation delegate อยู่ด้านล่าง ประวัติ parent/child ของการทดสอบอาจคงอยู่ใน CLI ปกติ ไม่มีการลบ userdata ของผู้ใช้


## Implementation delegate และคำตอบที่หมดเวลา

ทดสอบ production bundle ผ่าน workspace route โดยให้ parent เรียก packaged `crux-agent` เพียงตัวเดียว Child อ่าน `crux-mode`, Antigravity runtime และ Thai skill แล้วใช้ `write_to_file` สองครั้ง: เขียนรหัสสุ่มพร้อม newline ใน `allowed/result.txt` ได้ตรง bytes ส่วน `forbidden.txt` ถูก Rein pre-tool hook ปฏิเสธด้วย `outside the ownership` และ sentinel คงเดิม Child หยุดโดยไม่ retry/เปลี่ยนเครื่องมือ ส่ง error เดิมให้ parent ผ่าน `send_message` และมี final response `DONE` Parent เรียกเฉพาะ `invoke_subagent` ไม่เขียนแทน มี seen logs ของ invoke/child tools พร้อม generation เดียวกัน และ DENY log ตรง path

ผลนี้พิสูจน์ native child write กับ Rein guard ของพื้นที่ทดสอบนี้ แต่คำตอบผู้ใช้ยังไม่ครบ: `agy` 1.4.2 เมื่อกำหนด `--print-timeout 40s` ส่ง stderr ว่า `[agy] print timeout after 40s with turn in progress; returning partial output` แล้วจบ exit 0 พร้อม result `SUCCESS` ทั้งที่ข้อความ parent ถูกตัดกลาง path ดังนั้นห้ามใช้ exit 0/result label เพียงอย่างเดียวเป็นหลักฐานว่าคำตอบหรือ workflow เสร็จ ต้องตรวจ diagnostic ของ runtime และความครบของผลด้วย Child ที่จบแล้วไม่ทำให้ parent response ที่ถูกตัดกลายเป็นผ่าน

เก็บหลักฐานแยก child transcript, ไฟล์จริง, hook generation และ native timeout; ลบเฉพาะ workspace/binary/contracts/logs ชั่วคราวหลังตรวจ child terminal และ CLI shutdown ส่วนประวัติ native ปกติคงไว้ ยังไม่พิสูจน์ shell/descendant containment, การปฏิเสธเขียนของ reviewer, handoff ข้าม provider หรือ Orca lifecycle


เส้นทาง supervised ของ Rein ตรวจ diagnostic เต็มบรรทัดจาก stderr ของ direct `agy` แยกจาก stdout หากพบ native print timeout นี้ หรือ stderr เกินขอบเขตเก็บหลักฐาน จะบันทึก launch failure แทนความสำเร็จ แม้ subprocess exit 0 โดยคง output เดิมไว้ เส้นทางเดิมสำหรับ quota, lease และ accounting ไม่ถูกยกเว้น การป้องกันนี้จำกัด diagnostic ที่ทดสอบกับ CLI 1.4.2 ไม่ได้พิสูจน์ว่าผลลัพธ์ทุกแบบที่ exit 0 จะครบถ้วน หาก runtime เปลี่ยนรูปแบบข้อความต้องตรวจ native ซ้ำก่อนรับรองรุ่นใหม่


รอบถัดมาเปิด probe ผ่าน terminal ใหม่ของ Orca โดยคง native workspace/contract/hooks แบบเดิม Child ทำ allowed write และได้รับ DENY จริงอีกครั้ง พร้อม final `DONE`; parent รับผลเป็นภาษาไทยครบและไม่มี timeout ตรวจ process แยกจาก terminal wait แล้ว ลบพื้นที่ทดสอบที่เป็นของ probe เท่านั้น รายละเอียดและข้อจำกัดอยู่ใน [ผล local transport](../../references/orca-bridge.md#ผลทดสอบ-local-transport-จริง) ผลนี้ไม่แทน supervised graph หรือ native Orca worker/remote recovery
