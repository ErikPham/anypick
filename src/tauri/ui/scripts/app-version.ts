import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

/**
 * The Tauri app's marketing version, read from tauri.conf.json at build time.
 * `set-version` writes it alongside MARKETING_VERSION and Cargo.toml, so the
 * Settings footer shows the same string as the macOS About surfaces.
 */
export function readTauriVersion(repoRoot: string): string {
  const conf = JSON.parse(readFileSync(resolve(repoRoot, 'src/tauri/app/tauri.conf.json'), 'utf8')) as {
    version?: unknown;
  };
  if (typeof conf.version !== 'string' || conf.version === '') {
    throw new Error('src/tauri/app/tauri.conf.json has no version');
  }
  return conf.version;
}

export function appVersionDefine(repoRoot: string): Record<string, string> {
  return { __ANYPICK_VERSION__: JSON.stringify(readTauriVersion(repoRoot)) };
}
