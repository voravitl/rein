import assert from 'node:assert/strict';
import { existsSync, mkdtempSync, readFileSync, readdirSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { build } from '../adapters/antigravity/build.mjs';

test('Antigravity builds a native explicit main-agent bundle without installing global hooks or overwriting output', () => {
  const temp = mkdtempSync(join(tmpdir(), 'crux-antigravity-'));
  try {
    const output = build(join(temp, 'new bundle'));
    assert.deepEqual(Object.keys(JSON.parse(readFileSync(join(output, 'plugin.json'), 'utf8'))).sort(), ['description', 'name']);
    assert.equal(existsSync(join(output, 'hooks.json')), false);
    const agent = readFileSync(join(output, 'agents/crux.md'), 'utf8');
    const workspaceAgent = readFileSync(join(output, 'agent.md'), 'utf8');
    assert.equal(workspaceAgent.replaceAll('  - agents/', '  - '), agent);
    for (const [entry, text, expected] of [
      ['agent.md', workspaceAgent, ['agents/crux-agent.md', 'agents/comment-reviewer.md']],
      ['agents/crux.md', agent, ['crux-agent.md', 'comment-reviewer.md']]
    ]) {
      const refs = (text.match(/^agents:\n((?:  - [^\n]+\n)+)/m)?.[1] ?? '')
        .split('\n').filter(Boolean).map(line => line.slice(4));
      assert.deepEqual(refs, expected, `packaged delegates for ${entry}`);
      for (const ref of refs) assert.equal(existsSync(join(dirname(join(output, entry)), ref)), true);
    }
    for (const policy of ['skills/thai-communication/SKILL.md', 'references/communication.md']) {
      assert.ok(agent.includes(readFileSync(join(output, policy), 'utf8')), `embedded shared policy: ${policy}`);
    }
    assert.match(agent, /^mainAgent: true$/m);
    assert.match(agent, /^subagent: false$/m);
    assert.match(agent, /^model: inherit$/m);
    const mainTools = agent.match(/^tools:\n((?:  - [^\n]+\n)+)/m)?.[1] ?? '';
    for (const tool of ['view_file', 'write_to_file', 'run_command', 'invoke_subagent']) {
      assert.ok(mainTools.includes(`  - ${tool}\n`), `main agent exposes ${tool}`);
    }
    for (const unavailable of ['command_status', 'send_command_input']) {
      assert.ok(!mainTools.includes(`  - ${unavailable}\n`));
    }
    const implementation = readFileSync(join(output, 'agents/crux-agent.md'), 'utf8');
    const implementationTools = implementation.match(/^tools:\n((?:  - [^\n]+\n)+)/m)?.[1] ?? '';
    assert.ok(implementationTools.includes('  - write_to_file\n'));
    assert.ok(implementationTools.includes('  - run_command\n'));
    assert.ok(!implementationTools.includes('invoke_subagent'));
    const reviewer = readFileSync(join(output, 'agents/comment-reviewer.md'), 'utf8');
    const reviewerTools = reviewer.match(/^tools:\n((?:  - [^\n]+\n)+)/m)?.[1] ?? '';
    assert.ok(reviewerTools.includes('  - view_file\n'));
    for (const tool of ['write_to_file', 'replace_file_content', 'run_command', 'invoke_subagent']) {
      assert.ok(!reviewerTools.includes(`  - ${tool}\n`), `reviewer does not expose ${tool}`);
    }

    assert.match(agent, /skills\/thai-communication\/SKILL\.md/);
    assert.match(agent, /skills\/crux-mode\/SKILL\.md/);
    assert.doesNotMatch(agent, /@PLUGIN_ROOT@/);
    assert.equal(existsSync(join(output, 'skills/thai-communication/SKILL.md')), true);
    assert.equal(existsSync(join(output, 'references/runtime.md')), true);
    for (const file of readdirSync(join(output, 'agents')).filter(file => file !== 'crux.md')) {
      assert.match(readFileSync(join(output, 'agents', file), 'utf8'), /^mainAgent: false$/m);
    }
    assert.throws(() => build(output), /already exists/);
    const root = dirname(dirname(fileURLToPath(import.meta.url)));
    assert.throws(() => build(join(root, 'unexpected-output')), /outside the source plugin/);
  } finally { rmSync(temp, { recursive: true, force: true }); }
});
