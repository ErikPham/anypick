import { mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { materializeGeminiRuntimeAuth } from '../src/providers/gemini-runtime-auth';
import { materializeGrokRuntimeAuth } from '../src/providers/grok';

describe('provider runtime credential isolation', () => {
  it('preserves a rotated Grok runtime credential without changing its source snapshot', async () => {
    const root = await mkdtemp(join(tmpdir(), 'grok-runtime-auth-'));
    const sourcePath = join(root, 'snapshot', 'auth.json');
    const source = {
      'https://auth.x.ai::client': {
        key: 'source-access',
        refresh_token: 'source-refresh',
        oidc_issuer: 'https://auth.x.ai',
        oidc_client_id: 'client',
      },
    };
    await mkdir(join(root, 'snapshot'), { recursive: true });
    await writeFile(sourcePath, JSON.stringify(source));
    const runtimeDir = join(root, 'runtime');
    const runtimePath = await materializeGrokRuntimeAuth(sourcePath, runtimeDir);
    const rotated = structuredClone(source);
    rotated['https://auth.x.ai::client'].key = 'rotated-access';
    await writeFile(runtimePath, JSON.stringify(rotated));

    expect(await materializeGrokRuntimeAuth(sourcePath, runtimeDir)).toBe(runtimePath);
    expect(JSON.parse(await readFile(runtimePath, 'utf8'))).toEqual(rotated);
    expect(JSON.parse(await readFile(sourcePath, 'utf8'))).toEqual(source);
  });

  it('refreshes only a Gemini runtime copy and reuses it for the same durable login', async () => {
    const root = await mkdtemp(join(tmpdir(), 'gemini-runtime-auth-'));
    const sourceDir = join(root, 'snapshot');
    await mkdir(sourceDir, { recursive: true });
    const source = {
      access_token: 'source-access',
      refresh_token: 'durable-refresh',
      expiry_date: 1,
    };
    await writeFile(join(sourceDir, 'oauth_creds.json'), JSON.stringify(source));
    const runtimeRoot = join(root, 'runtime');
    const runtimeDir = await materializeGeminiRuntimeAuth(sourceDir, runtimeRoot, 'primary');
    const rotated = { ...source, access_token: 'rotated-access', expiry_date: Date.now() + 60_000 };
    await writeFile(join(runtimeDir, 'oauth_creds.json'), JSON.stringify(rotated));

    expect(await materializeGeminiRuntimeAuth(sourceDir, runtimeRoot, 'primary')).toBe(runtimeDir);
    expect(JSON.parse(await readFile(join(runtimeDir, 'oauth_creds.json'), 'utf8'))).toEqual(
      rotated,
    );
    expect(JSON.parse(await readFile(join(sourceDir, 'oauth_creds.json'), 'utf8'))).toEqual(source);
  });
});
