#!/usr/bin/env node
/**
 * Single local entry for the Go engine — the `pnpm dev` equivalent.
 *
 *   pnpm go                       → Go CLI help
 *   pnpm go list accounts         → run against your REAL data (same as pnpm dev)
 *   pnpm go use claude --with …
 *
 *   pnpm go:sandbox …             → identical, but jailed in a throwaway
 *                                   HOME/ANYPICK_HOME under /tmp
 *   pnpm go:test                  → go test -race ./...
 *
 *   pnpm go:dev                   → detached supervisor + the native tray app,
 *                                   backed by the Go engine (same UX as pnpm dev)
 *   pnpm go:dev:stop              → stop it
 *
 * Rebuilds before every run; the Go build cache keeps this near-instant.
 */
import { spawn, spawnSync } from 'node:child_process';
import {
  existsSync,
  mkdirSync,
  openSync,
  readFileSync,
  rmSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const goDir = join(root, 'go');
const bin = join(goDir, 'bin', 'anypick-dev');

let args = process.argv.slice(2);
let sandbox = false;
let launchApp = false;
let stopApp = false;
if (args[0] === '--sandbox') {
  sandbox = true;
  args = args.slice(1);
}
if (args[0] === '--launch-app') {
  launchApp = true;
  args = [];
}
if (args[0] === '--stop-app') {
  stopApp = true;
  args = [];
}

// ── build ────────────────────────────────────────────────────────────
mkdirSync(join(goDir, 'bin'), { recursive: true });

const which = spawnSync('go', ['version'], { encoding: 'utf8' });
if (which.error || which.status !== 0) {
  console.error('[go] ERROR: Go not found on PATH.');
  console.error('       Go >= 1.23 is required (brew install go).');
  if (which.error) console.error('       detail:', which.error.message);
  process.exit(1);
}

const build = spawnSync('go', ['build', '-o', bin, './cmd/anypick'], {
  cwd: goDir,
  stdio: ['ignore', 'inherit', 'inherit'],
  env: { ...process.env, GOTOOLCHAIN: 'auto' },
});
if (build.error) {
  console.error('[go] spawn go build failed:', build.error.message);
  process.exit(1);
}
if (build.status !== 0) {
  console.error(`[go] build failed (exit ${build.status}) — see compile errors above.`);
  process.exit(build.status ?? 1);
}

// ── environment ──────────────────────────────────────────────────────
const env = {
  ...process.env,
  GOTOOLCHAIN: 'auto',
  ANYPICK_BUNDLED_PLUGINS: join(root, 'plugins'),
  ANYPICK_PLUGIN_HOST_NODE: process.execPath,
  ANYPICK_PLUGIN_HOST_SCRIPT: join(root, 'scripts', 'plugin-host.mjs'),
};
if (sandbox) {
  const base = join(tmpdir(), 'anypick-go-sandbox');
  const home = join(base, 'home');
  const data = join(base, 'data');
  mkdirSync(home, { recursive: true });
  mkdirSync(data, { recursive: true });
  // Seed a fake signed-in Codex once so `add codex` works out of the box.
  const codexAuth = join(home, '.codex', 'auth.json');
  if (!existsSync(codexAuth)) {
    mkdirSync(join(home, '.codex'), { recursive: true });
    rmSync(codexAuth, { force: true });
    const header = 'eyJhbGciOiJub25lIn0';
    const payload = Buffer.from(JSON.stringify({ email: 'me@example.com' })).toString('base64url');
    writeFileSync(
      codexAuth,
      JSON.stringify({
        auth_mode: 'chatgpt',
        tokens: { id_token: `${header}.${payload}.sig`, access_token: 'tok', account_id: 'acct1' },
      }) + '\n',
      { mode: 0o600 },
    );
  }
  env.HOME = home;
  env.ANYPICK_HOME = data;
  // Providers must never reach the machine's real credential stores.
  for (const key of [
    'CODEX_AUTH_PATH',
    'GROK_AUTH_PATH',
    'OPENCODE_AUTH_PATH',
    'KIRO_SSO_CACHE_DIR',
    'ANYPICK_KIRO_SECRET_DB',
    'ANYPICK_CLAUDE_CREDENTIALS_PATH',
  ]) {
    delete env[key];
  }
  env.ANYPICK_CLAUDE_NO_KEYCHAIN = '1';
  console.error(`[go:sandbox] HOME=${home}`);
  console.error(`[go:sandbox] ANYPICK_HOME=${data}`);
  console.error('[go:sandbox] reset with: rm -rf ' + base);
}

// ── desktop launcher: supervisor + native tray app ───────────────────
function anypickHome() {
  return env.ANYPICK_HOME || join(homedir(), '.anypick');
}

function nativeAppBinary() {
  return join(anypickHome(), 'runtime', 'tray', 'AnyPick.app', 'Contents', 'MacOS', 'AnyPick');
}

const supervisorPidPath = () => join(anypickHome(), 'go-tray.pid');
const supervisorLogPath = () => join(anypickHome(), 'go-tray.log');

function pidAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function launchDesktop() {
  if (process.platform !== 'darwin') {
    console.error('[go:dev] the native tray app is macOS-only; use `pnpm go serve --detach`.');
    process.exit(1);
  }
  const pidFile = supervisorPidPath();
  if (existsSync(pidFile)) {
    const pid = Number(readFileSync(pidFile, 'utf8').trim());
    if (Number.isFinite(pid) && pidAlive(pid)) {
      console.log(`[go:dev] already running (pid ${pid})`);
      return;
    }
    unlinkSync(pidFile);
  }

  const appBin = nativeAppBinary();
  if (!existsSync(appBin)) {
    console.error('[go:dev] native app not found: ' + appBin);
    console.error('       Run `pnpm dev` once so the Node toolchain compiles it, then retry.');
    process.exit(1);
  }

  env.ANYPICK_APP_BIN = appBin;

  const logFd = openSync(supervisorLogPath(), 'a');
  const child = spawn(bin, ['app', 'run'], {
    detached: true,
    stdio: ['ignore', logFd, logFd],
    env,
  });
  child.unref();
  writeFileSync(pidFile, `${child.pid}\n`, { mode: 0o600 });

  // Fail fast if the supervisor died during startup instead of reporting a
  // tray that instantly vanished.
  let dead = false;
  child.on('exit', () => {
    dead = true;
  });
  setTimeout(() => {
    if (dead || !pidAlive(child.pid)) {
      console.error('[go:dev] supervisor exited during startup — log: ' + supervisorLogPath());
      process.exit(1);
    }
    console.log(`[go:dev] started (pid ${child.pid}) — tray icon should appear in the menu bar`);
    console.log(`[go:dev] log: ${supervisorLogPath()}`);
    console.log('[go:dev] stop with: pnpm go:dev:stop');
  }, 1200);
}

function stopDesktop() {
  const pidFile = supervisorPidPath();
  if (!existsSync(pidFile)) {
    console.log('[go:dev] not running');
    return;
  }
  const pid = Number(readFileSync(pidFile, 'utf8').trim());
  if (!Number.isFinite(pid) || !pidAlive(pid)) {
    unlinkSync(pidFile);
    console.log('[go:dev] not running');
    return;
  }
  // Detached spawn ⇒ the supervisor leads its own process group; signal the
  // group so the native app and any helpers die with it.
  try {
    process.kill(-pid, 'SIGTERM');
  } catch {
    try {
      process.kill(pid, 'SIGTERM');
    } catch {
      /* already gone */
    }
  }
  unlinkSync(pidFile);
  console.log(`[go:dev] stopped (pid ${pid})`);
}

if (launchApp) {
  launchDesktop();
} else if (stopApp) {
  stopDesktop();
} else {
  if (!sandbox && args.length === 0) {
    // Bare `pnpm go` mirrors bare `pnpm dev`: surface the entry point instead
    // of mutating anything.
    console.error('[go] usage: pnpm go <command> · pnpm go:sandbox <command> · pnpm go:dev\n');
  }
  const child = spawnSync(bin, args, { stdio: 'inherit', env });
  process.exit(child.status ?? 1);
}
