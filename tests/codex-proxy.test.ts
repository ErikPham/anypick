import { createServer, type IncomingMessage, type Server } from 'node:http';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { listenCodexProxy } from '../src/providers/codex-proxy/server';

const LOCAL_SECRET = 'local-codex-proxy-secret';
const LOCAL_AUTH = { authorization: `Bearer ${LOCAL_SECRET}` };

const servers: Server[] = [];
const dirs: string[] = [];

afterEach(async () => {
  for (const server of servers.splice(0)) {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
  for (const dir of dirs.splice(0)) {
    await rm(dir, { recursive: true, force: true });
  }
});

async function authFile(
  tokens: Record<string, unknown> = {
    access_token: 'chatgpt-access-token',
    refresh_token: 'chatgpt-refresh-token',
    account_id: 'acct-codex',
  },
): Promise<string> {
  const dir = await mkdtemp(join(tmpdir(), 'codex-proxy-test-'));
  dirs.push(dir);
  const path = join(dir, 'auth.json');
  await writeFile(path, JSON.stringify({ auth_mode: 'chatgpt', tokens }), { mode: 0o600 });
  return path;
}

async function upstream(
  handler: (req: IncomingMessage, body: string) => { status?: number; body: unknown },
): Promise<{ url: string }> {
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk: Buffer) => chunks.push(chunk));
    req.on('end', () => {
      const result = handler(req, Buffer.concat(chunks).toString('utf8'));
      res.writeHead(result.status ?? 200, { 'content-type': 'application/json' });
      res.end(JSON.stringify(result.body));
    });
  });
  servers.push(server);
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const address = server.address();
  if (!address || typeof address === 'string') {
    throw new Error('test upstream did not bind');
  }
  return { url: `http://127.0.0.1:${address.port}` };
}

describe('Codex Responses proxy', () => {
  it('replaces the local secret with ChatGPT auth and preserves the Responses body', async () => {
    let request:
      | {
          url?: string;
          authorization?: string;
          accountId?: string;
          localKey?: string;
          body: string;
        }
      | undefined;
    const remote = await upstream((req, body) => {
      request = {
        url: req.url,
        authorization: req.headers.authorization,
        accountId: req.headers['chatgpt-account-id'] as string | undefined,
        localKey: req.headers['x-api-key'] as string | undefined,
        body,
      };
      return {
        body: { id: 'resp_test', object: 'response', status: 'completed', output: [] },
      };
    });
    const { server, endpoint } = await listenCodexProxy({
      host: '127.0.0.1',
      port: 0,
      authPath: await authFile(),
      upstream: remote.url,
      token: LOCAL_SECRET,
      quiet: true,
    });
    servers.push(server);

    const body = { model: 'gpt-5.6-sol', input: 'hello', stream: false };
    const response = await fetch(`${endpoint}/responses`, {
      method: 'POST',
      headers: {
        ...LOCAL_AUTH,
        'content-type': 'application/json',
        'x-api-key': LOCAL_SECRET,
      },
      body: JSON.stringify(body),
    });

    expect(response.status).toBe(200);
    expect(request).toEqual({
      url: '/responses',
      authorization: 'Bearer chatgpt-access-token',
      accountId: 'acct-codex',
      localKey: undefined,
      body: JSON.stringify(body),
    });
    expect(JSON.stringify(request)).not.toContain(LOCAL_SECRET);
  });

  it('refreshes once after a 401 and persists only the proxy auth copy', async () => {
    const seenAuth: string[] = [];
    const remote = await upstream((req) => {
      seenAuth.push(req.headers.authorization ?? '');
      if (seenAuth.length === 1) {
        return { status: 401, body: { error: { message: 'expired' } } };
      }
      return {
        body: { id: 'resp_refreshed', object: 'response', status: 'completed', output: [] },
      };
    });
    const path = await authFile();
    const realFetch = fetch;
    const fetchFn: typeof fetch = async (input, init) => {
      const url = String(input);
      if (url.includes('/oauth/token')) {
        return new Response(
          JSON.stringify({
            access_token: 'refreshed-access-token',
            refresh_token: 'refreshed-refresh-token',
          }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        );
      }
      return realFetch(input, init);
    };
    const { server, endpoint } = await listenCodexProxy({
      host: '127.0.0.1',
      port: 0,
      authPath: path,
      upstream: remote.url,
      token: LOCAL_SECRET,
      fetchFn,
      persistRefreshedAuth: true,
      quiet: true,
    });
    servers.push(server);

    const response = await fetch(`${endpoint}/v1/responses`, {
      method: 'POST',
      headers: { ...LOCAL_AUTH, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-5.6-sol', input: 'hello' }),
    });

    expect(response.status).toBe(200);
    expect(seenAuth).toEqual(['Bearer chatgpt-access-token', 'Bearer refreshed-access-token']);
    const saved = JSON.parse(await readFile(path, 'utf8')) as {
      tokens?: { access_token?: string; refresh_token?: string };
    };
    expect(saved.tokens).toMatchObject({
      access_token: 'refreshed-access-token',
      refresh_token: 'refreshed-refresh-token',
    });
  });

  it('singleflights concurrent OAuth refreshes for one credential', async () => {
    const remote = await upstream((req) =>
      req.headers.authorization === 'Bearer old-access-token'
        ? { status: 401, body: { error: { message: 'expired' } } }
        : { body: { id: 'resp_ok', object: 'response', status: 'completed', output: [] } },
    );
    let refreshCalls = 0;
    const realFetch = fetch;
    const fetchFn: typeof fetch = async (input, init) => {
      if (String(input).includes('/oauth/token')) {
        refreshCalls += 1;
        await new Promise((resolve) => setTimeout(resolve, 25));
        return new Response(
          JSON.stringify({
            access_token: 'new-access-token',
            refresh_token: 'new-refresh-token',
          }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        );
      }
      return realFetch(input, init);
    };
    const { server, endpoint } = await listenCodexProxy({
      host: '127.0.0.1',
      port: 0,
      authPath: await authFile({
        access_token: 'old-access-token',
        refresh_token: 'old-refresh-token',
        account_id: 'acct-codex',
      }),
      upstream: remote.url,
      token: LOCAL_SECRET,
      fetchFn,
      quiet: true,
    });
    servers.push(server);

    const request = () =>
      fetch(`${endpoint}/v1/responses`, {
        method: 'POST',
        headers: { ...LOCAL_AUTH, 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'gpt-5.6-sol', input: 'hello' }),
      });
    const responses = await Promise.all([request(), request()]);

    expect(responses.map((response) => response.status)).toEqual([200, 200]);
    expect(refreshCalls).toBe(1);
  });

  it('rotates across pooled accounts after an upstream 429', async () => {
    const seen: string[] = [];
    const remote = await upstream((req) => {
      const account = req.headers['chatgpt-account-id'] as string;
      seen.push(account);
      return account === 'acct-one'
        ? { status: 429, body: { error: { message: 'quota' } } }
        : {
            body: { id: 'resp_pool', object: 'response', status: 'completed', output: [] },
          };
    });
    const first = await authFile({
      access_token: 'token-one',
      refresh_token: 'refresh-one',
      account_id: 'acct-one',
    });
    const second = await authFile({
      access_token: 'token-two',
      refresh_token: 'refresh-two',
      account_id: 'acct-two',
    });
    const { server, endpoint } = await listenCodexProxy({
      host: '127.0.0.1',
      port: 0,
      authPath: first,
      authPaths: [second],
      authAccountNames: ['one', 'two'],
      upstream: remote.url,
      token: LOCAL_SECRET,
      quiet: true,
    });
    servers.push(server);

    const response = await fetch(`${endpoint}/v1/responses`, {
      method: 'POST',
      headers: { ...LOCAL_AUTH, 'content-type': 'application/json' },
      body: JSON.stringify({ model: 'gpt-5.6-sol', input: 'hello' }),
    });

    expect(response.status).toBe(200);
    expect(seen).toEqual(['acct-one', 'acct-two']);
  });
});
