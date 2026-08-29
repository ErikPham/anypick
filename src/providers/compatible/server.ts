import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import type { CompatibleProviderDefinition } from '../../types';
import { assertLoopbackHost } from '../../utils/network';
import {
  anthropicToOpenAI,
  estimateAnthropicInputTokens,
  openAIToAnthropic,
  pipeOpenAIStreamToAnthropic,
  type AnthropicMessageRequest,
  type OpenAIChatResponse,
} from '../protocol/anthropic';
import {
  openAIChatToResponses,
  openAIResponsesToChat,
  writeOpenAIResponsesStream,
  type OpenAIResponsesRequest,
} from '../protocol/openai-responses';
import {
  linkProxyRequestAbort,
  normalizeLocalApiUrl,
  readBody,
  requireProxyAuth,
  writeResponseChunk,
} from '../proxy-shared';
import type { QuotaGuardOptions } from '../quota-guard';
import { CompatibleBackend } from './backend';
import {
  checkCors,
  ClientRequestError,
  json,
  modelIds,
  parseJson,
  parseRequestJson,
  pipeResponse,
  safeError,
} from './http';

export interface CompatibleProxyServerOptions {
  host: string;
  port: number;
  definition: CompatibleProviderDefinition;
  authDirs: readonly string[];
  accountNames?: readonly string[];
  token?: string;
  quotaGuard?: QuotaGuardOptions;
  log?: (line: string) => void;
  quiet?: boolean;
}

export function createCompatibleProxyServer(opts: CompatibleProxyServerOptions): Server {
  assertLoopbackHost(opts.host);
  const token = opts.token ?? process.env.ANYPICK_PROXY_TOKEN ?? '';
  const log = opts.log ?? (opts.quiet ? () => {} : (line) => process.stderr.write(`${line}\n`));
  const backend = new CompatibleBackend({
    definition: opts.definition,
    authDirs: opts.authDirs,
    accountNames: opts.accountNames,
    quotaGuard: opts.quotaGuard,
    log,
  });
  const definition = backend.definition;

  const server = createServer((req, res) => {
    void handle(req, res).catch((error) => {
      const message = error instanceof Error ? error.message : String(error);
      log(`✗ ${req.method ?? 'GET'} ${req.url ?? '/'}: ${message}`);
      if (!res.headersSent) {
        const clientError = error instanceof ClientRequestError;
        json(res, clientError ? 400 : 502, {
          error: { type: clientError ? 'invalid_request_error' : 'proxy_error', message },
        });
      } else if (!res.writableEnded) {
        res.end();
      }
    });
  });

  async function handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const url = normalizeLocalApiUrl(req.url ?? '/');
    const path = url.split('?')[0] ?? '/';
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
        service: 'anypick-compatible-proxy',
        provider: definition.id,
        compatibility: 'OpenAI + Anthropic API',
        instanceId: process.env.ANYPICK_INSTANCE_ID ?? null,
      });
      return;
    }
    if (!path.startsWith('/v1/') && path !== '/v1') {
      json(res, 404, { error: { type: 'not_found_error', message: `Not found: ${path}` } });
      return;
    }
    if (!requireProxyAuth(req, res, token)) {
      return;
    }

    if (method === 'POST' && path === '/v1/messages/count_tokens') {
      const body = parseRequestJson(await readBody(req));
      json(res, 200, { input_tokens: estimateAnthropicInputTokens(body) });
      return;
    }
    const abort = linkProxyRequestAbort(req, res);
    try {
      if ((method === 'GET' || method === 'HEAD') && path === '/v1/models') {
        await listModels(req, res, method, abort.signal);
        return;
      }
      if (method === 'POST' && path === '/v1/messages') {
        await handleMessages(req, res, abort.signal);
        return;
      }
      if (method === 'POST' && path === '/v1/chat/completions') {
        await passOpenAI(req, res, await readBody(req), '/chat/completions', abort.signal);
        return;
      }
      if (method === 'POST' && path === '/v1/responses') {
        await handleResponses(req, res, abort.signal);
        return;
      }
      json(res, 404, { error: { type: 'not_found_error', message: `Not found: ${path}` } });
    } finally {
      abort.dispose();
    }
  }

  async function listModels(
    req: IncomingMessage,
    res: ServerResponse,
    method: string,
    signal: AbortSignal,
  ): Promise<void> {
    try {
      const response = await backend.fetch('openai', req, '/models', { method, signal });
      if (response.ok) {
        const models = modelIds(await response.text());
        if (models.length) {
          json(res, 200, {
            object: 'list',
            data: models.map((model) => ({ id: model, object: 'model', owned_by: definition.id })),
          });
          return;
        }
      }
    } catch (error) {
      log(`models ✗ ${error instanceof Error ? error.message : String(error)}`);
    }
    json(res, 200, {
      object: 'list',
      data: definition.models.map((model) => ({
        id: model,
        object: 'model',
        owned_by: definition.id,
      })),
    });
  }

  async function handleMessages(
    req: IncomingMessage,
    res: ServerResponse,
    signal: AbortSignal,
  ): Promise<void> {
    const raw = await readBody(req);
    const body = parseRequestJson(raw) as unknown as AnthropicMessageRequest;
    if (!body.model) {
      json(res, 400, {
        type: 'error',
        error: { type: 'invalid_request_error', message: 'model is required' },
      });
      return;
    }
    // A provider pool may mix pay-as-you-go and subscription profiles. Use the
    // native route only when every candidate can participate; otherwise Chat
    // translation keeps failover available across the complete credential ring.
    if (await backend.allSupportAnthropic()) {
      const response = await backend.fetch('anthropic', req, '/v1/messages', {
        method: 'POST',
        body: new Uint8Array(raw),
        signal,
      });
      await pipeResponse(response, res);
      return;
    }

    const chat = anthropicToOpenAI({ ...body, max_tokens: body.max_tokens ?? 4096 });
    chat.stream = Boolean(body.stream);
    const payload = Buffer.from(JSON.stringify(chat), 'utf8');
    const response = await backend.fetch('openai', req, '/chat/completions', {
      method: 'POST',
      body: new Uint8Array(payload),
      signal,
    });
    if (!response.ok) {
      json(res, response.status, {
        type: 'error',
        error: safeError(await response.text(), response.status),
      });
      return;
    }
    if (body.stream) {
      res.writeHead(200, {
        'content-type': 'text/event-stream; charset=utf-8',
        'cache-control': 'no-cache, no-transform',
        connection: 'keep-alive',
      });
      await pipeOpenAIStreamToAnthropic(
        response,
        (chunk) => writeResponseChunk(res, chunk, signal),
        body.model,
        undefined,
        { signal },
      );
      res.end();
      return;
    }
    const text = await response.text();
    const result = parseJson(text) as OpenAIChatResponse;
    const converted = openAIToAnthropic(result, body.model);
    json(res, 200, converted);
  }

  async function passOpenAI(
    req: IncomingMessage,
    res: ServerResponse,
    body: Buffer,
    suffix: string,
    signal: AbortSignal,
  ): Promise<void> {
    const response = await backend.fetch('openai', req, suffix, {
      method: 'POST',
      body: new Uint8Array(body),
      signal,
    });
    await pipeResponse(response, res);
  }

  async function handleResponses(
    req: IncomingMessage,
    res: ServerResponse,
    signal: AbortSignal,
  ): Promise<void> {
    const raw = await readBody(req);
    if (await backend.allSupportNativeResponses()) {
      const response = await backend.fetch('openai', req, '/responses', {
        method: 'POST',
        body: new Uint8Array(raw),
        signal,
      });
      await pipeResponse(response, res);
      return;
    }
    const body = parseRequestJson(raw) as OpenAIResponsesRequest;
    if (!body.model) {
      json(res, 400, { error: { type: 'invalid_request_error', message: 'model is required' } });
      return;
    }
    const wantsStream = body.stream === true;
    const chat = openAIResponsesToChat(body, body.model);
    const payload = Buffer.from(JSON.stringify(chat), 'utf8');
    const response = await backend.fetch('openai', req, '/chat/completions', {
      method: 'POST',
      body: new Uint8Array(payload),
      signal,
    });
    const text = await response.text();
    if (!response.ok) {
      const error = safeError(text, response.status);
      if (wantsStream) {
        res.writeHead(200, {
          'content-type': 'text/event-stream; charset=utf-8',
          'cache-control': 'no-cache, no-transform',
        });
        const failed = {
          id: `resp_error_${Date.now().toString(36)}`,
          object: 'response',
          status: 'failed',
          error,
          output: [],
        };
        res.end(
          `event: response.failed\ndata: ${JSON.stringify({ type: 'response.failed', response: failed })}\n\n`,
        );
      } else {
        json(res, response.status, { error });
      }
      return;
    }
    const converted = openAIChatToResponses(parseJson(text), body.model);
    if (wantsStream) {
      writeOpenAIResponsesStream(res, converted);
    } else {
      json(res, 200, converted);
    }
  }

  return server;
}

export async function listenCompatibleProxy(
  opts: CompatibleProxyServerOptions,
): Promise<{ server: Server; endpoint: string; port: number }> {
  const server = createCompatibleProxyServer(opts);
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(opts.port, opts.host, () => {
      const address = server.address();
      if (!address || typeof address === 'string') {
        reject(new Error('Failed to bind compatible proxy'));
        return;
      }
      resolve({ server, endpoint: `http://${opts.host}:${address.port}`, port: address.port });
    });
  });
}
