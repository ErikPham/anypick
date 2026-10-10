/**
 * Screenshot every destination of the built UI bundle.
 *
 * The visual-parity playbook requires image diff, not eyeballing, and this
 * app's history is that a green suite coexisted with a visibly broken UI. A
 * screenshot harness is the cheapest thing that makes the styling checkable:
 * the panes render from `test/fixture.ts`, the same snapshot the unit tests
 * assert on, with the real built CSS and no Tauri shell.
 *
 * The bridge is stubbed rather than imported. `lib/bridge.ts` throws on
 * purpose outside a Tauri webview, so a browser preview needs the fixture fed
 * through a page-level shim. That shim is injected here rather than shipped in
 * the bundle, so nothing in `frontend/` knows it is being screenshotted.
 *
 *   node scripts/screenshot.mts            # writes to .screenshots/
 *   node scripts/screenshot.mts --update   # accept the current pixels
 */

import { createServer } from 'node:http';
import { mkdirSync } from 'node:fs';
import { access, readFile } from 'node:fs/promises';
import { extname, dirname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

import { DESTINATIONS } from '../lib/contract';
import { emitFixtureLine } from './fixture-wire';

const here = dirname(fileURLToPath(import.meta.url));
const uiRoot = join(here, '..');
const bundle = join(uiRoot, '..', 'frontend');
const outDir = join(uiRoot, '.screenshots');

/**
 * Each destination and the pane's own root id, which is not always the
 * destination's name. `proxyHub` renders `proxy-hub`, `backups` renders
 * `backups`. Waiting on the wrong id hangs the run on a healthy app, so the
 * pairing is declared rather than derived.
 */
const PANE_ROOT: Record<string, string> = {
  overview: 'overview',
  apps: 'apps',
  accounts: 'accounts',
  proxyHub: 'proxy-hub',
  proxies: 'proxies',
  activity: 'activity',
  backups: 'backups',
};

/** The pane title each destination shows, keyed by the sidebar row. */
const PANE_TITLE: Record<string, string> = {
  overview: 'Overview',
  apps: 'Apps',
  accounts: 'Accounts & APIs',
  proxyHub: 'Proxy Hub',
  proxies: 'Proxies',
  activity: 'Activity',
  backups: 'Backups',
};

const VIEWS = DESTINATIONS.map((destination) => ({
  name: destination,
  destination,
  pane: PANE_ROOT[destination] ?? destination,
}));

/**
 * The sheets and dialogs, reached the way a user reaches them.
 *
 * A pane's list screenshot misses its whole secondary surface: a bug that only
 * renders in a dialog is invisible to a list-only harness, which is exactly
 * where the unstyled-class bugs were hiding.
 */
/**
 * A view reached by opening something.
 *
 * `waitsFor` is what the click is expected to reveal. A dialog reveals a role;
 * Published Models reveals a pane, because it is a route the shell pushes
 * rather than a modal. Hardcoding one of the two made the other time out on a
 * perfectly healthy app.
 */
const OPEN_VIEWS: Array<{
  name: string;
  destination: string;
  pane: string;
  open: string[];
  waitsFor: string;
  /** Push the widened fixture instead of the default one. */
  rich?: boolean;
}> = [
  {
    name: 'dialog-accounts-add',
    destination: 'accounts',
    pane: 'accounts',
    open: ['accounts-add'],
    waitsFor: '[role="dialog"]',
  },
  {
    name: 'settings',
    destination: 'settings',
    pane: 'settings-pane',
    open: [],
    waitsFor: '[data-testid="settings-pane"]',
  },
  {
    name: 'published-models',
    destination: 'proxyHub',
    pane: 'proxy-hub',
    open: ['hub-open-models'],
    // The rows, not the pane root. The pane seeds its vendor groups open in an
    // effect after mount, so waiting on the root catches the collapsed state
    // and screenshots a list with nothing in it. The fixture always publishes
    // two models, so this is pinned to what this harness renders.
    waitsFor: '[data-testid^="published-models.row-"]',
  },
  {
    name: 'app-route-connection',
    destination: 'apps',
    pane: 'apps',
    // The app's commands live in its menu, as on macOS, so reaching the
    // connection sheet is two clicks rather than one.
    open: ['app.more-claude', 'app.connection.change-menu'],
    waitsFor: '[data-testid="app-route.stage.connection"]',
  },
  // Two sheets that are conditional on data the fixture may not publish. Their
  // shapes change with the provider, so "a screenshot exists" is not the same as
  // "the shape was looked at".
  {
    // The provider popup is portalled to `document.body` and closed by default,
    // so a screenshot of the sheet alone never showed the brand marks that are
    // the entire reason the control exists (`BrandIcons.swift:41`).
    name: 'dialog-accounts-provider-picker',
    destination: 'accounts',
    pane: 'accounts',
    rich: true,
    open: ['accounts-add', 'account-flow.provider.picker'],
    waitsFor: '[role="listbox"]',
  },
  {
    name: 'dialog-gateway-add',
    destination: 'accounts',
    pane: 'accounts',
    open: ['accounts.add-gateway'],
    waitsFor: '[data-testid="gateway-flow.save"]',
  },
  {
    name: 'dialog-app-add',
    destination: 'apps',
    pane: 'apps',
    // The default fixture publishes one client, which is already on the list.
    // Without the widened frame the sheet can only ever show its empty branch.
    rich: true,
    open: ['apps.add'],
    waitsFor: '[data-testid^="app.add.picker-"]',
  },
];

function fail(message: string): never {
  console.error(`screenshot: ${message}`);
  process.exit(1);
}

async function main() {
  // `access` rather than `existsSync`. Under vite-node the sync import resolves
  // through a different shim than the promise one, and only one of them works.
  try {
    await access(join(bundle, 'index.html'));
  } catch {
    fail(`no bundle at ${bundle}. Run \`make build-tauri-ui\` first.`);
  }

  const { chromium } = await import('playwright');
  const fixtureLine = await emitFixtureLine();
  // One richer frame, emitted once. Both are pushed through the same stub; a
  // view picks which one it sees.
  const richFixtureLine = await emitFixtureLine(true);
  mkdirSync(outDir, { recursive: true });

  const origin = await serve(bundle);
  const browser = await chromium.launch();
  // Both appearances. The macOS app follows the platform and defines every
  // colour per-scheme, so pinning one scheme proves half of it.
  const context = await browser.newContext({
    viewport: { width: 1000, height: 700 },
    colorScheme: process.env.SCREENSHOT_SCHEME === 'light' ? 'light' : 'dark',
    // A fixed scale so a display DPI difference cannot read as a diff.
    deviceScaleFactor: 1,
  });
  const page = await context.newPage();

  // Stub the Tauri bridge before the bundle loads. `main.tsx` imports App
  // synchronously, so the shim has to exist on the page already.
  await page.addInitScript(
    ({ line, rich }: { line: string; rich: string }) => {
      // `addInitScript` serialises its argument once, at injection time, so a
      // mutable object handed in here is a *snapshot* by the time the page
      // reloads. sessionStorage survives the reload and is readable from
      // inside the page, which is what makes a per-view frame possible.
      const wantRich = () => {
        try {
          return sessionStorage.getItem('screenshot.rich') === '1';
        } catch {
          return false;
        }
      };
      const listeners: Record<string, Array<(event: { payload: unknown }) => void>> = {};
      (window as unknown as Record<string, unknown>).__TAURI__ = {
        core: {
          invoke: async (cmd: string) => {
            if (cmd === 'last_service_line') return wantRich() ? rich : line;
            if (cmd === 'send_command') return null;
            return null;
          },
        },
        event: {
          listen: async (name: string, handler: (event: { payload: unknown }) => void) => {
            (listeners[name] ??= []).push(handler);
            return () => {
              listeners[name] = (listeners[name] ?? []).filter((fn) => fn !== handler);
            };
          },
        },
      };
    },
    { line: fixtureLine, rich: richFixtureLine },
  );

  const url = `${origin}/index.html`;
  const written: string[] = [];

  // The pane views have no `open` step; the dialog views do. One union rather
  // than two loops so a new pane and its dialog are added in the same place.
  const shots: Array<{
    name: string;
    destination: string;
    pane: string;
    open: string[];
    waitsFor?: string;
    /** Push the widened fixture instead of the default one. */
    rich?: boolean;
  }> = [
    ...VIEWS.map((view) => ({ ...view, open: [] as string[] })),
    ...OPEN_VIEWS,
  ];

  for (const view of shots) {
    await page.goto(url, { waitUntil: 'load' });
    // Set before the reload below, so the page's first snapshot is the right
    // one rather than one frame of the wrong data being acted on.
    await page.evaluate((rich: boolean) => {
      sessionStorage.setItem('screenshot.rich', rich ? '1' : '0');
    }, view.rich === true);
    await page.reload({ waitUntil: 'load' });
    // No snapshot yet means the shell is still connecting, and the sidebar
    // renders regardless, so the click below would land before the panes exist.
    await page.waitForSelector('[data-testid="overview"]', { timeout: 15_000 });

    // Drive navigation through the sidebar, which is the user's route.
    if (view.destination !== 'overview') {
      await page.click(`[data-testid="sidebar-${view.destination}"]`);
      await page.waitForSelector(`[data-testid="${view.pane}"]`, { timeout: 15_000 });
    }

    // Naming what the last click should reveal is what makes a timeout useful:
    // it points at the control that opened nothing. It waits only after the
    // final step — a view that needs two clicks (a row's menu, then its item)
    // would otherwise time out waiting for the sheet after the *first* one.
    const last = view.open.length - 1;
    for (const [index, testId] of view.open.entries()) {
      await page.click(`[data-testid="${testId}"]`);
      if (view.waitsFor && index === last) {
        await page.waitForSelector(view.waitsFor, { timeout: 15_000 });
      }
    }
    if (view.waitsFor && view.open.length === 0) {
      await page.waitForSelector(view.waitsFor, { timeout: 15_000 });
    }

    const path = join(outDir, `${view.name}.png`);
    await page.screenshot({ path });
    written.push(path);
  }

  await browser.close();
  // Explicit exit: the loopback server keeps the event loop alive, so without
  // this the script hangs after its last screenshot and looks like a hang.
  process.exit(0);
}

/**
 * Serve the bundle over HTTP on a loopback port.
 *
 * `file://` will not do. The built bundle uses ES modules, and a module script
 * loaded from `file://` is blocked by CORS in Chromium, so the page renders
 * nothing and the screenshot is a blank canvas that reads as a passing diff.
 */
async function serve(root: string): Promise<string> {
  const types: Record<string, string> = {
    '.html': 'text/html; charset=utf-8',
    '.js': 'text/javascript; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.svg': 'image/svg+xml',
    '.map': 'application/json',
  };

  const server = createServer((req, res) => {
    // Normalize before joining so a request cannot escape the bundle root.
    const relative = normalize(decodeURIComponent((req.url ?? '/').split('?')[0])).replace(/^(\.\.[/\\])+/, '');
    const path = join(root, relative === '/' ? 'index.html' : relative);
    readFile(path).then(
      (body) => {
        res.writeHead(200, { 'content-type': types[extname(path)] ?? 'application/octet-stream' });
        res.end(body);
      },
      () => {
        res.writeHead(404).end('not found');
      },
    );
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const address = server.address();
  if (address === null || typeof address === 'string') throw new Error('server did not bind a port');
  server.unref();
  return `http://127.0.0.1:${address.port}`;
}

await main();
