// F-06/F-07: the JS wrapper must never re-execute itself through a PATH entry that is (or points
// at) this same package's entry point, and a failed native launch must exit nonzero with a message.
import assert from 'node:assert/strict';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const isWin = process.platform === 'win32';

test('wrapper ignores a PATH entry that symlinks back to itself instead of re-executing', { timeout: 30000 }, () => {
  if (isWin) return;
  const temp = mkdtempSync(join(tmpdir(), 'rein-self-path-'));
  try {
    const pkg = join(temp, 'pkg');
    mkdirSync(join(pkg, 'bin'), { recursive: true });
    mkdirSync(join(pkg, 'scripts'), { recursive: true });
    cpSync(join(root, 'bin/rein.js'), join(pkg, 'bin/rein.js'));
    // A stub installer keeps this test offline and about selection: it only proves the wrapper
    // reached the build/download path instead of launching itself.
    writeFileSync(join(pkg, 'scripts/install.sh'), [
      '#!/bin/sh',
      'cd "${0%/*}/.." || exit 1',
      "printf '#!/bin/sh\\necho rein 0.0.0-test\\n' > bin/rein",
      '/bin/chmod +x bin/rein',
      'echo "stub installer ran"',
      ''
    ].join('\n'));

    // npm-style exposure: the first PATH entry has `rein` -> the JS wrapper itself.
    const pathDir = join(temp, 'path');
    mkdirSync(pathDir);
    symlinkSync(join(pkg, 'bin/rein.js'), join(pathDir, 'rein'));
    const emptyDir = join(temp, 'empty');
    mkdirSync(emptyDir);

    // Subprocess interception bounds the test even if a regression recurses: a spawn that
    // resolves to the wrapper is recorded and denied, any other spawn runs for real.
    const mock = join(temp, 'mock-spawn.cjs');
    writeFileSync(mock, [
      "'use strict';",
      "const cp = require('child_process');",
      "const fs = require('fs');",
      "const report = process.env.REIN_TEST_REPORT;",
      "const self = fs.realpathSync(process.env.REIN_TEST_WRAPPER);",
      "const realSpawnSync = cp.spawnSync;",
      "cp.spawnSync = function (cmd, args, options) {",
      "  let real = null;",
      "  try { real = fs.realpathSync(cmd); } catch {}",
      "  const selfCall = real !== null && real === self;",
      "  fs.appendFileSync(report, JSON.stringify({ cmd: String(cmd), args: (args || []).map(String), real, selfCall }) + '\\n');",
      "  if (selfCall) return { status: 0, signal: null, error: undefined, stdout: '', stderr: '', output: [], pid: 0 };",
      "  return realSpawnSync(cmd, args, options);",
      "};",
      ''
    ].join('\n'));

    const report = join(temp, 'calls.jsonl');
    const res = spawnSync(process.execPath, ['--require', mock, join(pkg, 'bin/rein.js'), 'version'], {
      cwd: pkg,
      encoding: 'utf8',
      timeout: 20000,
      env: {
        ...process.env,
        PATH: [pathDir, emptyDir, '/bin', '/usr/bin'].join(':'),
        REIN_TEST_REPORT: report,
        REIN_TEST_WRAPPER: join(pkg, 'bin/rein.js')
      }
    });
    assert.equal(res.status, 0, res.stderr);
    assert.ok(existsSync(report), 'the mock recorded subprocess calls');
    const calls = readFileSync(report, 'utf8').trim().split('\n').map((line) => JSON.parse(line));
    assert.ok(!calls.some((call) => call.selfCall), 'wrapper must not re-execute itself: ' + JSON.stringify(calls));
    assert.ok(calls.some((call) => call.args.some((arg) => arg.endsWith('install.sh'))),
      'wrapper must fall through to the build/download path: ' + JSON.stringify(calls));
    assert.match(res.stdout, /rein 0\.0\.0-test/, 'the installed binary result must be passed through');
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});

test('wrapper exits nonzero with a message when the native launch fails', { timeout: 30000 }, () => {
  if (isWin) return;
  const temp = mkdtempSync(join(tmpdir(), 'rein-launch-fail-'));
  try {
    const pkg = join(temp, 'pkg');
    mkdirSync(join(pkg, 'bin'), { recursive: true });
    cpSync(join(root, 'bin/rein.js'), join(pkg, 'bin/rein.js'));
    writeFileSync(join(pkg, 'bin/rein'), 'not a real binary\n');
    chmodSync(join(pkg, 'bin/rein'), 0o644);

    const res = spawnSync(process.execPath, [join(pkg, 'bin/rein.js'), 'version'], { cwd: pkg, encoding: 'utf8', timeout: 20000 });
    assert.notEqual(res.status, 0, 'a failed native launch must not exit 0 (stdout: ' + JSON.stringify(res.stdout) + ')');
    assert.match(res.stderr, /rein:/, 'the failure must be reported on stderr');
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});
