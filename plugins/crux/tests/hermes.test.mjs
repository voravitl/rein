import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const adapter = join(root, 'adapters', 'hermes');

const python = process.env.CRUX_PYTHON || 'python3';

const loadPlugin = (dir, extraEnv = {}) => {
  const script = `
import json, sys
sys.path.insert(0, ${JSON.stringify(dir)})
import __init__ as plugin

class Ctx:
    def __init__(self):
        self.hooks = {}
    def register_hook(self, name, cb):
        self.hooks[name] = cb

ctx = Ctx()
plugin.register(ctx)
payload = json.loads(sys.argv[1])
result = ctx.hooks["pre_llm_call"](**payload)
print(json.dumps(result))
`;
  const result = spawnSync(python, ['-c', script, JSON.stringify(extraEnv.payload ?? {})], {
    encoding: 'utf8',
    env: { ...process.env, ...extraEnv.env }
  });
  assert.equal(result.status, 0, result.stderr);
  return JSON.parse(result.stdout);
};

test('Hermes manifest declares the shared plugin and the pre_llm_call hook', () => {
  const manifest = readFileSync(join(adapter, 'plugin.yaml'), 'utf8');
  assert.match(manifest, /^name: crux$/m);
  assert.match(manifest, /^kind: standalone$/m);
  assert.match(manifest, /^\s+- pre_llm_call$/m);
  assert.ok(existsSync(join(adapter, '__init__.py')));
  assert.ok(existsSync(join(adapter, 'runtime.md')));
});

test('the Hermes adapter registers exactly the pre_llm_call hook', () => {
  const source = readFileSync(join(adapter, '__init__.py'), 'utf8');
  assert.match(source, /ctx\.register_hook\("pre_llm_call"/);
});

test('mode stays off until an explicit activation command', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-hermes-'));
  try {
    const state = join(temp, 'state');
    const off = loadPlugin(adapter, {
      env: { CRUX_STATE_DIR: state },
      payload: { session_id: 's1', user_message: 'ทำงานให้หน่อย', is_first_turn: true }
    });
    assert.equal(off, null);

    const on = loadPlugin(adapter, {
      env: { CRUX_STATE_DIR: state },
      payload: { session_id: 's1', user_message: '/crux:crux-mode', is_first_turn: true }
    });
    assert.ok(on && typeof on.context === 'string');
    assert.match(on.context, /crux persistent mode is active/);
    assert.match(on.context, /thai-communication\/SKILL\.md/);
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});

test('the mode is per session and survives a later turn', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-hermes-'));
  try {
    const state = join(temp, 'state');
    const env = { CRUX_STATE_DIR: state };
    loadPlugin(adapter, { env, payload: { session_id: 'a', user_message: '/crux:crux-mode', is_first_turn: true } });
    const other = loadPlugin(adapter, { env, payload: { session_id: 'b', user_message: 'ทำงานต่อ', is_first_turn: false } });
    assert.equal(other, null, 'a second session must not inherit the mode');
    const later = loadPlugin(adapter, { env, payload: { session_id: 'a', user_message: 'ทำงานต่อ', is_first_turn: false } });
    assert.ok(later && typeof later.context === 'string');
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});

test('the mode can be turned off again in the same session', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-hermes-'));
  try {
    const state = join(temp, 'state');
    const env = { CRUX_STATE_DIR: state };
    loadPlugin(adapter, { env, payload: { session_id: 's', user_message: '/crux:crux-mode', is_first_turn: true } });
    const off = loadPlugin(adapter, { env, payload: { session_id: 's', user_message: '/crux:crux-mode off', is_first_turn: false } });
    assert.equal(off, null);
    const after = loadPlugin(adapter, { env, payload: { session_id: 's', user_message: 'ทำงานต่อ', is_first_turn: false } });
    assert.equal(after, null);
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});

test('the adapter works when the plugin is relocated and does not touch another harness state', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-hermes-'));
  try {
    const relocated = join(temp, 'plugin with spaces');
    cpSync(adapter, relocated, { recursive: true });
    const state = join(temp, 'state');
    const result = loadPlugin(relocated, {
      env: { CRUX_STATE_DIR: state, PLUGIN_DATA: join(temp, 'other-harness') },
      payload: { session_id: 's', user_message: '/crux:crux-mode', is_first_turn: true }
    });
    assert.ok(result && typeof result.context === 'string');
    assert.ok(existsSync(state), 'the state must land in the adapter state directory');
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});
