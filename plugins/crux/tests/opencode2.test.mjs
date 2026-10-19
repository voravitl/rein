import assert from 'node:assert/strict';
import { cpSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';
import plugin from '../adapters/opencode2/index.js';

async function harness(storage = new Map(), adapter = plugin) {
  const hooks = new Map();
  await adapter.setup({
    session: { hook: async (event, handler) => { hooks.set(event, handler); } },
    storage: {
      get: async key => storage.get(key),
      set: async (key, value) => { storage.set(key, value); },
      remove: async key => { storage.delete(key); }
    }
  });
  return { storage, hooks };
}

test('OpenCode2 prompt/context hooks persist only the explicitly activated session and never rewrite prompts', async () => {
  assert.equal(plugin.id, 'crux');
  const first = await harness();
  const prompt = { sessionID: 'one', prompt: { text: '$crux:crux-mode ตรวจงาน' } };
  const copy = structuredClone(prompt);
  await first.hooks.get('prompt')(prompt);
  assert.deepEqual(prompt, copy);
  assert.deepEqual([...first.storage.values()], [true]);
  const restarted = await harness(first.storage);
  const active = { sessionID: 'one', system: [{ type: 'text', text: 'existing' }] };
  await restarted.hooks.get('context')(active);
  assert.equal(active.system[0].text, 'existing');
  assert.match(active.system[1].text, /thai-communication[\\/]SKILL\.md/);
  assert.match(active.system[1].text, /skills[\\/]crux-mode[\\/]SKILL\.md/);
  for (const id of ['two', '', undefined]) {
    const other = { sessionID: id, system: [] };
    await restarted.hooks.get('context')(other);
    assert.deepEqual(other.system, []);
  }
  await restarted.hooks.get('prompt')({ sessionID: 'one', prompt: { text: 'ordinary follow-up' } });
  assert.equal(first.storage.size, 1);
  await restarted.hooks.get('prompt')({ sessionID: 'one', prompt: { text: '/crux-off' } });
  const off = { sessionID: 'one', system: [] };
  await restarted.hooks.get('context')(off);
  assert.deepEqual(off.system, []);
  assert.equal(first.storage.size, 0);
});

test('OpenCode2 context resolves shared core from a relocated complete package', async () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-opencode2-'));
  try {
    const root = dirname(dirname(fileURLToPath(import.meta.url)));
    const relocated = join(temp, 'relocated plugin');
    cpSync(root, relocated, { recursive: true });
    const { default: moved } = await import(pathToFileURL(join(relocated, 'adapters/opencode2/index.js')));
    const { hooks } = await harness(new Map(), moved);
    await hooks.get('prompt')({ sessionID: 'moved', prompt: { text: '/crux-mode' } });
    const context = { sessionID: 'moved', system: [] };
    await hooks.get('context')(context);
    assert.ok(context.system[0].text.includes(join(relocated, 'skills/thai-communication/SKILL.md')));
  } finally { rmSync(temp, { recursive: true, force: true }); }
});
