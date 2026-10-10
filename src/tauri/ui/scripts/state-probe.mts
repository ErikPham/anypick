/**
 * Prove, in a real browser, that the things jsdom structurally cannot see.
 *
 *   npm run probe
 *
 * jsdom never loads `styles.css`, reports every element as 0x0, and never
 * fires a pointer. So `test/command-state.test.tsx` can only read the source,
 * and a class can be present and do nothing: `bg-badge` and `bg-active` both
 * shipped as silent no-ops that way, which is what `colour-tokens.test.ts`
 * was written to stop. This drives the built bundle in Chromium instead, and
 * fails the build when a control stops painting, stops spinning, or starts
 * moving its row.
 *
 * It is a gate, not a screenshot diff, because every check is an assertion
 * about a measured value. `make screenshots-tauri-ui` stays the human tool:
 * it needs a human to accept new pixels.
 *
 * The fixture frame comes from `test/fixture.ts`, the same snapshot the unit
 * tests assert on, so a preview can never drift from what the tests see.
 */

import { createServer } from 'node:http';
import { access, readFile } from 'node:fs/promises';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

import { emitFixtureLine } from './fixture-wire.mts';

const here = fileURLToPath(new URL('.', import.meta.url));
const uiRoot = join(here, '..');
const bundle = join(uiRoot, '..', 'frontend');

const MIME: Record<string, string> = {
  '.html': 'text/html',
  '.js': 'text/javascript',
  '.css': 'text/css',
  '.svg': 'image/svg+xml',
};

type Check = { name: string; pass: boolean; detail: string };

function fail(message: string): never {
  console.error(message);
  process.exit(1);
}

async function serve(): Promise<{ origin: string; close: () => Promise<void> }> {
  const server = createServer(async (req, res) => {
    const path = normalize(decodeURIComponent((req.url ?? '/').split('?')[0]));
    try {
      const body = await readFile(join(bundle, path === '/' ? 'index.html' : path));
      res.writeHead(200, { 'content-type': MIME[extname(path)] ?? 'application/octet-stream' });
      res.end(body);
    } catch {
      res.writeHead(404).end('not found');
    }
  });
  const port = await new Promise<number>((resolve) => {
    server.listen(0, '127.0.0.1', () => resolve((server.address() as { port: number }).port));
  });
  // An open listener keeps the event loop alive, so a gate that never returns
  // reads as a hang rather than a pass.
  return {
    origin: `http://127.0.0.1:${port}`,
    close: () => new Promise<void>((resolve) => server.close(() => resolve())),
  };
}

async function main(): Promise<void> {
  try {
    await access(join(bundle, 'index.html'));
  } catch {
    fail(`no bundle at ${bundle}. Run \`make build-tauri-ui\` first.`);
  }

  const { chromium } = await import('playwright');
  const frame = await emitFixtureLine();
  const site = await serve();
  const origin = site.origin;
  const browser = await chromium.launch();
  const context = await browser.newContext({ viewport: { width: 1100, height: 760 }, deviceScaleFactor: 1 });

  const checks: Check[] = [];
  const record = (name: string, pass: boolean, detail: string) => {
    checks.push({ name, pass, detail });
    console.log(`${pass ? 'PASS' : 'FAIL'}  ${name}\n      ${detail}`);
  };

  // The bridge is stubbed rather than imported: `lib/bridge.ts` throws on
  // purpose outside a Tauri webview, so a browser run needs the fixture fed
  // through a page-level shim. The clipboard is stubbed too, because jsdom's
  // stub does not exist here and `CopyButton` writes to it.
  const shim = (wire: string | null) => {
    (window as unknown as Record<string, unknown>).__TAURI__ = {
      core: { invoke: async (cmd: string) => (cmd === 'last_service_line' ? wire : null) },
      event: { listen: async () => () => {} },
    };
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async () => {}, readText: async () => '' },
    });
  };

  // ── A row command, in a real pane ────────────────────────────────────────
  const page = await context.newPage();
  await page.addInitScript(shim, frame);
  await page.goto(`${origin}/index.html`, { waitUntil: 'load' });
  await page.waitForSelector('[data-testid="overview"]', { timeout: 20_000 });
  await page.click('[data-testid="sidebar-proxies"]');
  await page.waitForSelector('[data-testid="proxies"]', { timeout: 20_000 });
  const row = await page.$('[data-testid^="proxy-"]');
  if (!row) fail('the fixture publishes no proxy row, so there is nothing to measure');
  await row.click();

  const copy = '[data-testid="proxies.inspector.copy"]';
  try {
    await page.waitForSelector(copy, { timeout: 20_000 });
  } catch {
    fail(`the proxy inspector never opened, so ${copy} was never measured`);
  }

  const box = () => page.$eval(copy, (el) => JSON.stringify(el.getBoundingClientRect()));
  const idle = await box();
  await page.click(copy);
  await page.waitForFunction(
    (sel) => document.querySelector(sel)?.getAttribute('title') === 'Copied',
    copy,
    { timeout: 10_000 },
  );
  const confirmed = await box();
  record(
    'a copy confirmation does not move the row',
    idle === confirmed,
    `idle ${idle}  confirmed ${confirmed}`,
  );
  record(
    'the confirmation is legible as text, not only as a colour',
    (await page.$eval(copy, (el) => el.getAttribute('aria-label'))) === 'Copied',
    `title="${await page.$eval(copy, (el) => el.getAttribute('title'))}" aria-label="${await page.$eval(copy, (el) => el.getAttribute('aria-label'))}"`,
  );

  // The click above leaves the pointer on the control, so parking it away
  // first is what makes "resting" mean resting.
  await page.mouse.move(5, 5);
  await page.waitForTimeout(200);
  const resting = await page.$eval(copy, (el) => getComputedStyle(el).backgroundColor);
  await page.hover(copy);
  await page.waitForTimeout(200);
  const hovered = await page.$eval(copy, (el) => getComputedStyle(el).backgroundColor);
  record('hover paints an icon command', resting !== hovered, `resting ${resting}  hovered ${hovered}`);

  const at = await page.$eval(copy, (el) => {
    const r = el.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  });
  await page.mouse.move(at.x, at.y);
  await page.mouse.down();
  await page.waitForTimeout(200);
  const pressed = await page.$eval(copy, (el) => getComputedStyle(el).backgroundColor);
  await page.mouse.up();
  record('pressed paints its own state', pressed !== hovered, `hovered ${hovered}  pressed ${pressed}`);
  await page.close();

  // ── The spinner ─────────────────────────────────────────────────────────
  // Race the loading state and the check is luck, so build the control the app
  // builds and read the real stylesheet against it.
  const spin = await context.newPage();
  await spin.addInitScript(shim, frame);
  await spin.goto(`${origin}/index.html`, { waitUntil: 'load' });
  await spin.waitForSelector('[data-testid="overview"]', { timeout: 20_000 });
  await spin.evaluate(() => {
    const el = document.createElement('div');
    el.className = 'spinner';
    el.id = 'probe-spinner';
    document.body.appendChild(el);
  });
  const turning = await spin.evaluate(async () => {
    const el = document.querySelector('#probe-spinner') as HTMLElement;
    const style = getComputedStyle(el);
    const lean = () => new DOMMatrixReadOnly(getComputedStyle(el).transform).m12;
    const before = lean();
    await new Promise((done) => setTimeout(done, 240));
    return { name: style.animationName, duration: style.animationDuration, moved: before !== lean() };
  });
  record(
    'the loading spinner turns',
    turning.name !== 'none' && turning.moved,
    `animation-name=${turning.name} duration=${turning.duration} transformed during 240ms=${turning.moved}`,
  );

  await spin.emulateMedia({ reducedMotion: 'reduce' });
  await spin.waitForTimeout(200);
  const reduced = await spin.evaluate(() => {
    const style = getComputedStyle(document.querySelector('#probe-spinner') as HTMLElement);
    return { name: style.animationName, duration: style.animationDuration };
  });
  // A frozen circle says "stalled", which is the one thing this control must
  // never say, so Reduce Motion slows it and never removes it.
  record(
    'Reduce Motion retards the spinner rather than removing it',
    reduced.name !== 'none' && reduced.duration !== turning.duration,
    `default ${turning.duration}  reduced ${reduced.duration} (still ${reduced.name})`,
  );
  await spin.close();

  // ── A command with nothing to act on ────────────────────────────────────
  const boot = await context.newPage();
  await boot.addInitScript(shim, null);
  await boot.goto(`${origin}/index.html`, { waitUntil: 'load' });
  try {
    await boot.waitForSelector('[data-testid="toolbar-refresh"]', { timeout: 20_000 });
    const state = await boot.$eval('[data-testid="toolbar-refresh"]', (el) => ({
      disabled: (el as HTMLButtonElement).disabled,
      opacity: getComputedStyle(el).opacity,
    }));
    record(
      'a command with nothing to act on greys itself',
      state.disabled,
      `disabled=${state.disabled} opacity=${state.opacity}`,
    );
  } catch {
    record('a command with nothing to act on greys itself', false, 'no toolbar on a shell with no snapshot');
  }
  await boot.close();

  await browser.close();
  await site.close();

  const broken = checks.filter((c) => !c.pass);
  console.log(`\n${checks.length - broken.length}/${checks.length} checks passed`);
  if (broken.length > 0) {
    // Name the file that owns the failure, because the two apps now share a
    // rule and only one of them is running here.
    console.error(
      `\nThese are the states jsdom cannot see: ${broken.map((c) => c.name).join('; ')}.\n` +
        'Either a class lost its token, or a control stopped painting. See\n' +
        'src/tauri/README.md, "An icon command owns its state".',
    );
    process.exit(1);
  }
}

await main();
