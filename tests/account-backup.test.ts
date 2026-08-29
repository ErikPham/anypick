import { afterEach, describe, expect, it } from 'vitest';
import { mkdtemp, readFile, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { exportSelectedAccountBackup } from '../src/core/account-backup';
import { buildAccountEnvelope } from '../src/core/account-transfer';
import { createTestEnv } from './helpers';

const cleanupRoots: string[] = [];

afterEach(async () => {
  await Promise.all(
    cleanupRoots.splice(0).map((root) => rm(root, { recursive: true, force: true })),
  );
});

describe('saved account backup package', () => {
  it('exports the selected accounts to one deterministic owner-only package', async () => {
    const env = await createTestEnv(['alpha', 'beta']);
    const parent = await mkdtemp(join(tmpdir(), 'anypick-backup-test-'));
    cleanupRoots.push(env.root, parent);

    await env.fakes.alpha.setLive({ email: 'alpha@example.test', token: 'alpha-secret' });
    await env.service.save('alpha', 'work');
    await env.fakes.beta.setLive({ email: 'beta@example.test', token: 'beta-secret' });
    await env.service.save('beta', 'personal');
    const destination = join(parent, 'selected.anypick-backup');

    const result = await env.service.exportAccountBackup(
      [
        { provider: 'beta', name: 'personal' },
        { provider: 'alpha', name: 'work' },
      ],
      destination,
    );

    expect(result).toMatchObject({ file: destination, accountCount: 2 });
    expect((await stat(destination)).mode & 0o777).toBe(0o600);
    expect(await readdir(parent)).toEqual(['selected.anypick-backup']);
    const pkg = JSON.parse(await readFile(destination, 'utf8')) as Record<string, unknown>;
    expect(pkg).toMatchObject({
      version: 1,
      kind: 'anypick-backup',
      accounts: [
        {
          provider: 'alpha',
          name: 'work',
          envelope: { version: 1, kind: 'anypick-account' },
        },
        {
          provider: 'beta',
          name: 'personal',
          envelope: { version: 1, kind: 'anypick-account' },
        },
      ],
    });
  });

  it('preserves an existing destination unless overwrite is explicitly enabled', async () => {
    const env = await createTestEnv(['fake']);
    const parent = await mkdtemp(join(tmpdir(), 'anypick-backup-overwrite-'));
    cleanupRoots.push(env.root, parent);

    await env.fakes.fake.setLive({ email: 'work@example.test', token: 'secret' });
    await env.service.save('fake', 'work');
    const destination = join(parent, 'selected.anypick-backup');
    await writeFile(destination, 'existing bytes', { mode: 0o644 });

    await expect(
      env.service.exportAccountBackup([{ provider: 'fake', name: 'work' }], destination),
    ).rejects.toMatchObject({ code: 'BACKUP_DESTINATION_EXISTS', mutated: false });
    expect(await readFile(destination, 'utf8')).toBe('existing bytes');
    expect(await readdir(parent)).toEqual(['selected.anypick-backup']);

    await env.service.exportAccountBackup([{ provider: 'fake', name: 'work' }], destination, {
      overwrite: true,
    });
    const pkg = JSON.parse(await readFile(destination, 'utf8')) as Record<string, unknown>;
    expect(pkg).toMatchObject({ version: 1, kind: 'anypick-backup' });
    expect((await stat(destination)).mode & 0o777).toBe(0o600);
    expect(await readdir(parent)).toEqual(['selected.anypick-backup']);
  });

  it('rejects empty and duplicate selections before creating a file', async () => {
    const env = await createTestEnv(['fake']);
    const parent = await mkdtemp(join(tmpdir(), 'anypick-backup-empty-'));
    cleanupRoots.push(env.root, parent);

    await expect(
      env.service.exportAccountBackup([], join(parent, 'empty.anypick-backup')),
    ).rejects.toMatchObject({
      code: 'BACKUP_EMPTY',
      mutated: false,
    });
    await expect(
      env.service.exportAccountBackup(
        [
          { provider: 'fake', name: 'same' },
          { provider: 'fake', name: 'same' },
        ],
        join(parent, 'duplicate.anypick-backup'),
      ),
    ).rejects.toMatchObject({ code: 'BACKUP_DUPLICATE', mutated: false });
    expect(await readdir(parent)).toEqual([]);
  });

  it('removes same-directory staging state when envelope construction fails', async () => {
    const env = await createTestEnv(['fake']);
    const parent = await mkdtemp(join(tmpdir(), 'anypick-backup-failure-'));
    cleanupRoots.push(env.root, parent);

    await env.fakes.fake.setLive({ email: 'first@example.test', token: 'first' });
    await env.service.save('fake', 'first');
    await env.fakes.fake.setLive({ email: 'second@example.test', token: 'second' });
    await env.service.save('fake', 'second');

    await expect(
      exportSelectedAccountBackup(
        env.store,
        [
          { provider: 'fake', name: 'first' },
          { provider: 'fake', name: 'second' },
        ],
        join(parent, 'failed.anypick-backup'),
        {},
        {
          buildEnvelope: async (store, provider, name) => {
            if (name === 'second') {
              throw new Error('simulated export failure');
            }
            return buildAccountEnvelope(store, provider, name);
          },
        },
      ),
    ).rejects.toThrow('simulated export failure');
    expect(await readdir(parent)).toEqual([]);
  });
});
