import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { openForeignDatabase } from '../src/core/db';
import { OpenCodeProvider } from '../src/providers/opencode';
import { resolveOpenCodeCredentials } from '../src/providers/opencode-proxy/auth';

/**
 * The beta OpenCode CLI records logins in its sqlite `credential` table and
 * stopped rewriting auth.json. These tests pin the resolution order: adjacent
 * db keys win over their own auth.json, auto mode leads with the live install's
 * db (a saved mirror can hold a previous account's key), and explicit-api pools
 * stay isolated from the live login.
 */
function seedCredentialDb(dataDir: string, entries: Array<{ service: string; key: string }>) {
  const db = openForeignDatabase(join(dataDir, 'opencode.db'));
  try {
    db.exec(`CREATE TABLE IF NOT EXISTS credential (
      id text PRIMARY KEY,
      integration_id text,
      label text NOT NULL,
      value text NOT NULL,
      connector_id text,
      method_id text,
      active integer,
      time_created integer NOT NULL,
      time_updated integer NOT NULL
    )`);
    const insert = db.prepare(
      'INSERT INTO credential (id, integration_id, label, value, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?)',
    );
    for (const [index, entry] of entries.entries()) {
      insert.run(
        `cred_${index}`,
        entry.service,
        'default',
        JSON.stringify({ type: 'key', key: entry.key }),
        0,
        0,
      );
    }
  } finally {
    db.close();
  }
}

describe('OpenCode credential store resolution', () => {
  it('prefers a fresh db key over a stale auth.json key', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    seedCredentialDb(dataDir, [{ service: 'opencode-go', key: 'sk-db-key' }]);
    await writeFile(
      join(dataDir, 'auth.json'),
      JSON.stringify({ 'opencode-go': { type: 'api', key: 'sk-stale-key' } }),
    );

    const credentials = await resolveOpenCodeCredentials([join(dataDir, 'auth.json')], 'api');
    expect(credentials.map((credential) => credential.apiKey)).toEqual(['sk-db-key']);
  });

  it('falls back to auth.json when no db exists (snapshots, pre-beta installs)', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    await writeFile(
      join(dataDir, 'auth.json'),
      JSON.stringify({ 'opencode-go': { type: 'api', key: 'sk-file-key' } }),
    );

    const credentials = await resolveOpenCodeCredentials([join(dataDir, 'auth.json')], 'api');
    expect(credentials.map((credential) => credential.apiKey)).toEqual(['sk-file-key']);
  });

  it('ignores non-platform and oauth-shaped rows', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    seedCredentialDb(dataDir, [
      { service: 'deepseek', key: 'sk-other-provider' },
      { service: 'github-copilot', key: 'sk-also-other' },
    ]);
    const db = openForeignDatabase(join(dataDir, 'opencode.db'));
    try {
      db.prepare(
        'INSERT INTO credential (id, integration_id, label, value, time_created, time_updated) VALUES (\'oauth\', \'openai\', \'default\', \'{"type":"oauth","access":"a","refresh":"r","expires":1}\', 0, 0)',
      ).run();
    } finally {
      db.close();
    }
    await writeFile(
      join(dataDir, 'auth.json'),
      JSON.stringify({ deepseek: { type: 'api', key: 'sk-file-only' } }),
    );

    // Nothing platform-scoped anywhere → api mode has no credential to serve.
    const credentials = resolveOpenCodeCredentials([join(dataDir, 'auth.json')], 'api');
    await expect(credentials).rejects.toThrow('No OpenCode Zen/Go key');
  });

  it('auto mode leads with the live db key even when a snapshot holds a stale one', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    seedCredentialDb(dataDir, [{ service: 'opencode-go', key: 'sk-live-db-key' }]);
    const snapshotHome = await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-snap-'));
    await mkdir(join(snapshotHome, 'snapshot'), { recursive: true });
    await writeFile(
      join(snapshotHome, 'snapshot', 'auth.json'),
      JSON.stringify({
        'opencode-go': { type: 'api', key: 'sk-snapshot-stale-key' },
        deepseek: { type: 'api', key: 'sk-unrelated' },
      }),
    );

    process.env.OPENCODE_DATA_DIR = dataDir;
    try {
      const credentials = await resolveOpenCodeCredentials(
        [join(snapshotHome, 'snapshot', 'auth.json')],
        'auto',
      );
      // The login that exists right now wins; the saved mirror only failovers.
      expect(credentials.map((credential) => credential.apiKey)).toEqual([
        'sk-live-db-key',
        'sk-snapshot-stale-key',
      ]);
    } finally {
      delete process.env.OPENCODE_DATA_DIR;
    }
  });

  it('keeps explicit-api pools isolated from the live db', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    seedCredentialDb(dataDir, [{ service: 'opencode-go', key: 'sk-live-db-key' }]);
    const snapshotHome = await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-pool-'));
    await mkdir(join(snapshotHome, 'snapshot'), { recursive: true });
    await writeFile(
      join(snapshotHome, 'snapshot', 'auth.json'),
      JSON.stringify({ deepseek: { type: 'api', key: 'sk-pool-only' } }),
    );

    process.env.OPENCODE_DATA_DIR = dataDir;
    try {
      const credentials = resolveOpenCodeCredentials(
        [join(snapshotHome, 'snapshot', 'auth.json')],
        'api',
      );
      await expect(credentials).rejects.toThrow('No OpenCode Zen/Go key');
    } finally {
      delete process.env.OPENCODE_DATA_DIR;
    }
  });

  it('reads usage with the db key when auth.json is absent', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    seedCredentialDb(dataDir, [{ service: 'opencode-go', key: 'sk-db-usage' }]);
    let authorization: string | undefined;
    const provider = new OpenCodeProvider(join(dataDir, '..', '..', '..'), {
      fetchFn: (input, init) => {
        authorization = new Request(input, init).headers.get('authorization') ?? undefined;
        return Promise.resolve(
          Response.json({ usage: { rolling: { percent: 10 }, weekly: { percent: 20 } } }),
        );
      },
    });

    await expect(provider.liveUsage()).resolves.toEqual({
      windows: [
        { label: '5 hours', remainingPercent: 90 },
        { label: 'Weekly', remainingPercent: 80 },
      ],
    });
    expect(authorization).toBe('Bearer sk-db-usage');
  });

  it('reports live presence for a db-only login', async () => {
    const dataDir = join(
      await mkdtemp(join(tmpdir(), 'anypick-opencode-dbauth-')),
      '.local',
      'share',
      'opencode',
    );
    await mkdir(dataDir, { recursive: true });
    seedCredentialDb(dataDir, [{ service: 'opencode-go', key: 'sk-db-live' }]);
    const provider = new OpenCodeProvider(join(dataDir, '..', '..', '..'));

    await expect(provider.detectLive()).resolves.toEqual({
      present: true,
      identity: 'opencode-go',
      details: 'opencode-go:api',
    });
  });
});
