#!/usr/bin/env node

import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

const root = resolve(import.meta.dirname, '..');
const goRoot = join(root, 'go');
const versionPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const versionVariable = 'github.com/outof0/anypick/go/internal/buildinfo.Version';

const targets = [
  {
    id: 'darwin-arm64',
    packageDir: 'cli-darwin-arm64',
    packageName: '@anypick/cli-darwin-arm64',
    goos: 'darwin',
    goarch: 'arm64',
    output: 'bin/anypick',
    format: 'mach-o',
    machine: 0x0100000c,
  },
  {
    id: 'darwin-x64',
    packageDir: 'cli-darwin-x64',
    packageName: '@anypick/cli-darwin-x64',
    goos: 'darwin',
    goarch: 'amd64',
    output: 'bin/anypick',
    format: 'mach-o',
    machine: 0x01000007,
  },
  {
    id: 'linux-arm64-gnu',
    packageDir: 'cli-linux-arm64-gnu',
    packageName: '@anypick/cli-linux-arm64-gnu',
    goos: 'linux',
    goarch: 'arm64',
    output: 'bin/anypick',
    format: 'elf',
    machine: 183,
  },
  {
    id: 'linux-x64-gnu',
    packageDir: 'cli-linux-x64-gnu',
    packageName: '@anypick/cli-linux-x64-gnu',
    goos: 'linux',
    goarch: 'amd64',
    output: 'bin/anypick',
    format: 'elf',
    machine: 62,
  },
  {
    id: 'win32-x64',
    packageDir: 'cli-win32-x64',
    packageName: '@anypick/cli-win32-x64',
    goos: 'windows',
    goarch: 'amd64',
    output: 'bin/anypick.exe',
    format: 'pe',
    machine: 0x8664,
  },
];

function fail(message) {
  throw new Error(`npm platform build failed: ${message}`);
}

function readJson(path) {
  return JSON.parse(readFileSync(path, 'utf8'));
}

function selectedTargets(args) {
  if (args[0] === '--') args = args.slice(1);
  if (args.length === 0) return targets;

  const selected = [];
  for (let index = 0; index < args.length; index += 1) {
    if (args[index] !== '--target' || !args[index + 1]) {
      fail(`expected repeated "--target <id>" arguments; received ${JSON.stringify(args)}`);
    }
    const id = args[index + 1];
    const target = targets.find((candidate) => candidate.id === id);
    if (!target) {
      fail(
        `unknown target ${JSON.stringify(id)}; expected ${targets.map((item) => item.id).join(', ')}`,
      );
    }
    if (!selected.includes(target)) selected.push(target);
    index += 1;
  }
  return selected;
}

function validateManifest(target, version) {
  const path = join(root, 'npm', target.packageDir, 'package.json');
  const manifest = readJson(path);
  const expectedFile = target.output;
  const expectedOs = target.goos === 'windows' ? 'win32' : target.goos;
  const expectedCpu = target.goarch === 'amd64' ? 'x64' : target.goarch;

  if (manifest.name !== target.packageName || manifest.version !== version) {
    fail(`${path} must identify ${target.packageName}@${version}`);
  }
  if (JSON.stringify(manifest.os) !== JSON.stringify([expectedOs])) {
    fail(`${path} must declare os ${JSON.stringify([expectedOs])}`);
  }
  if (JSON.stringify(manifest.cpu) !== JSON.stringify([expectedCpu])) {
    fail(`${path} must declare cpu ${JSON.stringify([expectedCpu])}`);
  }
  const expectedLibc = target.goos === 'linux' ? ['glibc'] : undefined;
  if (JSON.stringify(manifest.libc) !== JSON.stringify(expectedLibc)) {
    fail(`${path} must declare libc ${JSON.stringify(expectedLibc)}`);
  }
  if (JSON.stringify(manifest.files) !== JSON.stringify([expectedFile])) {
    fail(`${path} must package only ${expectedFile}`);
  }
}

function executableMachine(data, format) {
  if (format === 'mach-o') {
    if (data.length < 8 || !data.subarray(0, 4).equals(Buffer.from([0xcf, 0xfa, 0xed, 0xfe]))) {
      return undefined;
    }
    return data.readUInt32LE(4);
  }
  if (format === 'elf') {
    if (
      data.length < 20 ||
      !data.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46])) ||
      data[4] !== 2 ||
      data[5] !== 1
    ) {
      return undefined;
    }
    return data.readUInt16LE(18);
  }
  if (format === 'pe') {
    if (data.length < 64 || !data.subarray(0, 2).equals(Buffer.from([0x4d, 0x5a]))) {
      return undefined;
    }
    const peOffset = data.readUInt32LE(0x3c);
    if (
      peOffset + 6 > data.length ||
      !data.subarray(peOffset, peOffset + 4).equals(Buffer.from([0x50, 0x45, 0, 0]))
    ) {
      return undefined;
    }
    return data.readUInt16LE(peOffset + 4);
  }
  return undefined;
}

function build(target, version) {
  validateManifest(target, version);
  const output = join(root, 'npm', target.packageDir, ...target.output.split('/'));
  const temp = mkdtempSync(join(tmpdir(), `anypick-npm-${target.id}-`));
  const built = join(temp, target.goos === 'windows' ? 'anypick.exe' : 'anypick');

  try {
    const ldflags = `-s -w -buildid= -X ${versionVariable}=${version}`;
    const result = spawnSync(
      'go',
      [
        'build',
        '-mod=readonly',
        '-trimpath',
        '-buildvcs=false',
        '-ldflags',
        ldflags,
        '-o',
        built,
        './cmd/anypick',
      ],
      {
        cwd: goRoot,
        stdio: ['ignore', 'inherit', 'inherit'],
        env: {
          ...process.env,
          CGO_ENABLED: '0',
          GOARCH: target.goarch,
          GOARM64: 'v8.0',
          GOAMD64: 'v1',
          GOEXPERIMENT: '',
          GOFLAGS: '',
          GOOS: target.goos,
          GOTOOLCHAIN: 'auto',
          GOWORK: 'off',
        },
      },
    );
    if (result.error) fail(`could not start Go for ${target.id}: ${result.error.message}`);
    if (result.status !== 0) {
      fail(`Go exited ${result.status ?? 'without a status'} for ${target.id}`);
    }

    if (target.goos !== 'windows') chmodSync(built, 0o755);
    const data = readFileSync(built);
    const info = statSync(built);
    if (
      !info.isFile() ||
      info.size === 0 ||
      executableMachine(data, target.format) !== target.machine
    ) {
      fail(`${built} is not a non-empty ${target.id} executable`);
    }
    if (target.goos !== 'windows' && (info.mode & 0o111) === 0) {
      fail(`${built} is not executable`);
    }

    mkdirSync(dirname(output), { recursive: true });
    rmSync(output, { force: true });
    renameSync(built, output);
    const digest = createHash('sha256').update(data).digest('hex');
    process.stdout.write(`${target.id}\t${info.size}\tsha256:${digest}\t${output}\n`);
  } finally {
    rmSync(temp, { recursive: true, force: true });
  }
}

const rootManifest = readJson(join(root, 'package.json'));
if (!versionPattern.test(rootManifest.version ?? '')) {
  fail(
    `package.json must use a stable x.y.z version; received ${JSON.stringify(rootManifest.version)}`,
  );
}

for (const target of selectedTargets(process.argv.slice(2))) build(target, rootManifest.version);
