import type { IncomingMessage, ServerResponse } from 'node:http';
import { HOP_BY_HOP } from '../proxy-shared';

export class ClientRequestError extends Error {}

export async function pipeResponse(response: Response, res: ServerResponse): Promise<void> {
  const headers: Record<string, string> = {};
  response.headers.forEach((value, name) => {
    const lower = name.toLowerCase();
    if (!HOP_BY_HOP.has(lower) && lower !== 'content-encoding') {
      headers[name] = value;
    }
  });
  res.writeHead(response.status, headers);
  if (!response.body) {
    res.end();
    return;
  }
  const reader = response.body.getReader() as ReadableStreamDefaultReader<Uint8Array>;
  let clientGone = false;
  const onClose = () => {
    clientGone = true;
    void reader.cancel().catch(() => {});
  };
  res.once('close', onClose);
  try {
    while (!clientGone && !res.destroyed && !res.writableEnded) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      if (value?.byteLength && !res.write(Buffer.from(value)) && !(await waitForDrain(res))) {
        clientGone = true;
        void reader.cancel().catch(() => {});
        break;
      }
    }
  } finally {
    res.removeListener('close', onClose);
    try {
      reader.releaseLock();
    } catch {
      // Already released by cancellation.
    }
    if (!res.writableEnded) {
      res.end();
    }
  }
}

export function modelIds(text: string): string[] {
  let value: unknown;
  try {
    value = JSON.parse(text) as unknown;
  } catch {
    return [];
  }
  const list =
    value && typeof value === 'object' && !Array.isArray(value)
      ? ((value as { data?: unknown; models?: unknown }).data ??
        (value as { models?: unknown }).models)
      : value;
  if (!Array.isArray(list)) {
    return [];
  }
  return list
    .map((item) =>
      typeof item === 'string'
        ? item
        : item && typeof item === 'object'
          ? (item as { id?: unknown }).id
          : undefined,
    )
    .filter((item): item is string => typeof item === 'string' && item.trim().length > 0);
}

export function parseJson(value: Buffer | string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(
      typeof value === 'string' ? value : value.toString('utf8'),
    ) as unknown;
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
  } catch {
    // Throw one stable public error below.
  }
  throw new Error('Invalid JSON body');
}

export function parseRequestJson(value: Buffer | string): Record<string, unknown> {
  try {
    return parseJson(value);
  } catch {
    throw new ClientRequestError('Invalid JSON body');
  }
}

export function safeError(
  text: string,
  status: number,
): { message: string; type: string; code?: string } {
  try {
    const parsed = JSON.parse(text) as {
      error?: { message?: string; type?: string; code?: string };
      message?: string;
    };
    return {
      message: parsed.error?.message ?? parsed.message ?? `Upstream returned HTTP ${status}`,
      type: parsed.error?.type ?? 'api_error',
      ...(parsed.error?.code ? { code: parsed.error.code } : {}),
    };
  } catch {
    return { message: text.slice(0, 500) || `Upstream returned HTTP ${status}`, type: 'api_error' };
  }
}

export function checkCors(req: IncomingMessage, res: ServerResponse): boolean {
  const origin = req.headers.origin;
  if (origin) {
    try {
      const url = new URL(origin);
      if (!['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)) {
        throw new Error();
      }
      res.setHeader('access-control-allow-origin', origin);
      res.setHeader('vary', 'Origin');
    } catch {
      json(res, 403, { error: { type: 'forbidden_origin', message: 'Loopback origins only.' } });
      return false;
    }
  }
  res.setHeader('access-control-allow-methods', 'GET,POST,OPTIONS,HEAD');
  res.setHeader(
    'access-control-allow-headers',
    'authorization,content-type,x-api-key,anthropic-version,anthropic-beta,openai-beta',
  );
  return true;
}

export function json(res: ServerResponse, status: number, body: unknown): void {
  const raw = JSON.stringify(body);
  res.writeHead(status, {
    'content-type': 'application/json; charset=utf-8',
    'content-length': Buffer.byteLength(raw),
  });
  res.end(raw);
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
