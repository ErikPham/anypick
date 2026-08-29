/**
 * OpenAI Responses-compatible reverse proxy for saved Codex credentials.
 *
 * Codex custom providers call `/responses`; OpenAI-compatible clients commonly
 * call `/v1/responses`. The local boundary accepts both, authenticates with a
 * per-instance secret, then replaces that local credential with the saved
 * Codex API key or ChatGPT OAuth bearer + account id.
 */

import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { OPENAI_MODELS } from '../../catalog/providers';
import { assertLoopbackHost } from '../../utils/network';
import {
  HOP_BY_HOP,
  isForwardableUpstreamHeader,
  linkProxyRequestAbort,
  normalizeLocalApiUrl,
  readBody,
  requireProxyAuth,
} from '../proxy-shared';
import { ensureCodexProxySession, refreshCodexProxySession, type CodexProxySession } from './auth';

const CHATGPT_UPSTREAM = 'https://chatgpt.com/backend-api/codex';
const API_UPSTREAM = 'https://api.openai.com/v1';
const COMPATIBILITY = 'OpenAI Responses API';

export interface CodexProxyServerOptions {
  host: string;
  port: number;
  authPath: string;
  authPaths?: readonly string[];
  authAccountNames?: readonly string[];
  upstream?: string;
  token?: string;
  /** Runtime copies persist refreshes; Hub snapshot paths must remain read-only. */
  persistRefreshedAuth?: boolean;
  fetchFn?: typeof fetch;
  log?: (line: string) => void;
  quiet?: boolean;
}

interface AuthCandidate {
  path: string;
  name: string;
}

export function createCodexProxyServer(opts: CodexProxyServerOptions): Server {
  assertLoopbackHost(opts.host);
  const proxyToken = opts.token ?? process.env.ANYPICK_PROXY_TOKEN ?? '';
  const fetchFn = opts.fetchFn ?? fetch;
  const log =
    opts.log ?? (opts.quiet ? () => {} : (line: string) => process.stderr.write(`${line}\n`));
  const paths = [opts.authPath, ...(opts.authPaths ?? [])];
  const candidates = paths.map((path, index) => ({
    path,
    name: opts.authAccountNames?.[index] ?? `account-${index + 1}`,
  }));
  const sessions = new Map<string, CodexProxySession>();
  const sessionFlights = new Map<string, Promise<CodexProxySession>>();
  let nextCandidate = 0;

  const server = createServer((req, res) => {
    void handle(req, res).catch((error) => fail(res, error, log));
  });

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const normalized = normalizeLocalApiUrl(req.url ?? '/');
    const url = new URL(normalized, `http://${opts.host}`);
    const path = url.pathname;
    const method = req.method ?? 'GET';

    if (!checkCors(req, res)) {
      return;
    }
    if (method === 'OPTIONS') {
      res.writeHead(204);
      res.end();
      return;
    }
    if ((method === 'GET' || method === 'HEAD') && (path === '/' || path === '/health')) {
      json(res, 200, {
        ok: true,
        service: 'anypick-codex-proxy',
        compatibility: COMPATIBILITY,
        instanceId: process.env.ANYPICK_INSTANCE_ID ?? null,
        accounts: candidates.length,
        endpoints: ['/v1/responses', '/v1/models'],
      });
      return;
    }

    if (!path.startsWith('/v1/')) {
      json(res, 404, {
        error: { type: 'not_found_error', message: `Not found: ${path}` },
      });
      return;
    }
    // ADR 0006: no credentialed route may exercise upstream authority first.
    if (!requireProxyAuth(req, res, proxyToken)) {
      return;
    }

    const abort = linkProxyRequestAbort(req, res);
    try {
      if ((method === 'GET' || method === 'HEAD') && path === '/v1/models') {
        await proxyModels(req, res, url, abort.signal);
        return;
      }
      if ((method === 'GET' || method === 'HEAD') && path.startsWith('/v1/models/')) {
        const model = decodeURIComponent(path.slice('/v1/models/'.length));
        json(res, 200, modelMetadata(model));
        return;
      }
      if (method === 'POST' && path === '/v1/responses') {
        const body = await readBody(req);
        await proxyWithFailover(req, res, url, body, abort.signal);
        return;
      }

      json(res, 404, {
        error: {
          type: 'not_found_error',
          message: `Unsupported Codex proxy endpoint: ${path}`,
        },
      });
    } finally {
      abort.dispose();
    }
  }

  async function proxyModels(
    req: IncomingMessage,
    res: ServerResponse,
    url: URL,
    signal: AbortSignal,
  ): Promise<void> {
    const candidate = orderedCandidates()[0];
    if (!candidate) {
      throw new Error('Codex proxy has no account credentials');
    }
    try {
      let session = await sessionFor(candidate);
      let upstream = await requestUpstream(req, url, undefined, session, candidate, signal);
      if (upstream.status === 401 && session.mode === 'chatgpt') {
        await upstream.body?.cancel().catch(() => {});
        session = await refresh(candidate, session);
        upstream = await requestUpstream(req, url, undefined, session, candidate, signal);
      }
      if (upstream.ok) {
        await pipeResponse(upstream, res);
        return;
      }
      await upstream.body?.cancel().catch(() => {});
      log(`models ← ${upstream.status}; using bundled catalog`);
    } catch (error) {
      log(`models unavailable; using bundled catalog (${errorMessage(error)})`);
    }
    json(res, 200, bundledModels());
  }

  async function proxyWithFailover(
    req: IncomingMessage,
    res: ServerResponse,
    url: URL,
    body: Buffer,
    signal: AbortSignal,
  ): Promise<void> {
    const ordered = orderedCandidates();
    let last: Response | undefined;
    let lastError: unknown;

    for (const candidate of ordered) {
      try {
        let session = await sessionFor(candidate);
        let upstream = await requestUpstream(req, url, body, session, candidate, signal);
        if (upstream.status === 401 && session.mode === 'chatgpt') {
          await upstream.body?.cancel().catch(() => {});
          session = await refresh(candidate, session);
          upstream = await requestUpstream(req, url, body, session, candidate, signal);
        }
        if (upstream.status !== 429 || ordered.length === 1) {
          await pipeResponse(upstream, res);
          return;
        }
        log(`${candidate.name} rate limited; trying next Codex account`);
        await last?.body?.cancel().catch(() => {});
        last = upstream;
      } catch (error) {
        if (signal.aborted) {
          throw error;
        }
        lastError = error;
        log(`${candidate.name} failed: ${errorMessage(error)}`);
      }
    }

    if (last) {
      await pipeResponse(last, res);
      return;
    }
    throw lastError ?? new Error('No Codex account could serve the request');
  }

  function orderedCandidates(): AuthCandidate[] {
    if (candidates.length === 0) {
      return [];
    }
    const start = nextCandidate % candidates.length;
    nextCandidate = (nextCandidate + 1) % candidates.length;
    return [...candidates.slice(start), ...candidates.slice(0, start)];
  }

  async function sessionFor(candidate: AuthCandidate): Promise<CodexProxySession> {
    return singleflight(candidate, () =>
      ensureCodexProxySession(candidate.path, sessions.get(candidate.path), {
        fetchFn,
        persist: opts.persistRefreshedAuth,
      }),
    );
  }

  async function refresh(
    candidate: AuthCandidate,
    session: CodexProxySession,
  ): Promise<CodexProxySession> {
    const latest = sessions.get(candidate.path);
    if (latest && latest.accessToken !== session.accessToken) {
      return latest;
    }
    return singleflight(candidate, () =>
      refreshCodexProxySession(candidate.path, session, {
        fetchFn,
        persist: opts.persistRefreshedAuth,
      }),
    );
  }

  function singleflight(
    candidate: AuthCandidate,
    operation: () => Promise<CodexProxySession>,
  ): Promise<CodexProxySession> {
    const existing = sessionFlights.get(candidate.path);
    if (existing) {
      return existing;
    }
    const flight = operation()
      .then((session) => {
        sessions.set(candidate.path, session);
        return session;
      })
      .finally(() => {
        if (sessionFlights.get(candidate.path) === flight) {
          sessionFlights.delete(candidate.path);
        }
      });
    sessionFlights.set(candidate.path, flight);
    return flight;
  }

  async function requestUpstream(
    req: IncomingMessage,
    url: URL,
    body: Buffer | undefined,
    session: CodexProxySession,
    candidate: AuthCandidate,
    signal: AbortSignal,
  ): Promise<Response> {
    const upstream = (opts.upstream ?? defaultUpstream(session)).replace(/\/+$/u, '');
    const route = url.pathname === '/v1/models' ? '/models' : '/responses';
    const target = `${upstream}${route}${url.search}`;
    const headers = upstreamHeaders(req, session, body?.byteLength);
    log(`${req.method ?? 'GET'} ${url.pathname} → ${candidate.name}`);
    return fetchFn(target, {
      method: req.method,
      headers,
      body: body && body.byteLength > 0 ? new Uint8Array(body) : undefined,
      signal,
      redirect: 'manual',
    });
  }

  return server;
}

export async function listenCodexProxy(
  opts: CodexProxyServerOptions,
): Promise<{ server: Server; endpoint: string }> {
  const server = createCodexProxyServer(opts);
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject);
    server.listen(opts.port, opts.host, () => resolve());
  });
  const address = server.address();
  if (!address || typeof address === 'string') {
    throw new Error('Codex proxy did not bind a TCP address');
  }
  return { server, endpoint: `http://${opts.host}:${address.port}` };
}

function defaultUpstream(session: CodexProxySession): string {
  return session.mode === 'chatgpt' ? CHATGPT_UPSTREAM : API_UPSTREAM;
}

function upstreamHeaders(
  req: IncomingMessage,
  session: CodexProxySession,
  bodyLength?: number,
): Headers {
  const headers = new Headers();
  for (const [name, raw] of Object.entries(req.headers)) {
    const lower = name.toLowerCase();
    if (raw === undefined || !isForwardableUpstreamHeader(lower)) {
      continue;
    }
    headers.set(name, Array.isArray(raw) ? raw.join(', ') : raw);
  }
  headers.set('authorization', `Bearer ${session.accessToken}`);
  if (session.accountId) {
    headers.set('chatgpt-account-id', session.accountId);
  } else {
    headers.delete('chatgpt-account-id');
  }
  if (!headers.has('originator')) {
    headers.set('originator', 'anypick');
  }
  if (!headers.has('user-agent')) {
    headers.set('user-agent', 'anypick-codex-proxy/1');
  }
  if (bodyLength != null) {
    headers.set('content-length', String(bodyLength));
  }
  return headers;
}

async function pipeResponse(upstream: Response, res: ServerResponse): Promise<void> {
  const headers: Record<string, string> = {};
  upstream.headers.forEach((value, name) => {
    const lower = name.toLowerCase();
    if (!HOP_BY_HOP.has(lower) && lower !== 'content-encoding') {
      headers[name] = value;
    }
  });
  res.writeHead(upstream.status, headers);
  if (!upstream.body) {
    res.end();
    return;
  }
  const onClose = () => {
    void upstream.body?.cancel().catch(() => {});
  };
  res.once('close', onClose);
  const reader = upstream.body.getReader() as ReadableStreamDefaultReader<Uint8Array>;
  try {
    while (!res.writableEnded && !res.destroyed) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      if (value?.byteLength) {
        if (!res.write(Buffer.from(value)) && !(await waitForDrain(res))) {
          void reader.cancel().catch(() => {});
          break;
        }
      }
    }
  } finally {
    res.removeListener('close', onClose);
    try {
      reader.releaseLock();
    } catch {
      // already released
    }
    if (!res.writableEnded) {
      res.end();
    }
  }
}

function waitForDrain(res: ServerResponse): Promise<boolean> {
  if (res.writableEnded || res.destroyed || res.socket?.destroyed) {
    return Promise.resolve(false);
  }
  if (!res.writableNeedDrain) {
    return Promise.resolve(true);
  }
  return new Promise((resolve) => {
    const done = (ok: boolean) => {
      res.removeListener('drain', onDrain);
      res.removeListener('close', onFail);
      res.removeListener('error', onFail);
      resolve(ok);
    };
    const onDrain = () => done(true);
    const onFail = () => done(false);
    res.once('drain', onDrain);
    res.once('close', onFail);
    res.once('error', onFail);
  });
}

function bundledModels(): Record<string, unknown> {
  const models = [...new Set(Object.values(OPENAI_MODELS))];
  return {
    object: 'list',
    data: models.map((id) => modelMetadata(id)),
  };
}

function modelMetadata(id: string): Record<string, unknown> {
  return {
    id,
    slug: id,
    object: 'model',
    owned_by: 'openai',
    context_window: 400_000,
    max_context_window: 400_000,
    max_output_tokens: 128_000,
    input_modalities: ['text', 'image'],
    supports_parallel_tool_calls: true,
  };
}

function checkCors(req: IncomingMessage, res: ServerResponse): boolean {
  const origin = req.headers.origin;
  if (!origin) {
    return true;
  }
  try {
    const parsed = new URL(origin);
    const loopback =
      ['localhost', '127.0.0.1', '[::1]'].includes(parsed.hostname) &&
      (parsed.protocol === 'http:' || parsed.protocol === 'https:');
    if (!loopback) {
      json(res, 403, {
        error: { type: 'forbidden_origin', message: 'Only loopback browser origins are allowed.' },
      });
      return false;
    }
    res.setHeader('access-control-allow-origin', origin);
    res.setHeader('access-control-allow-headers', 'authorization, content-type, x-api-key');
    res.setHeader('access-control-allow-methods', 'GET, HEAD, POST, OPTIONS');
    res.setHeader('vary', 'Origin');
    return true;
  } catch {
    json(res, 403, {
      error: { type: 'forbidden_origin', message: 'Invalid browser origin.' },
    });
    return false;
  }
}

function json(res: ServerResponse, status: number, body: unknown): void {
  const raw = JSON.stringify(body);
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(raw),
  });
  res.end(raw);
}

function fail(res: ServerResponse, error: unknown, log: (line: string) => void): void {
  const message = errorMessage(error);
  log(`proxy error: ${message}`);
  if (!res.headersSent) {
    json(res, 502, { error: { type: 'proxy_error', message } });
  } else if (!res.writableEnded) {
    res.end();
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
