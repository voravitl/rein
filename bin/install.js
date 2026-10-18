#!/usr/bin/env node
// npm postinstall: make bin/rein exist for this platform through the same installer the wrapper
// uses. A failure here is not fatal (npm install must not break); the first `rein` execution
// retries the install and fails loudly if it still cannot provide a binary.
const { spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');

const rootDir = path.resolve(__dirname, '..');
const isWin = process.platform === 'win32';
const binName = isWin ? 'rein.exe' : 'rein';
const binPath = path.join(rootDir, 'bin', binName);

if (!fs.existsSync(binPath)) {
  const installScript = path.join(rootDir, 'scripts', isWin ? 'install.ps1' : 'install.sh');
  if (!fs.existsSync(installScript)) {
    console.warn('rein: install script missing at ' + installScript + '; the native binary is not installed.');
    process.exit(0);
  }
  console.log('rein: installing the native binary for this platform...');
  const runner = isWin ? 'powershell' : 'sh';
  const res = spawnSync(runner, [installScript], {
    cwd: rootDir,
    stdio: 'inherit'
  });
  if (res.status === 0 && fs.existsSync(binPath)) {
    console.log('rein: native binary installed at ' + binPath);
  } else {
    const why = res.error ? 'could not run ' + runner + ' (' + res.error.message + ')'
      : res.status === null ? 'installer terminated by signal ' + (res.signal || 'unknown')
        : res.status !== 0 ? 'installer exited ' + res.status
          : 'no binary at ' + binPath;
    console.warn('rein: native binary not installed (' + why + '); `rein` will retry the install on its first execution.');
  }
}
