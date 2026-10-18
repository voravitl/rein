import assert from 'node:assert/strict';
import { cpSync, existsSync, mkdtempSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { installAgents } from '../scripts/agents.mjs';

const source = dirname(dirname(fileURLToPath(import.meta.url)));

test('portable agent installation is idempotent, updates owned copies and preserves foreign files', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-agents-'));
  try {
    const plugin = join(temp, 'plugin with spaces');
    cpSync(source, plugin, { recursive: true });
    const home = join(temp, 'codex');
    mkdirSync(join(home, 'agents'), { recursive: true });
    writeFileSync(join(home, 'agents', 'foreign.toml'), 'unchanged');
    const installed = installAgents(plugin, home);
    assert.equal(installed.agents.length, 2);
    assert.deepEqual(installAgents(plugin, home), installed);
    for (const file of installed.agents) {
      const data = readFileSync(join(home, 'agents', file), 'utf8');
      assert.ok(!data.includes('@PLUGIN_ROOT@'));
      assert.ok(data.includes(plugin));
    }
    const moved = join(temp, 'moved plugin');
    cpSync(plugin, moved, { recursive: true });
    installAgents(moved, home);
    assert.ok(readFileSync(join(home, 'agents', installed.agents[0]), 'utf8').includes(moved));
    installAgents(moved, home, true);
    assert.deepEqual(readdirSync(join(home, 'agents')), ['foreign.toml']);
    assert.equal(readFileSync(join(home, 'agents', 'foreign.toml'), 'utf8'), 'unchanged');
  } finally { rmSync(temp, { recursive: true, force: true }); }
});

test('one modified or unowned target refuses the whole update/removal', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-agents-'));
  try {
    const home = join(temp, 'codex');
    const templates = readdirSync(join(source, 'adapters', 'codex', 'native-agents')).filter(f => f.endsWith('.toml'));
    mkdirSync(join(home, 'agents'), { recursive: true });
    writeFileSync(join(home, 'agents', templates[1]), 'user-owned');
    assert.throws(() => installAgents(source, home), /unowned\/modified/);
    assert.ok(!existsSync(join(home, 'agents', templates[0])));
    rmSync(join(home, 'agents', templates[1]));
    installAgents(source, home);
    writeFileSync(join(home, 'agents', templates[1]), 'user edit');
    assert.throws(() => installAgents(source, home, true), /unowned\/modified/);
    assert.ok(existsSync(join(home, 'agents', templates[0])));
    assert.equal(readFileSync(join(home, 'agents', templates[1]), 'utf8'), 'user edit');
  } finally { rmSync(temp, { recursive: true, force: true }); }
});
