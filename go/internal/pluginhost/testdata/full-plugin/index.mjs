/* oxlint-disable typescript/no-unsafe-assignment, typescript/no-unsafe-member-access, typescript/no-unsafe-argument, typescript/no-unsafe-call, typescript/no-unsafe-return -- The fixture intentionally exercises the untyped JavaScript plugin boundary. */
import { spawn } from 'node:child_process';
import { createHash, randomUUID, timingSafeEqual } from 'node:crypto';
import { appendFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import http from 'node:http';
import { helperValue } from './helper.mjs';

function record(value) {
  const path = process.env.ANYPICK_PLUGIN_TEST_EVENTS;
  if (path) {
    appendFileSync(path, `${value}\n`, 'utf8');
  }
}

// Loopback HTTP proxy fixture (GO-PARITY-M7 provider-proxy slice). One
// listener per provider+account, hosted inside this plugin-host process. The
// engine owns the per-instance token (ADR 0006): startProxy refuses to run
// without one, requires it on every credentialed route, and echoes only the
// instance id on unauthenticated /health (ADR 0003 §3).
const proxyServers = new Map();

function proxyKey(context) {
  return `${context.providerId}\u0000${context.accountName}`;
}

function assertLoopback(host) {
  if (!['127.0.0.1', '::1', 'localhost'].includes(String(host ?? '127.0.0.1'))) {
    throw new Error(`Refusing non-loopback proxy host: ${host}`);
  }
}

async function fixtureStartProxy(context) {
  const host = context.config.host ?? '127.0.0.1';
  assertLoopback(host);
  const token = context.token;
  if (typeof token !== 'string' || !/^[0-9a-f]{64}$/.test(token)) {
    throw new Error('startProxy requires an engine-generated 32-byte hex proxy token');
  }
  // The engine redacts its own secret; the event log records only a hash so
  // tests can prove delivery without persisting the token anywhere public.
  record(`proxy:token:${createHash('sha256').update(token).digest('hex').slice(0, 16)}`);
  if (context.config.options?.fixture === 'fail-start') {
    record('proxy:start-failed');
    mkdirSync(context.runtimeDir, { recursive: true, mode: 0o700 });
    appendFileSync(join(context.runtimeDir, 'proxy.log'), '[fixture] failed before bind\n');
    throw new Error('fixture start failure');
  }
  const existing = proxyServers.get(proxyKey(context));
  if (existing) {
    record(`proxy:reuse:${context.accountName}`);
    return existing.handle;
  }
  if (context.config.options?.fixture === 'bind-cancel-late') {
    // Simulates a cancelled invocation whose coroutine keeps running: the
    // plugin parks on the host-owned signal, then binds AFTER the abort. The
    // engine's compensation grace window must catch this listener.
    record('proxy:late-hold');
    await new Promise((resolve) => {
      context.signal?.addEventListener('abort', () => resolve(), { once: true });
    });
    record('proxy:late-aborted');
    await new Promise((resolve) => setTimeout(resolve, 60));
  }
  if (context.config.options?.fixture === 'bind-cancel-late-overshoot') {
    record('proxy:late-hold');
    await new Promise((resolve) => {
      context.signal?.addEventListener('abort', () => resolve(), { once: true });
    });
    record('proxy:late-aborted');
    await new Promise((resolve) => setTimeout(resolve, 700));
  }
  if (context.config.options?.fixture === 'hold-start') {
    record('proxy:hold');
    await new Promise((resolve, reject) => {
      context.signal?.addEventListener(
        'abort',
        () => {
          record('proxy:hold-aborted');
          reject(new Error('fixture start cancelled'));
        },
        { once: true },
      );
    });
  }

  if (context.config.options?.fixture === 'bind-child') {
    const instanceId = randomUUID();
    const port = context.config.port;
    const host = context.config.host ?? '127.0.0.1';
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      throw new Error(
        `startProxy requires a concrete listen port, received ${JSON.stringify(port)}`,
      );
    }
    mkdirSync(context.runtimeDir, { recursive: true, mode: 0o700 });
    const logPath = join(context.runtimeDir, 'proxy.log');
    const childCode = `const http=require('http');const id=${JSON.stringify(instanceId)};const srv=http.createServer((req,res)=>{if(req.url==='/health'){res.setHeader('content-type','application/json');res.end(JSON.stringify({instanceId:id,pid:process.pid}));return;}res.end('ok');});srv.listen(${JSON.stringify(port)},${JSON.stringify(host)});setInterval(()=>{},1000);`;
    const child = spawn(process.execPath, ['-e', childCode], { stdio: 'ignore' });
    await new Promise((r) => setTimeout(r, 200));
    const handle = {
      endpoint: `http://${host === '::1' ? '[::1]' : host}:${port}`,
      compatibility: 'Fixture OpenAI API',
      pid: child.pid,
      instanceId,
      logPath,
    };
    proxyServers.set(proxyKey(context), {
      server: child,
      instanceId,
      handle,
      logPath,
      isChild: true,
      child,
    });
    record(`proxy:started:${instanceId}`);
    writeFileSync(
      join(context.runtimeDir, 'proxy.pid'),
      JSON.stringify({
        recordVersion: 1,
        instanceId,
        pid: child.pid,
        provider: context.providerId,
        account: context.accountName,
        createdAt: new Date().toISOString(),
        endpoint: `http://${host}:${port}`,
      }),
      { mode: 0o600 },
    );
    appendFileSync(
      logPath,
      `[fixture] proxy ${instanceId} listening on ${host}:${port} (child ${child.pid})\n`,
    );
    return handle;
  }
  if (context.config.options?.fixture === 'bind-child-detached') {
    record(`proxy:bind-child-detached:runtime:${context.runtimeDir}`);
    const instanceId = randomUUID();
    const port = context.config.port;
    const host = context.config.host ?? '127.0.0.1';
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      throw new Error(
        `startProxy requires a concrete listen port, received ${JSON.stringify(port)}`,
      );
    }
    try {
      mkdirSync(context.runtimeDir, { recursive: true, mode: 0o700 });
    } catch (e) {
      record(`proxy:mkdir-err:${e.message}`);
      throw e;
    }
    const logPath = join(context.runtimeDir, 'proxy.log');
    const childCode = `const http=require('http');const id=${JSON.stringify(instanceId)};const srv=http.createServer((req,res)=>{if(req.url==='/health'){res.setHeader('content-type','application/json');res.end(JSON.stringify({instanceId:id,pid:process.pid}));return;}res.end('ok');});srv.listen(${JSON.stringify(port)},${JSON.stringify(host)});setInterval(()=>{},1000);`;
    const child = spawn(process.execPath, ['-e', childCode], { detached: true, stdio: 'ignore' });
    child.unref();
    await new Promise((r) => setTimeout(r, 300));
    const handle = {
      endpoint: `http://${host === '::1' ? '[::1]' : host}:${port}`,
      compatibility: 'Fixture OpenAI API',
      pid: child.pid,
      instanceId,
      logPath,
    };
    proxyServers.set(proxyKey(context), {
      server: child,
      instanceId,
      handle,
      logPath,
      isChild: true,
      child,
    });
    record(`proxy:started:${instanceId}`);
    try {
      writeFileSync(
        join(context.runtimeDir, 'proxy.pid'),
        JSON.stringify({
          recordVersion: 1,
          instanceId,
          pid: child.pid,
          provider: context.providerId,
          account: context.accountName,
          createdAt: new Date().toISOString(),
          endpoint: `http://${host}:${port}`,
        }),
        { mode: 0o600 },
      );
      record(`proxy:pid-written:${child.pid}`);
    } catch (e) {
      record(`proxy:pid-err:${e.message}`);
      throw e;
    }
    appendFileSync(
      logPath,
      `[fixture] proxy ${instanceId} listening on ${host}:${port} (child ${child.pid})\n`,
    );
    record(`proxy:bind-child-detached-done:${instanceId}`);
    return handle;
  }

  if (context.config.options?.fixture === 'bind-child-detached-abort') {
    // The P1 orphan shape: on abort the plugin detaches a child that binds
    // LONG after every compensation window and publishes the scoped ownership
    // record first (the engine's only identity for it). The start coroutine
    // then parks forever, so the host can never settle and gets terminated.
    // The engine must consult the record and kill the late binder instead of
    // trusting an empty planned socket.
    record('proxy:detached-hold');
    await new Promise((resolve) => {
      context.signal?.addEventListener('abort', () => resolve(), { once: true });
    });
    const instanceId = randomUUID();
    const port = context.config.port;
    const host = context.config.host ?? '127.0.0.1';
    if (!Number.isInteger(port) || port <= 0 || port > 65535) {
      throw new Error(
        `startProxy requires a concrete listen port, received ${JSON.stringify(port)}`,
      );
    }
    mkdirSync(context.runtimeDir, { recursive: true, mode: 0o700 });
    const logPath = join(context.runtimeDir, 'proxy.log');
    const childCode = `const http=require('http');const id=${JSON.stringify(instanceId)};setTimeout(()=>{const srv=http.createServer((req,res)=>{if(req.url==='/health'){res.setHeader('content-type','application/json');res.end(JSON.stringify({instanceId:id,pid:process.pid}));return;}res.end('ok');});srv.listen(${JSON.stringify(port)},${JSON.stringify(host)});},1500);setInterval(()=>{},1000);`;
    const child = spawn(process.execPath, ['-e', childCode], { detached: true, stdio: 'ignore' });
    child.unref();
    writeFileSync(
      join(context.runtimeDir, 'proxy.pid'),
      JSON.stringify({
        recordVersion: 1,
        instanceId,
        pid: child.pid,
        provider: context.providerId,
        account: context.accountName,
        createdAt: new Date().toISOString(),
        endpoint: `http://${host}:${port}`,
      }),
      { mode: 0o600 },
    );
    appendFileSync(
      logPath,
      `[fixture] detached proxy ${instanceId} will bind ${host}:${port} late (child ${child.pid})\n`,
    );
    record(`proxy:detached-spawned:${child.pid}:${instanceId}`);
    await new Promise(() => {}); // park forever: the runtime must be terminated
  }

  const instanceId = randomUUID();
  const port = context.config.port;
  if (!Number.isInteger(port) || port <= 0 || port > 65535) {
    throw new Error(`startProxy requires a concrete listen port, received ${JSON.stringify(port)}`);
  }
  mkdirSync(context.runtimeDir, { recursive: true, mode: 0o700 });
  const logPath = join(context.runtimeDir, 'proxy.log');
  const expected = Buffer.from(token, 'utf8');
  const server = http.createServer((request, response) => {
    if (request.url === '/health') {
      response.setHeader('content-type', 'application/json');
      response.end(JSON.stringify({ instanceId, pid: process.pid }));
      return;
    }
    const header = request.headers.authorization ?? '';
    const provided = header.startsWith('Bearer ')
      ? Buffer.from(header.slice('Bearer '.length), 'utf8')
      : request.headers['x-api-key'] !== undefined
        ? Buffer.from(String(request.headers['x-api-key']), 'utf8')
        : null;
    if (!provided || provided.length !== expected.length || !timingSafeEqual(provided, expected)) {
      response.statusCode = 401;
      response.end();
      return;
    }
    response.setHeader('content-type', 'application/json');
    response.end(JSON.stringify({ ok: true, account: context.accountName }));
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, host, resolve);
  });
  appendFileSync(logPath, `[fixture] proxy ${instanceId} listening on ${host}:${port}\n`);
  // Structured ownership record in the runtime dir mirrors ADR-0003; identity
  // decisions still belong to the /health echo, never this numeric pid.
  writeFileSync(
    join(context.runtimeDir, 'proxy.pid'),
    JSON.stringify({
      recordVersion: 1,
      instanceId,
      pid: process.pid,
      provider: context.providerId,
      account: context.accountName,
      createdAt: new Date().toISOString(),
    }),
    { mode: 0o600 },
  );
  const handle = {
    endpoint: `http://${host === '::1' ? '[::1]' : host}:${port}`,
    compatibility: 'Fixture OpenAI API',
    pid: process.pid,
    instanceId,
    logPath,
  };
  proxyServers.set(proxyKey(context), { server, instanceId, handle, logPath });
  record(`proxy:started:${instanceId}`);
  // Bind-then-throw modes: the listener is LIVE and registered when startProxy
  // still rejects — exactly the orphan shape the engine must compensate.
  const fixtureMode = context.config.options?.fixture;
  if (
    fixtureMode === 'bind-throw' ||
    fixtureMode === 'bind-cancel-late' ||
    fixtureMode === 'bind-cancel-late-overshoot'
  ) {
    throw new Error('fixture late failure after bind');
  }
  return handle;
}

async function fixtureStopProxy(context) {
  record(`proxy:stop:${context.accountName}`);
  const entry = proxyServers.get(proxyKey(context));
  if (!entry) {
    record('proxy:stop-noop');
    return; // Must not fail when already stopped.
  }
  if (entry.isChild) {
    try {
      process.kill(entry.child.pid, 'SIGTERM');
    } catch {}
    await new Promise((r) => setTimeout(r, 100));
    try {
      process.kill(entry.child.pid, 'SIGKILL');
    } catch {}
  } else {
    await new Promise((resolve) => entry.server.close(resolve));
  }
  proxyServers.delete(proxyKey(context));
  rmSync(join(context.runtimeDir, 'proxy.pid'), { force: true });
  appendFileSync(entry.logPath, `[fixture] proxy ${entry.instanceId} stopped\n`);
  record(`proxy:stopped:${entry.instanceId}`);
}

async function fixtureProxyStatus(context) {
  const entry = proxyServers.get(proxyKey(context));
  if (!entry) {
    return { enabled: context.config.enabled === true, running: false, detail: 'stopped' };
  }
  return {
    enabled: true,
    running: true,
    port: context.config.port,
    host: context.config.host ?? '127.0.0.1',
    endpoint: entry.handle.endpoint,
    compatibility: entry.handle.compatibility,
    pid: entry.handle.pid,
    logPath: entry.handle.logPath,
  };
}

async function fixtureReadProxyLogs(context, lines = 50) {
  try {
    const raw = readFileSync(join(context.runtimeDir, 'proxy.log'), 'utf8');
    const all = raw.split(/\r?\n/).filter(Boolean);
    return all.slice(Math.max(0, all.length - lines)).join('\n');
  } catch {
    return '(no log file yet)';
  }
}

record(`import:${basename(import.meta.dirname)}`);

class FixtureProvider {
  id = 'fixture-provider';
  name = 'Fixture Provider';
  description = 'Provider retained behind an RPC handle';
  nested = { helperValue };

  async detectLive() {
    console.log('provider stdout stays off the protocol stream');
    return { present: true, identity: 'fixture@example.test' };
  }

  loginMethods() {
    return [
      {
        id: 'fixture-browser',
        label: 'Fixture Browser',
        provenance: 'community',
        preferred: true,
      },
      {
        id: 'fixture-wait',
        label: 'Fixture Wait',
        provenance: 'community',
        available: true,
      },
    ];
  }

  async login(context) {
    record(`login:${context.methodId}`);
    if (context.methodId === 'mutate-die') {
      // Mutation whose outcome dies with the host: recovery must never
      // replay it (the engine dispatched the frame, no response ever came).
      process.exit(44);
    }
    if (context.methodId !== 'fixture-wait') {
      return;
    }
    await new Promise((resolve, reject) => {
      context.signal.addEventListener(
        'abort',
        () => {
          record('login:aborted');
          reject(context.signal.reason);
        },
        { once: true },
      );
    });
  }

  liveUsage() {
    return {
      windows: [{ label: 'Fixture window', remainingPercent: 73, resetsAtMs: 2_000_000_000_000 }],
    };
  }

  async startProxy(context) {
    return fixtureStartProxy(context);
  }

  async stopProxy(context) {
    return fixtureStopProxy(context);
  }

  async proxyStatus(context) {
    return fixtureProxyStatus(context);
  }

  async readProxyLogs(context, lines) {
    return fixtureReadProxyLogs(context, lines);
  }

  echo(value) {
    return { value, helperValue, receiver: this.id };
  }

  sourceAdapter(account) {
    return {
      sourceRef: { kind: 'account', provider: this.id, name: account.meta.name },
      capabilities: {
        sourceKind: 'account',
        provider: this.id,
        nativeClients: [],
        protocols: ['openai'],
        canRefresh: false,
        supportsModelDiscovery: false,
      },
      transportFor(clientId) {
        return clientId === 'fixture-client' ? 'direct' : 'unsupported';
      },
    };
  }

  async wait(milliseconds) {
    await new Promise((resolve) => setTimeout(resolve, milliseconds));
    return { waited: milliseconds };
  }

  voidResult() {}

  crash() {
    process.exit(42);
  }
}

const client = {
  id: 'fixture-client',
  name: 'Fixture Client',
  description: 'Client retained behind an RPC handle',
  supportedApiStyles: ['openai'],
  capabilities: {
    id: 'fixture-client',
    acceptedProtocols: ['openai'],
    supportsEnvironmentOverlay: false,
    supportsIsolatedHome: true,
    supportsPersistentConfig: true,
    protocolPreference: 'openai',
  },
  async validate() {},
  async apply(context) {
    return {
      managedPaths: [`${context.anypickRoot}/fixture-client.json`],
      managedEnvKeys: ['FIXTURE_API_KEY'],
    };
  },
  async applyPersistent(plan) {
    record(`persistent:${plan.mode}:${plan.source.ref.kind}`);
    return {
      managedPaths: [`${plan.anypickRoot}/fixture-client-persistent.json`],
      managedEnvKeys: ['FIXTURE_PERSISTENT_API_KEY'],
    };
  },
  async listIsolatablePaths(liveState) {
    record(`isolation-paths:${basename(liveState.home)}`);
    return [];
  },
  async createIsolatedRuntime(plan) {
    const directory = process.env.ANYPICK_PLUGIN_TEST_RUNTIME;
    if (!directory) {
      throw new Error('ANYPICK_PLUGIN_TEST_RUNTIME is required');
    }
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    record(`isolation:create:${plan.source.ref.kind}`);
    return {
      directory,
      environment: {
        HOME: directory,
        FIXTURE_ENDPOINT: plan.transport.endpoint,
      },
      args: ['--fixture-isolated'],
      async cleanup() {
        rmSync(directory, { recursive: true, force: true });
        record('isolation:cleanup');
      },
    };
  },
  async createEnvironmentOverlay(plan) {
    const base = process.env.ANYPICK_PLUGIN_TEST_RUNTIME;
    if (!base) {
      throw new Error('ANYPICK_PLUGIN_TEST_RUNTIME is required');
    }
    const directory = `${base}-overlay`;
    mkdirSync(directory, { recursive: true, mode: 0o700 });
    record(`overlay:create:${plan.source.ref.kind}`);
    return {
      directory,
      environment: { FIXTURE_OVERLAY: plan.transport.endpoint },
      async cleanup() {
        rmSync(directory, { recursive: true, force: true });
        record('overlay:cleanup');
      },
    };
  },
  async reset() {},
  async inspect() {
    return { present: true, configPaths: ['/fixture/client.json'] };
  },
};

const catalog = {
  id: 'fixture-catalog',
  name: 'Fixture Catalog',
  description: 'Catalog retained behind an RPC handle',
  apiStyle: 'openai',
  defaultEndpoint: 'https://fixture.example.test/v1',
  suggestModels() {
    return { fast: 'fixture-fast', best: 'fixture-best' };
  },
};

export default {
  activate(context) {
    if (process.env.ANYPICK_PLUGIN_TEST_FAIL_ACTIVATE === '1') {
      throw new Error('fixture activate failure');
    }
    const omit = new Set(
      String(process.env.ANYPICK_PLUGIN_TEST_OMIT ?? '')
        .split(',')
        .map((value) => value.trim())
        .filter(Boolean),
    );
    if (!omit.has('provider')) {
      context.registerProvider(new FixtureProvider());
    }
    if (!omit.has('client')) {
      context.registerClient(client);
    }
    if (!omit.has('catalog')) {
      context.registerCatalogProvider(catalog);
    }
    if (omit.has('compatible')) {
      return;
    }
    context.registerCompatibleProvider({
      id: 'fixture-compatible',
      name: 'Fixture Compatible',
      shortName: 'Fixture',
      description: 'Inert compatible-provider definition',
      defaultProxyPort: 43123,
      models: ['fixture-model'],
      credentials: [
        {
          kind: 'api-key',
          label: 'Fixture API key',
          credentialClass: 'provider_api_key',
          profiles: [
            {
              id: 'global',
              label: 'Global',
              openaiBase: 'https://fixture.example.test/v1',
            },
          ],
        },
      ],
      quotaErrorPatterns: ['fixture quota exhausted'],
    });
  },
  dispose() {
    record(`dispose:${basename(import.meta.dirname)}`);
  },
};
