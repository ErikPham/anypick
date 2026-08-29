import { mkdir, mkdtemp, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { GeminiProvider } from '../src/providers/gemini';
import { parseGeminiUsage } from '../src/providers/gemini-usage';
import { GrokProvider } from '../src/providers/grok';
import { parseGrokUsage } from '../src/providers/grok-usage';
import { parseKiroUsage } from '../src/providers/kiro-usage';
import { OpenCodeProvider } from '../src/providers/opencode';
import { parseOpenCodeUsage } from '../src/providers/opencode-usage';

describe('provider usage parsing', () => {
  it('preserves Gemini model buckets and reset times', () => {
    expect(
      parseGeminiUsage({
        buckets: [
          {
            modelId: 'models/gemini-2.5-pro',
            remainingFraction: 0.625,
            resetTime: '2030-01-02T03:04:05Z',
          },
        ],
      }),
    ).toEqual({
      windows: [
        {
          label: 'gemini-2.5-pro',
          remainingPercent: 63,
          resetsAtMs: Date.parse('2030-01-02T03:04:05Z'),
        },
      ],
    });
  });

  it('collapses identical Gemini quota buckets into readable model families', () => {
    expect(
      parseGeminiUsage({
        buckets: [
          { modelId: 'gemini-3-pro', remainingFraction: 0.5 },
          { modelId: 'gemini-3-flash', remainingFraction: 0.5 },
          { modelId: 'claude-sonnet', remainingFraction: 0.25 },
          { modelId: 'gpt-oss', remainingFraction: 0.25 },
        ],
      }),
    ).toEqual({
      windows: [
        { label: 'Gemini (2)', remainingPercent: 50 },
        { label: 'Claude + GPT (2)', remainingPercent: 25 },
      ],
    });
  });

  it('calculates Kiro remaining usage from current and limit', () => {
    expect(
      parseKiroUsage({
        next_date_reset: '2030-02-01T00:00:00Z',
        usage_breakdown: [
          { resource_type: 'AGENTIC_REQUEST', current_usage: 40, usage_limit: 200 },
        ],
      }),
    ).toEqual({
      windows: [
        {
          label: 'Agentic Request',
          remainingPercent: 80,
          resetsAtMs: Date.parse('2030-02-01T00:00:00Z'),
        },
      ],
    });
  });

  it('preserves the Grok billing period and official percentage', () => {
    expect(
      parseGrokUsage({
        config: {
          creditUsagePercent: 42.5,
          currentPeriod: {
            type: 'USAGE_PERIOD_TYPE_WEEKLY',
            end: '2030-03-08T00:00:00Z',
          },
        },
      }),
    ).toEqual({
      windows: [
        {
          label: 'Weekly',
          remainingPercent: 58,
          resetsAtMs: Date.parse('2030-03-08T00:00:00Z'),
        },
      ],
    });
  });

  it('shows every OpenCode Go quota window returned by the official endpoint', () => {
    expect(
      parseOpenCodeUsage({
        usage: {
          rolling: { percent: 25, resetsAt: '2030-04-01T05:00:00Z' },
          weekly: { percent: 40, resetsAt: '2030-04-07T00:00:00Z' },
          monthly: { percent: 10, resetsAt: '2030-05-01T00:00:00Z' },
        },
      }),
    ).toEqual({
      windows: [
        {
          label: '5 hours',
          remainingPercent: 75,
          resetsAtMs: Date.parse('2030-04-01T05:00:00Z'),
        },
        {
          label: 'Weekly',
          remainingPercent: 60,
          resetsAtMs: Date.parse('2030-04-07T00:00:00Z'),
        },
        {
          label: 'Monthly',
          remainingPercent: 90,
          resetsAtMs: Date.parse('2030-05-01T00:00:00Z'),
        },
      ],
    });
  });

  it('reads Grok usage with the current live login without refreshing it', async () => {
    const home = await mkdtemp(join(tmpdir(), 'anypick-grok-usage-'));
    await mkdir(join(home, '.grok'), { recursive: true });
    await writeFile(
      join(home, '.grok', 'auth.json'),
      JSON.stringify({
        'https://auth.x.ai::client': {
          key: 'live-token',
          refresh_token: 'refresh-token',
          expires_at: '2035-01-01T00:00:00Z',
          oidc_client_id: 'client',
          user_id: 'user-1',
        },
      }),
    );
    let request: Request | undefined;
    const provider = new GrokProvider(home, {
      fetchFn: (input, init) => {
        request = new Request(input, init);
        return Promise.resolve(
          Response.json({
            config: {
              creditUsagePercent: 20,
              currentPeriod: { type: 'USAGE_PERIOD_TYPE_WEEKLY' },
            },
          }),
        );
      },
    });

    await expect(provider.liveUsage()).resolves.toEqual({
      windows: [{ label: 'Weekly', remainingPercent: 80 }],
    });
    expect(request?.url).toBe('https://cli-chat-proxy.grok.com/v1/billing?format=credits');
    expect(request?.headers.get('authorization')).toBe('Bearer live-token');
    expect(request?.headers.get('x-userid')).toBe('user-1');
  });

  it('reads Antigravity usage from unified state without touching the keychain', async () => {
    const requests: Request[] = [];
    const provider = new GeminiProvider(
      await mkdtemp(join(tmpdir(), 'anypick-antigravity-usage-')),
      async () => {},
      async (payload) => payload,
      async () => false,
      async () => ({
        token: {
          access_token: 'antigravity-token',
          refresh_token: 'refresh-token',
          expiry: '2035-01-01T00:00:00Z',
        },
      }),
      (input, init) => {
        const request = new Request(input, init);
        requests.push(request);
        if (request.url.endsWith(':loadCodeAssist')) {
          return Promise.resolve(Response.json({ cloudaicompanionProject: 'project-1' }));
        }
        return Promise.resolve(
          Response.json({
            buckets: [{ modelId: 'models/gemini-pro', remainingFraction: 0.7 }],
          }),
        );
      },
    );

    await expect(provider.liveUsage()).resolves.toEqual({
      windows: [{ label: 'gemini-pro', remainingPercent: 70 }],
    });
    expect(requests.map((request) => request.url)).toEqual([
      'https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist',
      'https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota',
    ]);
    expect(requests[0]?.headers.get('authorization')).toBe('Bearer antigravity-token');
    expect(await requests[0]?.clone().json()).toEqual({ metadata: { ideType: 'ANTIGRAVITY' } });
  });

  it('caches the Antigravity keyring fallback used by quota polling', async () => {
    let keyringReads = 0;
    let tokenRefreshes = 0;
    const provider = new GeminiProvider(
      await mkdtemp(join(tmpdir(), 'anypick-antigravity-keyring-usage-')),
      async () => {},
      async (payload) => {
        tokenRefreshes += 1;
        return {
          token: {
            ...payload.token,
            access_token: 'refreshed-token',
            expiry: '2035-01-01T00:00:00Z',
          },
        };
      },
      async () => false,
      async () => null,
      (input) =>
        Promise.resolve(
          String(input).endsWith(':loadCodeAssist')
            ? Response.json({})
            : Response.json({ buckets: [{ modelId: 'models/gemini-pro', remainingFraction: 1 }] }),
        ),
      async () => {
        keyringReads += 1;
        return {
          refresh_token: 'refresh-token',
        };
      },
    );

    await provider.liveUsage();
    await provider.liveUsage();
    expect(keyringReads).toBe(1);
    expect(tokenRefreshes).toBe(1);
  });

  it('reads the three OpenCode Go windows from a live platform key', async () => {
    const home = await mkdtemp(join(tmpdir(), 'anypick-opencode-usage-'));
    const dataDir = join(home, '.local', 'share', 'opencode');
    await mkdir(dataDir, { recursive: true });
    await writeFile(
      join(dataDir, 'auth.json'),
      JSON.stringify({ opencode: { type: 'api', key: 'go-key' } }),
    );
    let request: Request | undefined;
    const provider = new OpenCodeProvider(home, {
      fetchFn: (input, init) => {
        request = new Request(input, init);
        return Promise.resolve(
          Response.json({
            usage: {
              rolling: { percent: 1 },
              weekly: { percent: 2 },
              monthly: { percent: 3 },
            },
          }),
        );
      },
    });

    await expect(provider.liveUsage()).resolves.toEqual({
      windows: [
        { label: '5 hours', remainingPercent: 99 },
        { label: 'Weekly', remainingPercent: 98 },
        { label: 'Monthly', remainingPercent: 97 },
      ],
    });
    expect(request?.url).toBe('https://opencode.ai/zen/go/v1/usage');
    expect(request?.headers.get('authorization')).toBe('Bearer go-key');
  });
});
