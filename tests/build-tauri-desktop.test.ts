import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  buildTauriArgs,
  executableMetadata,
  hostTarget,
  parseBuildArgs,
  stageBundleArtifacts,
  validateTarget,
} from '../scripts/build-tauri-desktop.mjs';

const cleanupPaths: string[] = [];

afterEach(() => {
  for (const path of cleanupPaths.splice(0)) {
    rmSync(path, { force: true, recursive: true });
  }
});

describe('Tauri desktop build arguments', () => {
  it('uses the local host and unsigned configured bundles by default', () => {
    expect(parseBuildArgs([])).toEqual({
      bundles: undefined,
      noBundle: false,
      target: undefined,
    });
    expect(buildTauriArgs(parseBuildArgs([]))).toEqual(['build', '--ci', '--no-sign']);
  });

  it('supports cross-target bundle selection and no-bundle builds', () => {
    const bundled = parseBuildArgs(['--target=x86_64-pc-windows-msvc', '--bundles', 'msi,nsis']);
    expect(buildTauriArgs(bundled)).toEqual([
      'build',
      '--ci',
      '--target',
      'x86_64-pc-windows-msvc',
      '--bundles',
      'msi,nsis',
      '--no-sign',
    ]);
    expect(buildTauriArgs(parseBuildArgs(['--no-bundle']))).toEqual([
      'build',
      '--ci',
      '--no-bundle',
    ]);
  });

  it('rejects ambiguous or path-like targets', () => {
    expect(() => parseBuildArgs(['--bundles', 'app', '--no-bundle'])).toThrow(
      /cannot be used together/,
    );
    expect(() => validateTarget('../../windows')).toThrow(/Unsupported desktop target/);
    expect(() => parseBuildArgs(['--wat'])).toThrow(/Unknown option/);
  });

  it('reads and validates the rustc host triple', () => {
    expect(hostTarget('rustc 1.90.0\nhost: aarch64-apple-darwin\n')).toBe('aarch64-apple-darwin');
    expect(() => hostTarget('rustc 1.90.0\n')).toThrow(/did not report a host target/);
  });
});

describe('Tauri desktop executable validation', () => {
  it('recognizes exact ELF, PE, and Mach-O architectures', () => {
    expect(executableMetadata(elf(62), 'x86_64-unknown-linux-gnu')).toEqual({
      architecture: 'x64',
      format: 'ELF',
    });
    expect(executableMetadata(pe(0xaa64), 'aarch64-pc-windows-msvc')).toEqual({
      architecture: 'arm64',
      format: 'PE',
    });
    expect(executableMetadata(machO(0x0100000c), 'aarch64-apple-darwin')).toEqual({
      architecture: 'arm64',
      format: 'Mach-O',
    });
  });

  it('rejects an executable compiled for another architecture', () => {
    expect(() => executableMetadata(elf(62), 'aarch64-unknown-linux-gnu')).toThrow(
      /architecture is x64; expected arm64/,
    );
  });
});

describe('unsigned bundle staging', () => {
  it('copies packages and writes a stable, sorted SHA-256 manifest', () => {
    const fixture = trackedTemporaryDirectory('anypick-tauri-bundle-');
    const bundleRoot = join(fixture, 'bundle');
    writeFixture(join(bundleRoot, 'deb', 'AnyPick_1.0.0_amd64.deb'), 'debian-package');
    writeFixture(join(bundleRoot, 'app', 'AnyPick.app', 'Contents', 'MacOS', 'AnyPick'), 'app');

    const nativeRoot = join(import.meta.dirname, '..', 'dist', 'native');
    mkdirSync(nativeRoot, { recursive: true });
    const destination = trackedTemporaryDirectory('tauri-stage-', nativeRoot);
    rmSync(destination, { recursive: true });
    const input = {
      bundleRoot,
      destination,
      target: 'x86_64-unknown-linux-gnu',
    };

    const first = stageBundleArtifacts(input);
    const firstManifest = readFileSync(first.manifestPath, 'utf8');
    const second = stageBundleArtifacts(input);
    const secondManifest = readFileSync(second.manifestPath, 'utf8');
    const parsed = JSON.parse(secondManifest) as {
      algorithm: string;
      artifacts: Array<{ path: string; sha256: string; type: string }>;
      note: string;
      signed: boolean;
      target: string;
    };

    expect(secondManifest).toBe(firstManifest);
    expect(parsed).toMatchObject({
      algorithm: 'SHA-256',
      signed: false,
      target: 'x86_64-unknown-linux-gnu',
    });
    expect(parsed.note).toContain('UNSIGNED');
    expect(parsed.artifacts.map((artifact) => artifact.path)).toEqual([
      'app/AnyPick.app/Contents/MacOS/AnyPick',
      'deb/AnyPick_1.0.0_amd64.deb',
    ]);
    expect(parsed.artifacts.every((artifact) => /^[a-f0-9]{64}$/.test(artifact.sha256))).toBe(true);
    expect(parsed.artifacts.every((artifact) => artifact.type === 'file')).toBe(true);
  });

  it('does not replace an existing destination when Tauri produced no bundle', () => {
    const fixture = trackedTemporaryDirectory('anypick-tauri-empty-');
    const nativeRoot = join(import.meta.dirname, '..', 'dist', 'native');
    mkdirSync(nativeRoot, { recursive: true });
    const destination = trackedTemporaryDirectory('tauri-stage-', nativeRoot);
    writeFixture(join(destination, 'keep.txt'), 'keep');

    expect(() =>
      stageBundleArtifacts({
        bundleRoot: join(fixture, 'missing'),
        destination,
        target: 'x86_64-unknown-linux-gnu',
      }),
    ).toThrow(/produced no package artifacts/);
    expect(readFileSync(join(destination, 'keep.txt'), 'utf8')).toBe('keep');
  });
});

function elf(machine: number): Buffer {
  const buffer = Buffer.alloc(64);
  buffer.set([0x7f, 0x45, 0x4c, 0x46]);
  buffer[4] = 2;
  buffer[5] = 1;
  buffer.writeUInt16LE(machine, 18);
  return buffer;
}

function pe(machine: number): Buffer {
  const buffer = Buffer.alloc(128);
  buffer.write('MZ', 0, 'ascii');
  buffer.writeUInt32LE(64, 0x3c);
  buffer.write('PE\0\0', 64, 'ascii');
  buffer.writeUInt16LE(machine, 68);
  return buffer;
}

function machO(cpuType: number): Buffer {
  const buffer = Buffer.alloc(32);
  buffer.writeUInt32LE(0xfeedfacf, 0);
  buffer.writeUInt32LE(cpuType, 4);
  return buffer;
}

function writeFixture(path: string, contents: string): void {
  mkdirSync(dirname(path), { recursive: true });
  writeFileSync(path, contents);
}

function trackedTemporaryDirectory(prefix: string, parent = tmpdir()): string {
  const path: string = mkdtempSync(join(parent, prefix));
  cleanupPaths.push(path);
  return path;
}
