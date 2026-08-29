/**
 * Live cross-runtime backup drift gate (review P1: frozen fixtures cannot
 * detect producer drift). Orchestrates both runtimes over TEMP artifacts so
 * the CURRENT producers are exercised, not committed snapshots:
 *
 *   1. TypeScript produces a backup  → $TMP/ts-authored.json
 *   2. Go consumes it                → go test -run TestParityExternalExchange
 *   3. Go produces a backup          → $TMP/go-authored.json
 *   4. TypeScript consumes it        → restore + assert envelope bytes
 *
 * Usage: npx tsx scripts/parity-drift-gate.ts
 * Exit code is the gate result; CI wiring lands with the M8 release jobs.
 */

import { execFileSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const self = join(repoRoot, 'scripts', 'parity-live.ts');
const tmp = mkdtempSync(join(tmpdir(), 'anypick-parity-gate-'));
const tsAuthored = join(tmp, 'ts-authored.json');
const goAuthored = join(tmp, 'go-authored.json');

function sh(cmd: string, env: Record<string, string> = {}, cwd = repoRoot): void {
  execFileSync('bash', ['-c', cmd], {
    cwd,
    env: { ...process.env, ...env },
    stdio: ['ignore', 'inherit', 'inherit'],
  });
}

try {
  console.log('[1/4] TypeScript producer → current export');
  sh(`npx tsx ${JSON.stringify(self)} export-ts ${JSON.stringify(tsAuthored)}`);

  console.log('[2/4] Go consumer restores the current TS export');
  sh(
    `go test ./internal/transfer -run TestParityExternalExchange -count=1 -v`,
    { PARITY_BACKUP_INPUT: tsAuthored },
    join(repoRoot, 'go'),
  );

  console.log('[3/4] Go producer → current export');
  sh(
    `go test ./internal/transfer -run TestParityExternalExchange -count=1 -v`,
    { PARITY_BACKUP_OUTPUT: goAuthored },
    join(repoRoot, 'go'),
  );

  console.log('[4/4] TypeScript consumer restores the current Go export');
  sh(`npx tsx ${JSON.stringify(self)} import-go ${JSON.stringify(goAuthored)}`);

  console.log('parity drift gate: PASS (current producers are mutually readable)');
} catch {
  console.error('parity drift gate: FAIL — producer/consumer formats have drifted');
  process.exitCode = 1;
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
