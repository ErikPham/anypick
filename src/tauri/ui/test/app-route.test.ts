import { describe, expect, it } from 'vitest';

import type { ActionRow, ClientModelConfig } from '../lib/contract';
import {
  allRouteOptions,
  filteredRouteGroups,
  gatewayProviders,
  hasMatchingConnection,
  matchesRouteSearch,
  modelScopeProviderName,
  needsModelChoice,
  noConnectionCopy,
  offersModelStep,
  primaryDisabled,
  primaryTitle,
  providerFromSourceRef,
  roleOptions,
  routeGroups,
  showsCancel,
  showsConnectionSearch,
} from '../lib/app-route';

/**
 * The Apps connection flow's rules, tested without a webview.
 *
 * The decisions here are what make the sheet safe rather than busy: which
 * connection is offered, when a model step appears, and when the apply button
 * is disabled. All of them are ported from `AppSheets.swift`, and all of them
 * are the kind of thing that reads correctly and behaves wrongly.
 */

function row(overrides: Partial<ActionRow> & { id: string }): ActionRow {
  return {
    clientId: 'claude',
    sourceId: 'account/claude/work',
    client: 'Claude Code',
    label: 'Work',
    kind: 'native',
    presentation: 'app-route',
    selected: false,
    enabled: true,
    routeKind: 'managed-account',
    ...overrides,
  };
}

const current = row({ id: 'act-current', selected: true, label: 'Work', detail: 'alex@studio.example' });
const personal = row({ id: 'act-personal', label: 'Personal', detail: 'sam@studio.example' });
const team = row({ id: 'act-team', sourceId: 'gateway/team', label: 'Team', routeKind: 'gateway', kind: 'gateway' });
const hub = row({ id: 'act-hub', sourceId: 'hub/default', label: 'Proxy Hub', routeKind: 'hub', kind: 'hub' });

describe('route grouping', () => {
  it('pins the current connection apart from the others', () => {
    // Offering the connection the app already has as a "change" makes the
    // review step say From and To are the same thing.
    const groups = routeGroups([current, personal, team, hub]);
    expect(groups.current?.id).toBe('act-current');
    expect(groups.accounts.map((option) => option.id)).toEqual(['act-personal']);
    expect(groups.hub?.id).toBe('act-hub');
    expect(groups.gateways.map((option) => option.id)).toEqual(['act-team']);
  });

  it('counts the current connection in the option total', () => {
    expect(allRouteOptions(routeGroups([current, personal])).map((o) => o.id)).toEqual([
      'act-current',
      'act-personal',
    ]);
  });

  it('classifies by routeKind, falling back to kind', () => {
    // An engine that sends only `kind` must still produce three groups. Each row
    // needs its own identity, because `uniqueCompatibleRoutes` collapses two
    // rows that name the same provider, label and detail — which is the point.
    const groups = routeGroups([
      row({ id: 'a', selected: true, label: 'Work', routeKind: undefined, kind: 'native' }),
      row({ id: 'b', label: 'Team', sourceId: 'gateway/team', routeKind: undefined, kind: 'gateway' }),
      row({ id: 'c', label: 'Proxy Hub', sourceId: 'hub/default', routeKind: undefined, kind: 'hub' }),
    ]);
    expect(groups.accounts).toEqual([]);
    expect(groups.hub?.id).toBe('c');
    expect(groups.gateways.map((o) => o.id)).toEqual(['b']);
  });

  it('keeps a selected row exactly once even when the engine repeats it', () => {
    const groups = routeGroups([current, { ...current, id: 'act-current-dup' }]);
    expect(allRouteOptions(groups)).toHaveLength(1);
  });
});

describe('the connection search', () => {
  it('appears only above six options', () => {
    // macOS's `showsConnectionSearch`. Below the threshold a search field is a
    // control for a list of four. Each synthetic row needs its own label or
    // `uniqueCompatibleRoutes` collapses them into one.
    const four = routeGroups([current, personal, team, hub]);
    expect(showsConnectionSearch(four)).toBe(false);

    const many = routeGroups([
      current,
      ...Array.from({ length: 6 }, (_, index) => row({ id: `act-${index}`, label: `Account ${index}` })),
    ]);
    expect(showsConnectionSearch(many)).toBe(true);
  });

  it('reads the provider off a serialized source ref', () => {
  // `ActionRow.sourceId` is `refs.SerializeRef` output, not a bare provider id.
  // Passing the whole ref through titles an account row "Account".
  expect(providerFromSourceRef('account/claude/work')).toBe('claude');
  expect(providerFromSourceRef('account/openai/team')).toBe('openai');
  // `refs.SerializeRef` writes a hub ref as `hub/<name>`; `proxy-hub/default`
  // is a proxy *row* id and never reaches here from the engine.
  expect(providerFromSourceRef('hub/default')).toBe('proxy-hub');
  expect(providerFromSourceRef('claude')).toBe('claude');
  // A gateway ref carries no vendor. Returning one here made every gateway row
  // wear the OpenRouter mark, including rows for LiteLLM and Cloudflare.
  expect(providerFromSourceRef('gateway/team')).toBe('');
  // Keyed by name: the engine's ref is `gateway/<name>`.
  expect(gatewayProviders([{ name: 'team', providerId: 'litellm' }]).get('team')).toBe('litellm');
  const [option] = routeGroups([{ ...team, id: 'act-gw' }], (id) =>
    id === 'team' ? 'litellm' : undefined,
  ).gateways;
  expect(option?.providerId).toBe('litellm');
});

it('matches on name, detail and provider', () => {
    const option = routeGroups([current]).current!;
    expect(matchesRouteSearch(option, 'work')).toBe(true);
    expect(matchesRouteSearch(option, 'alex')).toBe(true);
    expect(matchesRouteSearch(option, 'claude')).toBe(true);
    expect(matchesRouteSearch(option, 'absent')).toBe(false);
  });

  it('keeps the current connection visible while filtering', () => {
    // Hiding it would lose the only statement of what the app uses now.
    const filtered = filteredRouteGroups(routeGroups([current, personal]), 'personal');
    expect(filtered.current?.id).toBe('act-current');
    expect(filtered.accounts.map((o) => o.id)).toEqual(['act-personal']);
  });

  it('reports no match when the filter excludes everything', () => {
    const filtered = filteredRouteGroups(routeGroups([current, personal]), 'absent');
    expect(hasMatchingConnection(filtered)).toBe(false);
  });
});

describe('the empty copy', () => {
  it('distinguishes nothing configured from nothing matching', () => {
    // Three states, because they call for different actions: add an account,
    // clear the filter, or pick another search.
    expect(noConnectionCopy({ totalOptions: 0, needle: '' })).toBe(
      'No compatible connections are available.',
    );
    expect(noConnectionCopy({ totalOptions: 4, needle: '' })).toBe(
      'No other compatible connections are available.',
    );
    expect(noConnectionCopy({ totalOptions: 4, needle: ' gem ' })).toBe(
      'No other connections match “gem”.',
    );
  });
});

describe('the model step', () => {
  const config: ClientModelConfig = {
    clientId: 'claude',
    client: 'Claude Code',
    roles: [{ id: 'default', label: 'Default' }, { id: 'reasoning', label: 'Reasoning' }],
    modelRoles: { default: 'a1', reasoning: 'a2' },
    options: [
      { actionId: 'a1', modelId: 'claude-opus' },
      { actionId: 'a2', modelId: 'claude-sonnet' },
    ],
    editable: true,
    defaultModel: 'claude-opus',
  };

  it('appears only when the connection can be edited and has options', () => {
    expect(offersModelStep(config)).toBe(true);
    expect(offersModelStep({ ...config, editable: false })).toBe(false);
    expect(offersModelStep({ ...config, options: [] })).toBe(false);
    expect(offersModelStep({ ...config, roles: [] })).toBe(false);
    expect(offersModelStep(undefined)).toBe(false);
  });

  it('nudges for a choice only when nothing is pinned', () => {
    // An app that already has a default must not be forced through the step.
    expect(needsModelChoice(config)).toBe(false);
    expect(needsModelChoice({ ...config, defaultModel: '' })).toBe(true);
  });

  it('offers Same as Default on every role except the default', () => {
    // Sending an empty map clears the pins, and the engine refuses a map with
    // no default at all.
    expect(roleOptions(config, 'default').map((o) => o.label)).toEqual([
      'claude-opus',
      'claude-sonnet',
    ]);
    expect(roleOptions(config, 'reasoning').map((o) => o.label)).toEqual([
      'Same as Default',
      'claude-opus',
      'claude-sonnet',
    ]);
  });
});

describe('the model scope', () => {
  it('names the family whose default the provider scope covers', () => {
    const groups = routeGroups([current, personal, hub, team]);
    expect(modelScopeProviderName(groups.accounts[0] ?? null)).toBe('Claude');
    expect(modelScopeProviderName(groups.hub)).toBe('Proxy Hub');
    expect(modelScopeProviderName(groups.gateways[0] ?? null)).toBe('Team');
  });

  it('stays connection-scoped when the route names no provider', () => {
    // No provider means no family to scope over, so the picker does not appear.
    expect(modelScopeProviderName(null)).toBeNull();
    expect(
      modelScopeProviderName({ ...(routeGroups([current]).current!), providerId: '' }),
    ).toBeNull();
  });
});

describe('the primary button', () => {
  it('names what pressing it does next', () => {
    expect(primaryTitle('connection', false)).toBe('Review Change');
    expect(primaryTitle('review', false)).toBe('Apply Changes');
    expect(primaryTitle('applying', false)).toBe('Applying…');
    expect(primaryTitle('recovery', false)).toBe('Try Again');
    expect(primaryTitle('models', false)).toBe('Save Models');
    expect(primaryTitle('success', true)).toBe('Choose Models');
    expect(primaryTitle('success', false)).toBe('Done');
  });

  it('is disabled on an unchanged selection, not just an empty one', () => {
    // macOS disables when routeID equals the current route. Without that the
    // user can review a change from a source to itself.
    const base = {
      currentRouteId: 'act-current',
      stage: 'connection' as const,
      busy: false,
      applying: false,
      awaitingModelSave: false,
    };
    expect(primaryDisabled({ ...base, routeId: '' })).toBe(true);
    expect(primaryDisabled({ ...base, routeId: 'act-current' })).toBe(true);
    expect(primaryDisabled({ ...base, routeId: 'act-personal' })).toBe(false);
  });

  it('is disabled while the engine is single-flight', () => {
    const base = {
      routeId: 'act-personal',
      currentRouteId: 'act-current',
      stage: 'connection' as const,
      applying: false,
      awaitingModelSave: false,
    };
    expect(primaryDisabled({ ...base, busy: true })).toBe(true);
    expect(primaryDisabled({ ...base, busy: false })).toBe(false);
  });

  it('does not disable the review stage for an unchanged selection', () => {
    // The user gets to the review stage from a real change; re-checking the
    // selection there would lock them out of confirming it.
    expect(
      primaryDisabled({
        routeId: 'act-current',
        currentRouteId: 'act-current',
        stage: 'review',
        busy: false,
        applying: false,
        awaitingModelSave: false,
      }),
    ).toBe(false);
  });
});

describe('cancel', () => {
  it('is hidden while applying and after success', () => {
    // There is nothing to cancel once the write is in flight, and hiding it
    // stops the sheet looking dismissible when it is not.
    expect(showsCancel('connection')).toBe(true);
    expect(showsCancel('review')).toBe(true);
    expect(showsCancel('applying')).toBe(false);
    expect(showsCancel('success')).toBe(false);
    expect(showsCancel('recovery')).toBe(true);
  });
});