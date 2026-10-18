// F-05: the published npm tarball ships no Go sources and no prebuilt binary, so its only complete
// distribution path is the checksum-verified GitHub release download. This test packs the real
// package, extracts the tarball into a clean directory, serves the platform asset + SHA256SUMS from
// a local fixture over file:// and runs the shipped installer end to end, offline.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = dirname(dirname(fileURLToPath(import.meta.url)));

function platformAsset() {
  const os = { darwin: 'darwin', linux: 'linux' }[process.platform];
  const arch = { x64: 'amd64', arm64: 'arm64' }[process.arch];
  return os && arch ? { asset: `rein-${os}-${arch}` } : null;
}

function run(command, args, options) {
  const res = spawnSync(command, args, { encoding: 'utf8', ...options });
  assert.ok(!res.error, `${command} ${args.join(' ')}: ${res.error && res.error.message}`);
  return res;
}

test('npm tarball bootstraps bin/rein from the verified release asset without Go sources', { timeout: 240000 }, () => {
  const target = platformAsset();
  if (!target) return; // only the POSIX install.sh targets are exercised here

  const temp = mkdtempSync(join(tmpdir(), 'rein-npm-tarball-'));
  try {
    const packDir = join(temp, 'pack');
    mkdirSync(packDir);
    const pack = run('npm', ['pack', '--ignore-scripts', '--pack-destination', packDir, '--cache', join(temp, 'npm-cache')], { cwd: root, timeout: 120000 });
    assert.equal(pack.status, 0, pack.stderr);
    const tgz = readdirSync(packDir).find((name) => name.endsWith('.tgz'));
    assert.ok(tgz, 'npm pack produced a tarball');

    const extractDir = join(temp, 'extract');
    mkdirSync(extractDir);
    const untar = run('tar', ['-xzf', join(packDir, tgz), '-C', extractDir], { timeout: 60000 });
    assert.equal(untar.status, 0, untar.stderr);
    const pkg = join(extractDir, 'package');
    assert.ok(existsSync(join(pkg, 'scripts/install.sh')), 'the tarball ships scripts/install.sh');
    // Premise of the journey: no sources, no prebuilt binary, no dist artifact.
    assert.ok(!existsSync(join(pkg, 'go.mod')), 'tarball must not rely on local Go sources');
    assert.ok(!existsSync(join(pkg, 'bin', 'rein')), 'tarball must not ship a platform binary');

    // Local release fixture: the platform asset and its SHA256SUMS, exactly as release.yml publishes them.
    const fixture = join(temp, 'release');
    mkdirSync(fixture);
    const build = run('go', ['build', '-o', join(fixture, target.asset), './cmd/rein'], {
      cwd: root, timeout: 180000, env: { ...process.env, CGO_ENABLED: '0' }
    });
    assert.equal(build.status, 0, build.stderr);
    const digest = createHash('sha256').update(readFileSync(join(fixture, target.asset))).digest('hex');
    writeFileSync(join(fixture, 'SHA256SUMS'), `${digest}  ${target.asset}\n`);
    const releaseUrl = 'file://' + fixture;

    const install = run('sh', ['scripts/install.sh'], { cwd: pkg, timeout: 60000, env: { ...process.env, REIN_RELEASE_URL: releaseUrl } });
    assert.equal(install.status, 0, install.stderr);
    assert.ok(existsSync(join(pkg, 'bin', 'rein')), 'install.sh produced bin/rein');

    const native = run(join(pkg, 'bin', 'rein'), ['version'], { timeout: 30000 });
    assert.equal(native.status, 0, native.stderr);
    assert.match(native.stdout, /^rein /);
    const wrapper = run(process.execPath, ['bin/rein.js', 'version'], { cwd: pkg, timeout: 30000 });
    assert.equal(wrapper.status, 0, wrapper.stderr);
    assert.match(wrapper.stdout, /^rein /);

    // The postinstall entry point must exercise the same install path and report what happened.
    rmSync(join(pkg, 'bin', 'rein'));
    const postinstall = run(process.execPath, ['bin/install.js'], { cwd: pkg, timeout: 60000, env: { ...process.env, REIN_RELEASE_URL: releaseUrl } });
    assert.equal(postinstall.status, 0, postinstall.stderr);
    assert.ok(existsSync(join(pkg, 'bin', 'rein')), 'postinstall produced bin/rein');
    assert.match(postinstall.stdout, /installed/);
    assert.doesNotMatch(postinstall.stdout + postinstall.stderr, /compiled/i, 'a downloaded install must not be reported as a compile');

    const afterPostinstall = run(join(pkg, 'bin', 'rein'), ['version'], { timeout: 30000 });
    assert.equal(afterPostinstall.status, 0, afterPostinstall.stderr);
    assert.match(afterPostinstall.stdout, /^rein /);
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
});
