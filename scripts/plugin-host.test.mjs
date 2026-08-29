import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createInterface } from 'node:readline';
import test from 'node:test';

const hostScript = fileURLToPath(new URL('./plugin-host.mjs', import.meta.url));
const fixturePlugin = fileURLToPath(
  new URL('../go/internal/pluginhost/testdata/full-plugin', import.meta.url),
);

function freePort() {
  return new Promise((resolvePromise, rejectPromise) => {
    const server = createServer();
    server.listen(0, '127.0.0.1', () => {
      const { port } = server.address();
      server.close(() => resolvePromise(port));
    });
    server.once('error', rejectPromise);
  });
}

const engineToken = () => createHash('sha256').update(randomUUID()).digest('hex');

class HostPeer {
  constructor(extraEnv = {}) {
    this.child = spawn(process.execPath, [hostScript], {
      env: {
        ...process.env,
        ...extraEnv,
        ANYPICK_PLUGIN_HOST_MAX_FRAME_BYTES: '4096',
      },
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.lines = [];
    this.waiters = [];
    this.stderr = '';
    const lines = createInterface({ input: this.child.stdout });
    lines.on('line', (line) => {
      const waiter = this.waiters.shift();
      if (waiter) waiter(line);
      else this.lines.push(line);
    });
    this.child.stderr.setEncoding('utf8');
    this.child.stderr.on('data', (chunk) => {
      this.stderr += String(chunk);
    });
    this.exited = new Promise((resolve) => {
      this.child.once('exit', (code, signal) => resolve({ code, signal }));
    });
  }

  async handshake() {
    const hello = await this.nextFrame();
    assert.equal(hello.type, 'hello');
    assert.equal(hello.protocolVersion, 1);
    assert.equal(hello.maxFrameBytes, 4096);
    assert.ok(hello.capabilities.includes('invoke-abort-signal'));
    this.send({
      version: 1,
      type: 'hello',
      protocolVersion: 1,
      client: 'anypick-go',
    });
    const ready = await this.nextFrame();
    assert.equal(ready.type, 'ready');
  }

  send(frame) {
    this.writeRaw(Buffer.from(`${JSON.stringify(frame)}\n`, 'utf8'));
  }

  writeRaw(value) {
    this.child.stdin.write(value);
  }

  async nextFrame() {
    const line = this.lines.shift() ?? (await new Promise((resolve) => this.waiters.push(resolve)));
    const value = JSON.parse(line);
    assert.equal(typeof value, 'object');
    assert.notEqual(value, null);
    return value;
  }

  close() {
    if (this.child.exitCode === null && this.child.signalCode === null) this.child.kill();
  }
}

function withPeer(t, extraEnv = {}) {
  const peer = new HostPeer(extraEnv);
  t.after(() => peer.close());
  return peer;
}

test('handshakes and serves strictly identified requests', async (t) => {
  const peer = withPeer(t);
  await peer.handshake();
  peer.send({
    version: 1,
    type: 'request',
    id: 'status-1',
    method: 'status',
    params: {},
    timeoutMs: 1000,
  });
  const response = await peer.nextFrame();
  assert.equal(response.version, 1);
  assert.equal(response.type, 'response');
  assert.equal(response.id, 'status-1');
  assert.deepEqual(response.result, { activationCount: 0, objectCount: 0 });
  assert.equal(response.settled, true);
  peer.send({
    version: 1,
    type: 'request',
    id: 'shutdown-1',
    method: 'shutdown',
    params: {},
    timeoutMs: 1000,
  });
  assert.equal((await peer.nextFrame()).id, 'shutdown-1');
  assert.deepEqual(await peer.exited, { code: 0, signal: null });
});

test('rejects a duplicate active request id', async (t) => {
  const peer = withPeer(t);
  await peer.handshake();
  const request = JSON.stringify({
    version: 1,
    type: 'request',
    id: 'duplicate',
    method: 'status',
    params: {},
    timeoutMs: 1000,
  });
  peer.writeRaw(Buffer.from(`${request}\n${request}\n`, 'utf8'));
  assert.deepEqual(await peer.exited, { code: 64, signal: null });
  assert.match(peer.stderr, /already active/);
});

test('rejects malformed UTF-8 and oversized input without parsing it', async (t) => {
  const malformed = withPeer(t);
  await malformed.handshake();
  malformed.writeRaw(Buffer.from([0xff, 0x0a]));
  assert.deepEqual(await malformed.exited, { code: 64, signal: null });

  const oversized = withPeer(t);
  await oversized.handshake();
  oversized.writeRaw(Buffer.concat([Buffer.alloc(4096, 0x20), Buffer.from('\n')]));
  assert.deepEqual(await oversized.exited, { code: 64, signal: null });
  assert.match(oversized.stderr, /exceeded 4096 bytes/);
});

test('rejects unknown request fields instead of accepting protocol drift', async (t) => {
  const peer = withPeer(t);
  await peer.handshake();
  peer.send({
    version: 1,
    type: 'request',
    id: 'drift',
    method: 'status',
    params: {},
    timeoutMs: 1000,
    accidentalNewField: true,
  });
  assert.deepEqual(await peer.exited, { code: 64, signal: null });
  assert.match(peer.stderr, /Unexpected field/);
});

// ── GO-PARITY-M7 provider-proxy fixture contract (Node side) ────────────────
//
// The Go suite proves the same lifecycle through ready.App.Proxy; this test
// pins the fixture's own behavior at the raw protocol level, including the
// host-owned AbortSignal injection and cancellation delivery.

const DIGEST_SKIP_NAMES = new Set(['.git', '.hg', '.svn', '.DS_Store', '__pycache__']);

async function digestPluginPackage(root) {
  const files = [];
  async function walk(directory) {
    const { readdirSync, statSync } = await import('node:fs');
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (DIGEST_SKIP_NAMES.has(entry.name)) continue;
      const full = resolve(directory, entry.name);
      if (entry.isDirectory()) {
        await walk(full);
        continue;
      }
      files.push({ full, rel: relative(root, full).split(sep).join('/') });
    }
  }
  await walk(root);
  files.sort((a, b) => a.rel.localeCompare(b.rel));
  const hash = createHash('sha256');
  for (const { full, rel } of files) {
    const bytes = readFileSync(full);
    hash.update(rel);
    hash.update('\0');
    hash.update(String(bytes.byteLength));
    hash.update('\0');
    hash.update(bytes);
  }
  return hash.digest('hex');
}

function request(id, method, params, timeoutMs = 4000) {
  return { version: 1, type: 'request', id, method, params, timeoutMs };
}

async function invoke(peer, id, handle, method, args, extra = {}) {
  peer.send(request(id, 'invoke', { handle, method, arguments: args, ...extra }));
  for (;;) {
    const frame = await peer.nextFrame();
    assert.equal(frame.type, 'response');
    assert.equal(frame.id, id);
    if (frame.error) return { error: frame.error };
    return { result: frame.result };
  }
}

test('fixture proxy serves a token-gated loopback listener with verified stop', async (t) => {
  const runtime = mkdtempSync(join(tmpdir(), 'anypick-proxy-fixture-'), { recursive: true });
  t.after(() => rmSync(runtime, { recursive: true, force: true }));

  const peer = withPeer(t);
  await peer.handshake();
  const digest = await digestPluginPackage(fixturePlugin);
  peer.send(
    request('activate-1', 'activate', {
      root: fixturePlugin,
      trustedDigest: digest,
      expectedName: 'fixture-plugin',
    }),
  );
  const activated = await peer.nextFrame();
  assert.equal(activated.error, undefined);
  const providerDescriptor = activated.result.descriptors.find(
    (descriptor) => descriptor.kind === 'provider',
  );
  assert.ok(providerDescriptor.methods.includes('startProxy'));

  const context = {
    providerId: 'fixture-provider',
    accountName: 'work',
    snapshotDir: join(runtime, 'snapshot'),
    runtimeDir: join(runtime, 'runtime'),
    config: { enabled: true, port: 0, host: '127.0.0.1' },
    token: engineToken(),
  };
  // Port 0 is invalid for the fixture: it demands the planned concrete port.
  const { error: zeroPortError } = await invoke(
    peer,
    'start-zero',
    providerDescriptor.handle,
    'startProxy',
    [context],
    { signalArgumentIndex: 0 },
  );
  assert.equal(zeroPortError?.code, 'PLUGIN_HOST_ERROR');

  context.config.port = await freePort();
  const start = await invoke(peer, 'start-1', providerDescriptor.handle, 'startProxy', [context], {
    signalArgumentIndex: 0,
  });
  assert.equal(start.error, undefined);
  assert.match(start.result.endpoint, /^http:\/\/127\.0\.0\.1:\d+$/);
  assert.ok(start.result.instanceId.length >= 36);
  assert.match(start.result.logPath, /proxy\.log$/);

  // Unauthenticated /health echoes ownership only; credentialed routes need
  // the engine-generated bearer token.
  const health = await fetch(`${start.result.endpoint}/health`);
  assert.deepEqual((await health.json()).instanceId, start.result.instanceId);
  assert.equal((await fetch(`${start.result.endpoint}/v1/models`)).status, 401);
  assert.equal(
    (
      await fetch(`${start.result.endpoint}/v1/models`, {
        headers: { authorization: `Bearer ${context.token}` },
      })
    ).status,
    200,
  );

  // Repeated start reuses the same listener instead of binding a second one.
  const reuse = await invoke(
    peer,
    'start-2',
    providerDescriptor.handle,
    'startProxy',
    [{ ...context }],
    { signalArgumentIndex: 0 },
  );
  assert.equal(reuse.error, undefined);
  assert.deepEqual(
    { endpoint: reuse.result.endpoint, instanceId: reuse.result.instanceId },
    { endpoint: start.result.endpoint, instanceId: start.result.instanceId },
  );

  const status = await invoke(peer, 'status-1', providerDescriptor.handle, 'proxyStatus', [
    { ...context },
  ]);
  assert.equal(status.result.running, true);
  assert.equal(status.result.endpoint, start.result.endpoint);

  const logs = await invoke(peer, 'logs-1', providerDescriptor.handle, 'readProxyLogs', [
    { ...context },
    10,
  ]);
  assert.match(logs.result, new RegExp(`proxy ${start.result.instanceId} listening on`));

  const stopped = await invoke(peer, 'stop-1', providerDescriptor.handle, 'stopProxy', [
    { ...context },
  ]);
  assert.equal(stopped.error, undefined);
  await assert.rejects(fetch(`${start.result.endpoint}/health`));
  const noopStop = await invoke(peer, 'stop-2', providerDescriptor.handle, 'stopProxy', [
    { ...context },
  ]);
  assert.equal(noopStop.error, undefined); // must not throw when already stopped

  peer.send(request('shutdown-px', 'shutdown', {}));
  await peer.nextFrame();
});

test('cancelled startProxy aborts through the host-owned signal', async (t) => {
  const runtime = mkdtempSync(join(tmpdir(), 'anypick-proxy-hold-'), { recursive: true });
  const events = join(runtime, 'events.log');
  t.after(() => rmSync(runtime, { recursive: true, force: true }));

  const peer = withPeer(t, { ANYPICK_PLUGIN_TEST_EVENTS: events });
  await peer.handshake();
  const digest = await digestPluginPackage(fixturePlugin);
  peer.send(
    request('activate-2', 'activate', {
      root: fixturePlugin,
      trustedDigest: digest,
      expectedName: 'fixture-plugin',
    }),
  );
  const activated = await peer.nextFrame();
  assert.equal(activated.error, undefined);
  const providerHandle = activated.result.descriptors.find(
    (descriptor) => descriptor.kind === 'provider',
  ).handle;

  const context = {
    providerId: 'fixture-provider',
    accountName: 'hold',
    snapshotDir: join(runtime, 'snapshot'),
    runtimeDir: join(runtime, 'runtime'),
    config: {
      enabled: true,
      port: await freePort(),
      host: '127.0.0.1',
      options: { fixture: 'hold-start' },
    },
    token: engineToken(),
  };
  peer.send(
    request('start-hold', 'invoke', {
      handle: providerHandle,
      method: 'startProxy',
      arguments: [context],
      signalArgumentIndex: 0,
    }),
  );
  // No ack frame exists: wait until the plugin coroutine is parked on the
  // injected signal before cancelling.
  await waitForEvent(events, 'proxy:hold');
  peer.send({ version: 1, type: 'cancel', id: 'start-hold' });
  let cancelled;
  for (;;) {
    const frame = await peer.nextFrame();
    if (frame.id === 'start-hold') {
      cancelled = frame;
      break;
    }
  }
  assert.equal(cancelled.error?.code, 'CANCELLED');
  await waitForEvent(events, 'proxy:hold-aborted');
});

async function waitForEvent(path, value) {
  const deadline = Date.now() + 4000;
  for (;;) {
    try {
      if (readFileSync(path, 'utf8').includes(value)) return;
    } catch {
      // File may not exist yet.
    }
    if (Date.now() > deadline) throw new Error(`event ${value} did not arrive`);
    await new Promise((resolvePromise) => setTimeout(resolvePromise, 20));
  }
}
