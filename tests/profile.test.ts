import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createApp } from '../src/core/app';
import type { AnyPickApp } from '../src/core/app';
import { resolveRuntime } from '../src/clients/resolve';

describe('ProfileService', () => {
  let root: string;
  let app: AnyPickApp;

  beforeEach(async () => {
    root = await mkdtemp(join(tmpdir(), 'anypick-profile-'));
    app = createApp({ root });
  });

  afterEach(async () => {
    await rm(root, { recursive: true, force: true });
  });

  it('stores models on the profile at create time', async () => {
    const created = await app.profiles.create('Kiro Key - 1k', {
      provider: 'custom',
      endpoint: 'https://api.example.com',
      apiKey: 'sk-test',
      defaultModel: 'claude-sonnet-5',
      sonnetModel: 'claude-sonnet-5',
      opusModel: 'claude-opus-4-8',
      haikuModel: 'claude-haiku-4-5',
    });

    expect(created.meta.name).toBe('kiro-key-1k');
    expect(created.meta.label).toBe('Kiro Key - 1k');
    expect(created.meta.defaultModel).toBe('claude-sonnet-5');
    expect(created.meta.sonnetModel).toBe('claude-sonnet-5');
    expect(created.meta.opusModel).toBe('claude-opus-4-8');
    expect(created.meta.haikuModel).toBe('claude-haiku-4-5');
    expect(created.secrets.apiKey).toBe('sk-test');
  });

  it('edits models simply', async () => {
    await app.profiles.create('p1', {
      provider: 'custom',
      endpoint: 'https://x',
      apiKey: 'k',
      defaultModel: 'a',
    });
    const edited = await app.profiles.edit('p1', {
      defaultModel: 'b',
      sonnetModel: 's',
    });
    expect(edited.meta.defaultModel).toBe('b');
    expect(edited.meta.sonnetModel).toBe('s');
  });

  it('rejects unknown catalog provider', async () => {
    await expect(app.profiles.create('x', { provider: 'nope', apiKey: 'k' })).rejects.toThrow(
      /Unknown catalog provider/,
    );
  });

  it('keeps client override credentials in secrets and redacts legacy-shaped metadata', async () => {
    const created = await app.profiles.create('private-overlay', {
      provider: 'custom',
      clientOverrides: {
        claude: {
          apiKey: 'sk-client-secret',
          env: { ANTHROPIC_AUTH_TOKEN: 'token-secret', LOG_LEVEL: 'debug' },
        },
      },
    });

    expect(created.meta.clientOverrides?.claude).toEqual({ env: { LOG_LEVEL: 'debug' } });
    expect(created.secrets.clientOverrides?.claude).toEqual({
      apiKey: 'sk-client-secret',
      env: { ANTHROPIC_AUTH_TOKEN: 'token-secret' },
    });
    expect(resolveRuntime(created, 'claude').apiKey).toBe('sk-client-secret');

    const shown = app.profiles.redact(created);
    expect(JSON.stringify(shown)).not.toContain('client-secret');
    expect(JSON.stringify(shown)).not.toContain('token-secret');
  });
});
