# Crux Codex runtime adapter

This local port preserves the principles and task playbooks from cursor/plugins pstack 0.15.25, revision 019a6a20592c10bf92869e5740368f578a9997fd. Use these Codex runtime rules when executing any bundled skill. Workspace instructions and the user's scope remain authoritative.

## Activation and continuity

`$crux:crux-mode` activates the mode for the current task. The port's plugin hooks preserve that session's activation across subsequent prompts and compaction, when Codex loads and trusts those hooks. `$crux:crux-mode off` or `/crux-off` disables persistence. Other skill invocations run their named workflow; inspecting a skill is not activation. Hook registration and a hook self-test do not prove the running chat has loaded them. If unavailable, explicitly invoke the mode in a fresh chat or attach its skill path.

## Native tools and agents

Use exposed file/shell tools for Read, Grep, Glob, Shell, and repository work. Use native `spawn_agent`, agent results, `send_message`, `followup_task`, and `wait_agent` for delegation. These are asynchronous by design; do not pass Cursor-only background, readonly, environment, or base-branch parameters. Give every child its goal, owned paths, non-goals, verification, and evidence format. Say that other agents are working and their changes must be preserved. Read-only is a task constraint; do not claim it creates a sandbox. No recursive delegation by children. Respect the workspace's tighter concurrency/depth limits. A requested ten-lane swarm can be scheduled in waves; never assume ten free slots.

Choose only roles actually advertised by the current spawn tool. Use `crux-agent` and `crux-comment-reviewer` when those installed roles are exposed; otherwise use `executor` and `code-reviewer` and include the absolute paths to `skills/crux-mode/SKILL.md` and, for comments, `agents/comment-reviewer.md` in the brief. Other mappings are discovery to `explore`, design to `architect`, external sources to `researcher`, adversarial review to `code-reviewer` or `critic`. Missing roles use a supported default with the same scoped instructions. Every crux delegate reads this adapter and its relevant skill/playbook. The parent owns integration and verifies the result.

Agents share this computer and filesystem. Prepare isolated worktrees before concurrent writers; no cloud VMs or independent machines are provided. Never reset a dirty/shared worktree to simulate cloud isolation. A child that needs an unavailable remote runtime reports the exact missing proof. When native delegation is unavailable or the user forbids it, do bounded sequential work and explicitly label loss of independent review.

## Per-role models

Before spawning, read `${CODEX_HOME:-~/.codex}/crux/models.json` if it exists. It is crux-owned data, not a Codex configuration file. Shape is `{"version":1,"budget":"inherit","roles":{"how explorer":{"model":"inherit-parent"},"arena runners":[{"model":"inherit-parent"},{"model":"inherit-parent"}]}}`. Single roles are objects; panel roles are arrays. `model` may be `inherit-parent` or `auto`, both meaning omit model/effort overrides. Optional `reasoning_effort` is a separate field, never part of a fabricated model identifier.

Defaults inherit installed role/runtime settings. A model listed by a provider is not necessarily callable by the current agent-spawn surface. Use an override only if this surface exposes it and the exact model/effort is confirmed supported, including its fork restrictions. Fixed-role agents may ignore/disallow overrides; disclose this. On rejection, use the valid inherited role once and report the downgrade. Do not silently substitute an unverified family. Lists determine panel seats; cap active seats to capacity. Distinct independent instances of one model do not prove multi-family diversity. If only one family is callable, label that limit in arena/interrogate/reflect/swarm results.

## Missing companion skills and runtime surfaces

- Code cleanup uses the installed `ai-slop-cleaner` skill, preserving its scope and verification requirements. Skill authoring uses the installed system `skill-creator` and its validator. Resolve their actual installed paths; do not pretend a Cursor companion plugin was installed.
- Browser/web UI verification uses the installed browser/computer skill and exposed automation tools. Read their instructions before control. CLI/TUI verification uses native shell/PTY or the repo's harness. HTTP uses available HTTP tooling. Capture observable actions and results. No usable surface means a verification gap, never a passing live lane.
- Connector/history work discovers actual tools using the enabled inventory and tool search when present. Available configuration alone does not prove connector runtime health. Missing source categories are explicit gaps.
- Questions use the native structured input surface only when available in the current mode; otherwise ask a concise plain question. Do not invent AskQuestion options unsupported by the actual schema.
- Plans/todos use the exposed plan tool or a short task-local checklist. This port does not switch the host's collaboration mode.
- A long run uses bounded native waits/polls while this session is alive, with a checkable exit predicate, user-visible progress, and checkpoints. There is no promise of detached scheduling, `/loop`, persistence after the process exits, or autonomous wakeups without a separately configured and verified scheduler. The mode hook restores instructions, not processes.
- Cursor Automations, Grok Bot routines, `update_state`/secret-request cards, Bugbot rerun controls, and Cursor cloud dashboard are not supplied. Existing forge comments/CI can be read with the authenticated forge tools. Only use a special service when its matching tool is actually exposed. Do not install unrelated infrastructure to imitate it.
- Optional upstream scripts may require Bun, gh, jq, or other documented tools. Inspect prerequisites before running; do not auto-install dependencies as part of a routine task. In particular no bundled TypeScript orchestration script launches agents for you.

## Scoped Codex history

Prefer a provided current-session transcript path or supported history tool. For local Codex JSONL discovery, inspect session metadata only first and select exact `session_meta` id and `cwd` matching the requested workspace and time window before reading content. Worktree paths require an explicit verified relationship to that workspace. Do not search all transcript content across projects. Codex history can reside under `${CODEX_HOME:-~/.codex}/sessions/`, with event/message JSONL shapes that differ from Cursor. Check the actual schema. If the current session or prior topic cannot be identified safely, use visible conversation context and report missing history. Do not create or infer a transcript citation. A summary is not evidence of tool execution.

## Scope and authority

A playbook is not permission to publish, merge, send messages, delete user data, change host settings, or touch unrelated code. Use the user's existing authorization and higher-priority tool rules. Reversible authorized work proceeds autonomously. Never create a separate public PR for a broken skill unless that publication is authorized. If an external action is unavailable, finish the reviewable local result and report the precise remaining step.

## ผลทดสอบกับโมเดลจริง

วันที่ 18 ต.ค. 2026 Codex CLI 0.163.0 เรียก `gpt-6.1-sol` ที่ effort `low` ผ่านสองกรณี: อธิบาย checkpoint เป็นภาษาไทย และตอบอังกฤษเมื่อขออย่างชัดเจน ทั้งสองกรณีจบด้วย exit 0 และ transcript ยืนยันว่าอ่าน bytes ของ `thai-communication/SKILL.md` ตรงกับ source ก่อนตอบ คำตอบแยกผลทดสอบในเครื่องออกจากการยืนยันว่า remote worker หยุดจริง และระบุสิ่งที่ต้องตรวจต่อ

ทดสอบด้วย project skill ในโฟลเดอร์ชั่วคราว โดย symlink `.agents/skills/thai-communication` ไปยัง skill กลางของ Crux แล้วใช้:

```sh
codex exec --ignore-user-config --ephemeral --skip-git-repo-check \
  --sandbox read-only --cd TEMP_PROJECT --disable hooks \
  --model gpt-6.1-sol -c 'model_reasoning_effort="low"' --json \
  '$thai-communication อธิบาย checkpoint กับหลักฐานการหยุด remote worker ให้กระชับ'
```

แทน `TEMP_PROJECT` ด้วยโฟลเดอร์ที่เตรียมไว้ คำสั่งไม่ติดตั้ง plugin หรือแก้ config ส่วนกลาง และยังใช้ auth ของ Codex เดิม ไม่คัดลอก credentials ทดสอบครั้งนี้ปิด hooks และใช้ skill ชื่อ `$thai-communication` จึงไม่ใช่หลักฐานว่า namespace `$crux:...`, mode persistence, native delegate, guard หรือการส่งงานข้าม provider ผ่านแล้ว และสองกรณีนี้ไม่แทนผลทดสอบทุกสถานการณ์
