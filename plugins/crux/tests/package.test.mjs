import assert from 'node:assert/strict';
import { cpSync, existsSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { basename, dirname, join, relative, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
function files(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(e => e.isDirectory() ? files(join(dir, e.name)) : [join(dir, e.name)]);
}

test('plugin resources resolve and remain portable with upstream attribution', () => {
  const manifest = JSON.parse(readFileSync(join(root, 'plugin.json'), 'utf8'));
  assert.deepEqual(JSON.parse(readFileSync(join(root, '.codex-plugin/plugin.json'), 'utf8')), manifest);
  assert.equal(manifest.name, 'crux');
  for (const key of ['skills', 'hooks']) assert.ok(existsSync(resolve(root, manifest[key])), key);
  const entries = readdirSync(join(root, 'skills')).map(s => join(root, 'skills', s, 'SKILL.md'));
  assert.ok(entries.length > 0);
  for (const path of entries) {
    const data = readFileSync(path, 'utf8');
    assert.equal(data.match(/^name:\s*(\S+)/m)?.[1], basename(dirname(path)));
    assert.match(data, /^description:\s*\S/m);
  }
  for (const path of files(root).filter(p => /\.(?:md|json|toml|mjs|sh|py|ts)$/.test(p))) {
    const data = readFileSync(path, 'utf8');
    assert.ok(!/\/Users\/|\/home\/[^/\s]+\/\.codex\/plugins\/cache/.test(data), relative(root, path));
    if (!path.endsWith('.md')) continue;
    for (const match of data.matchAll(/\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g)) {
      const target = match[1].split('#')[0];
      if (!target || /^(?:[a-z]+:|\/|url$)/i.test(target) || /[<>{}]/.test(target)) continue;
      assert.ok(existsSync(resolve(dirname(path), decodeURIComponent(target))), relative(root, path) + ': ' + target);
    }
  }
  assert.match(readFileSync(join(root, 'LICENSE'), 'utf8'), /Copyright \(c\) 2026 Lauren Tan/);
  assert.match(readFileSync(join(root, 'UPSTREAM.md'), 'utf8'), /019a6a20592c10bf92869e5740368f578a9997fd/);
});

test('installed hook derives the plugin root after relocation and keeps data in its own directory', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-package-'));
  try {
    const relocated = join(temp, 'relocated plugin');
    cpSync(root, relocated, { recursive: true });
    const result = spawnSync(process.execPath, [join(relocated, 'adapters/codex/hooks/session-mode.mjs')], {
      input: JSON.stringify({ hook_event_name: 'UserPromptSubmit', session_id: 'relocation-test', prompt: '$crux:crux-mode test' }),
      encoding: 'utf8', env: { ...process.env, PLUGIN_DATA: join(temp, 'data') }
    });
    assert.equal(result.status, 0, result.stderr);
    const context = JSON.parse(result.stdout).hookSpecificOutput.additionalContext;
    assert.ok(context.includes(join(realpathSync(relocated), 'references/runtime.md')));
    assert.ok(context.includes(join(realpathSync(relocated), 'skills/crux-mode/SKILL.md')));
    assert.equal(readdirSync(join(temp, 'data/sessions')).length, 1);
  } finally { rmSync(temp, { recursive: true, force: true }); }
});

test('shared communication policy reaches every skill and installed native role', () => {
  const runtime = readFileSync(join(root, 'references/runtime.md'), 'utf8');
  assert.match(runtime, /\]\(communication\.md\)/);
  for (const skill of readdirSync(join(root, 'skills'))) {
    const content = readFileSync(join(root, 'skills', skill, 'SKILL.md'), 'utf8');
    assert.ok(content.includes('../../references/runtime.md'), skill);
  }
  for (const file of readdirSync(join(root, 'adapters/codex/native-agents'))) {
    const content = readFileSync(join(root, 'adapters/codex/native-agents', file), 'utf8');
    assert.ok(content.includes('@PLUGIN_ROOT@/references/communication.md'), file);
    assert.match(content, /User-facing output defaults to clear Thai/);
  }
  const teach = readFileSync(join(root, 'skills/teach/SKILL.md'), 'utf8');
  assert.doesNotMatch(teach, /in plain spoken English/);
});
