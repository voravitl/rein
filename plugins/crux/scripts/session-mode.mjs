import { createHash, randomUUID } from 'node:crypto';
import { mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const pluginRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const events = new Set(['SessionStart', 'UserPromptSubmit']);

// State is a session preference, never permission to run tools or resume work.
export function handle(payload, stateRoot) {
  if (!payload || typeof payload !== 'object' || !events.has(payload.hook_event_name)) return {};
  const id = payload.session_id;
  if (typeof id !== 'string' || !id.trim() || id.length > 256) return {};
  const path = join(stateRoot, createHash('sha256').update(id).digest('hex') + '.json');
  let active = false;
  try { active = JSON.parse(readFileSync(path, 'utf8')).active === true; }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  if (payload.hook_event_name === 'UserPromptSubmit' && typeof payload.prompt === 'string') {
    const change = activation(payload.prompt);
    if (change !== null) {
      active = change;
      mkdirSync(stateRoot, { recursive: true, mode: 0o700 });
      const temp = path + '.' + randomUUID() + '.tmp';
      try {
        writeFileSync(temp, JSON.stringify({ active }), { mode: 0o600, flag: 'wx' });
        renameSync(temp, path);
      } finally { rmSync(temp, { force: true }); }
      if (!active) return { hookSpecificOutput: { hookEventName: payload.hook_event_name,
        additionalContext: 'crux persistent mode is now off for this session. Continue following the user request and existing instructions.' } };
    }
  }
  if (!active) return {};
  return { hookSpecificOutput: { hookEventName: payload.hook_event_name,
    additionalContext: modeContext() } };

}


export function modeContext() {
  return `crux persistent mode is active for this session. User-facing output defaults to clear Thai; honor an explicit different language for its scope. Preserve exact commands and error text. Read ${pluginRoot}/skills/thai-communication/SKILL.md and ${pluginRoot}/references/communication.md. Before task work, read ${pluginRoot}/references/runtime.md and ${pluginRoot}/skills/crux-mode/SKILL.md. Follow the mode coordination sequence for ADLC, multi-agent work, Rein and Orca, then apply the relevant playbook to the current request; preserve existing approval and workspace boundaries. This mode does not authorize extra work, recursive delegation, external writes or an unattended loop. Turn off with $crux:crux-mode off (Codex), /crux:crux-mode off (Claude), or /crux-off where supported.`;
}

export async function readHookInput(stream = process.stdin) {
  const chunks = [];
  let size = 0;
  for await (const chunk of stream) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk);
    size += bytes.length;
    if (size > 1048576) throw new Error('hook input exceeds 1 MiB');
    chunks.push(bytes);
  }
  return JSON.parse(Buffer.concat(chunks, size).toString('utf8'));
}

export function activation(prompt) {
  if (typeof prompt !== 'string') return null;
  const text = prompt.trim();
  if (/^(?:(?:\$crux:crux-mode|\/(?:crux:)?crux-mode)\s+off|\/crux-off)\s*[.!]?$/i.test(text)) return false;
  return /^(?:\$crux:crux-mode|\/(?:crux:)?crux-mode)(?=\s|$)/i.test(text) ? true : null;
}
