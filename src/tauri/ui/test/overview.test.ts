import { describe, expect, it } from 'vitest';

import type { ActionRow } from '../lib/contract';
import {
  isOnboardingCoreComplete,
  onboardingFooter,
  overviewApps,
  overviewConnectionSummary,
  overviewTransportLabel,
} from '../lib/overview';

/**
 * The Overview pane's selection rules.
 *
 * macOS lists at most three apps and offers a way to the rest. The Tauri pane
 * listed every app, which made Overview a second Apps pane and pushed the app
 * that needed attention off the screen behind the ones that were already fine.
 */

function route(overrides: Partial<ActionRow> & { clientId: string }): ActionRow {
  return {
    id: `act-${overrides.clientId}`,
    sourceId: `account/claude/work`,
    client: 'Claude Code',
    label: 'Work',
    kind: 'account',
    presentation: 'native-account',
    selected: false,
    enabled: true,
    ...overrides,
  };
}

const CONFIGURED = route({ clientId: 'claude', selected: true, routeKind: 'managed-account', detail: 'alex@studio.example' });
const DIRECT = route({ clientId: 'codex', client: 'Codex', selected: true, routeKind: 'native', label: 'Direct' });
const UNCONFIGURED = route({ clientId: 'gemini', client: 'Gemini' });
const BLOCKED = route({ clientId: 'opencode', client: 'OpenCode', enabled: false, disabledReason: 'No login saved.' });

describe('which apps the pane lists', () => {
  it('lists at most three', () => {
    const many = Array.from({ length: 9 }, (_, index) =>
      route({ clientId: `app${index}`, client: `App ${index}` }),
    );
    const listed = overviewApps(many);
    expect(listed.listed).toHaveLength(3);
    expect(listed.total).toBe(9);
    expect(listed.hasMore).toBe(true);
  });

  it('shows active and broken apps first', () => {
    // The app that needs the user must not be pushed off by healthy rows, and an
    // app with no route and no reason is neither active nor broken, so it waits
    // for the See All row rather than taking one of three slots.
    const listed = overviewApps([
      UNCONFIGURED,
      route({ clientId: 'a', client: 'A' }),
      route({ clientId: 'b', client: 'B' }),
      route({ clientId: 'c', client: 'C' }),
      CONFIGURED,
      BLOCKED,
    ]);
    expect(listed.listed.map((app) => app.clientId)).toEqual(['claude', 'opencode']);
    expect(listed.title).toBe('Active Apps');
    expect(listed.hasMore).toBe(true);
  });

  it('falls back to the first three when nothing is active', () => {
    const listed = overviewApps([
      route({ clientId: 'a', client: 'A' }),
      route({ clientId: 'b', client: 'B' }),
      route({ clientId: 'c', client: 'C' }),
      route({ clientId: 'd', client: 'D' }),
    ]);
    expect(listed.listed.map((app) => app.clientId)).toEqual(['a', 'b', 'c']);
    expect(listed.title).toBe('Apps');
  });

  it('offers no See All when everything already fits', () => {
    const listed = overviewApps([CONFIGURED, DIRECT]);
    expect(listed.hasMore).toBe(false);
  });

  it('counts one row per app, not per route', () => {
    // An app with four routes is one app; four rows would push three others off.
    const routes = [1, 2, 3, 4].map((n) =>
      route({ clientId: 'claude', id: `act-${n}`, sourceId: `account/claude/w${n}`, selected: n === 1 }),
    );
    const listed = overviewApps(routes);
    expect(listed.total).toBe(1);
    expect(listed.listed).toHaveLength(1);
    expect(listed.listed[0]?.selected?.id).toBe('act-1');
  });

  it('says so when an app needs a login', () => {
    expect(overviewApps([UNCONFIGURED]).footer).toBe('Choose a login for each app before it can run.');
    expect(overviewApps([CONFIGURED]).footer).toBe('');
  });

  it('reports nothing for no routes at all', () => {
    const listed = overviewApps([]);
    expect(listed.listed).toEqual([]);
    expect(listed.total).toBe(0);
    expect(listed.hasMore).toBe(false);
  });
});

describe('the app row', () => {
  it('names the login an app is using, and says what that name is for', () => {
    // The row's title is the app, so a detail that is a bare login name leaves
    // the reader guessing what it refers to.
    expect(
      overviewConnectionSummary({ clientId: 'claude', client: 'Claude Code', selected: CONFIGURED, needsAttention: false }),
    ).toBe('Uses Work');
  });

  it('falls back to the engine own detail when there is no label', () => {
    expect(
      overviewConnectionSummary({
        clientId: 'codex',
        client: 'Codex',
        selected: route({ clientId: 'codex', selected: true, label: '', detail: 'alex@studio.example' }),
        needsAttention: false,
      }),
    ).toBe('alex@studio.example');
  });

  it('says an unconfigured app has no login chosen', () => {
    expect(
      overviewConnectionSummary({ clientId: 'gemini', client: 'Gemini', selected: null, needsAttention: false }),
    ).toBe('No login chosen');
  });

  it('uses the Apps pane own transport words', () => {
    // Two panes naming the same thing differently is how a user concludes they
    // are looking at two different routes.
    expect(
      overviewTransportLabel({ clientId: 'claude', client: 'Claude Code', selected: CONFIGURED, needsAttention: false }),
    ).toBe('AnyPick proxy');
    expect(
      overviewTransportLabel({ clientId: 'codex', client: 'Codex', selected: DIRECT, needsAttention: false }),
    ).toBe('Direct');
    expect(
      overviewTransportLabel({ clientId: 'gemini', client: 'Gemini', selected: null, needsAttention: false }),
    ).toBe('Not Configured');
  });
});

describe('the checklist footer', () => {
  it('says the Hub is optional either way', () => {
    // Without this, hiding the checklist reads as skipping a required step.
    expect(onboardingFooter(false)).toContain('Proxy Hub is optional');
    expect(onboardingFooter(true)).toContain('hide this list');
  });
});

describe('when setup is complete', () => {
  it('needs a saved login and an app pointed at one', () => {
    expect(isOnboardingCoreComplete({ accounts: [], actions: [CONFIGURED] })).toBe(false);
    expect(isOnboardingCoreComplete({ accounts: [{}], actions: [UNCONFIGURED] })).toBe(false);
    expect(isOnboardingCoreComplete({ accounts: [{}], actions: [CONFIGURED] })).toBe(true);
  });
});