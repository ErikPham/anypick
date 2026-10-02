import { describe, expect, it } from 'vitest';

import type { ActionRow } from '../lib/contract';
import {
  appEntries,
  appIssue,
  appSubtitle,
  appTrailing,
  appsSectionTitle,
} from '../lib/app-entry';

/**
 * The Apps row model.
 *
 * The shape that made this pane look like a control panel was building a row
 * per *route*. macOS builds one row per app (`AppsView.swift:34`) and puts the
 * route commands in its context menu. These rules are the difference.
 */

function route(clientId: string, overrides: Partial<ActionRow> = {}): ActionRow {
  return {
    id: `act-${clientId}-${overrides.label ?? 'default'}`,
    clientId,
    sourceId: `account/${clientId}/work`,
    client: 'Claude Code',
    label: 'Work',
    kind: 'native',
    presentation: 'direct-account',
    selected: false,
    enabled: true,
    ...overrides,
  };
}

const MANAGED = route('claude', { id: 'act-1', label: 'Work', selected: true, routeKind: 'managed-account', detail: 'alex@studio.example' });
const GATEWAY = route('claude', { id: 'act-2', label: 'Team gateway', routeKind: 'gateway', kind: 'gateway' });
const PERSONAL = route('claude', { id: 'act-3', label: 'Personal', routeKind: 'managed-account' });

describe('one row per app', () => {
  it('collapses every route an app publishes into one entry', () => {
    const entries = appEntries([MANAGED, GATEWAY, PERSONAL]);
    expect(entries).toHaveLength(1);
    expect(entries[0]?.routes).toHaveLength(3);
    expect(entries[0]?.selected?.id).toBe('act-1');
  });

  it('keeps one entry per app', () => {
    const entries = appEntries([MANAGED, route('codex', { id: 'act-4', client: 'Codex' })]);
    expect(entries.map((entry) => entry.clientId)).toEqual(['claude', 'codex']);
  });

  it('knows when AnyPick is routing the app', () => {
    expect(appEntries([MANAGED])[0]?.managed).toBe(true);
    expect(appEntries([route('gemini', { id: 'act-5' })])[0]?.managed).toBe(false);
  });

  it('surfaces the reason the engine refused any of them', () => {
    // The reason belongs to the app, not to whichever route happened to carry
    // it, so a user sees it next to the app it is about.
    const entries = appEntries([
      MANAGED,
      route('claude', { id: 'act-6', label: 'Team', enabled: false, disabledReason: 'Gateway is unreachable.' }),
    ]);
    expect(appIssue(entries[0]!)).toBe('Gateway is unreachable.');
  });

  it('says an app with no route has no login chosen', () => {
    expect(appIssue(appEntries([route('gemini', { id: 'act-7' })])[0]!)).toBe('No login chosen for this app.');
  });
});

describe('the row subtitle', () => {
  it('names the login in use, with the identity it reported', () => {
    // macOS builds `[routeName, detail]`, so the row says which account *and*
    // who is signed in. Just "Work" would leave the user checking elsewhere.
    expect(appSubtitle(appEntries([MANAGED])[0]!)).toBe('Work · alex@studio.example');
  });

  it('does not repeat a detail the label already carries', () => {
    const entry = appEntries([
      route('claude', { id: 'a', label: 'Work', selected: true, routeKind: 'managed-account', detail: 'Work' }),
    ])[0]!;
    expect(appSubtitle(entry)).toBe('Work');
  });

  it('unwraps the engine own "Uses …" phrasing', () => {
    const entry = appEntries([
      route('claude', { id: 'a', label: 'Work', selected: true, routeKind: 'managed-account', detail: 'Uses Work · alex@studio.example' }),
    ])[0]!;
    // "Uses Work · alex@studio.example" already names the route in its label,
    // so repeating "Work" next to "Work" is noise.
    expect(appSubtitle(entry)).toBe('Work · alex@studio.example');
  });

  it('drops a transport word the label already says', () => {
    const entry = appEntries([
      route('claude', { id: 'a', label: 'Direct', selected: true, routeKind: 'native', detail: 'Managed' }),
    ])[0]!;
    expect(appSubtitle(entry)).toBe('Direct');
  });

  it('says Not Configured for an app with no route', () => {
    expect(appSubtitle(appEntries([route('gemini', { id: 'a' })])[0]!)).toBe('Not Configured');
  });
});

describe('the trailing column', () => {
  it('shows the transport for a managed app', () => {
    expect(appTrailing(appEntries([MANAGED])[0]!)).toEqual({ kind: 'status', label: 'AnyPick proxy' });
  });

  it('offers Set Up for a managed app whose route is not live', () => {
    // The row has to say what to do next, not just name a state.
    const entry = appEntries([route('claude', { id: 'a', label: 'Work', routeKind: 'managed-account' })])[0]!;
    expect(appTrailing(entry)).toEqual({ kind: 'setup', label: 'Set Up…' });
  });

  it('says Direct when the app is using its own login', () => {
    const entry = appEntries([route('codex', { id: 'a', client: 'Codex', selected: true, routeKind: 'native' })])[0]!;
    expect(appTrailing(entry)).toEqual({ kind: 'direct', label: 'Direct' });
  });

  it('offers Set Up for an app with nothing configured', () => {
    expect(appTrailing(appEntries([route('gemini', { id: 'a' })])[0]!)).toEqual({ kind: 'setup', label: 'Set Up…' });
  });
});

describe('the section header', () => {
  it('counts the apps so a search is visibly narrower', () => {
    expect(appsSectionTitle(4, false)).toBe('Apps (4)');
    expect(appsSectionTitle(1, true)).toBe('Matching Apps (1)');
  });
});