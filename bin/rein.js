#!/usr/bin/env node
const { spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');

const rootDir = path.resolve(__dirname, '..');
const isWin = process.platform === 'win32';
const binName = isWin ? 'rein.exe' : 'rein';
let binPath = path.join(rootDir, 'bin', binName);

function findSystemRein() {
  const envPath = process.env.PATH || '';
  const delimiter = isWin ? ';' : ':';
  const dirs = envPath.split(delimiter);
  for (const dir of dirs) {
    const candidate = path.join(dir, binName);
    if (fs.existsSync(candidate)) {
      try {
        fs.accessSync(candidate, fs.constants.X_OK);
        return candidate;
      } catch {
        // continue search
      }
    }
  }
  return null;
}

if (!fs.existsSync(binPath)) {
  const sysRein = findSystemRein();
  if (sysRein) {
    binPath = sysRein;
  } else {
    // Attempt build
    const installScript = path.join(rootDir, 'scripts', isWin ? 'install.ps1' : 'install.sh');
    if (fs.existsSync(installScript)) {
      const runner = isWin ? 'powershell' : 'sh';
      const res = spawnSync(runner, [installScript], {
        cwd: rootDir,
        stdio: 'inherit'
      });
      if (res.status !== 0 || !fs.existsSync(binPath)) {
        console.error('rein: failed to build binary via ' + installScript);
        process.exit(res.status || 1);
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

process.exit(child.status !== null ? child.status : (child.signal ? 1 : 0));
