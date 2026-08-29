import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export function resolveCodexProxyMain(): string {
  const here = fileURLToPath(new URL('.', import.meta.url));
  const primary = join(here, 'codex-proxy', 'main.js');
  if (existsSync(primary)) {
    return primary;
  }
  const fromSrc = join(here, '..', '..', 'dist', 'providers', 'codex-proxy', 'main.js');
  return existsSync(fromSrc) ? fromSrc : primary;
}
