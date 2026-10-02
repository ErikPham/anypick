// Emit a desktop snapshot frame on stdout, in the exact wire form the Go
// service sends: `snapshot\t<base64 of UTF-8 JSON>`.
//
// The Tauri app's fixture mode reads this from stdin (ANYPICK_TRAY_DEMO), so
// the same snapshot the unit tests render can be pushed into the real window
// without a Go service and without touching ~/.anypick.
//
// It reuses test/fixture.ts rather than restating the data, so a preview can
// never drift from what the tests assert on.

import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = fileURLToPath(new URL('.', import.meta.url));
const workdir = mkdtempSync(join(tmpdir(), 'anypick-wire-'));
const entry = join(workdir, 'emit.ts');
const shim = join(workdir, 'shim.tsx');

writeFileSync(
  shim,
  `export { snapshotFixture } from ${JSON.stringify(join(here, 'test/fixture.ts'))};\n`,
);
writeFileSync(
  entry,
  `
import { snapshotFixture } from './shim.js';
import { encodeBase64Utf8 } from ${JSON.stringify(join(here, 'lib/wire.ts'))};
const snapshot = snapshotFixture();
process.stdout.write('snapshot\\t' + encodeBase64Utf8(JSON.stringify(snapshot)) + '\\n');
`,
);

try {
  const out = execFileSync(
    process.execPath,
    [
      join(here, 'node_modules/vite/dist/node/cli.js'),
      'node',
      '--config',
      join(here, 'vitest.config.ts'),
      entry,
    ],
    { encoding: 'utf8', cwd: here, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  process.stdout.write(out);
} finally {
  rmSync(workdir, { recursive: true, force: true });
}
