import { join } from 'node:path';
import { readFile, writeFile } from 'node:fs/promises';
import type { ProxyContext, ProxyHandle, ProxyStatus } from '../types';
import { fingerprintCodexAuth } from './auth-fingerprint';
import { loadCodexProxySession } from './codex-proxy/auth';
import { resolveCodexProxyMain } from './codex-proxy-entry';
import { AnyPickError } from '../utils/errors';
import { assertLoopbackHost } from '../utils/network';
import { ensureDir, pathExists, readJsonFile, writeJsonFile } from '../utils/fs';
import {
  isProcessRunning,
  readPidFile,
  spawnDetached,
  stopPidFile,
  waitForHttp,
} from '../utils/process';

interface CodexProxyLifecycleOptions {
  defaultPort: number;
  compatibility: string;
}

interface RuntimeAuthOrigin {
  version: 1;
  snapshotFingerprint: string;
}

export async function startCodexProxy(
  ctx: ProxyContext,
  opts: CodexProxyLifecycleOptions,
): Promise<ProxyHandle> {
  const host = ctx.config.host ?? '127.0.0.1';
  assertLoopbackHost(host);
  const port = ctx.config.port ?? opts.defaultPort;
  const mainJs = resolveCodexProxyMain();
  if (!(await pathExists(mainJs))) {
    throw new AnyPickError(
      `Codex proxy entry not found at ${mainJs}. Run: pnpm build`,
      'PROXY_BINARY_MISSING',
    );
  }

  await ensureDir(ctx.runtimeDir);
  const logPath = join(ctx.runtimeDir, 'proxy.log');
  const pidPath = join(ctx.runtimeDir, 'proxy.pid');
  const authPath = await materializeRuntimeAuth(ctx);
  // Fail in the foreground with a useful error instead of hiding it in the child log.
  await loadCodexProxySession(authPath);
  await writeFile(logPath, '', { mode: 0o600 });

  const upstream =
    typeof ctx.config.options?.upstream === 'string' ? ctx.config.options.upstream : undefined;
  const endpoint = `http://${host}:${port}`;
  const args = [
    mainJs,
    '--port',
    String(port),
    '--host',
    host,
    '--auth-path',
    authPath,
    ...(upstream ? ['--upstream', upstream] : []),
  ];
  const { pid, instanceId } = await spawnDetached(process.execPath, args, {
    logPath,
    pidPath,
    endpoint,
    provider: 'codex',
    account: ctx.accountName,
    env: {
      ...process.env,
      ...(ctx.token ? { ANYPICK_PROXY_TOKEN: ctx.token } : {}),
      ANYPICK_PROXY_LOG: logPath,
      ANYPICK_ACCOUNT: ctx.accountName,
    },
  });

  const ready = await waitForHttp(`${endpoint}/health`, {
    timeoutMs: 5_000,
    requirePid: pid,
    expectInstanceId: instanceId,
  });
  if (!ready || !isProcessRunning(pid)) {
    let tail = '';
    try {
      tail = (await readFile(logPath, 'utf8')).split(/\r?\n/u).slice(-20).join('\n');
    } catch {
      // ignore
    }
    if (isProcessRunning(pid)) {
      try {
        process.kill(pid, 'SIGTERM');
      } catch {
        // ignore
      }
    }
    throw new AnyPickError(
      `Codex proxy failed to bind ${host}:${port}.${tail ? `\n${tail}` : ''}`,
      'PROXY_START_FAILED',
    );
  }

  return {
    endpoint,
    compatibility: opts.compatibility,
    pid,
    instanceId,
    token: ctx.token,
    logPath,
  };
}

export async function stopCodexProxy(ctx: ProxyContext): Promise<void> {
  await stopPidFile(join(ctx.runtimeDir, 'proxy.pid'));
}

export async function codexProxyStatus(
  ctx: ProxyContext,
  compatibility: string,
  defaultPort: number,
): Promise<ProxyStatus> {
  const pid = await readPidFile(join(ctx.runtimeDir, 'proxy.pid'));
  const running = pid != null && isProcessRunning(pid);
  const host = ctx.config.host ?? '127.0.0.1';
  const port = ctx.config.port ?? defaultPort;
  const logPath = join(ctx.runtimeDir, 'proxy.log');
  return {
    enabled: ctx.config.enabled,
    running,
    host,
    port,
    endpoint: running ? `http://${host}:${port}` : undefined,
    compatibility,
    pid: running ? (pid ?? undefined) : undefined,
    logPath: (await pathExists(logPath)) ? logPath : undefined,
    detail: running ? undefined : ctx.config.enabled ? 'stopped' : 'disabled',
  };
}

export async function readCodexProxyLogs(ctx: ProxyContext, lines = 50): Promise<string> {
  const logPath = join(ctx.runtimeDir, 'proxy.log');
  if (!(await pathExists(logPath))) {
    return '(no log file yet)';
  }
  return (await readFile(logPath, 'utf8')).split(/\r?\n/u).slice(-lines).join('\n');
}

async function materializeRuntimeAuth(ctx: ProxyContext): Promise<string> {
  const snapshotPath = join(ctx.snapshotDir, 'auth.json');
  if (!(await pathExists(snapshotPath))) {
    throw new AnyPickError('Saved Codex account has no auth.json.', 'NO_LIVE_AUTH');
  }
  const snapshot = await readJsonFile<Record<string, unknown>>(snapshotPath);
  const fingerprint = fingerprintCodexAuth(snapshot);
  if (!fingerprint) {
    throw new AnyPickError(
      'Saved Codex auth.json has no usable credentials.',
      'INVALID_CREDENTIAL',
    );
  }

  const runtimePath = join(ctx.runtimeDir, 'codex-auth.json');
  const originPath = join(ctx.runtimeDir, 'codex-auth-origin.json');
  let canReuse = false;
  if ((await pathExists(runtimePath)) && (await pathExists(originPath))) {
    try {
      const origin = await readJsonFile<RuntimeAuthOrigin>(originPath);
      canReuse = origin.version === 1 && origin.snapshotFingerprint === fingerprint;
    } catch {
      canReuse = false;
    }
  }

  if (!canReuse) {
    await writeJsonFile(runtimePath, snapshot, 0o600);
    await writeJsonFile(
      originPath,
      { version: 1, snapshotFingerprint: fingerprint } satisfies RuntimeAuthOrigin,
      0o600,
    );
  }
  return runtimePath;
}
