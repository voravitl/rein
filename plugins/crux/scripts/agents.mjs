import { createHash, randomUUID } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, realpathSync, readdirSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const hash = text => createHash('sha256').update(text).digest('hex');
const read = path => existsSync(path) ? readFileSync(path, 'utf8') : null;

function atomic(path, value) {
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  const temp = path + '.' + randomUUID() + '.tmp';
  try {
    writeFileSync(temp, value, { flag: 'wx', mode: 0o600 });
    renameSync(temp, path);
  } finally { rmSync(temp, { force: true }); }
}

export function installAgents(pluginRoot, codexHome, remove = false) {
  const { name } = JSON.parse(readFileSync(join(pluginRoot, 'plugin.json'), 'utf8'));
  if (!/^[a-z][a-z0-9-]*$/.test(name)) throw new Error('Invalid plugin name');
  const recordPath = join(codexHome, name, 'agents.json');
  const raw = read(recordPath);
  const inventory = raw === null ? { version: 1, agents: {} } : JSON.parse(raw);
  if (inventory.version !== 1 || !inventory.agents || typeof inventory.agents !== 'object') throw new Error('Invalid agent ownership inventory');
  const templates = join(pluginRoot, 'adapters', 'codex', 'native-agents');
  const files = remove ? Object.keys(inventory.agents) : readdirSync(templates).filter(f => f.endsWith('.toml'));
  const plan = files.map(file => {
    if (!file.startsWith(name + '-') || !/^[a-z0-9-]+\.toml$/.test(file)) throw new Error('Unscoped agent path: ' + file);
    const dest = join(codexHome, 'agents', file);
    const old = read(dest);
    const content = remove ? null : readFileSync(join(templates, file), 'utf8')
      .replaceAll('@PLUGIN_ROOT@', JSON.stringify(resolve(pluginRoot)).slice(1, -1));
    if (old !== null && old !== content && hash(old) !== inventory.agents[file]) {
      throw new Error('Refusing to overwrite or remove an unowned/modified agent: ' + file);
    }
    return { file, dest, old, content };
  });
  // Validate every target before the first write so one foreign file cannot cause a partial install.
  const agents = { ...inventory.agents };
  for (const item of plan) {
    if (remove) {
      if (item.old !== null) rmSync(item.dest);
      delete agents[item.file];
    } else {
      if (item.old !== item.content) atomic(item.dest, item.content);
      agents[item.file] = hash(item.content);
    }
  }
  if (plan.length || raw !== null) atomic(recordPath, JSON.stringify({ version: 1, agents }, null, 2) + '\n');
  return { action: remove ? 'removed' : 'installed', agents: plan.map(p => p.file) };
}

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  try {
    const args = process.argv.slice(2);
    if (args.length && !(args.length === 1 && args[0] === '--remove')) throw new Error('Usage: node scripts/agents.mjs [--remove]');
    console.log(JSON.stringify(installAgents(root, process.env.CODEX_HOME || join(homedir(), '.codex'), args[0] === '--remove')));
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
