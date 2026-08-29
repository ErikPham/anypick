import { access, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it, vi } from 'vitest';
import {
  decodeTrayCommand,
  MAX_TRAY_COMMAND_BYTES,
  trayLogSourceId,
  TRAY_PROTOCOL_VERSION,
} from '../src/tray/protocol';
import { TrayBackupInspectionManager } from '../src/tray/backup-inspections';
import { trayMutationError } from '../src/tray/supervisor-errors';
import { redactTrayProxyLogs } from '../src/tray/supervisor-logs';
import {
  detectTrayAccount,
  invokeTrayMutation,
  testTrayGateway,
} from '../src/tray/supervisor-mutations';
import { AnyPickError } from '../src/utils/errors';
import type { AnyPickApp } from '../src/core/app';

function encodeInvoke(payload: unknown): string {
  return `invoke\t${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

function encodeMutation(payload: unknown): string {
  return `mutate\t${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

function encodeModelRoles(payload: unknown): string {
  return `model-roles\t${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

function encodeLogs(payload: unknown): string {
  return `logs\t${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

function encodeBackupInspect(payload: unknown): string {
  return `backup-inspect\t${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

function encodeCancel(payload: unknown): string {
  return `cancel\t${Buffer.from(JSON.stringify(payload), 'utf8').toString('base64')}`;
}

describe('tray command protocol', () => {
  it('uses one canonical identity for a log source and response', () => {
    expect(trayLogSourceId('proxy-hub', 'default')).toBe('proxy-hub/default');
    expect(trayLogSourceId('tray-supervisor', 'main')).toBe('tray-supervisor/main');
  });

  it('decodes the exact legacy commands', () => {
    expect(decodeTrayCommand('open')).toEqual({ kind: 'open' });
    expect(decodeTrayCommand('refresh')).toEqual({ kind: 'refresh' });
    expect(decodeTrayCommand('restart')).toEqual({ kind: 'restart' });
    expect(decodeTrayCommand('stop')).toEqual({ kind: 'stop' });
    expect(decodeTrayCommand('quit')).toEqual({ kind: 'quit' });
  });

  it('decodes only a bounded request-targeted cancellation command', () => {
    const payload = { version: TRAY_PROTOCOL_VERSION, requestId: 'login-account' };
    expect(decodeTrayCommand(encodeCancel(payload))).toEqual({ kind: 'cancel', payload });
    expect(decodeTrayCommand(encodeCancel({ ...payload, requestId: '' }))).toBeUndefined();
    expect(
      decodeTrayCommand(encodeCancel({ ...payload, operation: 'account-login' })),
    ).toBeUndefined();
  });

  it('decodes only the supported tray destinations', () => {
    expect(decodeTrayCommand('navigate\taccounts')).toEqual({
      kind: 'navigate',
      screen: 'accounts',
    });
    expect(decodeTrayCommand('navigate\tgateways')).toEqual({
      kind: 'navigate',
      screen: 'gateways',
    });
    expect(decodeTrayCommand('navigate\tproxy')).toEqual({ kind: 'navigate', screen: 'proxy' });
    expect(decodeTrayCommand('navigate\tproxy-hub')).toEqual({
      kind: 'navigate',
      screen: 'proxy-hub',
    });
    expect(decodeTrayCommand('navigate\tadd-account')).toEqual({
      kind: 'navigate',
      screen: 'add-account',
    });
    expect(decodeTrayCommand('navigate\tadd-gateway')).toEqual({
      kind: 'navigate',
      screen: 'add-gateway',
    });
    expect(decodeTrayCommand('navigate\tsettings')).toBeUndefined();
    expect(decodeTrayCommand('navigate\taccounts\textra')).toBeUndefined();
  });

  it('rejects non-exact or unknown commands', () => {
    for (const line of [' open', 'open\n', 'restart\t', 'invoke', 'status\t1', 'unknown']) {
      expect(decodeTrayCommand(line)).toBeUndefined();
    }
  });

  it('decodes a valid invoke request and permits additional payload fields', () => {
    const payload = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'request-1',
      revision: 0,
      actionId: 'refresh',
      futureField: true,
    };
    expect(decodeTrayCommand(encodeInvoke(payload))).toEqual({
      kind: 'invoke',
      payload,
    });
  });

  it('rejects malformed, truncated, and non-canonical base64', () => {
    for (const encoded of ['not base64!', 'eyJ2ZXJzaW9uIjox', 'TQ', 'TWE=', 'TWFu====']) {
      expect(decodeTrayCommand(`invoke\t${encoded}`)).toBeUndefined();
    }
  });

  it('rejects base64 that does not contain JSON', () => {
    expect(
      decodeTrayCommand(`invoke\t${Buffer.from('not json').toString('base64')}`),
    ).toBeUndefined();
  });

  it('rejects oversized command lines, payloads, and decoded payloads', () => {
    const valid = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'r',
      revision: 1,
      actionId: 'a',
    };
    expect(decodeTrayCommand(`open${'x'.repeat(MAX_TRAY_COMMAND_BYTES)}`)).toBeUndefined();
    expect(decodeTrayCommand(`invoke\t${'A'.repeat(MAX_TRAY_COMMAND_BYTES + 1)}`)).toBeUndefined();
    expect(
      decodeTrayCommand(encodeInvoke({ ...valid, extra: 'x'.repeat(MAX_TRAY_COMMAND_BYTES) })),
    ).toBeUndefined();
  });

  it('rejects invalid invoke fields and accepts their stated bounds', () => {
    const valid = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'r'.repeat(128),
      revision: Number.MAX_SAFE_INTEGER,
      actionId: 'a'.repeat(128),
    };
    expect(decodeTrayCommand(encodeInvoke(valid))).toEqual({ kind: 'invoke', payload: valid });

    for (const payload of [
      { ...valid, version: 2 },
      { ...valid, version: '1' },
      { ...valid, requestId: '' },
      { ...valid, requestId: 'r'.repeat(129) },
      { ...valid, revision: -1 },
      { ...valid, revision: 1.5 },
      { ...valid, revision: Number.MAX_SAFE_INTEGER + 1 },
      { ...valid, actionId: '' },
      { ...valid, actionId: 'a'.repeat(129) },
    ]) {
      expect(decodeTrayCommand(encodeInvoke(payload))).toBeUndefined();
    }
  });

  it('decodes bounded opaque model-role selections without accepting source refs or model ids', () => {
    const payload = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'models-1',
      revision: 7,
      clientId: 'claude',
      roleActionIds: {
        default: 'action-default',
        sonnet: 'action-sonnet',
        opus: 'action-opus',
        haiku: 'action-haiku',
      },
    };
    expect(decodeTrayCommand(encodeModelRoles(payload))).toEqual({
      kind: 'apply-model-roles',
      payload,
    });

    for (const invalid of [
      { ...payload, revision: -1 },
      { ...payload, clientId: '' },
      { ...payload, roleActionIds: {} },
      { ...payload, roleActionIds: { sonnet: 'action-sonnet' } },
      { ...payload, roleActionIds: { default: '' } },
      { ...payload, roleActionIds: { default: 'x'.repeat(129) } },
      { ...payload, roleActionIds: { default: 'action', bad: 'line\nbreak' } },
      { ...payload, source: 'hub/default' },
      { ...payload, model: 'raw-model-id' },
    ]) {
      expect(decodeTrayCommand(encodeModelRoles(invalid))).toBeUndefined();
    }
  });

  it('decodes bounded account and gateway mutations without exposing interpretation gaps', () => {
    const detect = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'detect-account',
      operation: 'account-detect',
      providerId: 'gemini',
      sourceId: 'antigravity',
      name: 'detect',
    };
    expect(decodeTrayCommand(encodeMutation(detect))).toEqual({
      kind: 'mutate',
      payload: detect,
    });

    const account = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'save-account',
      operation: 'account-save',
      providerId: 'gemini',
      sourceId: 'antigravity',
      name: 'lentaunao',
      label: 'Lentaunao',
      overwrite: true,
    };
    expect(decodeTrayCommand(encodeMutation(account))).toEqual({
      kind: 'mutate',
      payload: account,
    });

    const currentAccount = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'save-current-account',
      operation: 'account-save-current',
      providerId: 'gemini',
      sourceId: 'antigravity',
    };
    expect(decodeTrayCommand(encodeMutation(currentAccount))).toEqual({
      kind: 'mutate',
      payload: currentAccount,
    });
    expect(
      decodeTrayCommand(encodeMutation({ ...currentAccount, apiKey: 'must-not-be-accepted' })),
    ).toBeUndefined();

    const browserLogin = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'login-account',
      operation: 'account-login',
      providerId: 'claude',
      sourceId: 'claude-code',
      methodId: 'claude-subscription',
      label: 'Work',
    };
    expect(decodeTrayCommand(encodeMutation(browserLogin))).toEqual({
      kind: 'mutate',
      payload: browserLogin,
    });
    expect(decodeTrayCommand(encodeMutation({ ...browserLogin, methodId: '' }))).toBeUndefined();
    expect(
      decodeTrayCommand(encodeMutation({ ...browserLogin, oauthUrl: 'https://secret.test' })),
    ).toBeUndefined();

    const gateway = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'save-gateway',
      operation: 'gateway-create',
      providerId: 'openrouter',
      name: 'work',
      endpoint: 'https://openrouter.ai/api/v1',
      apiKey: 'secret-value',
      defaultModel: 'openai/gpt-5',
    };
    expect(decodeTrayCommand(encodeMutation(gateway))).toEqual({
      kind: 'mutate',
      payload: gateway,
    });

    const gatewayTest = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'test-gateway',
      operation: 'gateway-test',
      providerId: 'openrouter',
      name: 'test',
      endpoint: 'https://openrouter.ai/api/v1',
      apiKey: 'unsaved-secret-value',
    };
    expect(decodeTrayCommand(encodeMutation(gatewayTest))).toEqual({
      kind: 'mutate',
      payload: gatewayTest,
    });

    const apiKeyAccount = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'save-kiro',
      operation: 'account-save',
      providerId: 'kiro',
      name: 'work',
      apiKey: 'ksk_a_reasonably_long_key',
      region: 'us-east-1',
    };
    expect(decodeTrayCommand(encodeMutation(apiKeyAccount))).toEqual({
      kind: 'mutate',
      payload: apiKeyAccount,
    });

    const reset = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'reset-client',
      operation: 'client-reset',
      name: 'claude',
    };
    expect(decodeTrayCommand(encodeMutation(reset))).toEqual({
      kind: 'mutate',
      payload: reset,
    });

    const hubSource = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'hub-source',
      operation: 'hub-source-toggle',
      providerId: 'gemini',
      name: 'work',
      enabled: true,
    } as const;
    expect(decodeTrayCommand(encodeMutation(hubSource))).toEqual({
      kind: 'mutate',
      payload: hubSource,
    });

    const clearLive = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'clear-account',
      operation: 'account-clear',
      providerId: 'kiro',
      name: 'clear',
    };
    expect(decodeTrayCommand(encodeMutation(clearLive))).toEqual({
      kind: 'mutate',
      payload: clearLive,
    });

    const clearLiveSource = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'clear-antigravity',
      operation: 'account-clear',
      providerId: 'gemini',
      sourceId: 'antigravity',
      name: 'clear',
    };
    expect(decodeTrayCommand(encodeMutation(clearLiveSource))).toEqual({
      kind: 'mutate',
      payload: clearLiveSource,
    });

    const remove = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'remove-account',
      operation: 'account-remove',
      providerId: 'grok',
      name: 'work',
    };
    expect(decodeTrayCommand(encodeMutation(remove))).toEqual({
      kind: 'mutate',
      payload: remove,
    });

    const removeGateway = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'remove-gateway',
      operation: 'gateway-remove',
      name: 'work-relay',
    } as const;
    expect(decodeTrayCommand(encodeMutation(removeGateway))).toEqual({
      kind: 'mutate',
      payload: removeGateway,
    });
  });

  it('decodes only exact bounded backup inspection requests as read-only commands', () => {
    const payload = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'inspect-1',
      path: '/tmp/accounts.anypick-backup',
    };
    expect(decodeTrayCommand(encodeBackupInspect(payload))).toEqual({
      kind: 'backup-inspect',
      payload,
    });

    for (const invalid of [
      { ...payload, version: 2 },
      { ...payload, requestId: '' },
      { ...payload, requestId: 'r'.repeat(129) },
      { ...payload, path: '' },
      { ...payload, path: 'x'.repeat(4097) },
      { ...payload, path: '/tmp/bad\tpath' },
      { ...payload, path: `/tmp/bad${String.fromCharCode(1)}path` },
      { ...payload, extra: true },
    ]) {
      expect(decodeTrayCommand(encodeBackupInspect(invalid))).toBeUndefined();
    }
    expect(
      decodeTrayCommand(`backup-inspect\t${'A'.repeat(MAX_TRAY_COMMAND_BYTES)}`),
    ).toBeUndefined();
  });

  it('accepts only exact bounded bulk backup mutations', () => {
    const exported = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'backup-export',
      operation: 'account-backup-export',
      path: '/tmp/accounts.anypick-backup',
      accounts: [
        { provider: 'claude', name: 'personal' },
        { provider: 'codex', name: 'work' },
      ],
      overwrite: false,
    };
    const imported = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'backup-import',
      operation: 'account-backup-import',
      inspectionId: 'inspection-1',
      entryIds: ['0', '2'],
      overwriteConfirmed: true,
    };
    expect(decodeTrayCommand(encodeMutation(exported))).toEqual({
      kind: 'mutate',
      payload: exported,
    });
    expect(decodeTrayCommand(encodeMutation(imported))).toEqual({
      kind: 'mutate',
      payload: imported,
    });

    const refsAtLimit = Array.from({ length: 64 }, (_, index) => ({
      provider: 'claude',
      name: `account-${index}`,
    }));
    expect(decodeTrayCommand(encodeMutation({ ...exported, accounts: refsAtLimit }))).toBeDefined();
    expect(
      decodeTrayCommand(
        encodeMutation({ ...imported, entryIds: Array.from({ length: 64 }, (_, i) => `${i}`) }),
      ),
    ).toBeDefined();

    for (const invalid of [
      { ...exported, accounts: [] },
      { ...exported, accounts: [exported.accounts[0], exported.accounts[0]] },
      { ...exported, accounts: [...refsAtLimit, { provider: 'codex', name: 'overflow' }] },
      { ...exported, accounts: [{ provider: 'claude', name: 'work', label: 'raw' }] },
      { ...exported, overwrite: undefined },
      { ...exported, overwrite: 'yes' },
      { ...exported, inspectionId: 'not-for-export' },
      { ...exported, path: '/tmp/bad\tpath' },
      { ...imported, inspectionId: '' },
      { ...imported, inspectionId: 'i'.repeat(129) },
      { ...imported, entryIds: [] },
      { ...imported, entryIds: ['0', '0'] },
      { ...imported, entryIds: Array.from({ length: 65 }, (_, i) => `${i}`) },
      { ...imported, entryIds: ['x'.repeat(17)] },
      { ...imported, overwriteConfirmed: undefined },
      { ...imported, overwriteConfirmed: 'yes' },
      { ...imported, path: '/tmp/must-not-cross' },
      { ...imported, accounts: exported.accounts },
    ]) {
      expect(decodeTrayCommand(encodeMutation(invalid))).toBeUndefined();
    }
  });

  it('accepts only exact bounded account transfer payloads', () => {
    const imported = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'import-account',
      operation: 'account-import',
      providerId: 'codex',
      name: 'work',
      path: '/tmp/work.anypick.json',
      overwrite: true,
    };
    const exported = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'export-account',
      operation: 'account-export',
      providerId: 'claude',
      name: 'personal',
      path: '/tmp/personal.anypick.json',
    };
    const backup = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'backup-accounts',
      operation: 'account-backup-all',
      path: '/tmp/backups',
    };

    for (const payload of [imported, exported, backup]) {
      expect(decodeTrayCommand(encodeMutation(payload))).toEqual({ kind: 'mutate', payload });
    }

    for (const payload of [
      { ...imported, providerId: undefined },
      { ...imported, name: undefined },
      { ...imported, path: undefined },
      { ...imported, path: `/${'x'.repeat(4096)}` },
      { ...imported, path: '/tmp/bad\npath' },
      { ...imported, path: '/tmp/bad\rpath' },
      { ...imported, path: `/tmp/bad${String.fromCharCode(0)}path` },
      { ...imported, endpoint: 'https://irrelevant.example' },
      { ...exported, overwrite: true },
      { ...backup, providerId: 'codex' },
      { ...backup, name: 'irrelevant' },
      { ...backup, apiKey: 'must-not-cross' },
    ]) {
      expect(decodeTrayCommand(encodeMutation(payload))).toBeUndefined();
    }
  });

  it('rejects malformed tray mutations', () => {
    const valid = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'request',
      operation: 'account-edit',
      providerId: 'codex',
      name: 'work',
    };
    for (const payload of [
      { ...valid, operation: 'account-delete' },
      { ...valid, operation: 'account-remove', providerId: undefined },
      { ...valid, operation: 'gateway-test', providerId: undefined },
      { ...valid, providerId: undefined },
      { ...valid, name: '' },
      { ...valid, name: 'bad\nname' },
      { ...valid, apiKey: 'x'.repeat(8193) },
      { ...valid, region: 'x'.repeat(65) },
      { ...valid, overwrite: 'yes' },
      { ...valid, unknown: true },
      {
        version: TRAY_PROTOCOL_VERSION,
        requestId: 'hub-source',
        operation: 'hub-source-toggle',
        providerId: 'gemini',
        name: 'work',
      },
    ]) {
      expect(decodeTrayCommand(encodeMutation(payload))).toBeUndefined();
    }
  });

  it('requires an explicit boolean for settings mutations', () => {
    const setting = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'setting-1',
      operation: 'setting-show-quota',
      name: 'settings',
      enabled: false,
    };
    expect(decodeTrayCommand(encodeMutation(setting))).toEqual({
      kind: 'mutate',
      payload: setting,
    });
    expect(decodeTrayCommand(encodeMutation({ ...setting, enabled: undefined }))).toBeUndefined();
    expect(decodeTrayCommand(encodeMutation({ ...setting, enabled: 'false' }))).toBeUndefined();
    expect(
      decodeTrayCommand(
        encodeMutation({ ...setting, operation: 'setting-quota-guard', enabled: true }),
      ),
    ).toEqual({
      kind: 'mutate',
      payload: { ...setting, operation: 'setting-quota-guard', enabled: true },
    });
  });

  it('decodes only bounded proxy log requests', () => {
    const payload = {
      version: TRAY_PROTOCOL_VERSION,
      requestId: 'logs-1',
      providerId: 'opencode',
      name: 'work',
      lines: 80,
    };
    expect(decodeTrayCommand(encodeLogs(payload))).toEqual({ kind: 'logs', payload });
    expect(decodeTrayCommand(encodeLogs({ ...payload, lines: 0 }))).toBeUndefined();
    expect(decodeTrayCommand(encodeLogs({ ...payload, lines: 201 }))).toBeUndefined();
    expect(decodeTrayCommand(encodeLogs({ ...payload, extra: true }))).toBeUndefined();
  });

  it('redacts runtime and upstream secrets before logs cross into the tray helper', () => {
    const runtimeToken = 'runtime-secret-that-must-never-cross';
    const visible = redactTrayProxyLogs(
      [
        `Authorization: Bearer ${runtimeToken}`,
        'https://example.test/v1?api_key=sk-super-secret-value',
        '{"token":"another-secret-value"}',
      ].join('\n'),
      runtimeToken,
    );
    expect(visible).not.toContain(runtimeToken);
    expect(visible).not.toContain('sk-super-secret-value');
    expect(visible).not.toContain('another-secret-value');
    expect(visible).toContain('<redacted>');
  });

  it('tests an unsaved gateway only against a live model catalog', async () => {
    const get = vi.fn();
    const list = vi.fn(async () => ({ source: 'live' as const, models: [{ id: 'model-1' }] }));
    const app = {
      catalog: { get },
      modelDiscovery: { list },
    } as unknown as Pick<AnyPickApp, 'catalog' | 'modelDiscovery'>;

    await expect(
      testTrayGateway(app, 'openrouter', ' https://gateway.example/v1 ', ' secret-key '),
    ).resolves.toBe('Connection available: 1 model returned.');
    expect(get).toHaveBeenCalledExactlyOnceWith('openrouter');
    expect(list).toHaveBeenCalledExactlyOnceWith({
      providerId: 'openrouter',
      endpoint: 'https://gateway.example/v1',
      apiKey: 'secret-key',
      refresh: true,
    });
  });

  it.each(['cache', 'stale', 'catalog'] as const)(
    'does not report a %s gateway catalog as available',
    async (sourceKind) => {
      const app = {
        catalog: { get: vi.fn() },
        modelDiscovery: {
          list: vi.fn(async () => ({ source: sourceKind, models: [{ id: 'fallback' }] })),
        },
      } as unknown as Pick<AnyPickApp, 'catalog' | 'modelDiscovery'>;

      await expect(
        testTrayGateway(app, 'openrouter', undefined, 'secret-key'),
      ).rejects.toMatchObject({
        code: 'GATEWAY_TEST_FAILED',
      });
    },
  );

  it('turns account and gateway test failures into actionable, secret-free tray messages', () => {
    const noLogin = trayMutationError(
      new AnyPickError('/Users/me/.config contained token sk-do-not-leak', 'NO_LIVE_AUTH'),
      {
        operation: 'account-detect',
        providerId: 'gemini',
        sourceId: 'antigravity',
      },
    );
    expect(noLogin).toBe(
      'No signed-in account was found in Antigravity. Open Antigravity, sign in, then click Detect login again.',
    );
    expect(noLogin).not.toContain('sk-do-not-leak');
    expect(
      trayMutationError(new Error('HTTP 401: upstream body with secrets'), {
        operation: 'account-save-current',
        providerId: 'codex',
      }),
    ).toBe(
      'AnyPick could not save that account. Nothing was changed; check the official app login and try again.',
    );

    const gatewayFailure = trayMutationError(
      new Error('HTTP 401 from https://secret.example?api_key=sk-do-not-leak'),
      {
        operation: 'gateway-test',
        providerId: 'openrouter',
        endpoint: 'https://secret.example',
        apiKey: 'sk-do-not-leak',
      },
    );
    expect(gatewayFailure).toBe(
      'The gateway did not return a live model catalog. Check its URL and API key, then try again.',
    );
    expect(gatewayFailure).not.toContain('secret.example');
    expect(gatewayFailure).not.toContain('sk-do-not-leak');

    const transferFailure = trayMutationError(
      new Error('Could not read /Users/me/Private/account.anypick.json containing sk-secret'),
      {
        operation: 'account-import',
        providerId: 'codex',
        name: 'work',
        path: '/Users/me/Private/account.anypick.json',
      },
    );
    expect(transferFailure).toBe(
      'AnyPick could not import that account. Existing saved accounts were not changed.',
    );
    expect(transferFailure).not.toContain('/Users/me');
    expect(transferFailure).not.toContain('sk-secret');

    const backupFailure = trayMutationError(
      new Error('Could not read /Users/me/Private/accounts.anypick-backup with token secret'),
      { operation: 'account-backup-import', inspectionId: 'inspection-secret', entryIds: ['0'] },
    );
    expect(backupFailure).toBe(
      'AnyPick could not import that backup. Existing saved accounts were not changed.',
    );
    expect(backupFailure).not.toContain('/Users/me');
    expect(backupFailure).not.toContain('inspection-secret');

    const destinationExists = trayMutationError(
      new AnyPickError('Existing private path: /Users/me/backup', 'BACKUP_DESTINATION_EXISTS'),
      { operation: 'account-backup-export' },
    );
    expect(destinationExists).toBe(
      'A file already exists at that destination. Choose another name or confirm replacement.',
    );
    expect(destinationExists).not.toContain('/Users/me');
  });

  it('dispatches backup export overwrite through the account service', async () => {
    const exportAccountBackup = vi.fn(async () => ({ file: '/private/result', accountCount: 1 }));
    const app = { accounts: { exportAccountBackup } } as unknown as AnyPickApp;
    const native = {
      stdin: { writable: true, write: vi.fn() },
    } as unknown as Parameters<typeof invokeTrayMutation>[1];
    const activity = {
      record: vi.fn(async () => ({})),
    } as unknown as Parameters<typeof invokeTrayMutation>[4];

    await invokeTrayMutation(
      app,
      native,
      {
        version: 1,
        requestId: 'request-export',
        operation: 'account-backup-export',
        accounts: [{ provider: 'claude', name: 'work' }],
        path: '/private/result',
        overwrite: true,
      },
      'cli-entry',
      activity,
      {} as Parameters<typeof invokeTrayMutation>[5],
      vi.fn(async () => {}),
      vi.fn(),
    );

    expect(exportAccountBackup).toHaveBeenCalledExactlyOnceWith(
      [{ provider: 'claude', name: 'work' }],
      '/private/result',
      { overwrite: true },
    );
  });

  it('dispatches browser login by opaque method id without accepting credential data', async () => {
    const cancellation = new AbortController();
    const login = vi.fn(async () => ({
      provider: 'claude',
      account: { provider: 'claude', name: 'work' },
      method: {
        id: 'claude-subscription',
        label: 'Claude subscription',
        provenance: 'official',
      },
      backedUpTo: 'personal',
    }));
    const app = { accounts: { login } } as unknown as AnyPickApp;
    const native = {
      stdin: { writable: true, write: vi.fn() },
    } as unknown as Parameters<typeof invokeTrayMutation>[1];
    const activity = {
      record: vi.fn(async () => ({})),
    } as unknown as Parameters<typeof invokeTrayMutation>[4];

    await invokeTrayMutation(
      app,
      native,
      {
        version: 1,
        requestId: 'request-login',
        operation: 'account-login',
        providerId: 'claude',
        sourceId: 'claude-code',
        methodId: 'claude-subscription',
        name: 'work',
        label: 'Work',
      },
      'cli-entry',
      activity,
      {} as Parameters<typeof invokeTrayMutation>[5],
      vi.fn(async () => {}),
      vi.fn(),
      cancellation.signal,
    );

    expect(login).toHaveBeenCalledExactlyOnceWith('claude', {
      methodId: 'claude-subscription',
      source: 'claude-code',
      name: 'work',
      label: 'Work',
      signal: cancellation.signal,
    });
    expect(native.stdin.write).toHaveBeenCalled();
  });

  it('reports an aborted browser login as cancellation instead of an error', async () => {
    const login = vi.fn(async () => {
      throw new AnyPickError('Browser sign-in was cancelled.', {
        code: 'PROVIDER_LOGIN_CANCELLED',
        mutated: false,
      });
    });
    const app = { accounts: { login } } as unknown as AnyPickApp;
    const write = vi.fn();
    const native = {
      stdin: { writable: true, write },
    } as unknown as Parameters<typeof invokeTrayMutation>[1];
    const activity = {
      record: vi.fn(async () => ({})),
    } as unknown as Parameters<typeof invokeTrayMutation>[4];

    await invokeTrayMutation(
      app,
      native,
      {
        version: 1,
        requestId: 'request-cancelled-login',
        operation: 'account-login',
        providerId: 'claude',
        methodId: 'claude-subscription',
      },
      'cli-entry',
      activity,
      {} as Parameters<typeof invokeTrayMutation>[5],
      vi.fn(async () => {}),
      vi.fn(),
      AbortSignal.abort(),
    );

    const wire = String(write.mock.calls[0]?.[0]);
    const result = JSON.parse(Buffer.from(wire.trim().split('\t')[1], 'base64').toString('utf8'));
    expect(result).toMatchObject({
      requestId: 'request-cancelled-login',
      status: 'cancelled',
    });
  });

  it('saves the exact current source through the identity-resolving service path', async () => {
    const saveCurrent = vi.fn(async () => ({ provider: 'gemini', name: 'live-user' }));
    const app = {
      accounts: { saveCurrent },
      accountRegistry: { get: vi.fn(() => ({ id: 'gemini' })) },
    } as unknown as AnyPickApp;
    const native = {
      stdin: { writable: true, write: vi.fn() },
    } as unknown as Parameters<typeof invokeTrayMutation>[1];
    const activity = {
      record: vi.fn(async () => ({})),
    } as unknown as Parameters<typeof invokeTrayMutation>[4];

    await invokeTrayMutation(
      app,
      native,
      {
        version: 1,
        requestId: 'request-save-current',
        operation: 'account-save-current',
        providerId: 'gemini',
        sourceId: 'antigravity',
      },
      'cli-entry',
      activity,
      {} as Parameters<typeof invokeTrayMutation>[5],
      vi.fn(async () => {}),
      vi.fn(),
    );

    expect(saveCurrent).toHaveBeenCalledExactlyOnceWith('gemini', undefined, 'antigravity');
    expect(native.stdin.write).toHaveBeenCalled();
  });

  it('detects a live source without invoking any account mutation service', async () => {
    const detectLiveSource = vi.fn(async () => ({ present: true, identity: 'user@example.test' }));
    const app = {
      accountRegistry: {
        get: () => ({ name: 'Gemini', detectLive: vi.fn(), detectLiveSource }),
      },
    } as unknown as Pick<AnyPickApp, 'accountRegistry'>;

    await expect(detectTrayAccount(app, 'gemini', 'antigravity')).resolves.toBe(
      'Detected user@example.test in Antigravity.',
    );
    expect(detectLiveSource).toHaveBeenCalledExactlyOnceWith('antigravity');
    expect(app).not.toHaveProperty('accounts');
  });

  it('restores the privately staged preview bytes after the original changes', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-tray-inspection-'));
    const path = join(root, 'private.anypick-backup');
    const previewedBytes = 'credential-payload-that-must-not-cross';
    await writeFile(path, previewedBytes);
    try {
      const inspectAccountBackup = vi.fn(async (_candidate: string) => ({
        createdAt: '2026-08-09T12:00:00.000Z',
        entries: [
          {
            provider: 'claude',
            name: 'work',
            label: 'Work',
            disposition: 'replace' as const,
          },
        ],
      }));
      const restoreAccountBackup = vi.fn(async () => [
        {
          provider: 'claude',
          name: 'work',
          label: 'Work',
          createdAt: '2026-08-09T12:00:00.000Z',
          updatedAt: '2026-08-09T12:00:00.000Z',
        },
      ]);
      const service = {
        inspectAccountBackup,
        restoreAccountBackup,
      } as unknown as AnyPickApp['accounts'];
      const manager = new TrayBackupInspectionManager();

      const preview = await manager.inspect(service, path);
      const stagedPath = inspectAccountBackup.mock.calls[0]?.[0];
      expect(stagedPath).toBeTypeOf('string');
      expect(stagedPath).not.toBe(path);
      expect(await readFile(stagedPath, 'utf8')).toBe(previewedBytes);
      expect(preview.entries).toEqual([
        {
          id: '0',
          providerId: 'claude',
          name: 'work',
          label: 'Work',
          disposition: 'replace',
        },
      ]);
      expect(JSON.stringify(preview)).not.toContain(path);
      expect(JSON.stringify(preview)).not.toContain('credential-payload');
      await writeFile(path, 'replacement-that-must-not-be-restored');
      await expect(manager.restore(service, preview.inspectionId, ['0'], false)).rejects.toThrow();
      await expect(manager.restore(service, preview.inspectionId, ['0'], true)).resolves.toBe(1);
      expect(inspectAccountBackup).toHaveBeenLastCalledWith(stagedPath);
      expect(restoreAccountBackup).toHaveBeenCalledWith(
        stagedPath,
        [{ provider: 'claude', name: 'work' }],
        { replace: [{ provider: 'claude', name: 'work' }] },
      );
      await expect(access(stagedPath)).rejects.toThrow();

      const fixedError = manager.errorResult('request-1');
      expect(fixedError).toEqual({
        version: 1,
        requestId: 'request-1',
        status: 'error',
        error: 'inspection-failed',
        code: 'BACKUP_INSPECT_FAILED',
        reason: 'damaged',
        message:
          'AnyPick could not inspect that backup. Choose a valid AnyPick backup file and try again.',
      });
      expect(JSON.stringify(fixedError)).not.toContain(path);
      expect(
        manager.errorResult(
          'request-version',
          new AnyPickError('unsupported backup', 'BACKUP_VERSION'),
        ),
      ).toMatchObject({ reason: 'newer-version' });
      expect(
        manager.errorResult(
          'request-permission',
          Object.assign(new Error('denied'), { code: 'EACCES' }),
        ),
      ).toMatchObject({ reason: 'permission-denied' });
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('expires process-local backup inspections after ten minutes', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-tray-inspection-expiry-'));
    const path = join(root, 'accounts.anypick-backup');
    await writeFile(path, 'stable-version');
    try {
      let now = 1_000;
      const service = {
        inspectAccountBackup: vi.fn(async () => ({
          createdAt: '2026-08-09T12:00:00.000Z',
          entries: [
            {
              provider: 'claude',
              name: 'work',
              label: 'Work',
              disposition: 'create' as const,
            },
          ],
        })),
        restoreAccountBackup: vi.fn(),
      } as unknown as AnyPickApp['accounts'];
      const manager = new TrayBackupInspectionManager(() => now);
      const preview = await manager.inspect(service, path);
      const stagedPath = vi.mocked(service.inspectAccountBackup).mock.calls[0]?.[0];
      now += 10 * 60 * 1000;

      await expect(
        manager.restore(service, preview.inspectionId, ['0'], false),
      ).rejects.toMatchObject({ code: 'BACKUP_INSPECTION_INVALID' });
      expect(service.restoreAccountBackup).not.toHaveBeenCalled();
      await expect(access(stagedPath)).rejects.toThrow();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('cleans private staging when backup inspection fails', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-tray-inspection-failure-'));
    const path = join(root, 'accounts.anypick-backup');
    await writeFile(path, 'invalid-version');
    let stagedPath: string | undefined;
    try {
      const service = {
        inspectAccountBackup: vi.fn(async (candidate: string) => {
          stagedPath = candidate;
          throw new AnyPickError('private parser detail', 'BACKUP_FORMAT');
        }),
        restoreAccountBackup: vi.fn(),
      } as unknown as AnyPickApp['accounts'];
      const manager = new TrayBackupInspectionManager();

      await expect(manager.inspect(service, path)).rejects.toThrow('private parser detail');
      expect(stagedPath).not.toBe(path);
      if (!stagedPath) {
        throw new Error('Inspection service did not receive a staged path.');
      }
      await expect(access(stagedPath)).rejects.toThrow();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('cleans private staging after a failed final restore', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-tray-restore-failure-'));
    const path = join(root, 'accounts.anypick-backup');
    await writeFile(path, 'stable-version');
    try {
      const inspectAccountBackup = vi.fn(async (_candidate: string) => ({
        createdAt: '2026-08-09T12:00:00.000Z',
        entries: [
          {
            provider: 'codex',
            name: 'personal',
            label: 'Personal',
            disposition: 'create' as const,
          },
        ],
      }));
      const service = {
        inspectAccountBackup,
        restoreAccountBackup: vi.fn(async () => {
          throw new AnyPickError('private restore detail', 'BACKUP_RESTORE_FAILED');
        }),
      } as unknown as AnyPickApp['accounts'];
      const manager = new TrayBackupInspectionManager();
      const preview = await manager.inspect(service, path);
      const stagedPath = inspectAccountBackup.mock.calls[0]?.[0];

      await expect(manager.restore(service, preview.inspectionId, ['0'], false)).rejects.toThrow(
        'private restore detail',
      );
      await expect(access(stagedPath)).rejects.toThrow();
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('never throws for invalid runtime input', () => {
    expect(() => decodeTrayCommand(null as unknown as string)).not.toThrow();
    expect(decodeTrayCommand(null as unknown as string)).toBeUndefined();
  });
});
