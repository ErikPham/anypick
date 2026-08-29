/**
 * Compatibility launcher.
 *
 * Default: pnpm macos:design (in-memory fixture).
 * --real: same as pnpm dev (tray run against ~/.anypick).
 */
import { spawn } from 'node:child_process';
import { homedir } from 'node:os';
import { join, resolve } from 'node:path';

if (process.platform !== 'darwin') {
  throw new Error('The native macOS app only runs on macOS.');
}

const root = resolve(import.meta.dirname, '..');
const useRealData = process.argv.includes('--real');
const empty = process.argv.includes('--empty');
const renderFlagIndex = process.argv.indexOf('--render');
const renderPath = renderFlagIndex >= 0 ? process.argv[renderFlagIndex + 1] : undefined;
if (renderFlagIndex >= 0 && !renderPath) {
  throw new Error('--render requires a PNG output path.');
}

// ─── REAL DATA (explicit opt-in) ───────────────────────────────────────────
if (useRealData) {
  const anypickHome = process.env.ANYPICK_HOME || join(homedir(), '.anypick');
  process.stderr.write(
    [
      '',
      '[macOS app] REAL data mode',
      `  ANYPICK_HOME=${anypickHome}`,
      '  Snapshot comes from your accounts, proxies, and Proxy Hub on disk.',
      '  Fixture: pnpm macos:design',
      '',
    ].join('\n'),
  );

  // Always run from source in the repo. A stale dist/cli.js still points at the
  // old single-file AnyPickTray.swift and will look like fake/demo data.
  // Use `node --import tsx` (not the tsx CLI) so process.pid matches state.json.
  const srcEntry = resolve(root, 'src/cli.ts');
  const cmd = process.execPath;
  const args = ['--import', 'tsx', srcEntry, 'tray', 'run'];

  const child = spawn(cmd, args, {
    cwd: root,
    env: {
      ...process.env,
      ANYPICK_HOME: anypickHome,
      ANYPICK_TRAY_PROCESS: '1',
      ANYPICK_TRAY_OPEN_ON_LAUNCH: '1',
    },
    stdio: 'inherit',
  });

  const stop = () => {
    child.kill('SIGTERM');
  };
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  child.once('exit', (code, signal) => {
    process.exitCode = code ?? (signal ? 1 : 0);
  });
} else {
  process.stderr.write(
    [
      '',
      '[macOS app] Use pnpm macos:design for the in-memory fixture.',
      '  Real data:   pnpm dev   or   pnpm macos:app --real',
      '',
    ].join('\n'),
  );
  const forwarded = [];
  if (empty) forwarded.push('--empty');
  if (renderPath) forwarded.push('--render', renderPath);
  const child = spawn(
    process.execPath,
    [resolve(root, 'scripts/demo-macos-design.mjs'), ...forwarded],
    {
      cwd: root,
      env: process.env,
      stdio: 'inherit',
    },
  );
  const stop = () => child.kill('SIGTERM');
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  child.once('exit', (code, signal) => {
    process.exitCode = code ?? (signal ? 1 : 0);
  });
}
