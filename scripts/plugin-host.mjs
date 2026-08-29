#!/usr/bin/env node

/**
 * Private ESM plugin host for the native Go engine.
 *
 * Stdout is reserved for bounded, newline-delimited protocol frames. Plugin
 * output is redirected to sanitized stderr before any untrusted module is
 * imported. The host deliberately has no dependency on AnyPick's TypeScript
 * runtime so a packaged native application can ship this one asset beside its
 * private Node executable.
 */

import { createHash, randomBytes } from 'node:crypto';
import { readdir, readFile, stat } from 'node:fs/promises';
import { isAbsolute, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';
import { TextDecoder } from 'node:util';

const PROTOCOL_VERSION = 1;
const PLUGIN_API_VERSION = 1;
const DEFAULT_MAX_FRAME_BYTES = 4 * 1024 * 1024;
const MAX_FRAME_BYTES = parseBoundedInteger(
  process.env.ANYPICK_PLUGIN_HOST_MAX_FRAME_BYTES,
  DEFAULT_MAX_FRAME_BYTES,
  4096,
  16 * 1024 * 1024,
);
const DIGEST_SKIP_NAMES = new Set(['.git', '.hg', '.svn', '.DS_Store', '__pycache__']);
const NAME_PATTERN = /^[a-z0-9][a-z0-9-]{1,63}$/;
const utf8Decoder = new TextDecoder('utf-8', { fatal: true });

const rawStdoutWrite = process.stdout.write.bind(process.stdout);
const rawStderrWrite = process.stderr.write.bind(process.stderr);

function parseBoundedInteger(value, fallback, minimum, maximum) {
  const parsed = Number.parseInt(value ?? '', 10);
  return Number.isSafeInteger(parsed) && parsed >= minimum && parsed <= maximum ? parsed : fallback;
}

function sanitizeLog(value) {
  const text = String(value)
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, '\ufffd')
    .replace(/[\r\n]+/g, ' ')
    .slice(0, 4096);
  return text.length === 0 ? '' : `[plugin-host] ${text}\n`;
}

function cancellationError() {
  return new HostError('CANCELLED', 'Request was cancelled.');
}

function throwIfAborted(signal) {
  if (signal?.aborted) throw cancellationError();
}

function pluginLog(...values) {
  const line = sanitizeLog(values.map((value) => String(value)).join(' '));
  if (line) rawStderrWrite(line);
}

// Install the stdout boundary before loading any plugin. A plugin may still
// intentionally write output, but it cannot corrupt the RPC stream.
process.stdout.write = function redirectedStdout(chunk, encoding, callback) {
  pluginLog(Buffer.isBuffer(chunk) ? chunk.toString(encoding || 'utf8') : chunk);
  if (typeof encoding === 'function') encoding();
  if (typeof callback === 'function') callback();
  return true;
};
process.stderr.write = function sanitizedStderr(chunk, encoding, callback) {
  pluginLog(Buffer.isBuffer(chunk) ? chunk.toString(encoding || 'utf8') : chunk);
  if (typeof encoding === 'function') encoding();
  if (typeof callback === 'function') callback();
  return true;
};
console.log = pluginLog;
console.info = pluginLog;
console.warn = pluginLog;
console.error = pluginLog;
console.debug = pluginLog;

class HostError extends Error {
  constructor(code, message, details = undefined) {
    super(message);
    this.name = 'HostError';
    this.code = code;
    this.details = details;
  }
}

function fail(code, message, details) {
  throw new HostError(code, message, details);
}

function assertObject(value, label) {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    fail('INVALID_REQUEST', `${label} must be an object.`);
  }
  return value;
}

function assertKeys(value, required, optional = []) {
  const allowed = new Set([...required, ...optional]);
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) fail('INVALID_REQUEST', `Unexpected field "${key}".`);
  }
  for (const key of required) {
    if (!(key in value)) fail('INVALID_REQUEST', `Missing field "${key}".`);
  }
}

function assertString(value, label, maximum = 4096) {
  if (typeof value !== 'string' || value.length === 0 || value.length > maximum) {
    fail(
      'INVALID_REQUEST',
      `${label} must be a non-empty string of at most ${maximum} characters.`,
    );
  }
  return value;
}

function strictClone(value, label) {
  let encoded;
  try {
    encoded = JSON.stringify(value);
  } catch {
    fail('UNSERIALIZABLE_REGISTRATION', `${label} must contain only JSON values.`);
  }
  if (encoded === undefined) {
    fail('UNSERIALIZABLE_REGISTRATION', `${label} must be JSON serializable.`);
  }
  try {
    return JSON.parse(encoded);
  } catch {
    fail('UNSERIALIZABLE_REGISTRATION', `${label} must contain only JSON values.`);
  }
}

function emit(frame) {
  const encoded = Buffer.from(`${JSON.stringify(frame)}\n`, 'utf8');
  if (encoded.byteLength > MAX_FRAME_BYTES) {
    fail('FRAME_TOO_LARGE', `Protocol frame exceeds ${MAX_FRAME_BYTES} bytes.`);
  }
  rawStdoutWrite(encoded);
}

function sendError(id, error, settled = true) {
  const code = error instanceof HostError ? error.code : 'PLUGIN_HOST_ERROR';
  const message = error instanceof Error ? error.message : String(error);
  const details = error instanceof HostError ? error.details : undefined;
  const frame = {
    version: PROTOCOL_VERSION,
    type: 'response',
    id,
    error: { code, message: message.slice(0, 8192), ...(details === undefined ? {} : { details }) },
    settled,
  };
  try {
    emit(frame);
  } catch (emitError) {
    emit({
      version: PROTOCOL_VERSION,
      type: 'response',
      id,
      error: {
        code: 'FRAME_TOO_LARGE',
        message: emitError instanceof Error ? emitError.message : 'Response was too large.',
      },
      settled,
    });
  }
}

async function listPluginFiles(root, signal) {
  const files = [];
  async function walk(directory) {
    throwIfAborted(signal);
    const entries = await readdir(directory, { withFileTypes: true });
    for (const entry of entries) {
      throwIfAborted(signal);
      if (DIGEST_SKIP_NAMES.has(entry.name)) continue;
      const full = resolve(directory, entry.name);
      if (entry.isDirectory()) {
        await walk(full);
        continue;
      }
      if (entry.isFile() || entry.isSymbolicLink()) {
        const info = await stat(full).catch(() => null);
        if (info?.isFile()) files.push(full);
      }
    }
  }
  await walk(root);
  return files;
}

async function digestPluginPackage(root, signal) {
  const files = await listPluginFiles(root, signal);
  const entries = files
    .map((full) => ({ full, rel: relative(root, full).split(sep).join('/') }))
    .sort((a, b) => a.rel.localeCompare(b.rel));
  const hash = createHash('sha256');
  for (const { full, rel } of entries) {
    throwIfAborted(signal);
    const bytes = await readFile(full);
    hash.update(rel);
    hash.update('\0');
    hash.update(String(bytes.byteLength));
    hash.update('\0');
    hash.update(bytes);
  }
  throwIfAborted(signal);
  return hash.digest('hex');
}

async function readAndValidateManifest(root, expectedName) {
  let manifest;
  try {
    manifest = JSON.parse(await readFile(resolve(root, 'anypick.plugin.json'), 'utf8'));
  } catch (error) {
    fail(
      'PLUGIN_INVALID',
      `Cannot read a valid anypick.plugin.json: ${error instanceof Error ? error.message : String(error)}`,
    );
  }
  assertObject(manifest, 'Plugin manifest');
  const name = manifest.name;
  if (typeof name !== 'string' || !NAME_PATTERN.test(name)) {
    fail('PLUGIN_INVALID', 'Plugin manifest has an invalid name.');
  }
  if (name !== expectedName) {
    fail(
      'PLUGIN_INVALID',
      `Plugin now calls itself "${name}" but is installed as "${expectedName}".`,
    );
  }
  if (typeof manifest.version !== 'string' || manifest.version.length === 0) {
    fail('PLUGIN_INVALID', 'Plugin manifest is missing a version string.');
  }
  if (manifest.apiVersion !== PLUGIN_API_VERSION) {
    fail(
      'PLUGIN_API_MISMATCH',
      `Plugin ${name} targets plugin API ${String(manifest.apiVersion)}; this host supports ${PLUGIN_API_VERSION}.`,
    );
  }
  if (
    typeof manifest.main !== 'string' ||
    manifest.main.length === 0 ||
    isAbsolute(manifest.main)
  ) {
    fail('PLUGIN_INVALID', 'Plugin entry must be a relative path inside the plugin directory.');
  }
  const entry = resolve(root, manifest.main);
  const rel = relative(root, entry);
  if (rel.startsWith('..') || isAbsolute(rel)) {
    fail(
      'PLUGIN_INVALID',
      `Plugin entry "${manifest.main}" resolves outside the plugin directory.`,
    );
  }
  return {
    manifest: {
      name,
      version: manifest.version,
      ...(typeof manifest.description === 'string' ? { description: manifest.description } : {}),
      apiVersion: PLUGIN_API_VERSION,
      main: manifest.main,
    },
    entry,
  };
}

function pluginExport(module) {
  const candidate = module.default ?? module;
  if (
    typeof candidate !== 'object' ||
    candidate === null ||
    typeof candidate.activate !== 'function'
  ) {
    fail('PLUGIN_INVALID', 'Plugin must export an object with an activate function.');
  }
  return candidate;
}

function methodNames(object) {
  const methods = new Set();
  let current = object;
  while (current && current !== Object.prototype) {
    for (const name of Object.getOwnPropertyNames(current)) {
      if (name !== 'constructor' && typeof object[name] === 'function') methods.add(name);
    }
    current = Object.getPrototypeOf(current);
  }
  return [...methods].sort((a, b) => a.localeCompare(b));
}

function descriptorProperties(object) {
  const properties = {};
  for (const key of Object.keys(object).sort((a, b) => a.localeCompare(b))) {
    if (typeof object[key] !== 'function' && object[key] !== undefined) {
      properties[key] = strictClone(object[key], `Property ${key}`);
    }
  }
  return properties;
}

const objects = new Map();
const activations = [];
let nextHandle = 0;

function stageDescriptor(kind, object, stagedObjects, activationId) {
  if (typeof object !== 'object' || object === null) {
    fail('INVALID_REGISTRATION', `${kind} registration must be an object.`);
  }
  const properties = descriptorProperties(object);
  if (typeof properties.id !== 'string' || properties.id.length === 0) {
    fail('INVALID_REGISTRATION', `${kind} registration must expose a string id property.`);
  }
  const handle = `object-${++nextHandle}`;
  const methods = methodNames(object);
  stagedObjects.set(handle, { target: object, methods: new Set(methods), activationId });
  return { kind, handle, properties, methods };
}

function retainObjectResult(value, activationId) {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    fail('INVALID_OBJECT_RESULT', 'Retained method result must be an object.');
  }
  const activation = activations.find((entry) => entry.activationId === activationId);
  if (!activation) {
    fail('ACTIVATION_DISPOSED', 'The owning plugin activation has already been disposed.');
  }
  const properties = descriptorProperties(value);
  const methods = methodNames(value);
  if (methods.length === 0) {
    fail('INVALID_OBJECT_RESULT', 'Retained method result must expose at least one method.');
  }
  const handle = `object-${++nextHandle}`;
  objects.set(handle, { target: value, methods: new Set(methods), activationId });
  activation.handles.push(handle);
  return { handle, properties, methods };
}

async function disposePlugin(plugin) {
  if (typeof plugin.dispose !== 'function') return;
  await plugin.dispose();
}

async function activate(params, signal) {
  const object = assertObject(params, 'activate params');
  assertKeys(object, ['root', 'trustedDigest', 'expectedName']);
  const root = assertString(object.root, 'root');
  if (!isAbsolute(root)) fail('INVALID_REQUEST', 'Plugin root must be absolute.');
  const expectedDigest = assertString(object.trustedDigest, 'trustedDigest', 128);
  if (!/^[a-f0-9]{64}$/.test(expectedDigest)) {
    fail('INVALID_REQUEST', 'trustedDigest must be a lowercase SHA-256 digest.');
  }
  const expectedName = assertString(object.expectedName, 'expectedName', 64);
  if (!NAME_PATTERN.test(expectedName)) {
    fail('INVALID_REQUEST', 'expectedName must be a valid plugin name.');
  }
  throwIfAborted(signal);
  const { manifest, entry } = await readAndValidateManifest(root, expectedName);
  throwIfAborted(signal);

  // This is intentionally performed by the host, immediately before import.
  // The Go caller's digest is a trust decision, not proof about current bytes.
  const actualDigest = await digestPluginPackage(root, signal);
  if (actualDigest !== expectedDigest) {
    fail('PLUGIN_UNTRUSTED', `Plugin ${expectedName} has changed since it was trusted.`, {
      expectedDigest,
      actualDigest,
      imported: false,
    });
  }

  let plugin;
  try {
    throwIfAborted(signal);
    plugin = pluginExport(
      await import(`${pathToFileURL(entry).href}?anypickDigest=${actualDigest}`),
    );
    throwIfAborted(signal);
  } catch (error) {
    if (error instanceof HostError) throw error;
    fail('PLUGIN_IMPORT_FAILED', error instanceof Error ? error.message : String(error));
  }

  const activationId = `activation-${randomBytes(12).toString('hex')}`;
  const descriptors = [];
  const compatibleProviders = [];
  const stagedObjects = new Map();
  let accepting = true;
  const ensureAccepting = () => {
    if (!accepting)
      fail('LATE_REGISTRATION', 'Plugin registrations are closed after activate returns.');
  };
  const context = Object.freeze({
    apiVersion: PLUGIN_API_VERSION,
    pluginRoot: root,
    registerProvider(value) {
      ensureAccepting();
      descriptors.push(stageDescriptor('provider', value, stagedObjects, activationId));
    },
    registerClient(value) {
      ensureAccepting();
      descriptors.push(stageDescriptor('client', value, stagedObjects, activationId));
    },
    registerCatalogProvider(value) {
      ensureAccepting();
      descriptors.push(stageDescriptor('catalog-provider', value, stagedObjects, activationId));
    },
    registerCompatibleProvider(value) {
      ensureAccepting();
      compatibleProviders.push(strictClone(value, 'Compatible provider definition'));
    },
  });

  try {
    const returned = plugin.activate(context);
    if (returned && typeof returned.then === 'function') {
      // Do not leave a rejected async activation as an unhandled process-level
      // error after this request has already been rolled back.
      void Promise.resolve(returned).catch(() => {});
      fail('ASYNC_ACTIVATE_UNSUPPORTED', 'Plugin activate must be synchronous.');
    }
    accepting = false;
    throwIfAborted(signal);
  } catch (error) {
    accepting = false;
    let disposalError;
    try {
      await disposePlugin(plugin);
    } catch (disposeError) {
      disposalError = disposeError instanceof Error ? disposeError.message : String(disposeError);
    }
    if (error instanceof HostError && error.code === 'CANCELLED') throw error;
    fail('ACTIVATION_FAILED', error instanceof Error ? error.message : String(error), {
      rolledBack: true,
      disposalAttempted: typeof plugin.dispose === 'function',
      ...(disposalError ? { disposalError: disposalError.slice(0, 4096) } : {}),
    });
  }

  // This is the atomic host commit point. Every async boundary above checks
  // cancellation; a timed-out activation can never publish staged handles.
  throwIfAborted(signal);
  for (const [handle, value] of stagedObjects) objects.set(handle, value);
  activations.push({ activationId, plugin, handles: [...stagedObjects.keys()] });
  return { activationId, manifest, descriptors, compatibleProviders };
}

async function invoke(params, signal) {
  const object = assertObject(params, 'invoke params');
  assertKeys(object, ['handle', 'method', 'arguments'], ['retainResult', 'signalArgumentIndex']);
  const handle = assertString(object.handle, 'handle', 128);
  const method = assertString(object.method, 'method', 256);
  if (!Array.isArray(object.arguments)) fail('INVALID_REQUEST', 'arguments must be an array.');
  if (object.retainResult !== undefined && typeof object.retainResult !== 'boolean') {
    fail('INVALID_REQUEST', 'retainResult must be a boolean when provided.');
  }
  let args = object.arguments;
  if (object.signalArgumentIndex !== undefined) {
    if (
      !Number.isSafeInteger(object.signalArgumentIndex) ||
      object.signalArgumentIndex < 0 ||
      object.signalArgumentIndex >= args.length
    ) {
      fail('INVALID_REQUEST', 'signalArgumentIndex must identify an object argument.');
    }
    const original = assertObject(args[object.signalArgumentIndex], 'signal argument');
    args = [...args];
    args[object.signalArgumentIndex] = Object.freeze({ ...original, signal });
  }
  const registered = objects.get(handle);
  if (!registered) fail('UNKNOWN_HANDLE', `Unknown plugin object handle "${handle}".`);
  if (!registered.methods.has(method) || typeof registered.target[method] !== 'function') {
    fail('UNKNOWN_METHOD', `Plugin object ${handle} has no method "${method}".`);
  }
  const result = await registered.target[method](...args);
  if (object.retainResult === true) {
    return retainObjectResult(result, registered.activationId);
  }
  // JavaScript adapter methods commonly return void. JSON has no undefined,
  // so the protocol represents a successful void result as null.
  return result === undefined ? null : strictClone(result, `Result of ${method}`);
}

async function disposeActivation(params) {
  const object = assertObject(params, 'dispose params');
  assertKeys(object, ['activationId']);
  const activationId = assertString(object.activationId, 'activationId', 128);
  const index = activations.findIndex((entry) => entry.activationId === activationId);
  if (index < 0) return { disposed: false };
  const [activation] = activations.splice(index, 1);
  for (const handle of activation.handles) objects.delete(handle);
  await disposePlugin(activation.plugin);
  return { disposed: true };
}

async function disposeAll() {
  const errors = [];
  while (activations.length > 0) {
    const activation = activations.pop();
    for (const handle of activation.handles) objects.delete(handle);
    try {
      await disposePlugin(activation.plugin);
    } catch (error) {
      errors.push({
        activationId: activation.activationId,
        message: (error instanceof Error ? error.message : String(error)).slice(0, 4096),
      });
    }
  }
  return errors;
}

const operations = new Map();
let ready = false;
let shuttingDown = false;

function abortable(operation, signal) {
  return Promise.race([
    operation,
    new Promise((_, reject) => {
      if (signal.aborted) {
        reject(cancellationError());
        return;
      }
      signal.addEventListener('abort', () => reject(cancellationError()), { once: true });
    }),
  ]);
}

async function runRequest(frame) {
  const controller = new AbortController();
  operations.set(frame.id, controller);
  let timer;
  try {
    const timeoutMs = frame.timeoutMs;
    const baseOperation = (async () => {
      switch (frame.method) {
        case 'activate':
          return activate(frame.params, controller.signal);
        case 'invoke':
          return invoke(frame.params, controller.signal);
        case 'dispose':
          return disposeActivation(frame.params);
        case 'status':
          return { activationCount: activations.length, objectCount: objects.size };
        case 'shutdown': {
          shuttingDown = true;
          return { disposalErrors: await disposeAll() };
        }
        default:
          fail('UNKNOWN_METHOD', `Unknown host method "${frame.method}".`);
      }
    })();
    const operation = baseOperation;
    const abortPromise = new Promise((_, reject) => {
      if (controller.signal.aborted) {
        reject(cancellationError());
        return;
      }
      controller.signal.addEventListener('abort', () => reject(cancellationError()), {
        once: true,
      });
    });
    const timeout = new Promise((_, reject) => {
      timer = setTimeout(() => {
        controller.abort();
        reject(new HostError('TIMEOUT', `Request exceeded its ${timeoutMs}ms host deadline.`));
      }, timeoutMs);
      timer.unref();
    });
    try {
      const result = await Promise.race([operation, abortPromise, timeout]);
      emit({ version: PROTOCOL_VERSION, type: 'response', id: frame.id, result, settled: true });
    } catch (error) {
      const isAbort =
        error instanceof HostError && (error.code === 'CANCELLED' || error.code === 'TIMEOUT');
      if (isAbort) {
        const settleBudgetMs = 500;
        const didSettle = await Promise.race([
          operation.then(
            () => true,
            () => true,
          ),
          new Promise((resolve) => setTimeout(() => resolve(false), settleBudgetMs)),
        ]);
        sendError(frame.id, error, didSettle);
        operation.catch(() => {});
      } else {
        sendError(frame.id, error, true);
      }
    }
  } catch (error) {
    sendError(frame.id, error, true);
  } finally {
    if (timer) clearTimeout(timer);
    operations.delete(frame.id);
    if (frame.method === 'shutdown' && shuttingDown) setImmediate(() => process.exit(0));
  }
}

function handleFrame(value) {
  const frame = assertObject(value, 'Protocol frame');
  if (!ready) {
    assertKeys(frame, ['version', 'type', 'protocolVersion', 'client']);
    if (
      frame.version !== PROTOCOL_VERSION ||
      frame.type !== 'hello' ||
      frame.protocolVersion !== PROTOCOL_VERSION ||
      frame.client !== 'anypick-go'
    ) {
      fail('HANDSHAKE_REJECTED', 'Plugin-host protocol handshake was rejected.');
    }
    ready = true;
    emit({
      version: PROTOCOL_VERSION,
      type: 'ready',
      protocolVersion: PROTOCOL_VERSION,
      maxFrameBytes: MAX_FRAME_BYTES,
    });
    return;
  }
  if (frame.version !== PROTOCOL_VERSION || typeof frame.type !== 'string') {
    fail('INVALID_REQUEST', 'Unsupported protocol frame version or type.');
  }
  if (frame.type === 'cancel') {
    assertKeys(frame, ['version', 'type', 'id']);
    const id = assertString(frame.id, 'id', 128);
    operations.get(id)?.abort();
    return;
  }
  if (frame.type !== 'request') fail('INVALID_REQUEST', `Unexpected frame type "${frame.type}".`);
  assertKeys(frame, ['version', 'type', 'id', 'method', 'params', 'timeoutMs']);
  const id = assertString(frame.id, 'id', 128);
  if (operations.has(id)) fail('INVALID_REQUEST', `Request id "${id}" is already active.`);
  if (shuttingDown) fail('INVALID_REQUEST', 'Plugin host is shutting down.');
  assertString(frame.method, 'method', 128);
  if (!Number.isSafeInteger(frame.timeoutMs) || frame.timeoutMs < 1 || frame.timeoutMs > 300_000) {
    fail('INVALID_REQUEST', 'timeoutMs must be an integer between 1 and 300000.');
  }
  void runRequest(frame);
}

let buffered = Buffer.alloc(0);
let inputFailed = false;

function failInput(message) {
  if (inputFailed) return;
  inputFailed = true;
  pluginLog(message);
  process.stdin.destroy();
  setImmediate(() => process.exit(64));
}

function consumeLine(line) {
  if (line.byteLength === 0 || inputFailed) return;
  // The negotiated bound includes the newline delimiter, matching emit().
  if (line.byteLength + 1 > MAX_FRAME_BYTES) {
    failInput(`input frame exceeded ${MAX_FRAME_BYTES} bytes`);
    return;
  }
  try {
    handleFrame(JSON.parse(utf8Decoder.decode(line)));
  } catch (error) {
    // Protocol errors have no trustworthy request id. Closing the peer is
    // safer than attempting to continue on a potentially desynchronized stream.
    failInput(error instanceof Error ? error.message : String(error));
  }
}

process.stdin.on('data', (chunk) => {
  if (inputFailed) return;
  let offset = 0;
  while (offset < chunk.byteLength) {
    const newline = chunk.indexOf(0x0a, offset);
    if (newline < 0) {
      const remainder = chunk.subarray(offset);
      if (buffered.byteLength + remainder.byteLength + 1 > MAX_FRAME_BYTES) {
        failInput(`input frame exceeded ${MAX_FRAME_BYTES} bytes`);
        return;
      }
      buffered =
        buffered.byteLength === 0 ? Buffer.from(remainder) : Buffer.concat([buffered, remainder]);
      return;
    }

    const segment = chunk.subarray(offset, newline);
    if (buffered.byteLength + segment.byteLength + 1 > MAX_FRAME_BYTES) {
      failInput(`input frame exceeded ${MAX_FRAME_BYTES} bytes`);
      return;
    }
    const line = buffered.byteLength === 0 ? segment : Buffer.concat([buffered, segment]);
    buffered = Buffer.alloc(0);
    consumeLine(line);
    if (inputFailed) return;
    offset = newline + 1;
  }
});
process.stdin.on('end', async () => {
  if (buffered.byteLength > 0 && !inputFailed) {
    failInput('input ended with an unterminated protocol frame');
    return;
  }
  await disposeAll();
  process.exit(0);
});
process.stdin.on('error', () => process.exit(74));

emit({
  version: PROTOCOL_VERSION,
  type: 'hello',
  protocolVersion: PROTOCOL_VERSION,
  pluginApiVersion: PLUGIN_API_VERSION,
  maxFrameBytes: MAX_FRAME_BYTES,
  capabilities: [
    'activate',
    'cancel',
    'dispose',
    'invoke',
    'invoke-abort-signal',
    'retain-object-results',
    'reverse-disposal',
    'settled-acknowledgement',
  ],
});
