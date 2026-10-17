#!/usr/bin/env node
const { spawnSync } = require('child_process');
const path = require('path');
const fs = require('fs');

const rootDir = path.resolve(__dirname, '..');
const isWin = process.platform === 'win32';
const binName = isWin ? 'rein.exe' : 'rein';
const binPath = path.join(rootDir, 'bin', binName);

if (!fs.existsSync(binPath)) {
  const installScript = path.join(rootDir, 'scripts', isWin ? 'install.ps1' : 'install.sh');
  if (fs.existsSync(installScript)) {
    console.log('rein: compiling native binary for platform...');
    const runner = isWin ? 'powershell' : 'sh';
    const res = spawnSync(runner, [installScript], {
      cwd: rootDir,
      stdio: 'inherit'
    });
    if (res.status === 0 && fs.existsSync(binPath)) {
      console.log('rein: binary compiled successfully at ' + binPath);
    } else {
      console.warn('rein: postinstall compile failed or Go unavailable; will build or fallback on first execution.');
    }
  }
}
