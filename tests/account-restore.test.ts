import { afterEach, describe, expect, it } from 'vitest';
import { mkdtemp, readFile, rm, truncate, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { MAX_BACKUP_ENCODED_BYTES } from '../src/core/account-backup';
import { createTestEnv } from './helpers';

const cleanupRoots: string[] = [];

afterEach(async () => {
  await Promise.all(
    cleanupRoots.splice(0).map((root) => rm(root, { recursive: true, force: true })),
  );
});

describe('account backup restore', () => {
  it('inspects without secrets and restores a selected multi-provider round trip', async () => {
    const source = await createTestEnv(['alpha', 'beta'], { supportsProxy: true });
    const target = await createTestEnv(['alpha', 'beta'], { supportsProxy: true });
    const files = await mkdtemp(join(tmpdir(), 'anypick-restore-roundtrip-'));
    cleanupRoots.push(source.root, target.root, files);

    await source.fakes.alpha.setLive({ email: 'alpha@example.test', token: 'alpha-secret' });
    await source.service.save('alpha', 'work');
    await source.store.setProxyConfig('alpha', 'work', {
      enabled: true,
      port: 43123,
      options: { upstreamToken: 'must-not-cross' },
    });
    await source.fakes.beta.setLive({ email: 'beta@example.test', token: 'beta-secret' });
    await source.service.save('beta', 'personal');
    const path = join(files, 'roundtrip.anypick-backup');
    await source.service.exportAccountBackup(
      [
        { provider: 'alpha', name: 'work' },
        { provider: 'beta', name: 'personal' },
      ],
      path,
    );

    const preview = await target.service.inspectAccountBackup(path);
    expect(preview.entries).toEqual([
      {
        provider: 'alpha',
        name: 'work',
        label: 'work',
        disposition: 'create',
      },
      {
        provider: 'beta',
        name: 'personal',
        label: 'personal',
        disposition: 'create',
      },
    ]);
    expect(JSON.stringify(preview)).not.toContain('secret');
    expect(JSON.stringify(preview)).not.toContain('upstreamToken');

    await target.service.restoreAccountBackup(path, [{ provider: 'alpha', name: 'work' }]);
    const restored = await target.store.requireAccount('alpha', 'work');
    expect(JSON.parse(await readFile(join(restored.snapshotDir, 'auth.json'), 'utf8'))).toEqual({
      email: 'alpha@example.test',
      token: 'alpha-secret',
    });
    expect(await target.store.getAccount('beta', 'personal')).toBeNull();
    expect(await target.store.getProxyConfig('alpha', 'work')).toEqual({
      enabled: false,
      port: 43123,
    });
  });

  it('rejects a package larger than the bounded reader cap', async () => {
    const target = await createTestEnv(['fake']);
    const files = await mkdtemp(join(tmpdir(), 'anypick-restore-limit-'));
    cleanupRoots.push(target.root, files);
    const path = join(files, 'oversized.anypick-backup');
    await writeFile(path, '{}', { mode: 0o600 });
    await truncate(path, MAX_BACKUP_ENCODED_BYTES + 1);

    await expect(target.service.inspectAccountBackup(path)).rejects.toMatchObject({
      code: 'BACKUP_LIMIT',
      mutated: false,
    });
  });

  it('rejects a malformed unselected envelope before any mutation', async () => {
    const source = await createTestEnv(['fake']);
    const target = await createTestEnv(['fake']);
    const files = await mkdtemp(join(tmpdir(), 'anypick-restore-malformed-'));
    cleanupRoots.push(source.root, target.root, files);

    for (const name of ['first', 'second']) {
      await source.fakes.fake.setLive({ email: `${name}@example.test`, token: name });
      await source.service.save('fake', name);
    }
    const path = join(files, 'malformed.anypick-backup');
    await source.service.exportAccountBackup(
      [
        { provider: 'fake', name: 'first' },
        { provider: 'fake', name: 'second' },
      ],
      path,
    );
    const pkg = JSON.parse(await readFile(path, 'utf8')) as {
      accounts: Array<{ envelope: Record<string, unknown> }>;
    };
    pkg.accounts[1].envelope.unexpected = true;
    await writeFile(path, JSON.stringify(pkg), { mode: 0o600 });

    await expect(
      target.service.restoreAccountBackup(path, [{ provider: 'fake', name: 'first' }]),
    ).rejects.toMatchObject({ code: 'BACKUP_FORMAT', mutated: false });
    expect(await target.store.listAccounts('fake')).toEqual([]);
  });

  it('blocks a different-name identity collision without mutation', async () => {
    const source = await createTestEnv(['fake']);
    const target = await createTestEnv(['fake']);
    const files = await mkdtemp(join(tmpdir(), 'anypick-restore-collision-'));
    cleanupRoots.push(source.root, target.root, files);

    await source.fakes.fake.setLive({ email: 'same@example.test', token: 'incoming' });
    await source.service.save('fake', 'incoming');
    const path = join(files, 'collision.anypick-backup');
    await source.service.exportAccountBackup([{ provider: 'fake', name: 'incoming' }], path);

    await target.fakes.fake.setLive({ email: 'same@example.test', token: 'existing' });
    await target.service.save('fake', 'existing');
    expect((await target.service.inspectAccountBackup(path)).entries[0]).toMatchObject({
      disposition: 'blocked',
      reason: 'identity-conflict',
    });
    await expect(
      target.service.restoreAccountBackup(path, [{ provider: 'fake', name: 'incoming' }]),
    ).rejects.toMatchObject({ code: 'BACKUP_COLLISION', mutated: false });
    expect((await target.store.listAccounts('fake')).map((account) => account.meta.name)).toEqual([
      'existing',
    ]);
  });

  it('requires confirmation for exact replacement and preserves createdAt', async () => {
    const source = await createTestEnv(['fake']);
    const target = await createTestEnv(['fake']);
    const files = await mkdtemp(join(tmpdir(), 'anypick-restore-replace-'));
    cleanupRoots.push(source.root, target.root, files);

    await source.fakes.fake.setLive({ email: 'work@example.test', token: 'new-token' });
    await source.service.save('fake', 'work');
    const path = join(files, 'replace.anypick-backup');
    await source.service.exportAccountBackup([{ provider: 'fake', name: 'work' }], path);

    await target.fakes.fake.setLive({ email: 'work@example.test', token: 'old-token' });
    const existing = await target.service.save('fake', 'work');
    await target.store.setActive('fake', 'work');
    await expect(
      target.service.restoreAccountBackup(path, [{ provider: 'fake', name: 'work' }]),
    ).rejects.toMatchObject({ code: 'BACKUP_REPLACE_REQUIRED', mutated: false });

    await target.service.restoreAccountBackup(path, [{ provider: 'fake', name: 'work' }], {
      replace: [{ provider: 'fake', name: 'work' }],
    });
    const restored = await target.store.requireAccount('fake', 'work');
    expect(restored.meta.createdAt).toBe(existing.createdAt);
    expect(
      JSON.parse(await readFile(join(restored.snapshotDir, 'auth.json'), 'utf8')),
    ).toMatchObject({
      token: 'new-token',
    });
    expect(await target.store.getActive('fake')).toBe('work');
    expect(await target.fakes.fake.readLive()).toMatchObject({ token: 'old-token' });
  });

  it('rolls back all account writes when a later SQLite write fails', async () => {
    const source = await createTestEnv(['fake']);
    const target = await createTestEnv(['fake']);
    const files = await mkdtemp(join(tmpdir(), 'anypick-restore-rollback-'));
    cleanupRoots.push(source.root, target.root, files);

    for (const name of ['first', 'second']) {
      await source.fakes.fake.setLive({ email: `${name}@example.test`, token: `new-${name}` });
      await source.service.save('fake', name);
    }
    const path = join(files, 'rollback.anypick-backup');
    const refs = [
      { provider: 'fake', name: 'first' },
      { provider: 'fake', name: 'second' },
    ];
    await source.service.exportAccountBackup(refs, path);

    for (const name of ['first', 'second']) {
      await target.fakes.fake.setLive({ email: `${name}@example.test`, token: `old-${name}` });
      await target.service.save('fake', name);
    }
    target.store.db.exec(`
      CREATE TRIGGER fail_second_restore
      BEFORE UPDATE ON accounts
      WHEN NEW.provider = 'fake' AND NEW.name = 'second'
      BEGIN
        SELECT RAISE(ABORT, 'injected restore failure');
      END;
    `);

    await expect(
      target.service.restoreAccountBackup(path, refs, { replace: refs }),
    ).rejects.toThrow('injected restore failure');
    for (const name of ['first', 'second']) {
      const account = await target.store.requireAccount('fake', name);
      expect(
        JSON.parse(await readFile(join(account.snapshotDir, 'auth.json'), 'utf8')),
      ).toMatchObject({
        token: `old-${name}`,
      });
    }
  });
});
