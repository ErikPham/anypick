import { describe, expect, it } from 'vitest';
import { resolveTransportPolicy } from '../src/core/transport-resolver';
import { codexAccountAdapter } from '../src/sources/account-adapters';
import type { Account, SourceAdapter } from '../src/types';

const direct: SourceAdapter = {
  sourceRef: { kind: 'gateway', name: 'direct-api' },
  capabilities: {
    sourceKind: 'gateway',
    provider: 'test',
    nativeClients: [],
    protocols: ['openai'],
    canRefresh: false,
    supportsModelDiscovery: false,
  },
  transportFor: () => 'direct',
};

describe('transport policy resolver', () => {
  it('keeps the provider-owned default for auto', () => {
    expect(resolveTransportPolicy(direct, 'codex', 'auto')).toBe('direct');
  });

  it('rejects an unavailable explicit proxy instead of silently keeping direct', () => {
    expect(() => resolveTransportPolicy(direct, 'codex', 'proxy')).toThrow(
      /does not support local proxy/i,
    );
  });

  it('lets Codex choose direct or an already enabled local proxy explicitly', () => {
    const account: Account = {
      meta: {
        name: 'work',
        provider: 'codex',
        createdAt: '2026-01-01T00:00:00.000Z',
        updatedAt: '2026-01-01T00:00:00.000Z',
      },
      snapshotDir: '/tmp/work',
      accountDir: '/tmp/work',
      proxy: { enabled: true },
    };
    const adapter = codexAccountAdapter(account);

    expect(resolveTransportPolicy(adapter, 'codex', 'auto')).toBe('direct');
    expect(resolveTransportPolicy(adapter, 'codex', 'direct')).toBe('direct');
    expect(resolveTransportPolicy(adapter, 'codex', 'proxy')).toBe('managed_builtin_proxy');
  });
});
