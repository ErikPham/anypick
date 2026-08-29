#!/usr/bin/env node

import { spawn } from 'node:child_process';
import { accessSync, constants, readFileSync, realpathSync, statSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

/** @typedef {{ version: string }} RootPackage */
/** @typedef {{ key: string, packageName: string, binaryPath: string }} PlatformTarget */
/** @typedef {{ code: number | null, signal: NodeJS.Signals | null }} SpawnOutcome */

export class LauncherError extends Error {
  /** @param {string} message */
  constructor(message) {
    super(message);
    this.name = 'LauncherError';
  }
}

const require = createRequire(import.meta.url);
const rootPackagePath = fileURLToPath(new URL('../package.json', import.meta.url));
const rootPackage = parseRootPackage(readFileSync(rootPackagePath, 'utf8'));

/** @type {ReadonlyMap<string, readonly [string, string]>} */
const TARGETS = new Map([
  ['darwin-arm64', ['@anypick/cli-darwin-arm64', 'bin/anypick']],
  ['darwin-x64', ['@anypick/cli-darwin-x64', 'bin/anypick']],
  ['linux-arm64-glibc', ['@anypick/cli-linux-arm64-gnu', 'bin/anypick']],
  ['linux-x64-glibc', ['@anypick/cli-linux-x64-gnu', 'bin/anypick']],
  ['win32-x64', ['@anypick/cli-win32-x64', 'bin/anypick.exe']],
]);

const SUPPORTED_LABEL = [
  'darwin-arm64',
  'darwin-x64',
  'linux-arm64-glibc',
  'linux-x64-glibc',
  'win32-x64',
].join(', ');

/** @param {string} raw @returns {RootPackage} */
function parseRootPackage(raw) {
  /** @type {unknown} */
  const value = JSON.parse(raw);
  if (
    typeof value !== 'object' ||
    value === null ||
    !('version' in value) ||
    typeof value.version !== 'string'
  ) {
    throw new LauncherError('The installed AnyPick package manifest is invalid.');
  }
  return { version: value.version };
}

/** @param {readonly string[]} args @returns {boolean} */
export function isTuiInvocation(args) {
  const commandEnd = args.indexOf('--');
  const commandArgs = commandEnd === -1 ? args : args.slice(0, commandEnd);
  if (commandArgs.includes('--tui')) {
    return true;
  }
  const firstPositional = commandArgs.find((arg) => arg !== '' && !arg.startsWith('-'));
  return firstPositional === 'tui';
}

/**
 * @param {{ getReport?: () => { header?: { glibcVersionRuntime?: unknown } } } | undefined} report
 * @returns {'glibc' | 'non-glibc' | 'unknown'}
 */
export function detectLinuxLibc(report = process.report) {
  try {
    const header = report?.getReport?.()?.header;
    return header && typeof header.glibcVersionRuntime === 'string' ? 'glibc' : 'non-glibc';
  } catch {
    return 'unknown';
  }
}

/**
 * @param {{ platform?: NodeJS.Platform, arch?: string, report?: Parameters<typeof detectLinuxLibc>[0] }} options
 * @returns {PlatformTarget}
 */
export function platformTarget({
  platform = process.platform,
  arch = process.arch,
  report = process.report,
} = {}) {
  const libc = platform === 'linux' ? detectLinuxLibc(report) : undefined;
  const key = [platform, arch, libc].filter(Boolean).join('-');
  const match = TARGETS.get(key);
  if (!match) {
    throw new LauncherError(
      `AnyPick native CLI does not support ${key}. Supported targets: ${SUPPORTED_LABEL}.`,
    );
  }
  return { key, packageName: match[0], binaryPath: match[1] };
}

/** @param {PlatformTarget} target @returns {string} */
function optionalPackageMessage(target) {
  return [
    `AnyPick native CLI is unavailable for ${target.key}.`,
    `Expected optional package ${target.packageName}@${rootPackage.version}.`,
    `Reinstall without omitting optional dependencies: npm install anypick@${rootPackage.version}`,
  ].join('\n');
}

/**
 * @param {PlatformTarget} target
 * @param {(id: string) => string} resolvePackage
 * @returns {string}
 */
export function resolveNativeBinary(target, resolvePackage = require.resolve) {
  let manifestPath;
  try {
    manifestPath = resolvePackage(`${target.packageName}/package.json`);
  } catch {
    throw new LauncherError(optionalPackageMessage(target));
  }

  /** @type {unknown} */
  let manifest;
  try {
    manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  } catch {
    throw new LauncherError(
      `${optionalPackageMessage(target)}\nThe installed platform package manifest is unreadable.`,
    );
  }
  if (
    typeof manifest !== 'object' ||
    manifest === null ||
    !('name' in manifest) ||
    !('version' in manifest) ||
    manifest.name !== target.packageName ||
    manifest.version !== rootPackage.version
  ) {
    throw new LauncherError(
      `${optionalPackageMessage(target)}\nThe installed platform package has a mismatched name or version.`,
    );
  }

  const binary = join(dirname(manifestPath), ...target.binaryPath.split('/'));
  try {
    if (!statSync(binary).isFile()) {
      throw new Error('not a file');
    }
    if (process.platform !== 'win32') {
      accessSync(binary, constants.X_OK);
    }
  } catch {
    throw new LauncherError(
      `${optionalPackageMessage(target)}\nThe installed platform package does not contain an executable binary.`,
    );
  }
  return binary;
}

/** @param {NodeJS.Platform} platform @returns {NodeJS.Signals[]} */
function forwardedSignals(platform = process.platform) {
  return platform === 'win32' ? ['SIGINT', 'SIGTERM', 'SIGBREAK'] : ['SIGINT', 'SIGTERM', 'SIGHUP'];
}

/**
 * @param {string} command
 * @param {readonly string[]} args
 * @param {typeof spawn} spawnProcess
 * @param {NodeJS.ProcessEnv} childEnvironment
 * @returns {Promise<SpawnOutcome>}
 */
export function spawnPassthrough(
  command,
  args,
  spawnProcess = spawn,
  childEnvironment = process.env,
) {
  return new Promise((resolve, reject) => {
    const child = spawnProcess(command, args, {
      cwd: process.cwd(),
      env: childEnvironment,
      stdio: 'inherit',
      windowsHide: false,
    });
    let settled = false;
    /** @type {Map<NodeJS.Signals, () => void>} */
    const signalHandlers = new Map();
    const cleanup = () => {
      for (const [signal, handler] of signalHandlers) {
        process.off(signal, handler);
      }
    };
    /** @param {() => void} fn */
    const settle = (fn) => {
      if (settled) {
        return;
      }
      settled = true;
      cleanup();
      fn();
    };

    for (const signal of forwardedSignals()) {
      const handler = () => {
        try {
          child.kill(signal);
        } catch {
          // The child may already have exited between signal delivery and forwarding.
        }
      };
      signalHandlers.set(signal, handler);
      process.on(signal, handler);
    }

    child.once('error', (error) => settle(() => reject(error)));
    child.once('close', (code, signal) =>
      settle(() => resolve({ code: code ?? (signal ? null : 1), signal })),
    );
  });
}

/** @param {NodeJS.Signals} signal @returns {number} */
function signalExitCode(signal) {
  /** @type {Partial<Record<NodeJS.Signals, number>>} */
  const numbers = { SIGHUP: 1, SIGINT: 2, SIGTERM: 15, SIGBREAK: 21 };
  return 128 + (numbers[signal] ?? 1);
}

/** @param {readonly string[]} args @returns {Promise<void>} */
export async function runLauncher(args = process.argv.slice(2)) {
  let command;
  let childArgs;
  if (isTuiInvocation(args)) {
    command = process.execPath;
    childArgs = [fileURLToPath(new URL('../dist/cli.js', import.meta.url)), ...args];
  } else {
    const target = platformTarget();
    command = resolveNativeBinary(target);
    childArgs = args;
  }

  let outcome;
  try {
    // The Go engine owns everything except the TUI, so the native branch needs
    // no injected environment: no Node path, no plugin host, no bundled plugin
    // directory (ADR-0020).
    outcome = await spawnPassthrough(command, childArgs, spawn);
  } catch {
    throw new LauncherError(
      'AnyPick could not start its installed command binary. Reinstall AnyPick and try again.',
    );
  }
  if (outcome.signal) {
    try {
      process.kill(process.pid, outcome.signal);
    } catch {
      process.exitCode = signalExitCode(outcome.signal);
    }
    return;
  }
  process.exitCode = outcome.code ?? 1;
}

let invokedDirectly = false;
try {
  invokedDirectly =
    Boolean(process.argv[1]) &&
    realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url));
} catch {
  // Importing the launcher as a library has no process entry to resolve.
}
if (invokedDirectly) {
  runLauncher().catch((error) => {
    const message = error instanceof LauncherError ? error.message : 'AnyPick launcher failed.';
    process.stderr.write(`${message}\n`);
    process.exitCode = 1;
  });
}
