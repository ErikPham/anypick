import { join } from 'node:path';
import { readFile } from 'node:fs/promises';
import { pathExists, readJsonFile } from '../utils/fs';

export async function readIdentityFromDir(dir: string): Promise<string | undefined> {
  const accountsPath = join(dir, 'google_accounts.json');
  if (await pathExists(accountsPath)) {
    try {
      const data = await readJsonFile<{ active?: string }>(accountsPath);
      if (typeof data.active === 'string' && data.active.trim()) {
        return data.active.trim();
      }
    } catch {
      // fall through
    }
  }

  return readEnvRelayIdentity(join(dir, '.env'));
}

/** Host:port from a Gemini CLI .env so API-key relays have a stable display id. */
export async function readEnvRelayIdentity(envPath: string): Promise<string | undefined> {
  if (!(await pathExists(envPath))) {
    return undefined;
  }
  let raw: string;
  try {
    raw = await readFile(envPath, 'utf8');
  } catch {
    return undefined;
  }
  for (const line of raw.split(/\r?\n/)) {
    const match = line.match(/^(?:export\s+)?(?:GOOGLE_GEMINI_BASE_URL|GEMINI_API_BASE_URL)=(.*)$/);
    if (!match) {
      continue;
    }
    let value = (match[1] ?? '').trim();
    if (
      (value.startsWith('"') && value.endsWith('"')) ||
      (value.startsWith("'") && value.endsWith("'"))
    ) {
      value = value.slice(1, -1);
    }
    const host = hostFromBaseUrl(value);
    if (host) {
      return host;
    }
  }
  return undefined;
}

export function hostFromBaseUrl(value: string): string | undefined {
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  try {
    const url = new URL(trimmed);
    if (!url.hostname) {
      return undefined;
    }
    return url.port ? `${url.hostname}:${url.port}` : url.hostname;
  } catch {
    return undefined;
  }
}
