import { spawn } from 'node:child_process';
import { homedir } from 'node:os';
import type { ProviderLoginMethod } from '../types';
import { AnyPickError } from '../utils/errors';
import { resolveBinary } from '../utils/process';

export interface BrowserLoginCommand {
  names: string[];
  extraPaths?: string[];
  args: string[];
  env?: Record<string, string>;
  timeoutMs?: number;
}

export type BrowserLoginRunner = (
  command: BrowserLoginCommand,
  options?: { home?: string; signal?: AbortSignal },
) => Promise<void>;

export async function loginMethodAvailability(
  method: Omit<ProviderLoginMethod, 'available' | 'unavailableReason'>,
  command: BrowserLoginCommand,
): Promise<ProviderLoginMethod> {
  const binary = await resolveBrowserLoginBinary(command);
  return binary
    ? { ...method, available: true }
    : {
        ...method,
        available: false,
        unavailableReason: 'The provider login launcher is not installed.',
      };
}

/**
 * Run a provider's own login-only command without forwarding its output.
 * OAuth URLs, device codes, and tokens therefore never cross the tray/JSON
 * output boundary; the provider writes its normal native credential store.
 */
export const runBrowserLoginCommand: BrowserLoginRunner = async (command, options = {}) => {
  const binary = await resolveBrowserLoginBinary(command);
  if (!binary) {
    throw new AnyPickError('The provider login launcher is not installed.', {
      code: 'PROVIDER_LOGIN_UNAVAILABLE',
      suggestions: ["Install the provider's official login tool, then try again."],
      mutated: false,
    });
  }

  const signal = options.signal;
  if (signal?.aborted) {
    throw loginCancelled();
  }

  await new Promise<void>((resolve, reject) => {
    const child = spawn(binary, command.args, {
      env: {
        ...process.env,
        HOME: options.home ?? homedir(),
        ...command.env,
      },
      // Provider login output may contain an OAuth URL or one-time code. The
      // browser is the user-facing surface; no child bytes cross our boundary.
      stdio: 'ignore',
      windowsHide: true,
    });
    let settled = false;
    const finish = (fn: () => void) => {
      if (settled) {
        return;
      }
      settled = true;
      clearTimeout(timer);
      signal?.removeEventListener('abort', abort);
      fn();
    };
    const abort = () => {
      child.kill('SIGTERM');
      finish(() => reject(loginCancelled()));
    };
    const timer = setTimeout(
      () => {
        child.kill('SIGTERM');
        finish(() =>
          reject(
            new AnyPickError('Browser sign-in timed out.', {
              code: 'PROVIDER_LOGIN_TIMEOUT',
              suggestions: ['Try Sign In again and complete the browser flow within ten minutes.'],
              mutated: false,
            }),
          ),
        );
      },
      command.timeoutMs ?? 10 * 60 * 1_000,
    );
    timer.unref();
    signal?.addEventListener('abort', abort, { once: true });
    child.once('error', () =>
      finish(() =>
        reject(
          new AnyPickError('The provider login launcher could not be started.', {
            code: 'PROVIDER_LOGIN_FAILED',
            suggestions: ['Check the provider installation, then try again.'],
            mutated: false,
          }),
        ),
      ),
    );
    child.once('exit', (code, exitSignal) =>
      finish(() => {
        if (code === 0) {
          resolve();
          return;
        }
        reject(
          new AnyPickError(
            exitSignal ? 'Provider sign-in was interrupted.' : 'Provider sign-in did not complete.',
            {
              code: 'PROVIDER_LOGIN_FAILED',
              suggestions: ["Try Sign In again and finish the provider's browser flow."],
              mutated: false,
            },
          ),
        );
      }),
    );
  });
};

async function resolveBrowserLoginBinary(command: BrowserLoginCommand): Promise<string | null> {
  return resolveBinary(command.names, command.extraPaths ?? []);
}

function loginCancelled(): AnyPickError {
  return new AnyPickError('Browser sign-in was cancelled.', {
    code: 'PROVIDER_LOGIN_CANCELLED',
    mutated: false,
  });
}
