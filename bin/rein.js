#!/usr/bin/env node
const { spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');

const rootDir = path.resolve(__dirname, '..');
const isWin = process.platform === 'win32';
const binName = isWin ? 'rein.exe' : 'rein';
let binPath = path.join(rootDir, 'bin', binName);

// Entry points of this same package must never be selected from PATH: npm exposes this wrapper
// through symlinks (node_modules/.bin/rein, a global bin shim), and spawning one re-executes the
// wrapper instead of the native binary.
const selfEntrypoints = new Set();
for (const entry of [__filename, path.join(rootDir, 'bin', 'rein.js')]) {
  try { selfEntrypoints.add(fs.realpathSync(entry)); } catch { /* not resolvable */ }
}
let ownBinDir = null;
try { ownBinDir = fs.realpathSync(path.join(rootDir, 'bin')); } catch { /* absent */ }

function isOwnEntrypoint(realPath) {
  if (selfEntrypoints.has(realPath)) return true;
  return ownBinDir !== null && realPath.startsWith(ownBinDir + path.sep);
}

function findSystemRein() {
  const envPath = process.env.PATH || '';
  const delimiter = isWin ? ';' : ':';
  const dirs = envPath.split(delimiter);
  for (const dir of dirs) {
    const candidate = path.join(dir, binName);
    if (!fs.existsSync(candidate)) continue;
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
    } catch {
      continue;
    }
    let real;
    try {
      real = fs.realpathSync(candidate);
    } catch {
      continue; // cannot resolve it, so it cannot be proven not to be this wrapper
    }
    if (isOwnEntrypoint(real)) continue;
    return candidate;
  }
  return null;
}

if (!fs.existsSync(binPath)) {
  const sysRein = findSystemRein();
  if (sysRein) {
    binPath = sysRein;
  } else {
    // No usable system binary: build or download through the shipped installer.
    const installScript = path.join(rootDir, 'scripts', isWin ? 'install.ps1' : 'install.sh');
    if (fs.existsSync(installScript)) {
      const runner = isWin ? 'powershell' : 'sh';
      const res = spawnSync(runner, [installScript], {
        cwd: rootDir,
        stdio: 'inherit'
      });
      if (res.status !== 0 || !fs.existsSync(binPath)) {
        const why = res.error ? 'could not run ' + runner + ' (' + res.error.message + ')'
          : res.status === null ? 'terminated by signal ' + (res.signal || 'unknown')
            : res.status !== 0 ? 'exit ' + res.status
              : 'no binary at ' + binPath;
        console.error('rein: failed to install the binary via ' + installScript + ' (' + why + ')');
        process.exit(res.status ? res.status : 1);
      }
    } else {
      console.error('rein: binary not found and install script missing at ' + installScript);
      process.exit(1);
    }
  }
}

const child = spawnSync(binPath, process.argv.slice(2), {
  stdio: 'inherit'
});
if (child.error) {
  console.error('rein: failed to run ' + binPath + ': ' + child.error.message);
  process.exit(1);
}
if (child.status === null) {
  console.error('rein: ' + binPath + ' was terminated by signal ' + (child.signal || 'unknown'));
  process.exit(1);
}
process.exit(child.status);
