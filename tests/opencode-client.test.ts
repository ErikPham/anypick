import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { createOpenCodeClient } from '../src/clients/opencode';
import type { ApplyContext, ClientState } from '../src/types';

describe('OpenCode client adapter', () => {
  let root: string | undefined;

  afterEach(async () => {
    if (root) {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('merges JSONC config, stores the proxy key out of config, and restores defaults', async () => {
    root = await mkdtemp(join(tmpdir(), 'anypick-opencode-client-'));
    const home = join(root, 'home');
    const configDir = join(home, '.config', 'opencode');
    const configPath = join(configDir, 'opencode.jsonc');
    await import('node:fs/promises').then(({ mkdir }) => mkdir(configDir, { recursive: true }));
    await writeFile(
      configPath,
      '{\n  // user preference\n  "theme": "system",\n  "model": "openai/user-model",\n}\n',
    );
    const adapter = createOpenCodeClient(home);
    const ctx: ApplyContext = {
      profile: {
        meta: {
          name: 'proxy-gemini',
          provider: 'custom',
          createdAt: '',
          updatedAt: '',
          endpoint: 'http://127.0.0.1:4130',
          defaultModel: 'gemini-2.5-pro',
          models: {},
        },
        secrets: { apiKey: 'proxy-secret' },
        profileDir: '',
      },
      clientId: 'opencode',
      dryRun: false,
      verbose: false,
      anypickRoot: join(root, 'anypick'),
    };

    const applied = await adapter.apply(ctx);
    const config = JSON.parse(await readFile(configPath, 'utf8')) as Record<string, unknown>;
    expect(config).toMatchObject({
      theme: 'system',
      model: 'anypick-managed/gemini-2.5-pro',
      provider: {
        'anypick-managed': {
          npm: '@ai-sdk/openai-compatible',
          options: { baseURL: 'http://127.0.0.1:4130/v1' },
        },
      },
    });
    expect(JSON.stringify(config)).not.toContain('proxy-secret');
    expect(await readFile(join(configDir, '.anypick-api-key'), 'utf8')).toBe('proxy-secret');

    const state: ClientState = {
      clientId: 'opencode',
      mode: 'profile',
      updatedAt: new Date().toISOString(),
      managedPaths: applied.managedPaths,
      managedEnvKeys: [],
    };
    await adapter.reset(state);
    const reset = JSON.parse(await readFile(configPath, 'utf8')) as Record<string, unknown>;
    expect(reset).toEqual({ theme: 'system', model: 'openai/user-model' });
  });
});
