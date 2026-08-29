import { describe, expect, it, vi } from 'vitest';
import { ClaudeProvider } from '../src/providers/claude';
import { CodexProvider } from '../src/providers/codex';
import { GrokProvider } from '../src/providers/grok';
import { KiroProvider } from '../src/providers/kiro';
import type { BrowserLoginCommand, BrowserLoginRunner } from '../src/providers/browser-login';

describe('built-in provider browser login launchers', () => {
  it('delegates to each provider official login-only command', async () => {
    const calls: Array<{ command: BrowserLoginCommand; home?: string }> = [];
    const runner: BrowserLoginRunner = vi.fn(async (command, options) => {
      calls.push({ command, home: options?.home });
    });
    const home = '/private/tmp/anypick-browser-login-home';

    await new CodexProvider(home, fetch, runner).login?.({
      methodId: 'chatgpt-subscription',
      source: 'codex',
    });
    await new ClaudeProvider(home, { loginRunner: runner }).login?.({
      methodId: 'claude-subscription',
      source: 'claude-code',
    });
    await new KiroProvider(home, { loginRunner: runner }).login?.({
      methodId: 'builder-id',
      source: 'kiro',
    });
    await new GrokProvider(home, { loginRunner: runner }).login?.({
      methodId: 'grok-subscription',
    });

    expect(calls.map(({ command }) => command.args)).toEqual([
      ['login'],
      ['auth', 'login', '--claudeai'],
      ['login', '--license', 'free'],
      ['login', '--oauth'],
    ]);
    expect(calls.every((call) => call.home === home)).toBe(true);
  });

  it('rejects a method owned by another provider before launching anything', async () => {
    const runner: BrowserLoginRunner = vi.fn(async () => {});
    const provider = new CodexProvider('/private/tmp/anypick-browser-login-home', fetch, runner);

    await expect(provider.login?.({ methodId: 'community-unknown' })).rejects.toMatchObject({
      code: 'PROVIDER_LOGIN_METHOD_UNKNOWN',
    });
    expect(runner).not.toHaveBeenCalled();
  });
});
