/**
 * Regenerates the cross-runtime multi-account backup fixtures used by the
 * Go↔TypeScript drift tests (native cutover Milestone 1 / go PARITY M0):
 *
 *   go/testdata/parity/multi-account-backup.json        — TypeScript-authored
 *   tests/parity/multi-account-backup-go.json           — Go-authored
 *
 * The TypeScript half is written directly by this script. The Go half is
 * produced by `go test ./internal/transfer -run TestParityFixtureGeneration`
 * with ANYPICK_GEN_PARITY_FIXTURES=1 (the flag keeps ordinary test runs
 * read-only); this script shells out to it.
 *
 * Both fixtures use identical deterministic account data so either runtime can
 * restore either package and assert exact envelope contents.
 *
 * Usage: npx tsx scripts/gen-parity-fixtures.ts
 */

import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');

/** Deterministic live codex credentials; both runtimes embed these verbatim. */
function codexAuth(variant) {
  const b64url = (o) => Buffer.from(JSON.stringify(o)).toString('base64url');
  const header = b64url({ alg: 'none', typ: 'JWT' });
  const payload = b64url({
    email: `${variant}@example.test`,
    sub: `auth0|parity-${variant}`,
  });
  return {
    auth_mode: 'chatgpt',
    tokens: {
      id_token: `${header}.${payload}.sig`,
      access_token: `parity-access-${variant}`,
      account_id: `parity-acct-${variant}`,
    },
  };
}

const home = await mkdtemp(join(tmpdir(), 'anypick-parity-gen-'));
process.env.HOME = home;
process.env.ANYPICK_HOME = join(home, '.anypick');
process.env.CODEX_HOME = join(home, '.codex');
// Isolate from any developer machine state.
delete process.env.CODEX_AUTH_PATH;

const { createAppReady } = await import('../src/core/app');

const codexDir = join(home, '.codex');
await mkdir(codexDir, { recursive: true });

const app = await createAppReady({ root: process.env.ANYPICK_HOME });
try {
  const destination = join(repoRoot, 'go', 'testdata', 'parity', 'multi-account-backup.json');
  await mkdir(dirname(destination), { recursive: true });

  for (const variant of ['work', 'personal']) {
    await writeFile(join(codexDir, 'auth.json'), JSON.stringify(codexAuth(variant)), {
      mode: 0o600,
    });
    await app.accounts.save('codex', variant);
  }
  // Deliberately unsorted input: export must normalize ordering deterministically.
  const result = await app.accounts.exportAccountBackup(
    [
      { provider: 'codex', name: 'personal' },
      { provider: 'codex', name: 'work' },
    ],
    destination,
    { overwrite: true },
  );
  if (result.accountCount !== 2) {
    throw new Error(`expected 2 accounts, got ${result.accountCount}`);
  }
  console.log(`TS-authored fixture: ${destination}`);

  // Go-authored fixture via the generation-only test path.
  console.log(
    execFileSync(
      'go',
      [
        'test',
        './internal/transfer',
        '-run',
        'TestParityBackupFixtureGeneration',
        '-count=1',
        '-v',
      ],
      {
        cwd: join(repoRoot, 'go'),
        env: { ...process.env, ANYPICK_GEN_PARITY_FIXTURES: '1' },
        encoding: 'utf8',
        stdio: ['ignore', 'pipe', 'inherit'],
      },
    )
      .split('\n')
      .filter((l) => l.startsWith('fixture:'))
      .join('\n'),
  );
} finally {
  await app.close();
}
console.log('done');
