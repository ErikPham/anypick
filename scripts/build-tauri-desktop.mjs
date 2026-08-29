import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  cpSync,
  mkdirSync,
  readFileSync,
  readlinkSync,
  readdirSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, isAbsolute, relative, resolve, sep } from 'node:path';
import { pathToFileURL } from 'node:url';

const require = createRequire(import.meta.url);
const root = resolve(import.meta.dirname, '..');
const tauriRoot = resolve(root, 'src/tray/tauri/src-tauri');
const uiConfig = resolve(root, 'src/tray/tauri/ui/vite.config.ts');
const manifestName = 'UNSIGNED-SHA256SUMS.json';

export function parseBuildArgs(argv) {
  const options = {
    bundles: undefined,
    noBundle: false,
    target: undefined,
  };

  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === '--no-bundle') {
      options.noBundle = true;
      continue;
    }
    if (argument === '--target') {
      options.target = requireOptionValue(argv, ++index, '--target');
      continue;
    }
    if (argument.startsWith('--target=')) {
      options.target = argument.slice('--target='.length);
      continue;
    }
    if (argument === '--bundles') {
      const bundles = [];
      while (index + 1 < argv.length && !argv[index + 1].startsWith('-')) {
        bundles.push(argv[(index += 1)]);
      }
      options.bundles = bundles;
      continue;
    }
    if (argument.startsWith('--bundles=')) {
      const value = argument.slice('--bundles='.length);
      options.bundles = value === '' ? [] : [value];
      continue;
    }
    throw new Error(`Unknown option ${JSON.stringify(argument)}.`);
  }

  if (options.noBundle && options.bundles !== undefined) {
    throw new Error('--bundles and --no-bundle cannot be used together.');
  }
  if (options.target !== undefined) {
    validateTarget(options.target);
  }
  return options;
}

function requireOptionValue(argv, index, option) {
  const value = argv[index];
  if (value === undefined || value.startsWith('-')) {
    throw new Error(`${option} requires a value.`);
  }
  return value;
}

export function validateTarget(target) {
  const isDesktopTarget =
    /^(?:aarch64|x86_64)-[a-zA-Z0-9_.-]+$/.test(target) &&
    (target.includes('windows') || target.includes('linux') || target.endsWith('apple-darwin'));
  if (!isDesktopTarget || target.includes('..') || target.includes('/') || target.includes('\\')) {
    throw new Error(`Unsupported desktop target ${JSON.stringify(target)}.`);
  }
  return target;
}

export function buildTauriArgs(options) {
  const arguments_ = ['build', '--ci'];
  if (options.target !== undefined) {
    arguments_.push('--target', options.target);
  }
  if (options.noBundle) {
    arguments_.push('--no-bundle');
  } else {
    if (options.bundles !== undefined) {
      arguments_.push('--bundles', ...options.bundles);
    }
    // Desktop release signing is an explicit downstream step. This builder only
    // assembles inspectable artifacts and must not consult ambient signing state.
    arguments_.push('--no-sign');
  }
  return arguments_;
}

export function hostTarget(rustcOutput) {
  const match = /^host:\s*(\S+)$/m.exec(rustcOutput);
  if (match === null) {
    throw new Error('rustc -vV did not report a host target.');
  }
  return validateTarget(match[1]);
}

export function executableMetadata(buffer, target) {
  if (buffer.length < 20) {
    throw new Error('Executable is too small to contain a supported header.');
  }
  const expectedArchitecture = target.startsWith('aarch64-') ? 'arm64' : 'x64';

  if (target.includes('windows')) {
    if (buffer[0] !== 0x4d || buffer[1] !== 0x5a || buffer.length < 64) {
      throw new Error('Expected a Windows PE executable.');
    }
    const peOffset = buffer.readUInt32LE(0x3c);
    if (
      peOffset + 6 > buffer.length ||
      buffer.toString('ascii', peOffset, peOffset + 4) !== 'PE\0\0'
    ) {
      throw new Error('Windows executable has an invalid PE header.');
    }
    const machine = buffer.readUInt16LE(peOffset + 4);
    const architecture = machine === 0xaa64 ? 'arm64' : machine === 0x8664 ? 'x64' : 'unknown';
    assertArchitecture(architecture, expectedArchitecture);
    return { architecture, format: 'PE' };
  }

  if (target.includes('linux')) {
    if (buffer.subarray(0, 4).toString('hex') !== '7f454c46' || buffer[4] !== 2) {
      throw new Error('Expected a Linux ELF executable.');
    }
    const byteOrder = buffer[5];
    if (byteOrder !== 1 && byteOrder !== 2) {
      throw new Error('Linux executable has an invalid ELF byte order.');
    }
    const machine = byteOrder === 1 ? buffer.readUInt16LE(18) : buffer.readUInt16BE(18);
    const architecture = machine === 183 ? 'arm64' : machine === 62 ? 'x64' : 'unknown';
    assertArchitecture(architecture, expectedArchitecture);
    return { architecture, format: 'ELF' };
  }

  const magic = buffer.subarray(0, 4).toString('hex');
  const littleEndian = magic === 'cffaedfe';
  const bigEndian = magic === 'feedfacf';
  if (!littleEndian && !bigEndian) {
    throw new Error('Expected a macOS Mach-O executable.');
  }
  const cpuType = littleEndian ? buffer.readUInt32LE(4) : buffer.readUInt32BE(4);
  const architecture =
    cpuType === 0x0100000c ? 'arm64' : cpuType === 0x01000007 ? 'x64' : 'unknown';
  assertArchitecture(architecture, expectedArchitecture);
  return { architecture, format: 'Mach-O' };
}

function assertArchitecture(actual, expected) {
  if (actual !== expected) {
    throw new Error(`Executable architecture is ${actual}; expected ${expected}.`);
  }
}

export function inspectExecutable(path, target) {
  const metadata = executableMetadata(readFileSync(path), target);
  const bytes = statSync(path).size;
  if (bytes === 0) {
    throw new Error(`${path} is empty.`);
  }
  return { ...metadata, bytes, path };
}

function fileSha256(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

export function stageBundleArtifacts({ bundleRoot, destination, target }) {
  validateTarget(target);
  const artifactRoots = bundleArtifactRoots(bundleRoot);
  if (artifactRoots.length === 0) {
    throw new Error(`Tauri produced no package artifacts under ${bundleRoot}.`);
  }

  assertContained(resolve(root, 'dist/native'), destination);
  rmSync(destination, { force: true, recursive: true });
  mkdirSync(destination, { recursive: true });
  for (const source of artifactRoots) {
    const output = resolve(destination, relative(bundleRoot, source));
    assertContained(destination, output);
    mkdirSync(dirname(output), { recursive: true });
    cpSync(source, output, { force: true, recursive: true });
  }

  const artifacts = hashTree(destination);
  const manifest = {
    algorithm: 'SHA-256',
    artifacts,
    note: 'UNSIGNED build output. No code signing, notarization, or publishing was performed.',
    schemaVersion: 1,
    signed: false,
    target,
  };
  const manifestPath = resolve(destination, manifestName);
  writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return { artifacts, manifestPath };
}

function bundleArtifactRoots(bundleRoot) {
  let formatEntries;
  try {
    formatEntries = readdirSync(bundleRoot, { withFileTypes: true });
  } catch (error) {
    if (error instanceof Error && 'code' in error && error.code === 'ENOENT') {
      return [];
    }
    throw error;
  }

  const artifacts = [];
  for (const formatEntry of formatEntries.sort(compareNames)) {
    if (formatEntry.name.startsWith('.')) {
      continue;
    }
    const formatPath = resolve(bundleRoot, formatEntry.name);
    if (!formatEntry.isDirectory()) {
      artifacts.push(formatPath);
      continue;
    }
    for (const artifactEntry of readdirSync(formatPath, { withFileTypes: true }).sort(
      compareNames,
    )) {
      if (!artifactEntry.name.startsWith('.')) {
        artifacts.push(resolve(formatPath, artifactEntry.name));
      }
    }
  }
  return artifacts;
}

function hashTree(directory) {
  const entries = [];
  walk(directory, directory, entries);
  return entries.sort((left, right) => left.path.localeCompare(right.path, 'en'));
}

function walk(base, directory, entries) {
  for (const entry of readdirSync(directory, { withFileTypes: true }).sort(compareNames)) {
    const absolute = resolve(directory, entry.name);
    if (entry.isDirectory()) {
      walk(base, absolute, entries);
      continue;
    }
    const path = relative(base, absolute).split(sep).join('/');
    if (entry.isSymbolicLink()) {
      const link = readlinkSync(absolute);
      entries.push({
        bytes: Buffer.byteLength(link),
        path,
        sha256: createHash('sha256').update(link).digest('hex'),
        type: 'symlink',
      });
      continue;
    }
    if (!entry.isFile()) {
      continue;
    }
    const contents = readFileSync(absolute);
    entries.push({
      bytes: contents.length,
      path,
      sha256: createHash('sha256').update(contents).digest('hex'),
      type: 'file',
    });
  }
}

function compareNames(left, right) {
  return left.name.localeCompare(right.name, 'en');
}

function assertContained(parent, child) {
  const relation = relative(resolve(parent), resolve(child));
  if (
    relation !== '' &&
    !relation.startsWith(`..${sep}`) &&
    relation !== '..' &&
    !isAbsolute(relation)
  ) {
    return;
  }
  throw new Error(`${child} is outside ${parent}.`);
}

function packageBin(packageName, binaryName) {
  const packagePath = require.resolve(`${packageName}/package.json`);
  const packageJson = JSON.parse(readFileSync(packagePath, 'utf8'));
  const relativeBin =
    typeof packageJson.bin === 'string' ? packageJson.bin : packageJson.bin?.[binaryName];
  if (typeof relativeBin !== 'string') {
    throw new Error(`${packageName} does not declare the ${binaryName} executable.`);
  }
  return resolve(dirname(packagePath), relativeBin);
}

function cargoTargetRoot() {
  const configured = process.env.CARGO_TARGET_DIR;
  if (configured === undefined || configured === '') {
    return resolve(tauriRoot, 'target');
  }
  return isAbsolute(configured) ? configured : resolve(tauriRoot, configured);
}

function runNode(entry, arguments_, cwd) {
  execFileSync(process.execPath, [entry, ...arguments_], {
    cwd,
    env: process.env,
    stdio: 'inherit',
  });
}

export function run(argv = process.argv.slice(2)) {
  const options = parseBuildArgs(argv);
  const target =
    options.target ??
    hostTarget(execFileSync('rustc', ['-vV'], { cwd: tauriRoot, encoding: 'utf8' }));
  const targetRoot = cargoTargetRoot();
  const releaseRoot = resolve(
    targetRoot,
    ...(options.target === undefined ? [] : [target]),
    'release',
  );
  const extension = target.includes('windows') ? '.exe' : '';

  runNode(packageBin('vite', 'vite'), ['build', '--config', uiConfig], root);
  runNode(packageBin('@tauri-apps/cli', 'tauri'), buildTauriArgs(options), tauriRoot);

  const app = inspectExecutable(resolve(releaseRoot, `anypick-tray${extension}`), target);
  const preparedSidecar = inspectExecutable(
    resolve(tauriRoot, 'binaries', `anypick-core-${target}${extension}`),
    target,
  );
  const bundledSidecar = inspectExecutable(
    resolve(releaseRoot, `anypick-core${extension}`),
    target,
  );
  if (fileSha256(preparedSidecar.path) !== fileSha256(bundledSidecar.path)) {
    throw new Error('Tauri bundled a Go sidecar different from the exact target-specific input.');
  }
  console.log(
    `Validated ${app.format}/${app.architecture} desktop app (${app.bytes} bytes) and ` +
      `exact Go sidecar (${bundledSidecar.bytes} bytes) for ${target}.`,
  );

  if (!options.noBundle) {
    const { artifacts, manifestPath } = stageBundleArtifacts({
      bundleRoot: resolve(releaseRoot, 'bundle'),
      destination: resolve(root, 'dist/native', target),
      target,
    });
    console.log(`Staged ${artifacts.length} checksummed entries in ${dirname(manifestPath)}.`);
    console.log(`Unsigned manifest: ${manifestPath}`);
  }
}

const invokedPath =
  process.argv[1] === undefined ? undefined : pathToFileURL(resolve(process.argv[1])).href;
if (invokedPath === import.meta.url) {
  try {
    run();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
