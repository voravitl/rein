import { realpathSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { handle, readHookInput } from '../../../scripts/session-mode.mjs';

if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) {
  try {
    const dataRoot = process.env.CLAUDE_PLUGIN_DATA || join(process.env.CLAUDE_CONFIG_DIR || join(homedir(), '.claude'), 'crux');
    process.stdout.write(JSON.stringify(handle(await readHookInput(), join(dataRoot, 'sessions'))) + '\n');
  } catch (error) {
    console.error('crux session mode unavailable: ' + error.message);
    process.exitCode = 1;
  }
}
