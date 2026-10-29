# Crux

**Shared engineering workflows. Explicit handoffs. One execution authority.**

`crux-mode` is the main coordination mode for ADLC, multi-agent work, Rein and Orca in the Rein repository. Its 56 skills and 23 playbooks provide reusable engineering guidance across harnesses. Rein owns contracts, routing, checkpoints and approval evidence; Orca provides supported worker transport. Crux conducts the lifecycle, assigns bounded agent work, coordinates supported Orca operations through Rein, verifies results and prepares context for agent/provider handoffs. The plugin uses `crux-mode` in place of the upstream mode name.

This is an independent, evolving fork of pstack, not an upstream product. See [upstream attribution](UPSTREAM.md) and the preserved [MIT license](LICENSE).

## Crux is a workflow plugin, not a second execution engine

Crux owns the ADLC conversation: clarifying the objective, choosing a playbook, assigning bounded agent work, verifying the result and preparing a handoff. It does **not** create a second dispatch, lease, retry, approval, checkpoint or quota ledger. The only shared "engine" in this plugin is [`scripts/session-mode.mjs`](scripts/session-mode.mjs), which persists the explicit `crux-mode` flag per session and injects the communication/runtime policy. Rein remains the single execution authority for contracts, routing receipts, checkpoints, accounting and approvals; Orca remains the supported worker transport. A Crux plan is executed by Rein, not by a parallel Crux controller.

## ภาษาไทยและแผนเชื่อมระบบ

เป้าหมายต้องรองรับ **Claude Code, Codex, Antigravity, OpenCode2 และ Hermes Agent** ครบทั้งห้าตัว มี adapter ครบทั้งห้าตัวแล้ว แต่หลักฐานแต่ละตัวต่างกัน: tests ใน repo ผ่าน และ manifest ของ Claude กับ Antigravity ผ่านตัวตรวจของ CLI Claude พบ skills/agents ผ่าน SDK initialize ส่วน Antigravity เลือก Crux ผ่าน native workspace initialize ได้ และ OpenCode2 ผ่าน native loader ใน private server แล้ว ส่วนการทำงานกับโมเดลจริงยังตรวจไม่ครบ การเชื่อม runtime ครบวงจรยังไม่ยืนยัน

Crux ตอบผู้ใช้เป็นภาษาไทยโดยค่าเริ่มต้น ทั้งความคืบหน้า คำอธิบาย แผน รีวิว และสรุปผล ถ้าผู้ใช้ขอภาษาอื่น ให้ใช้ตามที่ขอ คำสั่ง ชื่อไฟล์ และ error ยังคงต้นฉบับ

- [Skill สื่อสารภาษาไทยให้เข้าใจง่าย](skills/thai-communication/SKILL.md)
- [กติกาการส่งต่อเรื่องภาษาระหว่าง agent](references/communication.md)
- [แบบและแผนเชื่อม Crux, Rein, Orca และหลาย provider](references/integration-plan.md)
- [เตรียมแผนงานในเครื่องและส่งต่อ independent review](references/local-plan.md)

## Structure and support

| Location | Responsibility |
| --- | --- |
| `skills/`, `agents/` | One shared copy of skills, principles, playbooks and role briefs |
| [runtime](references/runtime.md) | Harness selection and shared scope/authority boundaries |
| [adapters](references/integration-plan.md) | One adapter per harness: claude, codex, antigravity, opencode2, hermes |
| [ADLC](references/adlc.md), [handoff](references/handoff.md) | Existing Rein interfaces, evidence and a contextual handoff brief |
| [Codex adapter](adapters/codex/runtime.md) | Native tool mapping, session hooks and role templates |
| `scripts/agents.mjs` | Scoped, portable native-agent installation/removal |

Adapters are implemented for [Claude Code](adapters/claude/runtime.md), Codex, [Antigravity](adapters/antigravity/runtime.md) and [OpenCode2](adapters/opencode2/runtime.md). Follow each adapter’s installation and capability limits; their validation evidence does not establish live model/session behavior. A live two-provider handoff and remote Orca recovery are unverified. Shared prompts do not create provider credentials, transfer native chat histories or implement an unattended scheduler.

## Install for Codex

From this repository:

```sh
codex plugin marketplace add . --json
codex plugin add crux@rein --json
```

Use the `installedPath` returned by the second command to register the two optional native roles:

```sh
node <installedPath>/scripts/agents.mjs
```

The installer uses `CODEX_HOME` or `~/.codex`, replaces only unchanged owned copies, and refuses foreign/edited files. Its templates resolve the actual installed plugin path. Rerun it after moving/upgrading the plugin. It does not alter global AGENTS.md, model selection, other agents or hook trust.

Open `/hooks` in Codex and review/trust Crux's `SessionStart` and `UserPromptSubmit` definitions. Trust from another plugin does not carry over. Start a fresh chat for native role discovery.

```text
$crux:crux-mode <task>
$crux:crux-help
$crux:setup-crux
```

Trusted hooks preserve an explicitly activated mode for that session. `/crux-off` or `$crux:crux-mode off` turns it off. State contains only an active flag under plugin-owned session data. Model preferences, when configured, live under `~/.codex/crux/models.json`; they are not a global Codex model switch. This plugin does not migrate pstack preferences or activate itself by default.

Installation does not enable Rein's task guard. A Rein-managed worker still needs its own contract, supported guard/sandbox setup, routing identity and verification. See the repository's `docs/CLI_REFERENCE.md` for those interfaces.

## Develop

Node.js 22 or later is the supported adapter/test runtime. No dependency installation is needed for the adapter and its tests:

```sh
node --test plugins/crux/adapters/codex/hooks/*.test.mjs plugins/crux/tests/*.test.mjs
python3 -B -m unittest discover -s plugins/crux/tests -p 'test_*.py'
```

ชุดทดสอบ Python ข้างต้นทำงานออฟไลน์ ไม่เรียก provider ส่วนสคริปต์ live smoke ต้องระบุ `--execute` พร้อม config และช่วงโควตาที่ตรวจแล้ว ผล PASS ของ transport หรือการตรวจในเครื่องยังไม่ยืนยันคุณภาพคำตอบของโมเดลหรือการเชื่อมครบทั้งสี่ harness ดู [สถานะและข้อจำกัดล่าสุด](references/readiness.md)

ตรวจ adapter ทั้งสี่ตัวด้วยคำสั่งเดียวและอ่านสรุปภาษาไทยได้โดยไม่เรียกโมเดล:

```sh
python3 -B plugins/crux/scripts/harness-acceptance.py
python3 -B plugins/crux/scripts/harness-acceptance.py --format json
```

คำสั่งนี้รันชุดทดสอบ adapter จริงใน repo และแยกผลแพ็กเกจร่วมออกมา ใช้ `--details` เพื่ออ่านคำสั่งและ log รายชุด ผลตรวจระบบจริงจะคงเป็น `NOT_VERIFIED` เพราะ runner นี้ยังไม่เรียก native harness/โมเดล ใช้ `--require-live` หากต้องการให้คืน exit 2 เมื่อการตรวจในเครื่องผ่านแต่ยังไม่ครบระบบจริง; exit 1 หมายถึงการตรวจในเครื่องล้มเหลว ส่วน exit 0 ปกติรับรองเฉพาะ adapter ในเครื่อง

เพิ่มผล Codex ที่ติดตั้งจริงในรายงานรวมได้ด้วย `python3 -B plugins/crux/scripts/harness-acceptance.py --native-codex` ผลของอีกสามระบบยังเป็นการตรวจ adapter ในเครื่อง ไม่ถูกเปลี่ยนเป็นผ่านระบบจริงจากผล Codex

ตรวจการโหลดจริงทั้งสี่ runtime และรวมผลภาษาไทย:

```sh
python3 -B plugins/crux/scripts/harness-acceptance.py --native-all
python3 -B plugins/crux/scripts/harness-acceptance.py --native-all --format json
```

Codex ตรวจ plugin ที่ติดตั้งไว้ ส่วน Claude Code โหลด source ด้วย `--plugin-dir`, Antigravity สร้าง workspace-agent bundle ชั่วคราว และ OpenCode2 ใช้ private server กับข้อมูลชั่วคราวแยกจากผู้ใช้ การตรวจสามแบบหลังไม่ยืนยันว่าติดตั้งเป็นค่าเริ่มต้นแล้ว ทุกแบบไม่ส่ง prompt ให้โมเดล ไม่แก้ trust และไม่ใช้ผลโหลดสำเร็จแทนคุณภาพภาษาไทยหรือ workflow ครบวงจร ใช้ `--require-live` เพื่อคง exit 2 จนมีหลักฐาน live ที่ครบ ตัวเลือก `--native-all` กับ `--native-codex` ใช้พร้อมกันไม่ได้ ค่าเริ่มต้นที่ไม่ระบุทั้งคู่ยังเป็นการตรวจออฟไลน์

ข้อดีคือเห็นปัญหาการโหลดของแต่ละ runtime จากคำสั่งเดียวโดยไม่ใช้โควตาสร้างคำตอบ ข้อจำกัดคือ metadata ไม่พิสูจน์ guard, การแบ่งงาน, การหยุด worker หรือการส่งต่องานข้าม provider

ตรวจการค้นพบ Crux จาก Codex ที่ติดตั้งบนเครื่อง โดยไม่ส่ง prompt หรือแก้ trust:

```sh
python3 -B plugins/crux/scripts/codex-readiness.py --cwd /ABS/PROJECT
python3 -B plugins/crux/scripts/codex-readiness.py --cwd /ABS/PROJECT --format json
```

คำสั่งเรียกเฉพาะ `initialize`, `hooks/list` และ `skills/list` ผ่าน Codex app-server โดยนับเฉพาะ `crux@rein` ต้องพบ skills หลักทั้งสองตัวและ hooks ทั้งสอง event ที่เปิดใช้และมี trust เป็น `trusted` หรือ `managed` จึงคืน exit 0 ถ้า metadata ยังไม่พร้อมคืน exit 2; ตรวจไม่ได้คืน exit 1 ผลนี้ไม่รับรองว่าการตอบภาษาไทย การแบ่งงาน หรือการส่งต่องานจริงผ่านแล้ว ตัวตรวจไม่เปิด session/turn ไม่แก้การติดตั้งหรือ trust และไม่แสดงคำสั่ง hook หรือข้อมูล plugin อื่น

Run this from the repository root; the CI matrix runs the same suite. Optional imported helpers under `skills/crux-mode/scripts/` have their own Bun/tool prerequisites and are not part of the dependency-free adapter. Inspect their bootstrap before invoking them. No second orchestration store is used: coordinated work remains under Rein.

Add native integrations under `adapters/<harness>/` when there is an implementation and a testable execution surface. Keep the common skills independent of provider-specific model identifiers. Before calling cross-provider continuation supported, complete the pilot evidence described in [ADLC](references/adlc.md).

## Remove

Before removing the plugin cache:

```sh
node <installedPath>/scripts/agents.mjs --remove
codex plugin remove crux@rein
```

Agent removal refuses modified or unowned targets. Plugin preferences and any previous pstack installation remain separate user data; no broad cleanup is performed.
