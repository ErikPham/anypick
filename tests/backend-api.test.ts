import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { createApp } from '../src/core/app';
import { createBackendApi, toBackendError } from '../src/core/backend-api';
import { AnyPickError } from '../src/utils/errors';

const roots: string[] = [];

afterEach(async () => {
  await Promise.all(roots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

async function appWithGateway() {
  const root = await mkdtemp(join(tmpdir(), 'anypick-backend-api-'));
  roots.push(root);
  const app = createApp({ root, skipMigrate: true });
  await app.profiles.create('work', {
    provider: 'openrouter',
    endpoint: 'https://gateway.example/v1',
    apiKey: 'sk-secret-must-not-cross-the-ui-boundary',
  });
  app.bindings.upsertGlobal(
    'codex',
    {
      client: 'codex',
      source: { kind: 'gateway', name: 'work' },
      model: { mode: 'omitted' },
      transportPolicy: 'direct',
      clientOptions: {},
    },
    { kind: 'direct' },
  );
  return app;
}

describe('backend API boundary', () => {
  it('returns a versioned snapshot with execution metadata and no profile secret', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);

    const snapshot = await backend.snapshot();
    expect(snapshot.schemaVersion).toBe(1);
    expect(snapshot.gateways).toContainEqual(
      expect.objectContaining({ name: 'work', endpoint: 'https://gateway.example/v1' }),
    );
    expect(snapshot.bindings).toContainEqual(
      expect.objectContaining({
        clientId: 'codex',
        executionPath: 'provider_direct',
        transportPolicy: 'direct',
      }),
    );
    expect(JSON.stringify(snapshot)).not.toContain('sk-secret-must-not-cross-the-ui-boundary');
    backend.close();
    app.close();
  });

  it('maps AnyPick errors to a structured, secret-free recovery contract', () => {
    expect(
      toBackendError(
        new AnyPickError('Credential store requires a file.', {
          code: 'CODEX_CREDENTIAL_STORE_UNSUPPORTED',
          suggestions: ['Set cli_auth_credentials_store = "file".'],
        }),
      ),
    ).toEqual({
      code: 'CODEX_CREDENTIAL_STORE_UNSUPPORTED',
      message: 'Credential store requires a file.',
      recoverable: true,
      recoveryActions: ['switch_to_file_store'],
    });
  });

  it('redacts credentials carried by AnyPickError messages', () => {
    expect(
      toBackendError(
        new AnyPickError('Upstream rejected Bearer sk-super-secret-value-123456789', {
          code: 'UPSTREAM_REJECTED',
        }),
      ).message,
    ).toBe('Upstream rejected Bearer [redacted]');
  });

  it('dispatches mutations through the backend and returns a redacted snapshot', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);

    const result = await backend.execute('gateway.add', {
      name: 'second',
      providerId: 'openrouter',
      endpoint: 'https://second.example/v1',
      apiKey: 'sk-never-return-this-secret',
    });

    expect(result.ok).toBe(true);
    expect(JSON.stringify(result)).not.toContain('sk-never-return-this-secret');
    if (result.ok) {
      expect(result.operationId).toEqual(expect.any(String));
      expect(result.snapshot?.gateways).toContainEqual(
        expect.objectContaining({ name: 'second', endpoint: 'https://second.example/v1' }),
      );
    }
    backend.close();
    app.close();
  });

  it('emits one terminal event when a journaled operation completes', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);
    const events: string[] = [];
    const unsubscribe = backend.subscribe((event) => {
      if (event.type === 'operation' && event.operation.status !== 'progress') {
        events.push(`${event.operation.operationId}/${event.operation.status}`);
      }
    });
    await new Promise((resolve) => setTimeout(resolve, 0));

    const entry = app.journal.create('test-operation', { state: 'executing' });
    app.journal.update(entry.id, { state: 'committed' });
    await backend.execute('snapshot.get');
    // Triggering a mutation asks the subscriber publisher to process the
    // journal transition without a high-frequency poll.
    await backend.execute('gateway.edit', { name: 'work', label: 'Work' });
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(events.filter((event) => event === `${entry.id}/completed`)).toEqual([
      `${entry.id}/completed`,
    ]);
    unsubscribe();
    backend.close();
    app.close();
  });

  it('executes the exact opaque activation plan with one journal operation id', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);
    const events: string[] = [];
    const unsubscribe = backend.subscribe((event) => {
      if (event.type === 'operation' && event.operation.status !== 'progress') {
        events.push(event.operation.operationId);
      }
    });

    const planned = await backend.execute('activation.plan', {
      clientId: 'codex',
      source: { kind: 'gateway', name: 'work' },
      transportPolicy: 'direct',
    });
    expect(planned.ok).toBe(true);
    if (!planned.ok) {
      throw new Error('planning unexpectedly failed');
    }
    const planId = planned.data && 'planId' in planned.data ? planned.data.planId : undefined;
    expect(planId).toEqual(expect.any(String));
    if (typeof planId !== 'string') {
      throw new Error('plan id missing');
    }

    const executed = await backend.execute('activation.execute', {
      planId,
      dryRun: true,
    });
    expect(executed.ok).toBe(true);
    if (executed.ok) {
      expect(
        executed.data && 'journalId' in executed.data ? executed.data.journalId : undefined,
      ).toBe(executed.operationId);
    }
    expect(events.filter((id) => id === executed.operationId)).toHaveLength(1);
    unsubscribe();
    backend.close();
    app.close();
  });

  it('executes project activation with the project root carried by the opaque plan', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);
    const projectRoot = join(app.root, 'project');

    const planned = await backend.execute('activation.plan', {
      clientId: 'codex',
      source: { kind: 'gateway', name: 'work' },
      mode: 'project',
      projectRoot,
      transportPolicy: 'direct',
    });
    expect(planned.ok).toBe(true);
    if (!planned.ok) {
      throw new Error('planning unexpectedly failed');
    }

    const executed = await backend.execute('activation.execute', {
      planId: planned.data.planId,
    });
    expect(executed.ok).toBe(true);
    expect(app.bindings.getProject(projectRoot, 'codex')).toMatchObject({
      projectRoot,
      client: 'codex',
    });
    backend.close();
    app.close();
  });

  it('refreshes quota in the background and never carries it across an account switch', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);
    const now = new Date().toISOString();
    let active = 'A';

    vi.spyOn(app.accounts, 'list').mockImplementation(async () =>
      ['A', 'B'].map((name) => ({
        provider: 'codex',
        name,
        label: name,
        active: name === active,
        isLiveMatch: name === active,
        liveMatchKnown: true,
        proxyEnabled: false,
        proxyRunning: false,
        createdAt: now,
        updatedAt: now,
      })),
    );
    vi.spyOn(app.accounts, 'get').mockImplementation(async (provider, name) => ({
      meta: {
        provider,
        name,
        createdAt: now,
        updatedAt: now,
        credentialKind: 'native',
        credentialClass: 'native_subscription',
      },
      accountDir: join(app.root, provider, name),
      snapshotDir: join(app.root, provider, name, 'snapshot'),
      proxy: { enabled: false },
    }));
    vi.spyOn(app.accounts, 'liveUsage').mockImplementation(async () => ({
      windows: [{ label: 'Weekly', remainingPercent: active === 'A' ? 0 : 90 }],
    }));
    vi.spyOn(app.accounts, 'use').mockImplementation(async (provider, name) => {
      const from = active;
      active = name;
      return {
        provider,
        providerName: 'Codex',
        from,
        to: name,
        refreshedPrevious: false,
      };
    });

    // Core state is available before the network-backed quota refresh completes.
    const initial = await backend.snapshot();
    expect(initial.accounts.find((account) => account.name === 'A')?.usage).toBeUndefined();
    await vi.waitFor(
      async () => {
        const refreshed = await backend.snapshot();
        expect(refreshed.accounts.find((account) => account.name === 'A')?.usage).toMatchObject({
          windows: [{ remainingPercent: 0 }],
        });
      },
      { timeout: 5_000 },
    );

    const switched = await backend.execute('account.switch', {
      providerId: 'codex',
      name: 'B',
    });
    expect(switched.ok).toBe(true);
    if (!switched.ok) {
      throw new Error('switch unexpectedly failed');
    }
    expect(
      switched.snapshot?.accounts.find((account) => account.name === 'B')?.usage,
    ).toBeUndefined();
    await vi.waitFor(
      async () => {
        const refreshed = await backend.snapshot();
        expect(refreshed.accounts.find((account) => account.name === 'B')?.usage).toMatchObject({
          windows: [{ remainingPercent: 90 }],
        });
      },
      { timeout: 5_000 },
    );

    backend.close();
    app.close();
  });

  it('returns typed, secret-free pool and hub details', async () => {
    const app = await appWithGateway();
    const backend = createBackendApi(app);

    const pool = await backend.execute('pool.get', { providerId: 'codex' });
    expect(pool.ok).toBe(true);
    if (!pool.ok) {
      throw new Error('pool read unexpectedly failed');
    }
    expect(pool.data).toMatchObject({
      providerId: 'codex',
      mode: 'single',
      enabled: false,
      strategy: 'failover',
      members: [],
    });

    const hub = await backend.execute('hub.get', {});
    expect(hub.ok).toBe(true);
    if (!hub.ok) {
      throw new Error('hub read unexpectedly failed');
    }
    expect(hub.data).toMatchObject({
      name: 'default',
      enabled: false,
      running: false,
      sources: [],
      modelOwners: [],
    });
    expect(JSON.stringify({ pool: pool.data, hub: hub.data })).not.toContain('token');

    backend.close();
    app.close();
  });
});
