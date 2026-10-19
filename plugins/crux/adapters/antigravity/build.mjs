import { cpSync, existsSync, mkdirSync, readFileSync, readdirSync, realpathSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const adapter = dirname(fileURLToPath(import.meta.url));
const root = resolve(adapter, '../..');
const readTools = ['view_file', 'list_dir', 'find_by_name', 'grep_search', 'ask_question'];
const workTools = [...readTools, 'write_to_file', 'replace_file_content', 'multi_replace_file_content',
  'run_command', 'manage_task'];
const mainTools = [...workTools, 'define_subagent', 'invoke_subagent', 'manage_subagents', 'send_message'];


// Build a separate deployable package; never replace an existing installation.
export function build(destination) {
  const output = resolve(destination);
  if (output === root || output.startsWith(root + sep)) throw new Error('output must be outside the source plugin');
  if (existsSync(output)) throw new Error('output already exists; choose a new directory');
  mkdirSync(output, { recursive: true });
  for (const item of ['skills', 'references', 'agents', 'adapters', 'scripts', 'LICENSE', 'UPSTREAM.md']) {
    cpSync(join(root, item), join(output, item), { recursive: true });
  }
  for (const file of readdirSync(join(output, 'agents'))) {
    const path = join(output, 'agents', file);
    const tools = file === 'crux-agent.md' ? workTools : readTools;
    const content = readFileSync(path, 'utf8').replace(/^---\n/,
      '---\nmainAgent: false\nsubagent: true\nmodel: inherit\ntools:\n' + tools.map(tool => `  - ${tool}\n`).join(''))
      .replaceAll('Codex runtime adapter', 'Antigravity runtime adapter');
    writeFileSync(path, content);
  }
  cpSync(join(adapter, 'plugin.json'), join(output, 'plugin.json'));
  const agent = [readFileSync(join(adapter, 'agents/crux.md'), 'utf8')
    .replace('model: inherit\n', 'model: inherit\ntools:\n' + mainTools.map(tool => `  - ${tool}\n`).join('')),
    ...['skills/thai-communication/SKILL.md', 'references/communication.md']
      .map(path => readFileSync(join(root, path), 'utf8'))].join('\n\n');
  for (const [entry, prefix] of [['agent.md', 'agents/'], ['agents/crux.md', '']]) {
    const delegates = ['crux-agent.md', 'comment-reviewer.md'].map(file => `  - ${prefix}${file}\n`).join('');
    writeFileSync(join(output, entry), agent.replace('subagent: false\n', `subagent: false\nagents:\n${delegates}`));
  }
  return output;
}

if (process.argv[1] && existsSync(process.argv[1]) && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  if (process.argv.length !== 3) throw new Error('usage: node build.mjs <new-output-directory>');
  process.stdout.write(build(process.argv[2]) + '\n');
}
