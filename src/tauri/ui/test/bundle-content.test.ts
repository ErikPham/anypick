import { readFileSync, readdirSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/**
 * The shipped bundle must not carry the test fixture.
 *
 * resolveBridge used to return createBrowserDevBridge() when window.__TAURI__
 * was absent, and that module imports snapshotFixture from a test file. The
 * built bundle therefore contained fabricated accounts, so a window not hosted
 * by Tauri rendered invented data and looked like it worked. The fallback is
 * gone, but nothing failed if a future change re-imported test data into a
 * shipped module: jsdom never loads the built bundle, so no vitest case can see
 * it. This test reads the real artifact instead.
 *
 * It skips when frontend/ is absent, which is the case in a clean checkout
 * before `make build-tauri-ui`. That is deliberate rather than a silent pass:
 * the skip is reported, and `make check-tauri-ui` runs after the build in CI.
 */
const here = dirname(fileURLToPath(import.meta.url));
const assetsDir = join(here, '..', '..', 'frontend', 'assets');

const SENTINELS = ['Running in browser preview', 'alex@studio.example'];

describe('the built bundle carries no test fixture', () => {
  const built = existsSync(assetsDir);

  it.skipIf(!built)('has no fixture sentinel in any shipped asset', () => {
    const assets = readdirSync(assetsDir).filter((f) => f.endsWith('.js'));
    // A parser that matches no file reads exactly like a passing gate.
    expect(assets.length, 'no built .js asset found: the bundle scan proved nothing').toBeGreaterThan(0);
    for (const asset of assets) {
      const text = readFileSync(join(assetsDir, asset), 'utf8');
      for (const sentinel of SENTINELS) {
        expect(
          text.includes(sentinel),
          `${asset} contains the test fixture sentinel ${JSON.stringify(sentinel)}: a shipped module imported test data`,
        ).toBe(false);
      }
    }
  });
});
