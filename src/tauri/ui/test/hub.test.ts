import { describe, expect, it } from 'vitest';

import type { HubCatalog, HubConflict, HubSource, ProxyRow } from '../lib/contract';
import { hubSourceChange } from '../lib/desktop-actions';
import {
  catalogModelCount,
  conflictDisplayInfo,
  hubProxy,
  hubServiceFooter,
  hubSourceDetail,
  hubSourceRows,
  hubStatusSubtitle,
  publishedSummary,
  uniqueProviderCandidates,
} from '../lib/hub';

/**
 * The hub's display and wire rules, tested without a webview.
 *
 * The macOS app's copy is the product's copy, so these assertions are written
 * against the words `ProxyHubView.swift` and `DesignDisplay.swift` produce.
 * They are here rather than in the pane because a routing decision is the kind
 * of thing that is cheap to get wrong and expensive to see.
 */

const account: HubSource = {
  id: 'hub/default/claude/work',
  providerId: 'claude',
  name: 'work',
  label: 'Work',
  detail: 'alex@studio.example',
  enabled: true,
  hub: 'default',
};

const rotation: HubSource = {
  id: 'hub/default/rotation',
  providerId: '',
  name: 'rotation',
  label: 'Rotate across accounts',
  detail: 'Spread each model across the accounts that serve it',
  enabled: false,
  hub: 'default',
  rotates: true,
  accountCount: 2,
};

const ghost: HubSource = {
  id: 'hub/default/claude/gone',
  providerId: 'claude',
  name: 'gone',
  label: 'gone',
  detail: 'Account missing',
  enabled: true,
  hub: 'default',
  ghost: true,
  unavailableReason: 'Account claude/gone no longer exists.',
};

const catalogs: HubCatalog[] = [
  { accountID: 'hub/default/claude/work', catalog: 'claude', models: ['claude-opus', 'claude-sonnet'] },
];

describe('hub source rows', () => {
  it('puts the rotation row apart from the account rows', () => {
    // Rotation carries no account value and toggles through a different
    // operation. Rendering it as an account row is how the switch got inverted.
    const rows = hubSourceRows([account, rotation, ghost]);
    expect(rows.rotation.map((row) => row.id)).toEqual([rotation.id]);
    expect(rows.accounts.map((row) => row.id)).toContain(account.id);
    expect(rows.accounts.map((row) => row.id)).toContain(ghost.id);
    expect(rows.accounts).toHaveLength(2);
  });

  it('sorts accounts by name, so a ghost is ordered rather than sunk', () => {
    // "gone" sorts before "work". The point is that it is present and in a
    // predictable place, not that it is first or last.
    expect(hubSourceRows([account, ghost]).accounts.map((row) => row.name)).toEqual(['gone', 'work']);
  });

  it('keeps a ghost with its provider rather than sinking it', () => {
    // A ghost is a source the user chose and the engine cannot honour, so it
    // is the one row that needs attention.
    const rows = hubSourceRows([ghost, { ...account, providerId: 'openai', name: 'team' }]);
    expect(rows.accounts[0].id).toBe(ghost.id);
  });

  it('sorts by hub, then provider, then name', () => {
    const rows = hubSourceRows([
      { ...account, id: 'b', providerId: 'openai', name: 'team' },
      { ...account, id: 'a', providerId: 'claude', name: 'work' },
    ]);
    expect(rows.accounts.map((row) => row.id)).toEqual(['a', 'b']);
  });
});

describe('hub source detail', () => {
  it('counts the models a source can publish', () => {
    expect(hubSourceDetail(account, catalogs)).toBe('work · 2 models');
  });

  it('says a source with no catalog has no count rather than zero', () => {
    // "work · 0 models" reads as a failure. The absence is the fact.
    expect(hubSourceDetail({ ...account, id: 'hub/default/claude/other' }, catalogs)).toBe('work');
  });

  it('says a disabled source is excluded from the hub', () => {
    expect(hubSourceDetail({ ...account, enabled: false }, catalogs)).toBe('Excluded from Hub');
  });

  it('uses singular for one model', () => {
    expect(
      hubSourceDetail(account, [{ accountID: account.id, catalog: 'claude', models: ['one'] }]),
    ).toBe('work · 1 model');
  });

  it('reads a ghost as excluded', () => {
    expect(hubSourceDetail(ghost, catalogs)).toBe('Excluded from Hub');
  });

  it('counts a catalog by the full hub source id, not the account name', () => {
    // Two hubs can hold the same account. A catalog keyed by account name would
    // give both hubs the first hub's count.
    expect(catalogModelCount(catalogs, 'hub/other/claude/work')).toBe(0);
    expect(catalogModelCount(catalogs, 'hub/default/claude/work')).toBe(2);
  });
});

describe('hub wire form', () => {
  it('sends hub-rotation with only the hub name', () => {
    const action = hubSourceChange({ hub: 'default', source: rotation, enabled: true });
    expect(action).toEqual({ operation: 'hub-rotation', extra: { name: 'default', enabled: true } });
    expect(action.extra).not.toHaveProperty('providerId');
    expect(action.extra).not.toHaveProperty('sourceId');
  });

  it('sends hub-source-toggle with the provider and the account in sourceId', () => {
    const action = hubSourceChange({ hub: 'default', source: account, enabled: false });
    expect(action).toEqual({
      operation: 'hub-source-toggle',
      extra: { name: 'default', providerId: 'claude', sourceId: 'work', enabled: false },
    });
  });

  it('always sends enabled explicitly', () => {
    // The engine's handler honours the flag. The old presence-toggle inferred
    // it from whether the key was there, which inverted the switch.
    for (const enabled of [true, false]) {
      expect(hubSourceChange({ hub: 'default', source: account, enabled }).extra.enabled).toBe(enabled);
    }
  });

  it('defaults an unnamed hub to "default", which is what the engine reads', () => {
    expect(hubSourceChange({ hub: '', source: account, enabled: true }).extra.name).toBe('default');
  });
});

describe('routing conflicts', () => {
  const conflict: HubConflict = {
    id: 'overlap-1',
    kind: 'model-overlap',
    title: 'claude-sonnet',
    models: ['claude-sonnet'],
    candidates: [
      { id: 'c1', providerId: 'claude', label: 'Claude', detail: 'Work', actionId: 'a1', providerChoice: true },
      { id: 'c2', providerId: 'openai', label: 'OpenAI', detail: 'Team', actionId: 'a2', providerChoice: true },
    ],
  };

  it('collapses two accounts of one provider into one choice', () => {
    // Two saved Claude accounts are one routing decision. Offering both would
    // let the user choose something that changes nothing.
    const withTwoAccounts: HubConflict = {
      ...conflict,
      candidates: [
        ...conflict.candidates,
        { id: 'c3', providerId: 'claude', label: 'Claude', detail: 'Personal', actionId: 'a3', providerChoice: true },
      ],
    };
    expect(uniqueProviderCandidates(withTwoAccounts).map((c) => c.actionId)).toEqual(['a1', 'a2']);
  });

  it('keeps distinct providers apart', () => {
    expect(uniqueProviderCandidates(conflict)).toHaveLength(2);
  });

  it('names the model and the models that collided', () => {
    expect(conflictDisplayInfo(conflict).title).toBe('claude-sonnet has two owners');
    const two: HubConflict = { ...conflict, models: ['a', 'b'] };
    expect(conflictDisplayInfo(two).title).toBe('a and b need a provider');
    const many: HubConflict = { ...conflict, models: ['a', 'b', 'c', 'd'] };
    expect(conflictDisplayInfo(many).title).toBe('4 models need a provider: a, b, and 2 more');
  });

  it('names who can serve the model', () => {
    expect(conflictDisplayInfo(conflict).subtitle).toBe('Available via Claude, OpenAI');
  });
});

describe('the published-model summary', () => {
  const model = { id: 'claude-sonnet', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false };

  it('counts what is published', () => {
    expect(publishedSummary([model], 1).summary).toBe('1 model published');
    expect(publishedSummary([model, { ...model, id: 'x' }], 1).summary).toBe('2 models published');
  });

  it('counts the models that still need an owner', () => {
    const summary = publishedSummary([model, { ...model, id: 'y', needsChoice: true }], 1);
    expect(summary.needsChoice).toBe(1);
  });

  it('distinguishes no sources from no models', () => {
    // Both read as zero published, and they are different problems: one needs
    // an account switched on, the other needs a model.
    expect(publishedSummary([], 0).summary).toBe('No accounts switched on yet');
    expect(publishedSummary([], 2).summary).toBe('No models published. Turn on an account below.');
  });
});

describe('the service footer', () => {
  it('leads with a conflict, because the hub cannot route until it is resolved', () => {
    const footer = hubServiceFooter({
      running: true,
      enabled: true,
      sourceCount: 2,
      publishedCount: 5,
      conflictCount: 1,
    });
    expect(footer).toContain('Resolve the routing conflicts');
  });

  it('says what to do on a fresh install', () => {
    expect(
      hubServiceFooter({ running: false, enabled: false, sourceCount: 0, publishedCount: 0, conflictCount: 0 }),
    ).toBe('Turn the hub on to publish models on one local address.');
  });

  it('asks for an account before a start when nothing is switched on', () => {
    expect(
      hubServiceFooter({ running: false, enabled: true, sourceCount: 0, publishedCount: 0, conflictCount: 0 }),
    ).toBe('Turn on an account below, then start the hub.');
  });

  it('reports the published count once serving', () => {
    expect(
      hubServiceFooter({ running: true, enabled: true, sourceCount: 2, publishedCount: 1, conflictCount: 0 }),
    ).toBe('1 model on one local address.');
  });
});

describe('the hub status subtitle', () => {
  const proxy: ProxyRow = {
    id: 'proxy-hub/default',
    providerId: 'proxy-hub',
    label: 'Proxy Hub',
    detail: 'running on port 5200',
    running: true,
    enabled: true,
    address: 'http://127.0.0.1:5200',
    toggleActionId: 'act-toggle',
    restartActionId: 'act-restart',
  };

  it('finds the hub proxy among the account proxies', () => {
    const rows = [
      { ...proxy, id: 'claude/work', providerId: 'claude' },
      proxy,
    ];
    expect(hubProxy(rows)?.providerId).toBe('proxy-hub');
  });

  it('says running with the address, enabled-but-down, and stopped', () => {
    expect(hubStatusSubtitle(proxy)).toBe('Running · http://127.0.0.1:5200');
    expect(hubStatusSubtitle({ ...proxy, running: false })).toBe('Enabled, not serving yet');
    expect(hubStatusSubtitle({ ...proxy, running: false, enabled: false })).toBe('Stopped');
  });

  it('does not invent a state when the engine published no hub', () => {
    expect(hubStatusSubtitle(undefined)).toBe('Not configured');
  });
});