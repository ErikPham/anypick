import { createHash } from 'node:crypto';
import { join } from 'node:path';
import {
  copyFileSafe,
  ensureDir,
  pathExists,
  readJsonFile,
  removePath,
  writeJsonFile,
} from '../utils/fs';
import { readGeminiApiKeyFromEnvFile } from './gemini-env';

const RUNTIME_AUTH_FILES = [
  '.env',
  'oauth_creds.json',
  'google_accounts.json',
  'antigravity_oauth.json',
] as const;

interface RuntimeAuthOrigin {
  version: 1;
  sourceFingerprint: string;
}

function hash(value: string): string {
  return createHash('sha256').update(value).digest('hex');
}

/** Identify a Gemini login by durable secret material, never its rotating access token. */
export async function geminiAuthFingerprint(dir: string): Promise<string | null> {
  const parts: string[] = [];
  const oauthPath = join(dir, 'oauth_creds.json');
  if (await pathExists(oauthPath)) {
    try {
      const creds = await readJsonFile<Record<string, unknown>>(oauthPath);
      const refreshToken = creds.refresh_token;
      if (typeof refreshToken === 'string' && refreshToken.trim()) {
        parts.push(`rt:${hash(refreshToken.trim())}`);
      } else {
        // Access-only snapshots are still isolated. The source file is stable,
        // while a refreshed runtime copy can rotate independently.
        parts.push(`oauth:${hash(JSON.stringify(creds))}`);
      }
    } catch {
      // Unreadable credential is no credential.
    }
  }

  const key = await readGeminiApiKeyFromEnvFile(join(dir, '.env')).catch(() => undefined);
  if (key) {
    parts.push(`key:${hash(key)}`);
  }

  const antigravityPath = join(dir, 'antigravity_oauth.json');
  if (await pathExists(antigravityPath)) {
    try {
      const payload = await readJsonFile<Record<string, unknown>>(antigravityPath);
      const token = payload.token;
      const refreshToken =
        token && typeof token === 'object'
          ? (token as Record<string, unknown>).refresh_token
          : payload.refresh_token;
      parts.push(
        typeof refreshToken === 'string' && refreshToken.trim()
          ? `ag-rt:${hash(refreshToken.trim())}`
          : `ag:${hash(JSON.stringify(payload))}`,
      );
    } catch {
      // Unreadable credential is no credential.
    }
  }

  return parts.length > 0 ? parts.toSorted().join('|') : null;
}

/**
 * Copy an authoritative Gemini snapshot into owner-only runtime storage.
 * OAuth refreshes may mutate this copy, but never the snapshot or live CLI files.
 */
export async function materializeGeminiRuntimeAuth(
  sourceDir: string,
  runtimeRoot: string,
  slot: string,
): Promise<string> {
  const sourceFingerprint = await geminiAuthFingerprint(sourceDir);
  if (!sourceFingerprint) {
    throw new Error(`Gemini auth source has no usable credentials: ${sourceDir}`);
  }

  const runtimeDir = join(runtimeRoot, 'gemini-auth', slot);
  const originPath = join(runtimeRoot, 'gemini-auth', `${slot}.origin.json`);
  let canReuse = false;
  if ((await pathExists(runtimeDir)) && (await pathExists(originPath))) {
    try {
      const origin = await readJsonFile<RuntimeAuthOrigin>(originPath);
      canReuse = origin.version === 1 && origin.sourceFingerprint === sourceFingerprint;
    } catch {
      canReuse = false;
    }
  }
  if (canReuse) {
    return runtimeDir;
  }

  await removePath(runtimeDir);
  await ensureDir(runtimeDir);
  for (const name of RUNTIME_AUTH_FILES) {
    await copyFileSafe(join(sourceDir, name), join(runtimeDir, name), 0o600);
  }
  await writeJsonFile(
    originPath,
    { version: 1, sourceFingerprint } satisfies RuntimeAuthOrigin,
    0o600,
  );
  return runtimeDir;
}
