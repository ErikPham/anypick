import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { gunzipSync } from 'node:zlib';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import {
  LauncherError,
  platformTarget,
  resolveNativeBinary,
} from '../bin/anypick.js';

const root = resolve(import.meta.dirname, '..');
const rootManifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const platformPackages = [
  {
    dir: 'cli-darwin-arm64',
    name: '@anypick/cli-darwin-arm64',
    os: ['darwin'],
    cpu: ['arm64'],
  },
  {
    dir: 'cli-darwin-x64',
    name: '@anypick/cli-darwin-x64',
    os: ['darwin'],
    cpu: ['x64'],
  },
  {
    dir: 'cli-linux-arm64-gnu',
    name: '@anypick/cli-linux-arm64-gnu',
    os: ['linux'],
    cpu: ['arm64'],
    libc: ['glibc'],
  },
  {
    dir: 'cli-linux-x64-gnu',
    name: '@anypick/cli-linux-x64-gnu',
    os: ['linux'],
    cpu: ['x64'],
    libc: ['glibc'],
  },
  {
    dir: 'cli-win32-x64',
    name: '@anypick/cli-win32-x64',
    os: ['win32'],
    cpu: ['x64'],
  },
];

function glibcReport() {
  return { getReport: () => ({ header: { glibcVersionRuntime: '2.31' } }) };
}

function checkContracts() {
  assert.equal(
    platformTarget({ platform: 'darwin', arch: 'arm64' }).packageName,
    '@anypick/cli-darwin-arm64',
  );
  assert.equal(
    platformTarget({ platform: 'darwin', arch: 'x64' }).packageName,
    '@anypick/cli-darwin-x64',
  );
  assert.equal(
    platformTarget({ platform: 'linux', arch: 'arm64', report: glibcReport() }).packageName,
    '@anypick/cli-linux-arm64-gnu',
  );
  assert.equal(
    platformTarget({ platform: 'linux', arch: 'x64', report: glibcReport() }).packageName,
    '@anypick/cli-linux-x64-gnu',
  );
  assert.equal(
    platformTarget({ platform: 'win32', arch: 'x64' }).packageName,
    '@anypick/cli-win32-x64',
  );
  assert.throws(
    () =>
      platformTarget({
        platform: 'linux',
        arch: 'x64',
        report: { getReport: () => ({ header: {} }) },
      }),
    (error) => error instanceof LauncherError && error.message.includes('linux-x64-non-glibc'),
  );

  for (const expected of platformPackages) {
    const manifest = JSON.parse(
      readFileSync(join(root, 'npm', expected.dir, 'package.json'), 'utf8'),
    );
    assert.equal(manifest.name, expected.name);
    assert.equal(manifest.version, rootManifest.version);
    assert.deepEqual(manifest.os, expected.os);
    assert.deepEqual(manifest.cpu, expected.cpu);
    assert.deepEqual(manifest.libc, expected.libc);
    assert.equal(manifest.scripts, undefined, `${expected.name} must not have install scripts`);
    assert.equal(rootManifest.optionalDependencies[expected.name], 'workspace:*');
    assert.deepEqual(manifest.files, [
      expected.os[0] === 'win32' ? 'bin/anypick.exe' : 'bin/anypick',
    ]);
    assert.equal(manifest.publishConfig?.access, 'public');
    assert.equal(manifest.bin, undefined, `${expected.name} must not create a competing CLI shim`);
  }
  assert.deepEqual(
    Object.keys(rootManifest.optionalDependencies).sort(),
    platformPackages.map(({ name }) => name).sort(),
  );
  assert.equal(rootManifest.scripts?.postinstall, undefined);
}

function tarEntries(tarball) {
  const archive = gunzipSync(readFileSync(tarball));
  const entries = [];
  for (let offset = 0; offset + 512 <= archive.length;) {
    const header = archive.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    const name = header.subarray(0, 100).toString('utf8').replace(/\0.*$/s, '');
    const modeText = header.subarray(100, 108).toString('ascii').replace(/\0.*$/s, '').trim();
    const sizeText = header.subarray(124, 136).toString('ascii').replace(/\0.*$/s, '').trim();
    const mode = Number.parseInt(modeText || '0', 8);
    const size = Number.parseInt(sizeText || '0', 8);
    const bodyStart = offset + 512;
    entries.push({ name, mode, body: archive.subarray(bodyStart, bodyStart + size) });
    offset = bodyStart + Math.ceil(size / 512) * 512;
  }
  return entries;
}

function checkPackedManifest() {
  const packDir = mkdtempSync(join(tmpdir(), 'anypick-npm-pack-'));
  try {
    const pnpm = process.platform === 'win32' ? 'pnpm.cmd' : 'pnpm';
    const packed = spawnSync(pnpm, ['pack', '--pack-destination', packDir], {
      cwd: root,
      encoding: 'utf8',
      env: { ...process.env, NO_COLOR: '1' },
    });
    assert.equal(packed.status, 0, packed.stderr || packed.stdout);
    const tarball = join(packDir, `anypick-${rootManifest.version}.tgz`);
    assert.equal(existsSync(tarball), true, `pnpm pack did not create ${tarball}`);
    const entries = tarEntries(tarball);
    const manifestEntry = entries.find(({ name }) => name === 'package/package.json');
    assert.ok(manifestEntry, 'packed root manifest is missing');
    const manifest = JSON.parse(manifestEntry.body.toString('utf8'));
    for (const { name } of platformPackages) {
      assert.equal(
        manifest.optionalDependencies[name],
        rootManifest.version,
        `pnpm pack must rewrite ${name} to the exact release version`,
      );
    }
    assert.equal(
      Object.values(manifest.optionalDependencies).some((value) => value.startsWith('workspace:')),
      false,
    );
    const launcher = entries.find(({ name }) => name === 'package/bin/anypick.js');
    assert.ok(launcher, 'packed launcher is missing');
    assert.notEqual(launcher.mode & 0o111, 0, 'packed launcher must be executable');
    // ADR-0020: the root tarball is a distribution adapter. It carries the
    // launcher and nothing that needs a Node runtime at run time.
    assert.equal(
      entries.some(({ name }) => name.startsWith('package/scripts/')),
      false,
      'packed root manifest must not ship runtime scripts (plugin host was removed)',
    );
    assert.equal(
      entries.some(({ name }) => name.startsWith('package/plugins/')),
      false,
      'packed root manifest must not ship plugin packages',
    );
    assert.equal(
      entries.some(({ name }) => name.startsWith('package/npm/')),
      false,
      'root tarball must not embed platform binaries',
    );
  } finally {
    rmSync(packDir, { recursive: true, force: true });
  }
}

function installedPackageDir(appDir, packageName) {
  return join(appDir, 'node_modules', ...packageName.split('/'));
}

function makeFixture() {
  const temp = mkdtempSync(join(tmpdir(), 'anypick-npm-launcher-'));
  const appDir = join(temp, 'node_modules', 'anypick');
  mkdirSync(join(appDir, 'bin'), { recursive: true });
  copyFileSync(join(root, 'bin', 'anypick.js'), join(appDir, 'bin', 'anypick.js'));
  writeFileSync(
    join(appDir, 'package.json'),
    JSON.stringify({ name: 'anypick', version: rootManifest.version, type: 'module' }),
  );
  // No dist/ is created on purpose. The launcher used to spawn
  // dist/cli.js for `anypick tui`; the TUI is Go now, so if any code path
  // still reaches for a Node entry point this fixture fails instead of
  // quietly passing.
  const target = platformTarget();
  const platformDir = installedPackageDir(appDir, target.packageName);
  const binary = join(platformDir, ...target.binaryPath.split('/'));
  mkdirSync(dirname(binary), { recursive: true });
  writeFileSync(
    join(platformDir, 'package.json'),
    JSON.stringify({ name: target.packageName, version: rootManifest.version }),
  );
  if (process.platform !== 'win32') {
    writeFileSync(
      binary,
      [
        '#!/usr/bin/env node',
        "const fs = require('node:fs');",
        'const args = process.argv.slice(2);',
        "if (args[0] === '--wait') {",
        "  process.on('SIGTERM', () => {",
        "    fs.writeFileSync(process.env.ANYPICK_LAUNCHER_SIGNAL_FILE, 'SIGTERM');",
        '    process.exit(42);',
        '  });',
        "  fs.writeFileSync(process.env.ANYPICK_LAUNCHER_READY_FILE, 'ready');",
        '  setInterval(() => {}, 1000);',
        "} else if (args[0] === '--wait-default') {",
        "  fs.writeFileSync(process.env.ANYPICK_LAUNCHER_READY_FILE, 'ready');",
        '  setInterval(() => {}, 1000);',
        '} else {',
        "  let input = '';",
        "  process.stdin.setEncoding('utf8');",
        "  process.stdin.on('data', (chunk) => { input += chunk; });",
        "  process.stdin.on('end', () => {",
        "    process.stdout.write(JSON.stringify({ engine: 'native', args, env: process.env.ANYPICK_LAUNCHER_PASSTHROUGH, injectedPluginEnv: ['ANYPICK_BUNDLED_PLUGINS', 'ANYPICK_PLUGIN_HOST_NODE', 'ANYPICK_PLUGIN_HOST_SCRIPT'].filter((key) => process.env[key]), input }) + '\\n');",
        '    if (process.env.ANYPICK_LAUNCHER_STDERR) process.stderr.write(process.env.ANYPICK_LAUNCHER_STDERR);',
        '    process.exit(Number(process.env.ANYPICK_LAUNCHER_FIXTURE_EXIT || 0));',
        '  });',
        '}',
        '',
      ].join('\n'),
    );
    chmodSync(binary, 0o755);
  }
  return { temp, appDir, launcher: join(appDir, 'bin', 'anypick.js'), target, platformDir, binary };
}

function runFixture(launcher, args, env = {}, input) {
  return spawnSync(process.execPath, [launcher, ...args], {
    encoding: 'utf8',
    env: { ...process.env, ...env },
    input,
  });
}

async function waitForFile(path) {
  for (let attempt = 0; attempt < 200 && !existsSync(path); attempt += 1) {
    await delay(5);
  }
  assert.equal(existsSync(path), true, `fixture never created ${path}`);
}

function waitForExit(child) {
  return new Promise((resolveExit, rejectExit) => {
    child.once('error', rejectExit);
    child.once('close', (code, signal) => resolveExit({ code, signal }));
  });
}

async function checkPassthrough() {
  const fixture = makeFixture();
  try {
    if (process.platform !== 'win32') {
      // The TUI is Go now (go/internal/tui), so it must reach the platform
      // binary like every other command. There is no dist/cli.js in the
      // fixture, so this fails loudly if a Node dispatch path ever comes back.
      const tui = runFixture(fixture.launcher, ['tui', '--probe']);
      assert.equal(tui.status, 0, tui.stderr);
      const tuiOut = JSON.parse(tui.stdout);
      assert.equal(tuiOut.engine, 'native', `tui must run on the Go engine, got ${tui.stdout}`);
      assert.deepEqual(tuiOut.args, ['tui', '--probe']);
      assert.deepEqual(tuiOut.injectedPluginEnv, []);

      const native = runFixture(
        fixture.launcher,
        ['--json', 'value with spaces', '--literal=$TOKEN'],
        {
          ANYPICK_LAUNCHER_FIXTURE_EXIT: '23',
          ANYPICK_LAUNCHER_PASSTHROUGH: 'env value with spaces',
          ANYPICK_LAUNCHER_STDERR: 'native stderr\n',
        },
        'native stdin\n',
      );
      assert.equal(native.status, 23, native.stderr);
      assert.equal(native.stderr, 'native stderr\n');
      // The launcher injects no runtime environment for the Go engine: no
      // plugin host, no bundled plugin directory (ADR-0020 §4).
      assert.deepEqual(JSON.parse(native.stdout), {
        engine: 'native',
        args: ['--json', 'value with spaces', '--literal=$TOKEN'],
        env: 'env value with spaces',
        injectedPluginEnv: [],
        input: 'native stdin\n',
      });

      const readyFile = join(fixture.temp, 'ready');
      const signalFile = join(fixture.temp, 'signal');
      const waiting = spawn(process.execPath, [fixture.launcher, '--wait'], {
        stdio: 'ignore',
        env: {
          ...process.env,
          ANYPICK_LAUNCHER_READY_FILE: readyFile,
          ANYPICK_LAUNCHER_SIGNAL_FILE: signalFile,
        },
      });
      await waitForFile(readyFile);
      waiting.kill('SIGTERM');
      const exit = await waitForExit(waiting);
      assert.deepEqual(exit, { code: 42, signal: null });
      assert.equal(readFileSync(signalFile, 'utf8'), 'SIGTERM');

      const defaultReadyFile = join(fixture.temp, 'ready-default');
      const defaultSignal = spawn(process.execPath, [fixture.launcher, '--wait-default'], {
        stdio: 'ignore',
        env: { ...process.env, ANYPICK_LAUNCHER_READY_FILE: defaultReadyFile },
      });
      await waitForFile(defaultReadyFile);
      defaultSignal.kill('SIGTERM');
      assert.deepEqual(await waitForExit(defaultSignal), { code: null, signal: 'SIGTERM' });

      chmodSync(fixture.binary, 0o644);
      const notExecutable = runFixture(fixture.launcher, ['--version']);
      assert.equal(notExecutable.status, 1);
      assert.match(notExecutable.stderr, /does not contain an executable binary/);
      chmodSync(fixture.binary, 0o755);
    }

    rmSync(fixture.platformDir, { recursive: true, force: true });
    const missing = runFixture(fixture.launcher, ['--version']);
    assert.equal(missing.status, 1);
    assert.equal(missing.stdout, '');
    assert.match(missing.stderr, new RegExp(fixture.target.packageName.replace('/', '\\/')));
    assert.match(missing.stderr, /without omitting optional dependencies/);

    assert.throws(
      () => resolveNativeBinary(fixture.target, () => join(fixture.platformDir, 'package.json')),
      /manifest is unreadable/,
    );

    mkdirSync(fixture.platformDir, { recursive: true });
    writeFileSync(
      join(fixture.platformDir, 'package.json'),
      JSON.stringify({ name: fixture.target.packageName, version: '0.0.0' }),
    );
    const mismatched = runFixture(fixture.launcher, ['--version']);
    assert.equal(mismatched.status, 1);
    assert.equal(mismatched.stdout, '');
    assert.match(mismatched.stderr, /mismatched name or version/);
  } finally {
    rmSync(fixture.temp, { recursive: true, force: true });
  }
}

checkContracts();
checkPackedManifest();
await checkPassthrough();
console.log('npm launcher tests PASSED');
