import type { IncomingMessage } from 'node:http';
import type { CompatibleAuthScheme, CompatibleProviderDefinition } from '../../types';
import { readJsonFile } from '../../utils/fs';
import { isForwardableUpstreamHeader } from '../proxy-shared';
import { QuotaGuard, type QuotaGuardOptions } from '../quota-guard';
import {
  isCredentialQuotaFailure,
  isInsufficientBalanceFailure,
  parseRetryAfter,
} from '../upstream-policy';
import {
  COMPATIBLE_CREDENTIAL_FILE,
  endpointProfile,
  trimBaseUrl,
  type StoredCompatibleCredential,
  validateCompatibleProviderDefinition,
} from './definition';

export type CompatibleProtocol = 'openai' | 'anthropic';

export interface CompatibleCredentialCandidate {
  accountName?: string;
  stored: StoredCompatibleCredential;
  openaiBase: string;
  openaiAuth: CompatibleAuthScheme;
  nativeResponses: boolean;
  anthropicBase?: string;
  anthropicAuth: CompatibleAuthScheme;
}

export interface CompatibleBackendOptions {
  definition: CompatibleProviderDefinition;
  authDirs: readonly string[];
  accountNames?: readonly string[];
  quotaGuard?: QuotaGuardOptions;
  log?: (line: string) => void;
}

/**
 * Credential-aware upstream backend shared by every compatible protocol route.
 *
 * Keeping key loading, auth injection, cooldown ordering, and failover here
 * prevents the HTTP route layer from growing provider-specific behavior. A
 * declarative plugin supplies data; this backend owns the secret boundary.
 */
export class CompatibleBackend {
  readonly definition: CompatibleProviderDefinition;
  private readonly quotaGuard: QuotaGuard;
  private readonly log: (line: string) => void;
  private ringPromise: Promise<CompatibleCredentialCandidate[]> | undefined;

  constructor(private readonly options: CompatibleBackendOptions) {
    this.definition = validateCompatibleProviderDefinition(options.definition);
    this.log = options.log ?? (() => {});
    this.quotaGuard = new QuotaGuard(
      options.quotaGuard ?? { enabled: false, cooldownMs: 60 * 60_000 },
    );
  }

  candidates(): Promise<CompatibleCredentialCandidate[]> {
    return (this.ringPromise ??= Promise.all(
      this.options.authDirs.map(async (dir, index): Promise<CompatibleCredentialCandidate> => {
        const stored = await readJsonFile<StoredCompatibleCredential>(
          `${dir}/${COMPATIBLE_CREDENTIAL_FILE}`,
        );
        const profile = endpointProfile(this.definition, stored.kind, stored.profile);
        if (!profile || stored.version !== 1 || !stored.secret) {
          throw new Error(`Invalid ${this.definition.id} credential snapshot at ${dir}`);
        }
        return {
          accountName: this.options.accountNames?.[index],
          stored,
          openaiBase: trimBaseUrl(profile.openaiBase),
          openaiAuth: profile.openaiAuth ?? 'bearer',
          nativeResponses: profile.nativeResponses ?? false,
          anthropicBase: profile.anthropicBase ? trimBaseUrl(profile.anthropicBase) : undefined,
          anthropicAuth: profile.anthropicAuth ?? 'x-api-key',
        };
      }),
    ));
  }

  async allSupportAnthropic(): Promise<boolean> {
    const candidates = await this.candidates();
    return candidates.length > 0 && candidates.every((candidate) => candidate.anthropicBase);
  }

  async allSupportNativeResponses(): Promise<boolean> {
    const candidates = await this.candidates();
    return candidates.length > 0 && candidates.every((candidate) => candidate.nativeResponses);
  }

  async fetch(
    protocol: CompatibleProtocol,
    req: IncomingMessage,
    suffix: string,
    init: RequestInit,
  ): Promise<Response> {
    const ordered = await this.quotaGuard.ordered(await this.candidates());
    if (!ordered.length) {
      return jsonResponse(429, 'All accounts are cooling down.');
    }
    let last: Response | undefined;
    for (const [index, candidate] of ordered.entries()) {
      const base = protocol === 'anthropic' ? candidate.anthropicBase : candidate.openaiBase;
      if (!base) {
        continue;
      }
      this.log(
        `${init.method ?? 'GET'} ${suffix} → ${this.definition.id}/${candidate.stored.profile}`,
      );
      const response = await fetch(`${base}${suffix}`, {
        ...init,
        headers: upstreamHeaders(req, candidate, protocol, init.body != null),
        redirect: 'manual',
        signal: init.signal ?? AbortSignal.timeout(600_000),
      });
      if (response.status < 400) {
        await this.quotaGuard.activate(candidate.accountName);
        return response;
      }
      const bytes = await response.arrayBuffer();
      const text = new TextDecoder().decode(bytes);
      last = new Response(bytes, { status: response.status, headers: response.headers });
      const next = ordered[index + 1];
      if (!next || !this.quotaGuard.enabled || !this.isExhausted(response, text)) {
        return last;
      }
      await this.quotaGuard.exhausted(
        candidate.accountName,
        next.accountName,
        parseRetryAfter(response.headers.get('retry-after')),
      );
      this.log(
        `↻ ${candidate.accountName ?? index + 1} exhausted → ${next.accountName ?? index + 2}`,
      );
    }
    return last ?? jsonResponse(503, 'No usable credential.');
  }

  private isExhausted(response: Response, body: string): boolean {
    if (response.status === 401 || response.status === 402) {
      return true;
    }
    if (isInsufficientBalanceFailure(response.status, body)) {
      return true;
    }
    if (isCredentialQuotaFailure(response.status, response.headers, body)) {
      return true;
    }
    return (this.definition.quotaErrorPatterns ?? []).some((pattern) =>
      new RegExp(pattern, 'iu').test(body.slice(0, 4000)),
    );
  }
}

function upstreamHeaders(
  req: IncomingMessage,
  candidate: CompatibleCredentialCandidate,
  protocol: CompatibleProtocol,
  hasBody: boolean,
): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const [name, value] of Object.entries(req.headers)) {
    const lower = name.toLowerCase();
    if (value == null || !isForwardableUpstreamHeader(lower)) {
      continue;
    }
    headers[name] = Array.isArray(value) ? value.join(', ') : String(value);
  }
  if (hasBody && !headers['content-type']) {
    headers['content-type'] = 'application/json';
  }
  const scheme = protocol === 'openai' ? candidate.openaiAuth : candidate.anthropicAuth;
  applyAuth(headers, scheme, candidate.stored.secret);
  if (protocol === 'anthropic') {
    headers['anthropic-version'] = headers['anthropic-version'] ?? '2023-06-01';
  }
  return headers;
}

function applyAuth(
  headers: Record<string, string>,
  scheme: CompatibleAuthScheme,
  secret: string,
): void {
  if (scheme === 'bearer') {
    headers.authorization = `Bearer ${secret}`;
  } else {
    headers[scheme] = secret;
  }
}

function jsonResponse(status: number, message: string): Response {
  return new Response(JSON.stringify({ error: { message } }), {
    status,
    headers: { 'content-type': 'application/json' },
  });
}
