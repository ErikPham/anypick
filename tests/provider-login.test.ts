import { describe, expect, it } from 'vitest';
import type { AnyPickApp } from '../src/core/app';
import { buildTrayAccountProviders } from '../src/tray/snapshot-resources';
import type { ProviderLoginContext, ProviderLoginMethod } from '../src/types';
import { createTestEnv, type FakeProvider } from './helpers';

type LoginFake = FakeProvider & {
  loginMethods: () => Promise<readonly ProviderLoginMethod[]>;
  login: (ctx: ProviderLoginContext) => Promise<void>;
};

function enableLogin(
  provider: FakeProvider,
  methods: ProviderLoginMethod[],
  login: (ctx: ProviderLoginContext) => Promise<void>,
): LoginFake {
  return Object.assign(provider, {
    loginMethods: async () => methods,
    login,
  });
}

const official: ProviderLoginMethod = {
  id: 'official-browser',
  label: 'Official browser login',
  provenance: 'official',
  preferred: true,
  available: true,
};

const community: ProviderLoginMethod = {
  id: 'community-browser',
  label: 'Community browser login',
  provenance: 'community',
  available: true,
};

describe('provider browser login', () => {
  it('saves a completed native login into AnyPick and makes it active', async () => {
    const { service, fakes, store } = await createTestEnv(['fake']);
    const fake = enableLogin(fakes.fake, [official], async () => {
      await fakes.fake.setLive({ email: 'alice@example.test', token: 'new-token' });
    });

    const result = await service.login('fake');

    expect(result.method.id).toBe('official-browser');
    expect(result.account.name).toBe('alice');
    expect(result.backedUpTo).toBeNull();
    expect(await fake.readLive()).toEqual({
      email: 'alice@example.test',
      token: 'new-token',
    });
    expect(await store.getActive('fake')).toBe('alice');
    expect((await store.requireAccount('fake', 'alice')).meta.identity).toBe('alice@example.test');
  });

  it('backs up the previous native login before replacing it', async () => {
    const { service, fakes, store } = await createTestEnv(['fake']);
    const fake = fakes.fake;
    await fake.setLive({ email: 'old@example.test', token: 'old-token' });
    await service.save('fake', 'old');
    enableLogin(fake, [official], async () => {
      await fake.setLive({ email: 'new@example.test', token: 'new-token' });
    });

    const result = await service.login('fake');

    expect(result.backedUpTo).toBe('old');
    expect(result.account.name).toBe('new');
    expect(await store.getActive('fake')).toBe('new');
    expect((await store.requireAccount('fake', 'old')).meta.identity).toBe('old@example.test');
    expect(await fake.readLive()).toEqual({ email: 'new@example.test', token: 'new-token' });
  });

  it('restores the exact previous login and active pointer after a partial failure', async () => {
    const { service, fakes, store } = await createTestEnv(['fake']);
    const fake = fakes.fake;
    await fake.setLive({ email: 'old@example.test', token: 'old-token' });
    await service.save('fake', 'old');
    enableLogin(fake, [official], async () => {
      await fake.setLive({ email: 'partial@example.test', token: 'partial-token' });
      throw new Error('browser flow failed');
    });

    await expect(service.login('fake')).rejects.toThrow('browser flow failed');

    expect(await fake.readLive()).toEqual({ email: 'old@example.test', token: 'old-token' });
    expect(await store.getActive('fake')).toBe('old');
    expect(await store.getAccount('fake', 'partial')).toBeNull();
  });

  it('prefers an official method but permits an explicitly selected community method', async () => {
    const { service, fakes } = await createTestEnv(['fake']);
    const invoked: string[] = [];
    const fake = fakes.fake;
    enableLogin(
      fake,
      [
        { ...community, preferred: true },
        { ...official, preferred: false },
      ],
      async (ctx) => {
        invoked.push(ctx.methodId);
        await fake.setLive({ email: `${ctx.methodId}@example.test`, token: ctx.methodId });
      },
    );

    const automatic = await service.login('fake');
    const selected = await service.login('fake', { methodId: 'community-browser' });

    expect(automatic.method.provenance).toBe('official');
    expect(selected.method.provenance).toBe('community');
    expect(invoked).toEqual(['official-browser', 'community-browser']);
  });

  it('rejects an unavailable method before changing native auth', async () => {
    const { service, fakes } = await createTestEnv(['fake']);
    const fake = fakes.fake;
    await fake.setLive({ email: 'old@example.test', token: 'old-token' });
    enableLogin(
      fake,
      [{ ...community, available: false, unavailableReason: 'launcher missing' }],
      async () => {
        throw new Error('must not run');
      },
    );

    await expect(service.login('fake', { methodId: 'community-browser' })).rejects.toMatchObject({
      code: 'PROVIDER_LOGIN_UNAVAILABLE',
    });
    expect(await fake.readLive()).toEqual({ email: 'old@example.test', token: 'old-token' });
  });

  it('does not infer a native source from an unscoped login method', async () => {
    const { service, fakes } = await createTestEnv(['fake']);
    const fake = enableLogin(fakes.fake, [official], async () => {
      throw new Error('must not run');
    });
    Object.assign(fake, {
      accountSources: [
        { id: 'first', label: 'First source' },
        { id: 'second', label: 'Second source' },
      ],
    });

    await expect(service.login('fake', { methodId: 'official-browser' })).rejects.toMatchObject({
      code: 'ACCOUNT_SOURCE_REQUIRED',
    });
    expect(await fake.readLive()).toBeNull();
  });

  it('publishes official and community methods to the tray without credential data', async () => {
    const { service, fakes, registry } = await createTestEnv(['fake']);
    enableLogin(fakes.fake, [community, official], async () => {});
    await fakes.fake.setLive({ email: 'live@example.test', token: 'must-not-cross' });
    const app = {
      accounts: service,
      accountRegistry: registry,
      clients: { list: () => [] },
    } as unknown as AnyPickApp;

    const providers = await buildTrayAccountProviders(app, {});

    expect(providers).toHaveLength(1);
    expect(providers[0]).toMatchObject({
      installed: true,
      livePresent: true,
      liveIdentity: 'live@example.test',
    });
    expect(providers[0]?.loginMethods).toEqual([
      expect.objectContaining({
        id: 'official-browser',
        provenance: 'official',
        preferred: true,
      }),
      expect.objectContaining({
        id: 'community-browser',
        provenance: 'community',
        preferred: false,
      }),
    ]);
    expect(JSON.stringify(providers)).not.toContain('must-not-cross');
  });
});
