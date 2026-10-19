import assert from 'node:assert/strict';
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { handle } from './session-mode.mjs';
import { readHookInput } from '../../../scripts/session-mode.mjs';

test('persistent mode is explicit, isolated, resumable and reversible', () => {
  const root = mkdtempSync(join(tmpdir(), 'crux-mode-'));
  const submit = (id, prompt) => handle({ hook_event_name: 'UserPromptSubmit', session_id: id, prompt }, root);
  try {
    assert.deepEqual(submit('a', 'Please explain $crux:crux-mode'), {});
    assert.deepEqual(submit('a', 'text\n/crux-mode'), {});
    assert.equal(readdirSync(root).length, 0);
    assert.match(submit('a', '$crux:crux-mode fix test').hookSpecificOutput.additionalContext, /active/);
    assert.match(submit('a', 'continue').hookSpecificOutput.additionalContext, /active/);
    assert.deepEqual(submit('b', 'continue'), {});
    for (const event of ['SessionStart']) {
      assert.match(handle({ hook_event_name: event, session_id: 'a', source: 'compact' }, root).hookSpecificOutput.additionalContext, /active/);
    }
    const files = readdirSync(root);
    assert.equal(files.length, 1);
    assert.equal(statSync(join(root, files[0])).mode & 0o777, process.platform === 'win32' ? 0o666 : 0o600);
    assert.deepEqual(JSON.parse(readFileSync(join(root, files[0]), 'utf8')), { active: true });
    assert.match(submit('a', '/crux-off').hookSpecificOutput.additionalContext, /now off/);
    assert.deepEqual(submit('a', 'continue'), {});
    submit('b', '/crux-mode');
    submit('b', '$crux:crux-mode off');
    assert.deepEqual(submit('b', 'continue'), {});
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('invalid identity and unsupported events cannot enable or write state', () => {
  const root = mkdtempSync(join(tmpdir(), 'crux-mode-'));
  try {
    for (const id of [undefined, null, '', ' ', 22, 'x'.repeat(257)]) {
      assert.deepEqual(handle({ hook_event_name: 'UserPromptSubmit', session_id: id, prompt: '/crux-mode' }, root), {});
    }
    assert.deepEqual(handle({ hook_event_name: 'PreToolUse', session_id: 'a', prompt: '/crux-mode' }, root), {});
    assert.deepEqual(handle({ hook_event_name: 'PostCompact', session_id: 'a' }, root), {});
    assert.equal(readdirSync(root).length, 0);
  } finally { rmSync(root, { recursive: true, force: true }); }
});


test('activation uses only Crux commands with an exact command boundary', () => {
  const root = mkdtempSync(join(tmpdir(), 'crux-mode-'));
  const submit = prompt => handle({ hook_event_name: 'UserPromptSubmit', session_id: 'isolated', prompt }, root);
  try {
    for (const prompt of ['$pstack:poteto-mode', '/poteto-mode', '/pstack-off',
      '$crux:crux-mode-extra', '/crux-mode-extra', '/crux-off explanation']) {
      assert.deepEqual(submit(prompt), {});
    }
    assert.equal(readdirSync(root).length, 0);
    assert.match(submit('/crux-mode implement scoped task').hookSpecificOutput.additionalContext, /references[\\/]runtime\.md/);
    assert.match(submit('$crux:crux-mode off').hookSpecificOutput.additionalContext, /now off/);
    assert.deepEqual(submit('continue'), {});
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('active mode carries Thai communication rules across continuation and compaction', () => {
  const root = mkdtempSync(join(tmpdir(), 'crux-language-'));
  const submit = prompt => handle({ hook_event_name: 'UserPromptSubmit', session_id: 'thai-session', prompt }, root);
  try {
    const outputs = [
      submit('$crux:crux-mode อธิบายการส่งต่องาน'),
      submit('Explain the result'),
      handle({ hook_event_name: 'SessionStart', session_id: 'thai-session', source: 'compact' }, root)
    ];
    for (const output of outputs) {
      const context = output.hookSpecificOutput.additionalContext;
      assert.match(context, /User-facing output defaults to clear Thai/);
      assert.match(context, /honor an explicit different language for its scope/);
      assert.match(context, /Preserve exact commands and error text/);
      assert.match(context, /references[\\/]communication\.md/);
    }
    submit('/crux-off');
    assert.deepEqual(submit('continue'), {});
  } finally { rmSync(root, { recursive: true, force: true }); }
});

test('Claude namespaced activation uses the shared mode and exact disable boundary', () => {
  const root = mkdtempSync(join(tmpdir(), 'crux-claude-mode-'));
  const submit = prompt => handle({ hook_event_name: 'UserPromptSubmit', session_id: 'claude-session', prompt }, root);
  try {
    for (const prompt of ['explain /crux:crux-mode', '/crux:crux-mode-extra']) {
      assert.deepEqual(submit(prompt), {});
    }
    assert.match(submit('/crux:crux-mode ออกแบบงาน').hookSpecificOutput.additionalContext, /clear Thai/);
    assert.match(submit('/crux:crux-mode off').hookSpecificOutput.additionalContext, /now off/);
    assert.deepEqual(submit('continue'), {});
  } finally { rmSync(root, { recursive: true, force: true }); }
});


test('hook input preserves Thai and emoji across every byte boundary', async () => {
  const payload = { hook_event_name: 'UserPromptSubmit', session_id: 'session-ไทย😀', prompt: '/crux-mode อธิบายงาน' };
  const bytes = Buffer.from(JSON.stringify(payload));
  for (let split = 1; split < bytes.length; split++) {
    async function* chunks() { yield bytes.subarray(0, split); yield bytes.subarray(split); }
    assert.deepEqual(await readHookInput(chunks()), payload, `split at byte ${split}`);
  }
});

test('hook input enforces the original byte limit and rejects malformed JSON', async () => {
  const limit = 1048576;
  const exact = Buffer.from(JSON.stringify('x'.repeat(limit - 2)));
  async function* chunks(bytes) { yield bytes.subarray(0, 5); yield bytes.subarray(5); }
  assert.equal((await readHookInput(chunks(exact))).length, limit - 2);
  await assert.rejects(readHookInput(chunks(Buffer.concat([exact, Buffer.from(' ')]))), /exceeds 1 MiB/);
  await assert.rejects(readHookInput(chunks(Buffer.from('{'))), SyntaxError);
});
