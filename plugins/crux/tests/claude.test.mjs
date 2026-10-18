import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));

test('Claude manifest uses shared skills and Claude-only hooks and roles', () => {
  const manifest = JSON.parse(readFileSync(join(root, '.claude-plugin/plugin.json'), 'utf8'));
  assert.equal(manifest.name, 'crux');
  assert.equal(manifest.skills, './skills/');
  assert.equal(manifest.hooks, './adapters/claude/hooks/hooks.json');
  assert.equal(manifest.agents.length, 2);
  for (const path of manifest.agents) {
    assert.ok(existsSync(resolve(root, path)));
    const role = readFileSync(resolve(root, path), 'utf8');
    for (const resource of ['skills/thai-communication/SKILL.md', 'references/runtime.md', 'skills/crux-mode/SKILL.md']) {
      assert.ok(role.includes('${CLAUDE_PLUGIN_ROOT}/' + resource), resource);
    }
    assert.doesNotMatch(role, /^model:|^permissionMode:/m);
  }
  const hooks = JSON.parse(readFileSync(resolve(root, manifest.hooks), 'utf8')).hooks;
  assert.deepEqual(Object.keys(hooks).sort(), ['SessionStart', 'UserPromptSubmit']);
  for (const event of Object.values(hooks)) {
    const command = event[0].hooks[0].command;
    assert.equal(command, 'node "${CLAUDE_PLUGIN_ROOT}/adapters/claude/hooks/session-mode.mjs"');
  }
});

test('Claude hook subprocess persists explicit activation after relocation without touching Codex data', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-claude-'));
  try {
    const relocated = join(temp, 'plugin with spaces');
    cpSync(root, relocated, { recursive: true });
    const data = join(temp, 'claude data');
    const codexData = join(temp, 'codex data');
    const invoke = (payload, extraEnv = {}) => {
      const result = spawnSync(process.execPath, [join(relocated, 'adapters/claude/hooks/session-mode.mjs')], {
        input: JSON.stringify(payload), encoding: 'utf8',
        env: { ...process.env, CLAUDE_PLUGIN_DATA: data, PLUGIN_DATA: codexData, CODEX_HOME: codexData, ...extraEnv }
      });
      assert.equal(result.status, 0, result.stderr);
      return JSON.parse(result.stdout);
    };
    const prompt = (text, session = 'same-session') => invoke({ hook_event_name: 'UserPromptSubmit', session_id: session, prompt: text });
    const start = session => invoke({ hook_event_name: 'SessionStart', session_id: session, source: 'compact' });
    assert.deepEqual(start('same-session'), {});
    assert.deepEqual(prompt('ช่วยตรวจงาน'), {});
    assert.equal(existsSync(data), false);
    const active = prompt('/crux:crux-mode ช่วยตรวจงาน');
    assert.equal(active.hookSpecificOutput.hookEventName, 'UserPromptSubmit');
    const context = active.hookSpecificOutput.additionalContext;
    assert.ok(context.includes(join(realpathSync(relocated), 'skills/thai-communication/SKILL.md')));
    assert.ok(context.includes(join(realpathSync(relocated), 'skills/crux-mode/SKILL.md')));
    assert.equal(start('same-session').hookSpecificOutput.additionalContext, context);
    assert.deepEqual(start('different-session'), {});
    assert.deepEqual(prompt('คำถามใหม่', 'different-session'), {});
    assert.equal(readdirSync(join(data, 'sessions')).length, 1);
    assert.equal(existsSync(codexData), false);
    assert.equal(prompt('/crux:crux-mode off').hookSpecificOutput.hookEventName, 'UserPromptSubmit');
    assert.deepEqual(start('same-session'), {});
    assert.deepEqual(prompt('ทำต่อ'), {});
    prompt('/crux:crux-mode');
    prompt('/crux-off');
    assert.deepEqual(start('same-session'), {});
    assert.deepEqual(invoke({ hook_event_name: 'PreToolUse', session_id: 'ignored' }), {});
    const records = readdirSync(join(data, 'sessions')).map(file => JSON.parse(readFileSync(join(data, 'sessions', file), 'utf8')));
    assert.deepEqual(records, [{ active: false }]);
    const fallback = join(temp, 'isolated Claude config');
    invoke({ hook_event_name: 'UserPromptSubmit', session_id: 'fallback', prompt: '/crux:crux-mode' }, {
      CLAUDE_PLUGIN_DATA: '', CLAUDE_CONFIG_DIR: fallback
    });
    assert.equal(readdirSync(join(fallback, 'crux/sessions')).length, 1);
  } finally { rmSync(temp, { recursive: true, force: true }); }
});

test('Claude driver rejects malformed hook input without creating state', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-claude-invalid-'));
  try {
    const result = spawnSync(process.execPath, [join(root, 'adapters/claude/hooks/session-mode.mjs')], {
      input: '{', encoding: 'utf8', env: { ...process.env, CLAUDE_PLUGIN_DATA: join(temp, 'data') }
    });
    assert.equal(result.status, 1);
    assert.match(result.stderr, /crux session mode unavailable:/);
    assert.equal(result.stdout, '');
    assert.equal(existsSync(join(temp, 'data')), false);
  } finally { rmSync(temp, { recursive: true, force: true }); }
});
