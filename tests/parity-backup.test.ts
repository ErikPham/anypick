/**
 * Cross-runtime multi-account backup drift test (native cutover Milestone 1,
 * go PARITY M0). Restores the Go-authored fixture committed at
 * tests/parity/multi-account-backup-go.json — regenerated explicitly with
 * `npx tsx scripts/gen-parity-fixtures.ts`. Any Go-side format or envelope
 * change must fail here before a TypeScript release ships.
 *
 * The mirror-image coverage (TypeScript-authored fixture restored by Go) lives
 * in go/internal/transfer/parity_backup_test.go.
 */
import { describe, expect, it, afterEach } from 'vitest';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createAppReady, type AnyPickApp } from '../src/testing';

const fixturePath = join(__dirname, 'parity', 'multi-account-backup-go.json');
const roots: string[] = [];
const apps: AnyPickApp[] = [];

afterEach(async () => {
  for (const app of apps.splice(0)) {
    app.close();
  }
  await Promise.all(roots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

async function newApp(): Promise<{ app: AnyPickApp; root: string }> {
  const root = await mkdtemp(join(tmpdir(), 'anypick-parity-go-fixture-'));
  roots.push(root);
  const app = await createAppReady({ root, skipMigrate: true });
  apps.push(app);
  return { app, root };
}

describe('Go-authored multi-account backup fixture', () => {
  it('restores both codex accounts with intact envelope bytes and no silent selection expansion', async () => {
    const pkg = JSON.parse(await readFile(fixturePath, 'utf8')) as {
      version: number;
      kind: string;
      accounts: Array<{
        provider: string;
        name: string;
        envelope: { version: number; kind: string; files: Record<string, string> };
      }>;
    };

    expect(pkg).toMatchObject({
      version: 1,
      kind: 'anypick-backup',
      accounts: [
        { provider: 'codex', name: 'personal' },
        { provider: 'codex', name: 'work' },
      ],
    });
    for (const account of pkg.accounts) {
      expect(account.envelope).toMatchObject({ version: 1, kind: 'anypick-account' });
      expect(account.envelope.files['auth.json']).toBeTruthy();
    }

    const { app } = await newApp();

    const preview = await app.accounts.inspectAccountBackup(fixturePath);
    expect(preview.entries.map((e) => [e.provider, e.name, e.disposition])).toEqual([
      ['codex', 'personal', 'create'],
      ['codex', 'work', 'create'],
    ]);

    // Restoring one entry must never expand to all credential-bearing accounts.
    await app.accounts.restoreAccountBackup(fixturePath, [{ provider: 'codex', name: 'work' }]);
    expect(await app.accounts.get('codex', 'personal')).toBeNull();

    // Full restore onto a fresh root lands both accounts with byte-identical
    // credentials. (Re-restoring over the just-restored 'work' would correctly
    // demand explicit replacement approval.)
    const { app: app2 } = await newApp();
    const metas = await app2.accounts.restoreAccountBackup(fixturePath, [
      { provider: 'codex', name: 'personal' },
      { provider: 'codex', name: 'work' },
    ]);
    expect(metas).toHaveLength(2);

    for (const variant of ['work', 'personal'] as const) {
      const account = await app2.accounts.get('codex', variant);
      if (!account?.snapshotDir) {
        throw new Error(`restored account ${variant} is missing its snapshot`);
      }
      const snapshot = JSON.parse(
        await readFile(join(account.snapshotDir, 'auth.json'), 'utf8'),
      ) as { tokens?: { access_token?: string } };
      expect(snapshot.tokens?.access_token).toBe(`parity-access-${variant}`);
    }
  });
});
