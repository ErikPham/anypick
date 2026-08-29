/**
 * Authentication for the Codex Responses proxy.
 *
 * The proxy reads a private runtime copy of a saved Codex auth.json. OAuth
 * rotation is persisted only to that runtime copy; the account snapshot stays
 * under AccountService's coordinator/commit discipline (ADR 0004 / 0009).
 */

import { pathExists, readJsonFile, writeJsonFile } from '../../utils/fs';
import { AnyPickError } from '../../utils/errors';
import { refreshCodexAuth, type CodexAuthFile } from '../codex-refresh';

export type CodexProxyAuthMode = 'chatgpt' | 'api-key';

export interface CodexProxySession {
  mode: CodexProxyAuthMode;
  accessToken: string;
  accountId?: string;
  auth: CodexAuthFile;
}

const REFRESH_SKEW_MS = 2 * 60_000;

export async function loadCodexProxySession(authPath: string): Promise<CodexProxySession> {
  if (!(await pathExists(authPath))) {
    throw new AnyPickError('Codex proxy auth.json is missing.', 'NO_LIVE_AUTH');
  }

  let auth: CodexAuthFile;
  try {
    auth = await readJsonFile<CodexAuthFile>(authPath);
  } catch {
    throw new AnyPickError('Codex proxy auth.json is unreadable.', 'INVALID_CREDENTIAL');
  }

  const apiKey = auth.OPENAI_API_KEY?.trim();
  if (apiKey) {
    return { mode: 'api-key', accessToken: apiKey, auth };
  }

  const accessToken = auth.tokens?.access_token?.trim();
  const accountId = auth.tokens?.account_id?.trim();
  if (!accessToken) {
    throw new AnyPickError(
      'Codex account has no access token. Refresh it or sign in again with `codex login`.',
      'INVALID_CREDENTIAL',
    );
  }
  if (!accountId) {
    throw new AnyPickError(
      'Codex ChatGPT account has no account_id in auth.json. Sign in again with `codex login`.',
      'INVALID_CREDENTIAL',
    );
  }
  return { mode: 'chatgpt', accessToken, accountId, auth };
}

export async function ensureCodexProxySession(
  authPath: string,
  cached?: CodexProxySession,
  opts: { fetchFn?: typeof fetch; persist?: boolean } = {},
): Promise<CodexProxySession> {
  const session = cached ?? (await loadCodexProxySession(authPath));
  if (session.mode !== 'chatgpt' || !tokenNeedsRefresh(session.accessToken)) {
    return session;
  }
  if (!session.auth.tokens?.refresh_token) {
    return session;
  }
  return refreshCodexProxySession(authPath, session, opts);
}

export async function refreshCodexProxySession(
  authPath: string,
  session: CodexProxySession,
  opts: { fetchFn?: typeof fetch; persist?: boolean } = {},
): Promise<CodexProxySession> {
  if (session.mode !== 'chatgpt') {
    return session;
  }
  const { auth } = await refreshCodexAuth(session.auth, opts.fetchFn ?? fetch);
  const accessToken = auth.tokens?.access_token?.trim();
  const accountId = auth.tokens?.account_id?.trim() ?? session.accountId;
  if (!accessToken || !accountId) {
    throw new AnyPickError(
      'Codex OAuth refresh returned incomplete credentials.',
      'INVALID_CREDENTIAL',
    );
  }
  if (opts.persist !== false) {
    await writeJsonFile(authPath, auth, 0o600);
  }
  return { mode: 'chatgpt', accessToken, accountId, auth };
}

function tokenNeedsRefresh(token: string): boolean {
  const parts = token.split('.');
  if (parts.length < 2 || !parts[1]) {
    return false;
  }
  try {
    const payload = JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8')) as {
      exp?: unknown;
    };
    return (
      typeof payload.exp === 'number' &&
      Number.isFinite(payload.exp) &&
      payload.exp * 1000 <= Date.now() + REFRESH_SKEW_MS
    );
  } catch {
    return false;
  }
}
