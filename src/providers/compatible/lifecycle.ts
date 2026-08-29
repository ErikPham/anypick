import { join } from 'node:path';
import { readFile, writeFile } from 'node:fs/promises';
import type {
  CompatibleProviderDefinition,
  ProxyContext,
  ProxyHandle,
  ProxyStatus,
} from '../../types';
import { AnyPickError } from '../../utils/errors';
import { ensureDir, pathExists, writeJsonFile } from '../../utils/fs';
import { assertLoopbackHost } from '../../utils/network';
import {
  isProcessRunning,
  readPidFile,
  spawnDetached,
  stopPidFile,
  waitForHttp,
} from '../../utils/process';
import { resolveAnyPickCliLaunch } from '../opencode-cli-entry';

function runtimePath(ctx: ProxyContext, name: string): string {
  return join(ctx.runtimeDir, name);
}

function stringList(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((entry): entry is string => typeof entry === 'string' && entry.trim().length > 0)
    : [];
}

export async function startCompatibleProxy(
  ctx: ProxyContext,
  definition: CompatibleProviderDefinition,
  compatibility: string,
  defaultPort: number,
): Promise<ProxyHandle> {
  const host = ctx.config.host ?? '127.0.0.1';
  assertLoopbackHost(host);
  const port = ctx.config.port ?? defaultPort;
  await ensureDir(ctx.runtimeDir);
  const logPath = runtimePath(ctx, 'proxy.log');
  const pidPath = runtimePath(ctx, 'proxy.pid');
  const definitionPath = runtimePath(ctx, 'provider.json');
  await writeFile(logPath, '', { mode: 0o600 });
  await writeJsonFile(definitionPath, definition, 0o600);

  const configuredDirs = stringList(ctx.config.options?.authDirs);
  const authDirs = configuredDirs.length ? configuredDirs : [ctx.snapshotDir];
  const accountNames = stringList(ctx.config.options?.quotaGuardAccountNames);
  const subArgs = [
    'proxy',
    'serve',
    'compatible',
    '--definition',
    definitionPath,
    '--port',
    String(port),
    '--host',
    host,
  ];
  for (const authDir of authDirs) {
    subArgs.push('--auth-dir', authDir);
  }
  for (const accountName of accountNames) {
    subArgs.push('--auth-account-name', accountName);
  }
  const quotaGuard = ctx.config.options?.quotaGuard;
  if (quotaGuard && typeof quotaGuard === 'object' && !Array.isArray(quotaGuard)) {
    const policy = quotaGuard as Record<string, unknown>;
    if (policy.enabled === true) {
      subArgs.push('--quota-guard', '--quota-guard-state', runtimePath(ctx, 'quota-guard.json'));
      if (typeof policy.cooldownMs === 'number' && Number.isFinite(policy.cooldownMs)) {
        subArgs.push(
          '--quota-guard-cooldown-ms',
          String(Math.max(1_000, Math.floor(policy.cooldownMs))),
        );
      }
    }
  }

  const launch = resolveAnyPickCliLaunch(subArgs);
  if (!(await pathExists(launch.entry))) {
    throw new AnyPickError(
      `AnyPick CLI entry not found at ${launch.entry}. Run: pnpm build`,
      'PROXY_BINARY_MISSING',
    );
  }
  const endpoint = `http://${host}:${port}`;
  const { pid, instanceId } = await spawnDetached(launch.command, launch.args, {
    logPath,
    pidPath,
    endpoint,
    provider: definition.id,
    account: ctx.accountName,
    env: {
      ...process.env,
      ...(ctx.token ? { ANYPICK_PROXY_TOKEN: ctx.token } : {}),
      ANYPICK_PROXY_LOG: logPath,
      ...(launch.watch ? { ANYPICK_DEV_WATCH: process.env.ANYPICK_DEV_WATCH ?? '1' } : {}),
    },
  });
  const ready = await waitForHttp(`${endpoint}/health`, {
    timeoutMs: launch.watch ? 15_000 : 5_000,
    requirePid: pid,
    expectInstanceId: instanceId,
  });
  if (!ready || !isProcessRunning(pid)) {
    let tail = '';
    try {
      tail = (await readFile(logPath, 'utf8')).split(/\r?\n/u).slice(-20).join('\n');
    } catch {
      // The startup error below remains actionable without a log tail.
    }
    if (isProcessRunning(pid)) {
      try {
        process.kill(pid, 'SIGTERM');
      } catch {
        // Child already exited.
      }
    }
    throw new AnyPickError(
      `${definition.name} proxy failed to bind ${host}:${port}.${tail ? `\n${tail}` : ''}`,
      'PROXY_START_FAILED',
    );
  }
  return { endpoint, compatibility, pid, instanceId, token: ctx.token, logPath };
}

export async function stopCompatibleProxy(ctx: ProxyContext): Promise<void> {
  await stopPidFile(runtimePath(ctx, 'proxy.pid'));
}

export async function statusCompatibleProxy(
  ctx: ProxyContext,
  compatibility: string,
  defaultPort: number,
): Promise<ProxyStatus> {
  const pidPath = runtimePath(ctx, 'proxy.pid');
  const logPath = runtimePath(ctx, 'proxy.log');
  const pid = await readPidFile(pidPath);
  const running = pid != null && isProcessRunning(pid);
  const host = ctx.config.host ?? '127.0.0.1';
  const port = ctx.config.port ?? defaultPort;
  return {
    enabled: ctx.config.enabled,
    running,
    host,
    port,
    endpoint: running ? `http://${host}:${port}` : undefined,
    compatibility,
    pid: running ? (pid ?? undefined) : undefined,
    logPath: (await pathExists(logPath)) ? logPath : undefined,
    detail: running ? 'official API · dual protocol' : ctx.config.enabled ? 'stopped' : 'disabled',
  };
}

export async function readCompatibleProxyLogs(ctx: ProxyContext, lines = 50): Promise<string> {
  const logPath = runtimePath(ctx, 'proxy.log');
  if (!(await pathExists(logPath))) {
    return '(no log file yet)';
  }
  const all = (await readFile(logPath, 'utf8')).split(/\r?\n/u);
  return all.slice(Math.max(0, all.length - lines)).join('\n');
}
