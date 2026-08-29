#!/usr/bin/env node

import { appendFileSync } from 'node:fs';
import { parseArgs } from 'node:util';
import { installProxyShutdown } from '../proxy-shared';
import { listenCodexProxy } from './server';

const { values } = parseArgs({
  options: {
    port: { type: 'string', default: '4140' },
    host: { type: 'string', default: '127.0.0.1' },
    'auth-path': { type: 'string' },
    upstream: { type: 'string' },
    help: { type: 'boolean', short: 'h' },
  },
});

if (values.help) {
  process.stdout.write(`anypick-codex-proxy — OpenAI Responses proxy using Codex auth

Usage:
  anypick proxy serve codex --name <account>
  node dist/providers/codex-proxy/main.js --auth-path <auth.json> [--port 4140]
`);
  process.exit(0);
}

if (!values['auth-path']) {
  process.stderr.write('Missing --auth-path\n');
  process.exit(2);
}

const port = Number(values.port);
if (!Number.isInteger(port) || port < 0 || port > 65_535) {
  process.stderr.write(`Invalid --port: ${values.port}\n`);
  process.exit(2);
}

const logPath = process.env.ANYPICK_PROXY_LOG;
const log = (line: string) => {
  const message = `[codex-proxy] ${line}\n`;
  if (logPath) {
    try {
      appendFileSync(logPath, message, { mode: 0o600 });
      return;
    } catch {
      // fall through to stderr
    }
  }
  process.stderr.write(message);
};

const { server, endpoint } = await listenCodexProxy({
  host: values.host ?? '127.0.0.1',
  port,
  authPath: values['auth-path'],
  upstream: values.upstream,
  token: process.env.ANYPICK_PROXY_TOKEN,
  persistRefreshedAuth: true,
  log,
});
installProxyShutdown(server);
log(`started ${endpoint} account=${process.env.ANYPICK_ACCOUNT ?? 'unknown'}`);
process.stdout.write(`anypick-codex-proxy listening on ${endpoint}\n`);
