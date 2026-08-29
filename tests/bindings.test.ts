import { describe, expect, it, beforeEach, afterEach } from 'vitest';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createAppReady } from '../src/core/app';
import { registerBuiltinCatalog, CatalogRegistry } from '../src/catalog/providers';
import { registerBuiltinClients } from '../src/clients';
import { ProviderRegistry } from '../src/core/registry';
import { gatewayRef, parseRef } from '../src/core/refs';
import {
  codexAccountAdapter,
  grokAccountAdapter,
  kiroAccountAdapter,
  opencodeAccountAdapter,
} from '../src/sources/account-adapters';
import { gatewayAdapterFromProfile } from '../src/sources/gateway-adapters';
import type { Account, RuntimeProfile } from '../src/types';
import { ClientRegistry } from '../src/clients/registry';
import { createClaudeCodeClient } from '../src/clients/claude-code';
import { createCodexClient } from '../src/clients/codex';
import { createTestEnv, FakeProvider } from './helpers';

describe('binding store + presets', () => {
  let root: string;

  beforeEach(async () => {
    root = await mkdtemp(join(tmpdir(), 'anypick-bind-'));
  });

  afterEach(async () => {
    await rm(root, { recursive: true, force: true });
  });

  it('stores global bindings and presets', async () => {
    const app = await createAppReady({ root, skipMigrate: true });
    const spec = {
      client: 'claude',
      source: gatewayRef('openrouter-work'),
      model: { mode: 'omitted' as const },
      transportPolicy: 'auto' as const,
      clientOptions: {},
    };
    app.bindings.upsertGlobal('claude', spec, { kind: 'direct' });
    const g = app.bindings.getGlobal('claude');
    expect(g?.spec.source).toEqual(gatewayRef('openrouter-work'));

    const preset = app.presets.create('work', {
      ...spec,
      model: { mode: 'omitted' },
    });
    expect(preset.revision).toBe(1);
    expect(app.presets.exists('work')).toBe(true);

    // gateway and preset may share a base name
    expect(parseRef('work').kind).toBe('gateway');
    expect(parseRef('@work').kind).toBe('preset');
  });

  it('journal stores only serializable refs', async () => {
    const app = await createAppReady({ root, skipMigrate: true });
    const entry = app.journal.create('activate:persistent', {
      affectedResources: ['client/claude', 'account/grok/work'],
      params: { client: 'claude', source: 'account/grok/work' },
    });
    const loaded = app.journal.get(entry.id)!;
    expect(loaded.affectedResources).toEqual(['client/claude', 'account/grok/work']);
    expect(JSON.stringify(loaded)).not.toMatch(/function|adapter/i);
  });

  it('accounts.use moves the native client binding onto the live login', async () => {
    const providers = new ProviderRegistry();
    const gemini = new FakeProvider('gemini', join(root, 'live', 'gemini'));
    providers.register(gemini);
    const clients = new ClientRegistry();
    registerBuiltinClients(clients);
    const catalog = new CatalogRegistry();
    registerBuiltinCatalog(catalog);
    const app = await createAppReady({
      root,
      bare: true,
      skipMigrate: true,
      accountRegistry: providers,
      clients,
      catalog,
    });
    await gemini.setLive({ email: 'lenta@example.test', token: 'lenta-secret' });
    await app.accounts.save('gemini', 'lentaunao');
    await app.bindingService.use('gemini', { with: 'gemini/lentaunao' });
    expect(app.bindings.getGlobal('gemini')?.spec.source).toEqual({
      kind: 'account',
      provider: 'gemini',
      name: 'lentaunao',
    });

    await gemini.setLive({ email: 'stack@example.test', token: 'stack-secret' });
    await app.accounts.save('gemini', 'stacktify');
    await app.accounts.use('gemini', 'stacktify', { noProxy: true });
    expect(app.bindings.getGlobal('gemini')?.spec.source).toEqual({
      kind: 'account',
      provider: 'gemini',
      name: 'stacktify',
    });
    app.close();
  });
});

function fakeAccount(provider: string, name: string): Account {
  return {
    meta: {
      name,
      provider,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
    },
    snapshotDir: '/tmp/x',
    accountDir: '/tmp/x',
    proxy: { enabled: false },
  };
}

describe('source adapters transportFor', () => {
  it('grok → claude is managed_builtin_proxy', () => {
    const a = grokAccountAdapter(fakeAccount('grok', 'work'));
    expect(a.transportFor('claude')).toBe('managed_builtin_proxy');
    expect(a.transportFor('codex')).toBe('managed_builtin_proxy');
    expect(a.transportFor('opencode')).toBe('managed_builtin_proxy');
  });

  it('codex native → codex is direct; claude unsupported', () => {
    const a = codexAccountAdapter(fakeAccount('codex', 'personal'));
    expect(a.transportFor('codex')).toBe('direct');
    expect(a.transportFor('claude')).toBe('unsupported');
    expect(a.capabilities.requiresNativeAuthWrite).toBe(true);
  });

  it('codex proxy opt-in keeps auto direct and exposes the managed proxy explicitly', () => {
    const account = fakeAccount('codex', 'personal');
    account.proxy.enabled = true;
    const adapter = codexAccountAdapter(account);
    expect(adapter.transportFor('codex')).toBe('direct');
    expect(adapter.transportForPolicy?.('codex', 'proxy')).toBe('managed_builtin_proxy');
    expect(adapter.transportFor('claude')).toBe('unsupported');
  });

  it('kiro missing executable → external_manual_proxy for claude', () => {
    const a = kiroAccountAdapter(fakeAccount('kiro', 'work'), {
      findExecutable: () => null,
    });
    expect(a.transportFor('claude')).toBe('external_manual_proxy');
    expect(a.transportFor('opencode')).toBe('external_manual_proxy');
    expect(a.transportFor('kiro')).toBe('direct');
  });

  it('OpenCode account is native for OpenCode and proxy-backed for Claude/Codex', () => {
    const adapter = opencodeAccountAdapter(fakeAccount('opencode', 'personal'));
    expect(adapter.transportFor('opencode')).toBe('direct');
    expect(adapter.transportFor('claude')).toBe('managed_builtin_proxy');
    expect(adapter.transportFor('codex')).toBe('managed_builtin_proxy');
    expect(adapter.capabilities.nativeClients).toEqual(['opencode']);
  });

  it('gateway adapter classifies by protocol', () => {
    const profile = {
      meta: {
        name: 'openrouter-work',
        provider: 'openrouter',
        createdAt: '',
        updatedAt: '',
        models: {},
        endpoint: 'https://openrouter.ai/api/v1',
      },
      secrets: {},
      profileDir: '/tmp',
    } as RuntimeProfile;
    const clients = new ClientRegistry();
    clients.register(createClaudeCodeClient('/tmp/anypick-test-claude'));
    clients.register(createCodexClient('/tmp/anypick-test-codex'));
    const adapter = gatewayAdapterFromProfile(profile, {
      catalogProvider: {
        id: 'openrouter',
        name: 'OpenRouter',
        description: 'test',
        apiStyle: 'openai',
        protocols: ['openai', 'anthropic'],
      },
      clients,
    });
    expect(adapter.transportFor('codex')).toBe('direct');
    // openrouter is dual-protocol
    expect(adapter.transportFor('claude')).toBe('direct');
  });
});

describe('use non-TTY missing source exits 2', () => {
  it('bindingService.use without with/current throws INVALID_USAGE', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-use-'));
    try {
      const app = await createAppReady({ root, skipMigrate: true });
      await expect(app.bindingService.use('claude', {})).rejects.toMatchObject({
        exitCode: 2,
        code: 'MISSING_SOURCE',
      });
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('run without binding throws NO_ACTIVE_BINDING', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-run-'));
    try {
      const app = await createAppReady({ root, skipMigrate: true });
      await expect(app.bindingService.runPrepare('claude', {})).rejects.toMatchObject({
        code: 'NO_ACTIVE_BINDING',
        exitCode: 5,
      });
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
});

describe('legacy migration exact evidence', () => {
  it('does not create binding from activeProfile alone', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-mig-'));
    try {
      const app = await createAppReady({ root, skipMigrate: true });
      // only set activeProfile — no ClientState
      await app.config.write({
        ...(await app.config.read()),
        activeProfile: 'openrouter-work',
      });
      // re-run migration by clearing meta
      const { setMeta } = await import('../src/core/db');
      setMeta(app.db, 'bindings_migrated_v1', '0');
      const { migrateBindingsIfNeeded } = await import('../src/core/migrate-bindings');
      const result = await migrateBindingsIfNeeded(app);
      expect(result.bindingsCreated).toBe(0);
      expect(app.bindings.listGlobal()).toHaveLength(0);
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });
});

describe('no production supportsProxy', () => {
  it('Provider type and FakeProvider use startProxy presence', async () => {
    const { service, fakes } = await createTestEnv(['p'], {
      supportsProxy: true,
    });
    expect(typeof fakes.p.startProxy).toBe('function');
    expect(service.provider('p').startProxy).toBeTypeOf('function');

    const { service: s2, fakes: f2 } = await createTestEnv(['q'], {
      supportsProxy: false,
    });
    expect(f2.q.startProxy).toBeUndefined();
    expect(() => s2.proxy.requireProxyProvider('q')).toThrow(/does not support/);
  });
});
