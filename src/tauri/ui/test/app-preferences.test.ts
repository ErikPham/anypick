import { describe, expect, it, beforeEach } from 'vitest';

import type { ActionRow } from '../lib/contract';
import {
  addApp,
  appShortDescription,
  availableAppsToAdd,
  effectiveConfiguredAppIDs,
  removeApp,
  storeConfiguredAppIDs,
  storeRemovedAppIDs,
  storedConfiguredAppIDs,
  storedRemovedAppIDs,
} from '../lib/app-preferences';

/**
 * Which apps the user manages.
 *
 * The engine publishes every registered client, so this list is a local
 * preference and nothing else. The Tauri pane listed everything, which meant a
 * user who removed an app saw it come back on the next launch.
 */

function route(clientId: string, overrides: Partial<ActionRow> = {}): ActionRow {
  return {
    id: `act-${clientId}`,
    clientId,
    sourceId: `account/${clientId}/work`,
    client: clientId,
    label: 'Work',
    kind: 'account',
    presentation: 'native-account',
    selected: false,
    enabled: true,
    ...overrides,
  };
}

const ALL = ['claude', 'codex', 'kiro', 'gemini', 'opencode'];

beforeEach(() => {
  localStorage.clear();
});

describe('the first-run list', () => {
  it('includes apps AnyPick is already managing', () => {
    // Dropping them on a first run after configuring one login would make the
    // app the user just configured disappear.
    const configured = effectiveConfiguredAppIDs({
      allAppIds: ALL,
      actions: [route('codex', { selected: true, routeKind: 'managed-account' })],
      stored: null,
      removed: [],
    });
    expect(configured).toEqual(['codex']);
  });

  it('includes an app AnyPick is already routing', () => {
    expect(
      effectiveConfiguredAppIDs({
        allAppIds: ALL,
        actions: [route('gemini', { routeKind: 'direct-account', selected: false })],
        stored: null,
        removed: [],
      }),
    ).toEqual(['gemini']);
  });

  it('does not treat an untouched native route as a managed app', () => {
    // Every registered client publishes a native row whether or not AnyPick has
    // ever touched it. Counting those as "already added" made Add App offer
    // nothing on every first run — the feature was dead on arrival, and the
    // tests missed it because no fixture had a native-only client in it.
    const native = [
      route('claude', { routeKind: 'managed-account', selected: true }),
      route('gemini', { routeKind: 'native' }),
      route('opencode', { routeKind: 'native' }),
    ];
    const configured = effectiveConfiguredAppIDs({
      allAppIds: ALL,
      actions: native,
      stored: null,
      removed: [],
    });
    expect(configured).toEqual(['claude']);
    expect(availableAppsToAdd(ALL, configured)).toContain('gemini');
    expect(availableAppsToAdd(ALL, configured)).toContain('opencode');
  });

  it('treats a selected native row as managed, because the user chose it', () => {
    expect(
      effectiveConfiguredAppIDs({
        allAppIds: ALL,
        actions: [route('gemini', { routeKind: 'native', selected: true })],
        stored: null,
        removed: [],
      }),
    ).toEqual(['gemini']);
  });

  it('falls back to three familiar names when the engine says nothing', () => {
    // Otherwise the first run opens on an empty pane, and the empty state asks
    // the user to add apps they were never offered.
    expect(
      effectiveConfiguredAppIDs({ allAppIds: ALL, actions: [], stored: null, removed: [] }),
    ).toEqual(['claude', 'codex', 'kiro']);
  });

  it('never offers a name the engine does not publish', () => {
    expect(
      effectiveConfiguredAppIDs({ allAppIds: ['claude'], actions: [], stored: null, removed: [] }),
    ).toEqual(['claude']);
  });
});

describe('a stored choice', () => {
  it('is the list, minus removals', () => {
    expect(
      effectiveConfiguredAppIDs({
        allAppIds: ALL,
        actions: [],
        stored: ['gemini', 'opencode'],
        removed: ['opencode'],
      }),
    ).toEqual(['gemini']);
  });

  it('survives a restart, which is the whole point of storing it', () => {
    storeConfiguredAppIDs(['gemini']);
    storeRemovedAppIDs(['claude']);
    expect(storedConfiguredAppIDs()).toEqual(['gemini']);
    expect(storedRemovedAppIDs()).toEqual(['claude']);
    // A removed app stays out even though the engine still publishes it.
    expect(
      effectiveConfiguredAppIDs({
        allAppIds: ALL,
        actions: ALL.map((id) => route(id)),
        stored: storedConfiguredAppIDs(),
        removed: storedRemovedAppIDs(),
      }),
    ).toEqual(['gemini']);
  });

  it('reads unreadable storage as no choice rather than throwing', () => {
    localStorage.setItem('anypick.configuredApps', 'not json');
    expect(storedConfiguredAppIDs()).toBeNull();
  });

  it('ignores a stored entry that is not a string', () => {
    localStorage.setItem('anypick.configuredApps', JSON.stringify(['gemini', 7, null]));
    expect(storedConfiguredAppIDs()).toEqual(['gemini']);
  });
});

describe('adding and removing', () => {
  it('offers exactly what is not on the list', () => {
    expect(availableAppsToAdd(ALL, ['claude', 'codex', 'kiro'])).toEqual(['gemini', 'opencode']);
    expect(availableAppsToAdd(ALL, ALL)).toEqual([]);
  });

  it('adds without duplicating', () => {
    expect(addApp(['claude'], [], 'codex').configured).toEqual(['claude', 'codex']);
    expect(addApp(['claude', 'codex'], [], 'codex').configured).toEqual(['claude', 'codex']);
  });

  it('clears the removal when an app is added back', () => {
    // `effectiveConfiguredAppIDs` subtracts removals, so leaving the flag behind
    // makes "add it back" report success and show nothing.
    const next = addApp([], ['gemini'], 'gemini');
    expect(next.configured).toEqual(['gemini']);
    expect(next.removed).toEqual([]);
    expect(
      effectiveConfiguredAppIDs({ allAppIds: ALL, actions: [], stored: next.configured, removed: next.removed }),
    ).toEqual(['gemini']);
  });

  it('records a removal separately from the list', () => {
    // One list cannot express both "never added" and "added then removed".
    const next = removeApp(['claude', 'codex'], [], 'codex');
    expect(next.configured).toEqual(['claude']);
    expect(next.removed).toEqual(['codex']);
  });

  it('does not record a removal twice', () => {
    expect(removeApp(['claude'], ['codex'], 'codex').removed).toEqual(['codex']);
  });
});

describe('the one-line description', () => {
  it('says what each known app is', () => {
    expect(appShortDescription('claude')).toBe('Anthropic’s terminal coding agent');
    expect(appShortDescription('Codex')).toBe('OpenAI’s terminal and desktop coding tool');
  });

  it('has something for an app it does not know', () => {
    expect(appShortDescription('brand-new-tool')).toBe('Supported coding application');
  });
});