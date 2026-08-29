#!/usr/bin/env node
/**
 * ADR 0020 artifact gate: native downloads must not contain a Node runtime,
 * plugin-host script, package-manager shims, leaked developer paths, or
 * symlinks that escape the artifact.
 *
 * Content scan bound: files larger than MAX_TEXT_BYTES are not grepped for
 * developer paths (they are still subject to name/symlink/shebang rules).
 * Files at or under that bound are scanned in full, not a 4 KiB prefix.
 *
 * Usage: node scripts/check-node-free-artifact.mjs <artifact-root>
 *
 * The production macOS path is src/macos/scripts/check-node-free-app.sh.
 * Both scanners must reject the fixtures in node-free-artifact-fixtures.mjs.
 */

import { lstatSync, readdirSync, readFileSync, realpathSync } from 'node:fs';
import { basename, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

/** Content-scan bound shared with src/macos/scripts/check-node-free-app.sh. */
export const MAX_TEXT_BYTES = 512 * 1024;

const FORBIDDEN_NAMES = new Set([
  'node',
  'node.exe',
  'npm',
  'npm.cmd',
  'npm.ps1',
  'npx',
  'npx.cmd',
  'pnpm',
  'pnpm.cjs',
  'pnpm.cmd',
  'pnpm.exe',
  'tsx',
  'tsx.cmd',
  'plugin-host.mjs',
  'plugin-host.js',
]);

const FORBIDDEN_EXTENSIONS = new Set(['.mjs', '.cjs']);

const DEV_PATH_PATTERN =
  /(?:^|[\s"'=])(?:\/Users\/|\/home\/|\/opt\/homebrew\/opt\/node(?:@\d+)?\/|GOROOT=|GOPATH=)/m;

export function inspectNodeFreeArtifact(root) {
  const artifactRoot = resolve(root);
  let realRoot;
  try {
    realRoot = realpathSync(artifactRoot);
  } catch {
    return [`${artifactRoot} is not a directory`];
  }
  const stat = lstatSync(realRoot);
  if (!stat.isDirectory()) {
    return [`${artifactRoot} is not a directory`];
  }
  const violations = [];
  walk(realRoot, realRoot, violations);
  return violations;
}

function walk(entry, realRoot, violations) {
  const name = basename(entry);
  const lower = name.toLowerCase();
  const rel = displayPath(entry, realRoot);

  if (FORBIDDEN_NAMES.has(lower)) {
    violations.push(`forbidden name ${rel}`);
  }
  for (const ext of FORBIDDEN_EXTENSIONS) {
    if (lower.endsWith(ext)) {
      violations.push(`forbidden Node module ${rel}`);
      break;
    }
  }
  if (lower === 'plugin-host') {
    violations.push(`forbidden plugin-host path ${rel}`);
  }

  let stat;
  try {
    stat = lstatSync(entry);
  } catch (error) {
    violations.push(`unreadable ${rel}: ${error instanceof Error ? error.message : String(error)}`);
    return;
  }

  if (stat.isSymbolicLink()) {
    let target;
    try {
      target = realpathSync(entry);
    } catch {
      violations.push(`dangling symlink ${rel}`);
      return;
    }
    if (!isInside(target, realRoot)) {
      violations.push(`symlink ${rel} escapes the artifact`);
    }
    return;
  }

  if (stat.isDirectory()) {
    for (const child of readdirSync(entry)) {
      walk(join(entry, child), realRoot, violations);
    }
    return;
  }

  if (!stat.isFile() || stat.size === 0) {
    return;
  }

  const bytes = readFileSync(entry);
  if (hasShebangNode(bytes)) {
    violations.push(`Node shebang ${rel}`);
  }
  if (stat.size > MAX_TEXT_BYTES) {
    return;
  }
  if (!containsNul(bytes) && DEV_PATH_PATTERN.test(bytes.toString('utf8'))) {
    violations.push(`developer path leaked in ${rel}`);
  }
}

function hasShebangNode(bytes) {
  if (bytes.length < 3 || bytes[0] !== 0x23 || bytes[1] !== 0x21) {
    return false;
  }
  const lineEnd = bytes.indexOf(0x0a);
  const line = bytes.subarray(0, lineEnd === -1 ? bytes.length : lineEnd).toString('utf8');
  return /\bnode(?:exe)?\b/i.test(line);
}

function containsNul(bytes) {
  return bytes.includes(0);
}

function isInside(target, root) {
  const rel = relative(root, target);
  return rel === '' || (!rel.startsWith(`..${sep}`) && rel !== '..' && !rel.startsWith('..'));
}

function displayPath(entry, realRoot) {
  const rel = relative(realRoot, entry);
  return rel === '' ? basename(realRoot) : rel.split(sep).join('/');
}

function main(argv) {
  const target = argv[0];
  if (!target) {
    throw new Error('Usage: node scripts/check-node-free-artifact.mjs <artifact-root>');
  }
  const violations = inspectNodeFreeArtifact(target);
  if (violations.length > 0) {
    process.stderr.write(
      `ADR 0020: ${target} is not a Node-free native artifact:\n${violations
        .map((line) => `  - ${line}`)
        .join('\n')}\n`,
    );
    process.exitCode = 1;
    return;
  }
  process.stdout.write(`Node-free artifact OK: ${resolve(target)}\n`);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  main(process.argv.slice(2));
}
