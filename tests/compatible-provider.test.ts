import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { CompatibleProviderDefinition } from '../src/types';
import { CompatibleProvider } from '../src/providers/compatible/provider';
import {
  COMPATIBLE_CREDENTIAL_FILE,
  validateCompatibleProviderDefinition,
} from '../src/providers/compatible/definition';
import { listenCompatibleProxy } from '../src/providers/compatible/server';
import { createAppReady } from '../src/core/app';
import { buildProgram } from '../src/cli/commands';

const baseDefinition: CompatibleProviderDefinition = {
  id: 'acme-ai',
  name: 'ACME AI',
  description: 'Test compatible provider',
  models: ['acme-coder'],
  credentials: [
    {
      kind: 'api-key',
      label: 'API key',
      profiles: [
        {
          id: 'global',
          openaiBase: 'https://api.example.com/v1',
          anthropicBase: 'https://api.example.com/anthropic',
        },
      ],
    },
    {
      kind: 'subscription-key',
      label: 'Coding Plan key',
      profiles: [{ id: 'coding', openaiBase: 'https://coding.example.com/v1' }],
    },
  ],
  quotaErrorPatterns: ['plan quota'],
};

async function writeCredential(
  dir: string,
  secret: string,
  kind = 'api-key',
  profile = 'global',
): Promise<void> {
  await mkdir(dir, { recursive: true });
  await writeFile(
    join(dir, COMPATIBLE_CREDENTIAL_FILE),
    JSON.stringify({ version: 1, kind, profile, secret }),
    { mode: 0o600 },
  );
}

describe('compatible provider definition', () => {
  it('refuses insecure upstream endpoints', () => {
    const invalid: CompatibleProviderDefinition = {
      ...baseDefinition,
      credentials: [
        {
          kind: 'api-key',
          label: 'API key',
          profiles: [{ id: 'local', openaiBase: 'http://127.0.0.1:9999/v1' }],
        },
      ],
    };
    expect(() => validateCompatibleProviderDefinition(invalid)).toThrow(/must use HTTPS/);
  });

  it('exposes API and subscription credentials without native auth writes', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-compatible-provider-'));
    try {
      const provider = new CompatibleProvider(baseDefinition);
      expect(provider.credentialInputs).toEqual(['api-key', 'subscription-key']);
      expect(provider.credentialInputLabel('subscription-key')).toBe('Coding Plan key');
      const meta = await provider.backupInput(
        { kind: 'subscription-key', secret: 'plan-secret-value' },
        root,
      );
      expect(meta).toMatchObject({
        credentialKind: 'proxy-only',
        label: 'Coding Plan key · coding',
      });
      expect(meta.identity).not.toContain('plan-secret-value');
      const adapter = provider.sourceAdapter({
        meta: {
          provider: provider.id,
          name: 'plan',
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        },
        snapshotDir: root,
        accountDir: root,
        proxy: { enabled: true },
      });
      expect(adapter.capabilities.requiresNativeAuthWrite).toBe(false);
      expect(adapter.transportFor('claude')).toBe('managed_builtin_proxy');
      expect(adapter.transportFor('codex')).toBe('managed_builtin_proxy');
      expect(adapter.transportFor('gemini')).toBe('unsupported');
    } finally {
      await rm(root, { recursive: true, force: true });
    }
  });

  it('adds a subscription account through the generic non-interactive CLI flags', async () => {
    const root = await mkdtemp(join(tmpdir(), 'anypick-compatible-cli-'));
    const output = vi.spyOn(console, 'log').mockImplementation(() => {});
    let app: Awaited<ReturnType<typeof createAppReady>> | undefined;
    try {
      const bootstrap = await createAppReady({ root });
      await bootstrap.plugins.add('bundled:popular-providers');
      await bootstrap.plugins.setEnabled('popular-providers', true);
      bootstrap.close();
      app = await createAppReady({ root });
      const program = buildProgram(app);
      await program.parseAsync([
        'node',
        'anypick',
        '--json',
        '--no-input',
        'add',
        'account',
        'mimo',
        '--credential',
        'subscription-key',
        '--secret',
        'tp-secret-value',
        '--name',
        'plan',
        '--option',
        'profile=singapore',
      ]);
      const account = await app.accounts.get('mimo', 'plan');
      expect(account?.meta).toMatchObject({
        credentialKind: 'proxy-only',
        label: 'MiMo Token Plan key · singapore',
      });
      expect(output).toHaveBeenCalledWith(expect.stringContaining('"mode":"subscription-key"'));
    } finally {
      output.mockRestore();
      app?.close();
      await rm(root, { recursive: true, force: true });
    }
  });
});

describe('compatible provider proxy', () => {
  let root: string;
  const realFetch = globalThis.fetch;

  beforeEach(async () => {
    root = await mkdtemp(join(tmpdir(), 'anypick-compatible-proxy-'));
  });

  afterEach(async () => {
    vi.unstubAllGlobals();
    await rm(root, { recursive: true, force: true });
  });

  it('fails closed locally and injects the upstream key without changing User-Agent', async () => {
    const authDir = join(root, 'auth');
    await writeCredential(authDir, 'upstream-secret');
    const upstream = vi.fn(async (_target: string | URL | Request, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      expect(headers.get('authorization')).toBe('Bearer upstream-secret');
      expect(headers.get('x-api-key')).toBeNull();
      expect(headers.get('cookie')).toBeNull();
      expect(headers.get('origin')).toBeNull();
      expect(headers.get('user-agent')).toBe('codex-cli/1.2.3');
      return new Response(
        JSON.stringify({
          id: 'chat_1',
          model: 'acme-coder',
          choices: [{ message: { role: 'assistant', content: 'done' }, finish_reason: 'stop' }],
          usage: { prompt_tokens: 10, completion_tokens: 2, total_tokens: 12 },
        }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      );
    });
    vi.stubGlobal('fetch', upstream);
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition: baseDefinition,
      authDirs: [authDir],
      token: 'local-token',
    });
    try {
      const denied = await realFetch(`${endpoint}/v1/chat/completions`, {
        method: 'POST',
        body: JSON.stringify({ model: 'acme-coder', messages: [] }),
      });
      expect(denied.status).toBe(401);
      expect(upstream).not.toHaveBeenCalled();

      const invalid = await realFetch(`${endpoint}/v1/responses`, {
        method: 'POST',
        headers: { authorization: 'Bearer local-token', 'content-type': 'application/json' },
        body: '{',
      });
      expect(invalid.status).toBe(400);
      expect(upstream).not.toHaveBeenCalled();

      const response = await realFetch(`${endpoint}/v1/chat/completions`, {
        method: 'POST',
        headers: {
          authorization: 'Bearer local-token',
          'content-type': 'application/json',
          'user-agent': 'codex-cli/1.2.3',
          cookie: 'session=must-not-leak',
          origin: 'http://localhost:4321',
        },
        body: JSON.stringify({ model: 'acme-coder', messages: [] }),
      });
      expect(response.status).toBe(200);
      expect((await response.json()) as object).toMatchObject({ id: 'chat_1' });
      expect(String(upstream.mock.calls[0]?.[0])).toBe(
        'https://api.example.com/v1/chat/completions',
      );
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it('passes Responses through unchanged when every credential declares native support', async () => {
    const authDir = join(root, 'native-responses');
    await writeCredential(authDir, 'native-secret');
    const definition: CompatibleProviderDefinition = {
      ...baseDefinition,
      credentials: [
        {
          kind: 'api-key',
          label: 'API key',
          profiles: [
            {
              id: 'global',
              openaiBase: 'https://api.example.com/v1',
              nativeResponses: true,
            },
          ],
        },
      ],
    };
    const upstream = vi.fn(async (target: string | URL | Request, init?: RequestInit) => {
      expect(String(target)).toBe('https://api.example.com/v1/responses');
      expect(new Headers(init?.headers).get('authorization')).toBe('Bearer native-secret');
      expect(JSON.parse(new TextDecoder().decode(init?.body as Uint8Array))).toMatchObject({
        model: 'acme-coder',
        input: 'hello',
        store: true,
      });
      return new Response(
        JSON.stringify({ id: 'resp_native', object: 'response', status: 'completed' }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      );
    });
    vi.stubGlobal('fetch', upstream);
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition,
      authDirs: [authDir],
      token: 'local-token',
    });
    try {
      const response = await realFetch(`${endpoint}/v1/responses`, {
        method: 'POST',
        headers: { authorization: 'Bearer local-token', 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'acme-coder', input: 'hello', store: true }),
      });
      expect(response.status).toBe(200);
      expect(await response.json()).toMatchObject({ id: 'resp_native', status: 'completed' });
      expect(upstream).toHaveBeenCalledTimes(1);
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it('translates subscription Chat Completions into Codex Responses SSE', async () => {
    const authDir = join(root, 'plan');
    await writeCredential(authDir, 'plan-secret', 'subscription-key', 'coding');
    const upstream = vi.fn(async (_target: string | URL | Request, init?: RequestInit) => {
      const body = JSON.parse(new TextDecoder().decode(init?.body as Uint8Array)) as {
        stream?: boolean;
        messages?: Array<{ role: string; content: string }>;
        tools?: unknown[];
      };
      expect(body.stream).toBe(false);
      expect(body.messages?.map((message) => message.role)).toEqual(['system', 'user']);
      expect(body.tools).toHaveLength(1);
      return new Response(
        JSON.stringify({
          id: 'chat_2',
          model: 'acme-coder',
          choices: [
            {
              message: {
                role: 'assistant',
                content: null,
                tool_calls: [
                  {
                    id: 'call_1',
                    type: 'function',
                    function: { name: 'read_file', arguments: '{"path":"a.ts"}' },
                  },
                ],
              },
              finish_reason: 'tool_calls',
            },
          ],
          usage: { prompt_tokens: 20, completion_tokens: 4, total_tokens: 24 },
        }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      );
    });
    vi.stubGlobal('fetch', upstream);
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition: baseDefinition,
      authDirs: [authDir],
      token: 'local-token',
    });
    try {
      const response = await realFetch(`${endpoint}/v1/responses`, {
        method: 'POST',
        headers: { authorization: 'Bearer local-token', 'content-type': 'application/json' },
        body: JSON.stringify({
          model: 'acme-coder',
          instructions: 'Be concise',
          input: [{ role: 'user', content: [{ type: 'input_text', text: 'Read a.ts' }] }],
          tools: [
            {
              type: 'function',
              name: 'read_file',
              parameters: { type: 'object', properties: { path: { type: 'string' } } },
            },
          ],
          stream: true,
        }),
      });
      const text = await response.text();
      expect(response.status).toBe(200);
      expect(text).toContain('event: response.created');
      expect(text).toContain('event: response.function_call_arguments.delta');
      expect(text).toContain('event: response.completed');
      expect(String(upstream.mock.calls[0]?.[0])).toBe(
        'https://coding.example.com/v1/chat/completions',
      );
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it('passes Claude Messages through the official Anthropic endpoint with x-api-key auth', async () => {
    const authDir = join(root, 'anthropic');
    await writeCredential(authDir, 'anthropic-secret');
    const upstream = vi.fn(async (target: string | URL | Request, init?: RequestInit) => {
      expect(String(target)).toBe('https://api.example.com/anthropic/v1/messages');
      const headers = new Headers(init?.headers);
      expect(headers.get('x-api-key')).toBe('anthropic-secret');
      expect(headers.get('authorization')).toBeNull();
      return new Response(
        JSON.stringify({
          id: 'msg_1',
          type: 'message',
          role: 'assistant',
          model: 'acme-coder',
          content: [{ type: 'text', text: 'hello' }],
          stop_reason: 'end_turn',
          stop_sequence: null,
          usage: { input_tokens: 3, output_tokens: 1 },
        }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      );
    });
    vi.stubGlobal('fetch', upstream);
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition: baseDefinition,
      authDirs: [authDir],
      token: 'local-token',
    });
    try {
      const response = await realFetch(`${endpoint}/v1/messages`, {
        method: 'POST',
        headers: {
          'x-api-key': 'local-token',
          'content-type': 'application/json',
          'anthropic-version': '2023-06-01',
        },
        body: JSON.stringify({
          model: 'acme-coder',
          max_tokens: 100,
          messages: [{ role: 'user', content: 'hello' }],
        }),
      });
      expect(response.status).toBe(200);
      expect((await response.json()) as object).toMatchObject({ id: 'msg_1' });
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it('uses a provider-declared bearer scheme for Anthropic-compatible endpoints', async () => {
    const authDir = join(root, 'bearer-anthropic');
    await writeCredential(authDir, 'glm-secret');
    const definition: CompatibleProviderDefinition = {
      ...baseDefinition,
      credentials: [
        {
          kind: 'api-key',
          label: 'API key',
          profiles: [
            {
              id: 'global',
              openaiBase: 'https://api.example.com/v1',
              anthropicBase: 'https://api.example.com/anthropic',
              anthropicAuth: 'bearer',
            },
          ],
        },
      ],
    };
    const upstream = vi.fn(async (_target: string | URL | Request, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      expect(headers.get('authorization')).toBe('Bearer glm-secret');
      expect(headers.get('x-api-key')).toBeNull();
      expect(headers.get('anthropic-version')).toBe('2023-06-01');
      return new Response(
        JSON.stringify({
          id: 'msg_bearer',
          type: 'message',
          role: 'assistant',
          model: 'acme-coder',
          content: [{ type: 'text', text: 'hello' }],
          stop_reason: 'end_turn',
          usage: { input_tokens: 1, output_tokens: 1 },
        }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      );
    });
    vi.stubGlobal('fetch', upstream);
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition,
      authDirs: [authDir],
      token: 'local-token',
    });
    try {
      const response = await realFetch(`${endpoint}/v1/messages`, {
        method: 'POST',
        headers: { authorization: 'Bearer local-token', 'content-type': 'application/json' },
        body: JSON.stringify({
          model: 'acme-coder',
          max_tokens: 100,
          messages: [{ role: 'user', content: 'hello' }],
        }),
      });
      expect(response.status).toBe(200);
      expect((await response.json()) as object).toMatchObject({ id: 'msg_bearer' });
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it('returns translated upstream failures in the Anthropic error envelope', async () => {
    const authDir = join(root, 'chat-only');
    await writeCredential(authDir, 'plan-secret', 'subscription-key', 'coding');
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({ error: { message: 'invalid plan key', code: 'bad_key' } }),
            {
              status: 401,
              headers: { 'content-type': 'application/json' },
            },
          ),
      ),
    );
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition: baseDefinition,
      authDirs: [authDir],
      token: 'local-token',
    });
    try {
      const response = await realFetch(`${endpoint}/v1/messages`, {
        method: 'POST',
        headers: { authorization: 'Bearer local-token', 'content-type': 'application/json' },
        body: JSON.stringify({
          model: 'acme-coder',
          max_tokens: 100,
          messages: [{ role: 'user', content: 'hello' }],
        }),
      });
      expect(response.status).toBe(401);
      expect(await response.json()).toEqual({
        type: 'error',
        error: { message: 'invalid plan key', type: 'api_error', code: 'bad_key' },
      });
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });

  it('fails over a subscription pool only after an authoritative quota error', async () => {
    const first = join(root, 'first');
    const second = join(root, 'second');
    await writeCredential(first, 'first-secret');
    await writeCredential(second, 'second-secret');
    const seen: string[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_target: string | URL | Request, init?: RequestInit) => {
        const key = new Headers(init?.headers).get('authorization') ?? '';
        seen.push(key);
        if (key === 'Bearer first-secret') {
          return new Response(JSON.stringify({ error: { message: 'plan quota exhausted' } }), {
            status: 429,
            headers: { 'content-type': 'application/json' },
          });
        }
        return new Response(
          JSON.stringify({
            id: 'chat_ok',
            choices: [{ message: { content: 'ok' }, finish_reason: 'stop' }],
          }),
          { status: 200, headers: { 'content-type': 'application/json' } },
        );
      }),
    );
    const { server, endpoint } = await listenCompatibleProxy({
      host: '127.0.0.1',
      port: 0,
      definition: baseDefinition,
      authDirs: [first, second],
      accountNames: ['first', 'second'],
      token: 'local-token',
      quotaGuard: { enabled: true, cooldownMs: 60_000 },
    });
    try {
      const response = await realFetch(`${endpoint}/v1/chat/completions`, {
        method: 'POST',
        headers: { authorization: 'Bearer local-token', 'content-type': 'application/json' },
        body: JSON.stringify({ model: 'acme-coder', messages: [] }),
      });
      expect(response.status).toBe(200);
      expect(seen).toEqual(['Bearer first-secret', 'Bearer second-secret']);
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
    }
  });
});
