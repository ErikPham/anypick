/** Internal `anypick proxy serve compatible` process entry. */
import { appendFileSync } from 'node:fs';
import { parseArgs } from 'node:util';
import type { CompatibleProviderDefinition } from '../../types';
import { expandHome, readJsonFile } from '../../utils/fs';
import { assertLoopbackHost } from '../../utils/network';
import { installProxyShutdown } from '../proxy-shared';
import { listenCompatibleProxy } from './server';

export async function runCompatibleProxyCli(args: string[]): Promise<void> {
  const { values } = parseArgs({
    args,
    options: {
      definition: { type: 'string' },
      'auth-dir': { type: 'string', multiple: true, default: [] },
      'auth-account-name': { type: 'string', multiple: true, default: [] },
      port: { type: 'string', short: 'p', default: '4140' },
      host: { type: 'string', default: '127.0.0.1' },
      'quota-guard': { type: 'boolean', default: false },
      'quota-guard-state': { type: 'string', default: '' },
      'quota-guard-cooldown-ms': { type: 'string', default: '3600000' },
      quiet: { type: 'boolean', short: 'q', default: false },
      help: { type: 'boolean', short: 'h', default: false },
    },
    allowPositionals: true,
  });
  if (values.help) {
    process.stdout.write(`anypick proxy serve compatible (internal)\n`);
    return;
  }
  if (!values.definition || !values['auth-dir']?.length) {
    throw new Error('--definition and at least one --auth-dir are required');
  }
  const port = Number(values.port);
  if (!Number.isInteger(port) || port < 0 || port > 65535) {
    throw new Error(`Invalid port: ${values.port}`);
  }
  const cooldownMs = Number(values['quota-guard-cooldown-ms']);
  if (!Number.isFinite(cooldownMs) || cooldownMs < 1_000) {
    throw new Error(`Invalid quota guard cooldown: ${values['quota-guard-cooldown-ms']}`);
  }
  const host = values.host ?? '127.0.0.1';
  assertLoopbackHost(host);
  const definition = await readJsonFile<CompatibleProviderDefinition>(
    expandHome(values.definition),
  );
  const authDirs = values['auth-dir']
    .map((dir) => dir.trim())
    .filter(Boolean)
    .map(expandHome);
  const accountNames = values['auth-account-name'].map((name) => name.trim()).filter(Boolean);
  const log = values.quiet ? () => {} : makeLog();
  const { endpoint, server } = await listenCompatibleProxy({
    host,
    port,
    definition,
    authDirs,
    accountNames,
    token: process.env.ANYPICK_PROXY_TOKEN,
    quotaGuard: {
      enabled: values['quota-guard'],
      cooldownMs: Math.floor(cooldownMs),
      statePath: values['quota-guard-state'] ? expandHome(values['quota-guard-state']) : undefined,
      accountNames,
      providerId: definition.id,
    },
    log,
  });
  process.stdout.write(`anypick-${definition.id}-proxy listening on ${endpoint}\n`);
  log(`listening on ${endpoint}`);
  log(`provider: ${definition.id}`);
  log(`credential ring: ${authDirs.length} account(s)`);
  installProxyShutdown(server);
}

function makeLog(): (line: string) => void {
  return (line) => {
    const ts = new Date().toISOString().slice(11, 19);
    process.stderr.write(`${ts} ${line}\n`);
    const extraLog = process.env.ANYPICK_PROXY_LOG;
    if (extraLog && process.stderr.isTTY) {
      try {
        appendFileSync(extraLog, `${ts} ${line}\n`);
      } catch {
        // Detached stderr already remains authoritative.
      }
    }
  };
}
