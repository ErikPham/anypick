#!/usr/bin/env node
/**
 * Reproducible performance baseline for the native cutover (ADR 0020,
 * NATIVE_APP_CUTOVER.md Milestone 0). Measures the Go engine only; Node
 * comparisons use identical fixture roots and are recorded separately.
 *
 * Coverage:
 *   - CLI cold/warm startup (p50/p95/min/max) and per-run max RSS
 *   - service spawn → first snapshot (`app run`)
 *   - idle service CPU / RSS sampling
 *   - proxy request + streaming first-byte overhead (go test -bench)
 *   - artifact size, raw and gzip-compressed
 *   - machine / OS / toolchain metadata
 *
 * Usage: node scripts/perf-baseline.mjs [--runs N] [--out FILE]
 * Writes a markdown report to dist/perf-baseline.md by default.
 * Never touches ~/.anypick: all runs execute against a throwaway root.
 */

import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, rmSync, statSync, writeFileSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const goDir = join(repoRoot, 'go');
const args = process.argv.slice(2);
function argValue(name, fallback) {
  const i = args.indexOf(name);
  return i >= 0 && args[i + 1] ? Number(args[i + 1]) : fallback;
}
const RUNS = Math.max(5, argValue('--runs', 30));
const outArg = args.indexOf('--out');
const outPath =
  outArg >= 0 && args[outArg + 1]
    ? resolve(args[outArg + 1])
    : join(repoRoot, 'dist', 'perf-baseline.md');

const lines = [];
const report = (s = '') => {
  lines.push(s);
};

function sh(cmd, opts = {}) {
  return execFileSync('bash', ['-c', cmd], { encoding: 'utf8', ...opts }).trim();
}

function percentiles(samples) {
  const sorted = [...samples].sort((a, b) => a - b);
  const at = (q) => sorted[Math.min(sorted.length - 1, Math.ceil(q * sorted.length) - 1)];
  return {
    min: sorted[0],
    p50: at(0.5),
    p95: at(0.95),
    max: sorted[sorted.length - 1],
  };
}

const fmtMs = (ms) => `${ms.toFixed(1)} ms`;

/** Run fn once, returning { ms, maxRssKb }. */
async function timedRun(fn) {
  const start = process.hrtime.bigint();
  const result = await fn();
  const ms = Number(process.hrtime.bigint() - start) / 1e6;
  return { ms, ...result };
}

/** Spawn a short-lived command wrapped in `/usr/bin/time` to capture max RSS. */
function runWithTime(bin, cliArgs, env) {
  return new Promise((resolvePromise, reject) => {
    // darwin: `-l` prints "<bytes>  maximum resident set size" (value first);
    // linux: `-v` prints "Maximum resident set size (kbytes): <kb>".
    const isDarwin = process.platform === 'darwin';
    const timeArgs = isDarwin ? ['-l'] : ['-v'];
    const rssRe = isDarwin
      ? /(\d+)\s+maximum resident set size/i
      : /Maximum resident set size[^\d]*(\d+)/i;
    const child = spawn('/usr/bin/time', [...timeArgs, bin, ...cliArgs], {
      env,
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    let stderr = '';
    child.stderr.on('data', (d) => (stderr += d));
    child.on('close', () => {
      const m = stderr.match(rssRe);
      resolvePromise({ maxRssKb: m ? Number(m[1]) / (isDarwin ? 1024 : 1) : null });
    });
    child.on('error', reject);
  });
}

function metadata() {
  report('# AnyPick performance baseline');
  report();
  report('| Field | Value |');
  report('| --- | --- |');
  const rows = {
    Date: new Date().toISOString(),
    Hostname: require_(() => sh('hostname')),
    OS: `${process.platform} ${process.arch}`,
    'Kernel / release': require_(() => sh(process.platform === 'darwin' ? 'uname -v' : 'uname -r')),
    'OS version': require_(() =>
      process.platform === 'darwin'
        ? sh('sw_vers -productVersion')
        : sh('. /etc/os-release && echo "$PRETTY_NAME"'),
    ),
    CPU: require_(() =>
      process.platform === 'darwin'
        ? sh('sysctl -n machdep.cpu.brand_string')
        : sh("grep -m1 'model name' /proc/cpuinfo | cut -d: -f2 | xargs"),
    ),
    'Go toolchain': sh('go version'),
    'Node runtime': process.version,
    'Git commit': require_(() => sh(`git -C ${JSON.stringify(repoRoot)} rev-parse HEAD`)),
    'Working tree': require_(() =>
      sh(`git -C ${JSON.stringify(repoRoot)} status --porcelain`) ? 'dirty' : 'clean',
    ),
  };
  for (const [k, v] of Object.entries(rows)) report(`| ${k} | ${v ?? 'n/a'} |`);
  report();
}
// tiny lazy helper so one failing metadata probe never kills the report
function require_(fn) {
  try {
    return fn();
  } catch {
    return null;
  }
}

async function buildBinary(outBin) {
  process.stdout.write('Building Go binary (release flags)…\n');
  sh(`go build -trimpath -ldflags '-s -w' -o ${JSON.stringify(outBin)} ./cmd/anypick`, {
    cwd: goDir,
  });
  process.stdout.write('done\n');
  return statSync(outBin).size;
}

async function measureCli(bin, env) {
  // Cold: first execution of the freshly linked binary (page cache effects
  // included). Warm: subsequent executions.
  const cold = await timedRun(async () => runWithTime(bin, ['--version'], env));
  const warm = [];
  let warmRss = [];
  for (let i = 0; i < RUNS; i++) {
    const r = await timedRun(() => runWithTime(bin, ['--version'], env));
    warm.push(r.ms);
    if (r.maxRssKb != null) warmRss.push(r.maxRssKb);
  }
  const cp = percentiles([cold.ms]);
  const wp = percentiles(warm);
  const rssP = warmRss.length ? percentiles(warmRss) : null;

  report('## CLI startup (`anypick --version`, no DB init)');
  report();
  report('| Metric | Value |');
  report('| --- | --- |');
  report(`| Cold wall time | ${fmtMs(cold.ms)} |`);
  report(
    `| Warm p50 / p95 / max (${RUNS} runs) | ${fmtMs(wp.p50)} / ${fmtMs(wp.p95)} / ${fmtMs(wp.max)} |`,
  );
  if (rssP)
    report(
      `| Warm max RSS p50 / p95 | ${(rssP.p50 / 1024).toFixed(1)} MB / ${(rssP.p95 / 1024).toFixed(1)} MB |`,
    );
  report();

  return { warmP50: wp.p50, warmP95: wp.p95, rssP50Kb: rssP?.p50 };
}

function desktopHostPlatform() {
  if (process.platform === 'darwin') return 'macos';
  if (process.platform === 'win32') return 'windows';
  return 'linux';
}

function decodeServiceFrame(line) {
  const separator = line.indexOf('\t');
  if (separator === -1) throw new Error(`unframed desktop-service output: ${line}`);
  const prefix = line.slice(0, separator);
  const encoded = line.slice(separator + 1);
  const raw = Buffer.from(encoded, 'base64');
  if (raw.toString('base64') !== encoded) throw new Error(`non-canonical ${prefix} frame`);
  return { prefix, value: JSON.parse(raw.toString('utf8')) };
}

/** Run one real v2 service lifecycle through hello → ready → initial snapshot. */
function serviceProbe(bin, env, { sampleIdle = false } = {}) {
  return new Promise((resolvePromise, reject) => {
    const start = process.hrtime.bigint();
    const child = spawn(bin, ['desktop-service', '--stdio'], {
      env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    const lines = createInterface({ input: child.stdout, crlfDelay: Infinity });
    let readyMs = null;
    let snapshotMs = null;
    let idleCpuPercent = null;
    let idleRssKb = null;
    let stderrTail = '';
    let finishing = false;
    const deadline = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`desktop-service probe timed out: ${stderrTail.slice(-400)}`));
    }, 8000);

    const stopGracefully = () => {
      if (finishing) return;
      finishing = true;
      child.stdin.end('quit\n');
    };

    lines.on('line', (line) => {
      let frame;
      try {
        frame = decodeServiceFrame(line);
      } catch (error) {
        clearTimeout(deadline);
        child.kill('SIGKILL');
        reject(error);
        return;
      }
      if (frame.prefix === 'hello') {
        const hello = {
          protocolVersion: frame.value.protocolVersion,
          appVersion: frame.value.engineVersion,
          platform: desktopHostPlatform(),
          capabilities: [
            'cancel-v1',
            'command-dto-v1',
            'explicit-refresh',
            'host-effects-v1',
            'post-mutation-events',
            'snapshot-dto-v1',
            'usage-events-v1',
          ],
          hostState: { launchAtLogin: false },
        };
        child.stdin.write(`hello\t${Buffer.from(JSON.stringify(hello)).toString('base64')}\n`);
      } else if (frame.prefix === 'ready') {
        readyMs = Number(process.hrtime.bigint() - start) / 1e6;
      } else if (frame.prefix === 'snapshot') {
        snapshotMs = Number(process.hrtime.bigint() - start) / 1e6;
        if (!sampleIdle) stopGracefully();
        else {
          setTimeout(() => {
            const ps = spawn('ps', ['-o', '%cpu=,rss=', '-p', String(child.pid)], {
              stdio: ['ignore', 'pipe', 'ignore'],
            });
            let out = '';
            ps.stdout.on('data', (data) => (out += data));
            ps.on('close', () => {
              const [cpu, rss] = out.trim().split(/\s+/);
              idleCpuPercent = cpu != null ? Number(cpu) : null;
              idleRssKb = rss != null ? Number(rss) : null;
              stopGracefully();
            });
          }, 2000);
        }
      }
    });
    child.stderr.on('data', (data) => {
      stderrTail = `${stderrTail}${String(data)}`.slice(-4000);
    });
    child.on('close', (code) => {
      clearTimeout(deadline);
      lines.close();
      if (code !== 0 || readyMs == null || snapshotMs == null) {
        reject(
          new Error(
            `desktop-service exited ${String(code)} before ready snapshot: ${stderrTail.slice(-400)}`,
          ),
        );
        return;
      }
      resolvePromise({ readyMs, snapshotMs, idleCpuPercent, idleRssKb, stderrTail });
    });
    child.on('error', reject);
  });
}

/** Measure the app-owned v2 service, then retain one lifecycle for idle sampling. */
async function measureService(bin, env) {
  const warm = [];
  for (let index = 0; index < RUNS; index += 1) {
    warm.push(await serviceProbe(bin, env));
  }
  const idle = await serviceProbe(bin, env, { sampleIdle: true });
  const ready = percentiles(warm.map((sample) => sample.readyMs));
  const snapshot = percentiles(warm.map((sample) => sample.snapshotMs));

  report('## Desktop service (`desktop-service --stdio`, protocol v2)');
  report();
  report('| Metric | Value |');
  report('| --- | --- |');
  report(
    `| Spawn → ready p50 / p95 / max (${RUNS} runs) | ${fmtMs(ready.p50)} / ${fmtMs(ready.p95)} / ${fmtMs(ready.max)} |`,
  );
  report(
    `| Spawn → initial snapshot p50 / p95 / max (${RUNS} runs) | ${fmtMs(snapshot.p50)} / ${fmtMs(snapshot.p95)} / ${fmtMs(snapshot.max)} |`,
  );
  report(`| Idle CPU (single sample after settle) | ${idle.idleCpuPercent ?? 'n/a'} % |`);
  report(
    `| Idle RSS | ${idle.idleRssKb != null ? (idle.idleRssKb / 1024).toFixed(1) + ' MB' : 'n/a'} |`,
  );
  report();
  report('> This probe performs the mandatory v2 version/capability handshake and');
  report('> waits for the initial snapshot. It does not exercise the legacy `app run` bridge.');
  report();

  return {
    readyP95: ready.p95,
    snapshotP95: snapshot.p95,
    idleCpuPercent: idle.idleCpuPercent,
    idleRssKb: idle.idleRssKb,
  };
}

function measureProxyOverhead() {
  process.stdout.write('Running proxy benchmarks…\n');
  const out = sh(
    `go test -bench BenchmarkBaseline -run '^$' -benchmem -benchtime ${Math.max(100, RUNS * 5)}x ./internal/proxy`,
    { cwd: goDir },
  );
  report('## Proxy additive overhead (loopback stub upstream, see baseline_bench_test.go)');
  report();
  report('```text');
  report(
    out
      .split('\n')
      .filter((l) => l.startsWith('Benchmark') || /^(ok|PASS)/.test(l))
      .join('\n'),
  );
  report('```');
  report();
  const directReq = out.match(/BenchmarkBaselineDirectRequest-\d+\s+\d+\s+([\d.]+) ns\/op/);
  const proxyReq = out.match(/BenchmarkBaselineProxyRequest-\d+\s+\d+\s+([\d.]+) ns\/op/);
  if (directReq && proxyReq) {
    const overheadUs = (Number(proxyReq[1]) - Number(directReq[1])) / 1000;
    report(
      `Non-streaming additive overhead ≈ **${overheadUs.toFixed(0)} µs** per request (budget ≤ 10 ms).`,
    );
  }
  const directTtfb = out.match(/BenchmarkBaselineDirectFirstByte-.*?([\d.]+) ttfb-ms/);
  const proxyTtfb = out.match(/BenchmarkBaselineProxyFirstByte-.*?([\d.]+) ttfb-ms/);
  if (directTtfb && proxyTtfb) {
    report(
      `Streaming first-byte: direct ${directTtfb[1]} ms vs proxied ${proxyTtfb[1]} ms (mean over benchmark iterations).`,
    );
  }
  report();
}

function artifactSize(bytes) {
  const gzipBytes = (() => {
    try {
      return execFileSync('gzip', ['-c', '-6'], {
        input: readFileSync(binaryPath),
        maxBuffer: 64 << 20,
      }).length;
    } catch {
      return null;
    }
  })();
  report('## Artifact size');
  report();
  report('| Artifact | Size |');
  report('| --- | --- |');
  report(`| anypick binary (-trimpath -ldflags "-s -w") | ${(bytes / 1048576).toFixed(1)} MB |`);
  if (gzipBytes != null) report(`| └ gzip -6 | ${(gzipBytes / 1048576).toFixed(1)} MB |`);
  report();
}

metadata();
mkdirSync(dirname(outPath), { recursive: true });
const workRoot = mkdtempSync(join(tmpdir(), 'anypick-perf-'));
let binaryPath = '';
try {
  binaryPath = join(workRoot, 'anypick');
  const size = await buildBinary(binaryPath);
  const benchEnv = { ...process.env };
  const env = {
    ...benchEnv,
    HOME: workRoot,
    ANYPICK_HOME: join(workRoot, '.anypick'),
  };

  const cli = await measureCli(binaryPath, env);
  const svc = await measureService(binaryPath, env);
  measureProxyOverhead();
  artifactSize(size);

  report('## Budget check (initial budgets, NATIVE_APP_CUTOVER.md M4)');
  report();
  const budget = (label, value, limit, ok) =>
    report(`| ${label} | ${value} | ≤ ${limit} | ${ok ? 'pass' : '**over**'} |`);
  report('| Metric | Measured | Budget | Status |');
  report('| --- | --- | --- | --- |');
  if (cli) budget('Warm CLI read-only p95', fmtMs(cli.warmP95), '100 ms', cli.warmP95 <= 100);
  if (svc?.readyP95 != null)
    budget('Service spawn → ready p95', fmtMs(svc.readyP95), '500 ms', svc.readyP95 <= 500);
  if (svc?.snapshotP95 != null)
    budget(
      'Service spawn → initial snapshot p95',
      fmtMs(svc.snapshotP95),
      '2,000 ms',
      svc.snapshotP95 <= 2000,
    );
  if (svc?.idleCpuPercent != null)
    budget('Idle service CPU', `${svc.idleCpuPercent} %`, '0.2 %', svc.idleCpuPercent <= 0.2);
  if (svc?.idleRssKb != null)
    budget(
      'Idle service RSS',
      `${(svc.idleRssKb / 1024).toFixed(1)} MB`,
      '40 MB',
      svc.idleRssKb / 1024 <= 40,
    );
  report();

  writeFileSync(outPath, lines.join('\n') + '\n');
  process.stdout.write(`\nReport written to ${outPath}\n`);
} finally {
  rmSync(workRoot, { recursive: true, force: true });
}
