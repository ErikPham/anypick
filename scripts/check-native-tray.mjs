import { mkdtempSync, rmSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';

// The production app compiles this Swift source lazily at startup. Compile it
// during a macOS build as well, so a broken copied asset cannot reach users.
if (process.platform === 'darwin') {
  const stage = mkdtempSync(join(tmpdir(), 'anypick-macos-build-'));
  try {
    const macosDir = resolve('dist/macos');
    const names = readFileSync(join(macosDir, 'sources.txt'), 'utf8')
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && line.endsWith('.swift'));
    if (names.length === 0) {
      throw new Error('No Swift sources found under dist/macos.');
    }
    const sources = names.map((name) => join(macosDir, name));
    const result = spawnSync(
      '/usr/bin/xcrun',
      [
        'swiftc',
        '-parse-as-library',
        '-D',
        'DEBUG',
        ...sources,
        '-o',
        join(stage, 'anypick-macos-build-check'),
        '-framework',
        'AppKit',
        '-framework',
        'SwiftUI',
      ],
      { stdio: 'inherit' },
    );
    if (result.status !== 0) {
      throw new Error('macOS app compilation failed.');
    }
  } finally {
    rmSync(stage, { recursive: true, force: true });
  }
}
