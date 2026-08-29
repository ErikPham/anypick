/**
 * One runtime half of the live parity drift gate (see parity-drift-gate.ts).
 * Shares the deterministic parity credentials with gen-parity-fixtures.ts and
 * the Go tests' parityAuthJSON.
 *
 * Subcommands:
 *   export-ts <outPath>  — CURRENT TypeScript producer writes a backup
 *   import-go <inPath>   — TypeScript restores a CURRENT Go-produced backup
 */

import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { AnyPickApp } from '../src/testing';

const [command, targetPath] = process.argv.slice(2);
if (!command || !targetPath) {
  console.error('usage: tsx scripts/parity-live.ts <export-ts|import-go> <path>');
  process.exit(2);
}

const home = await mkdtemp(join(tmpdir(), 'anypick-parity-live-'));
process.env.HOME = home;
process.env.ANYPICK_HOME = join(home, '.anypick');
process.env.CODEX_HOME = join(home, '.codex');
delete process.env.CODEX_AUTH_PATH;

const { createAppReady } = await import('../src/core/app');

/** Opens one app lifecycle over an isolated root and closes it afterwards. */
async function withApp(fn: (app: AnyPickApp) => Promise<void>): Promise<void> {
  const app = await createAppReady({ root: process.env.ANYPICK_HOME });
  try {
    await fn(app);
  } finally {
    app.close();
  }
}

/** Deterministic live codex credentials shared across both runtimes. */
function codexAuth(variant: 'work' | 'personal'): string {
  const b64url = (o: object) => Buffer.from(JSON.stringify(o)).toString('base64url');
  const header = b64url({ alg: 'none', typ: 'JWT' });
  const payload = b64url({
    email: `${variant}@example.test`,
    sub: `auth0|parity-${variant}`,
  });
  return JSON.stringify({
    auth_mode: 'chatgpt',
    tokens: {
      id_token: `${header}.${payload}.sig`,
      access_token: `parity-access-${variant}`,
      account_id: `parity-acct-${variant}`,
    },
  });
}

if (command === 'export-ts') {
  // Fresh isolated root so the package is self-contained.
  process.env.HOME = await mkdtemp(join(tmpdir(), 'anypick-parity-live-'));
  process.env.ANYPICK_HOME = join(process.env.HOME, '.anypick');
  process.env.CODEX_HOME = join(process.env.HOME, '.codex');
  await mkdir(join(process.env.CODEX_HOME), { recursive: true });

  await withApp(async (app) => {
    for (const variant of ['work', 'personal'] as const) {
      await writeFile(join(process.env.CODEX_HOME!, 'auth.json'), codexAuth(variant), {
        mode: 0o600,
      });
      await app.accounts.save('codex', variant);
    }
    const result = await app.accounts.exportAccountBackup(
      [
        { provider: 'codex', name: 'personal' },
        { provider: 'codex', name: 'work' },
      ],
      targetPath,
      { overwrite: true },
    );
    if (result.accountCount !== 2)
      throw new Error(`expected 2 accounts, got ${result.accountCount}`);
  });
  console.log(`TS producer wrote ${targetPath}`);
} else if (command === 'import-go') {
  await withApp(async (app) => {
    const metas = await app.accounts.restoreAccountBackup(targetPath, [
      { provider: 'codex', name: 'personal' },
      { provider: 'codex', name: 'work' },
    ]);
    if (metas.length !== 2) throw new Error(`restored ${metas.length}, want 2`);
    for (const variant of ['work', 'personal'] as const) {
      const account = await app.accounts.get('codex', variant);
      if (!account?.snapshotDir) throw new Error(`account ${variant} missing after restore`);
      const snapshot = JSON.parse(await readFile(join(account.snapshotDir, 'auth.json'), 'utf8'));
      const want = `parity-access-${variant}`;
      if (snapshot.tokens?.access_token !== want) {
        throw new Error(
          `PRODUCER DRIFT: ${variant} token ${snapshot.tokens?.access_token}, want ${want}`,
        );
      }
    }
  });
  console.log(`TS consumer verified the current Go export (${targetPath})`);
} else {
  console.error(`unknown subcommand: ${command}`);
  process.exit(2);
}
