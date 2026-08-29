import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { CodexProvider } from '../src/providers/codex';
import { inspectCodexCredentialStore } from '../src/providers/codex-credential-store';

const dirs: string[] = [];

afterEach(async () => {
  await Promise.all(dirs.splice(0).map((dir) => rm(dir, { recursive: true, force: true })));
});

async function makeHome(): Promise<string> {
  const root = await mkdtemp(join(tmpdir(), 'anypick-codex-store-'));
  dirs.push(root);
  await mkdir(join(root, '.codex'), { recursive: true });
  return root;
}

describe('Codex CLI credential store', () => {
  it('uses the documented file-store default when no config exists', async () => {
    const root = await makeHome();
    await expect(inspectCodexCredentialStore(join(root, '.codex'))).resolves.toMatchObject({
      mode: 'file',
      supportsSnapshotSwitch: true,
    });
  });

  it('reads only the root credential-store setting', async () => {
    const root = await makeHome();
    const config = join(root, '.codex', 'config.toml');
    await writeFile(
      config,
      [
        'cli_auth_credentials_store = "keyring"',
        '',
        '[model_providers.demo]',
        'cli_auth_credentials_store = "file"',
      ].join('\n'),
    );

    await expect(inspectCodexCredentialStore(join(root, '.codex'))).resolves.toMatchObject({
      mode: 'keyring',
      supportsSnapshotSwitch: false,
    });
  });

  it.each(['keyring', 'auto'] as const)(
    'fails closed for %s instead of treating auth.json as the live credential',
    async (mode) => {
      const root = await makeHome();
      const codexDir = join(root, '.codex');
      const authPath = join(codexDir, 'auth.json');
      await writeFile(join(codexDir, 'config.toml'), `cli_auth_credentials_store = "${mode}"\n`);
      await writeFile(
        authPath,
        JSON.stringify({ auth_mode: 'chatgpt', tokens: { access_token: 'old' } }),
      );
      const provider = new CodexProvider(root);

      await expect(provider.detectLive()).resolves.toMatchObject({ present: false });
      await expect(provider.restore(codexDir)).rejects.toMatchObject({
        code: 'CODEX_CREDENTIAL_STORE_UNSUPPORTED',
      });
      await expect(readFile(authPath, 'utf8')).resolves.toContain('"old"');
    },
  );
});
