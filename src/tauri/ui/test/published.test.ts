import { describe, expect, it } from 'vitest';

import type { HubCatalog, HubConflict, HubPublishedModel, HubSource } from '../lib/contract';
import {
  allRouteCandidates,
  hubProviderDisplayName,
  modelOwnerLabel,
  needsChoiceModels,
  parseModelIdentity,
  routableModels,
  seedExpanded,
  switchableModels,
  vendorGroups,
} from '../lib/published';

/**
 * Published-model identity, routing, and the pane that renders them.
 *
 * The action ids reconstructed here must match the strings
 * `registerModelRoutingActions` registers in `hub_routing.go:488`, or every
 * route picker invokes an id the registry does not have and silently does
 * nothing. That is the one assertion in this file that is really about the Go
 * side, and it is here because nothing else would catch the drift.
 */

const claudeSource: HubSource = {
  id: 'hub/default/claude/work',
  providerId: 'claude',
  name: 'work',
  label: 'Work',
  detail: 'alex@studio.example',
  enabled: true,
  hub: 'default',
};

const openAiSource: HubSource = {
  id: 'hub/default/openai/team',
  providerId: 'openai',
  name: 'team',
  label: 'Team',
  detail: 'team@studio.example',
  enabled: true,
  hub: 'default',
};

const disabledSource: HubSource = { ...claudeSource, id: 'hub/default/claude/off', enabled: false };

const ghostSource: HubSource = {
  ...claudeSource,
  id: 'hub/default/claude/gone',
  ghost: true,
  unavailableReason: 'gone',
};

describe('model identity', () => {
  it('classifies by prefix when the id carries no separator', () => {
    expect(parseModelIdentity('claude-sonnet-4', 'claude').vendorKey).toBe('claude');
    expect(parseModelIdentity('gpt-4o', 'openai').vendorKey).toBe('codex');
    expect(parseModelIdentity('o3-mini', 'openai').vendorKey).toBe('codex');
    expect(parseModelIdentity('gemini-2.5-pro', 'gemini').vendorKey).toBe('gemini');
    expect(parseModelIdentity('grok-4', 'grok').vendorKey).toBe('grok');
  });

  it('reads the vendor from a namespaced id', () => {
    // `~vendor/model` is the hub's own form, where the prefix is the account's
    // vendor rather than part of the model name.
    const identity = parseModelIdentity('~anthropic/claude-opus', 'claude');
    expect(identity.vendorKey).toBe('claude');
    expect(identity.cleanName).toBe('claude-opus');
  });

  it('strips a namespaced prefix rather than showing it', () => {
    expect(parseModelIdentity('~openai/gpt-4o', 'openai').cleanName).toBe('gpt-4o');
  });

  it('strips a metadata suffix off the clean name and keeps it as a tag', () => {
    const identity = parseModelIdentity('claude-sonnet-4-preview', 'claude');
    expect(identity.cleanName).toBe('claude-sonnet-4');
    expect(identity.tag).toBe('preview');
  });

  it('strips only one suffix', () => {
    expect(parseModelIdentity('mistral-large-latest', 'mistral').tag).toBe('latest');
  });

  it('names the vendor for a group heading', () => {
    expect(parseModelIdentity('gpt-4o', 'openai').vendorName).toBe('OpenAI');
    expect(parseModelIdentity('claude-opus', 'claude').vendorName).toBe('Anthropic Claude');
  });

  it('falls back to the owner provider when the id says nothing', () => {
    expect(parseModelIdentity('internal-preview', 'kiro').vendorKey).toBe('kiro');
  });

  it('groups aliases together so two spellings do not split a group', () => {
    // `openai/gpt-4o` and `gpt-4o` must land in one group, or the catalog
    // picker offers the same vendor twice under two names.
    expect(parseModelIdentity('openai/gpt-4o', 'openai').vendorKey).toBe('codex');
    expect(parseModelIdentity('gpt-4o', 'openai').vendorKey).toBe('codex');
  });
});

describe('provider display names', () => {
  it('uses the short label a picker needs, not the client name', () => {
    // "Claude Code" beside "OpenAI" in a provider picker reads as a list of
    // clients, which is the opposite of what it is.
    expect(hubProviderDisplayName('claude')).toBe('Claude');
    expect(hubProviderDisplayName('openai')).toBe('OpenAI');
  });

  it('maps the alias spellings onto the same label', () => {
    expect(hubProviderDisplayName('anthropic')).toBe('Claude');
    expect(hubProviderDisplayName('xai')).toBe('Grok');
  });
});

describe('route candidates', () => {
  const catalogs: HubCatalog[] = [
    { accountID: claudeSource.id, catalog: 'claude', models: ['claude-sonnet', 'shared-model'] },
    { accountID: openAiSource.id, catalog: 'codex', models: ['gpt-4o', 'shared-model'] },
    { accountID: disabledSource.id, catalog: 'claude', models: ['shared-model'] },
    { accountID: ghostSource.id, catalog: 'claude', models: ['shared-model'] },
  ];

  it('offers nothing for a model only one provider can serve', () => {
    // A picker with one option is a picker that cannot change anything.
    const candidates = allRouteCandidates(catalogs, [claudeSource, openAiSource]);
    expect(candidates.get('claude-sonnet')).toBeUndefined();
  });

  it('offers one candidate per provider for a shared model', () => {
    const candidates = allRouteCandidates(catalogs, [claudeSource, openAiSource]);
    expect(candidates.get('shared-model')?.map((c) => c.label)).toEqual(['Claude', 'OpenAI']);
  });

  it('reconstructs the action id the engine registered', () => {
    // The contract with `hub_routing.go:488`. If this string drifts, every
    // route change is accepted and does nothing.
    const candidates = allRouteCandidates(catalogs, [claudeSource, openAiSource]);
    expect(candidates.get('shared-model')?.map((c) => c.actionId)).toEqual([
      'model-route:shared-model:provider/claude',
      'model-route:shared-model:provider/openai',
    ]);
  });

  it('ignores a disabled source', () => {
    // A disabled source cannot serve, so offering it produces a pick that
    // fails on the first request.
    const candidates = allRouteCandidates(
      [{ accountID: openAiSource.id, catalog: 'codex', models: ['m'] }, { accountID: disabledSource.id, catalog: 'claude', models: ['m'] }],
      [claudeSource, openAiSource, disabledSource],
    );
    expect(candidates.get('m')).toBeUndefined();
  });

  it('ignores a ghost source', () => {
    const candidates = allRouteCandidates(
      [{ accountID: ghostSource.id, catalog: 'claude', models: ['m'] }, { accountID: openAiSource.id, catalog: 'codex', models: ['m'] }],
      [claudeSource, ghostSource, openAiSource],
    );
    expect(candidates.get('m')).toBeUndefined();
  });

  it('collapses two accounts of one provider into one candidate', () => {
    const second = { ...claudeSource, id: 'hub/default/claude/personal', name: 'personal' };
    const candidates = allRouteCandidates(
      [
        { accountID: claudeSource.id, catalog: 'claude', models: ['m'] },
        { accountID: second.id, catalog: 'claude', models: ['m'] },
        { accountID: openAiSource.id, catalog: 'codex', models: ['m'] },
      ],
      [claudeSource, second, openAiSource],
    );
    expect(candidates.get('m')).toHaveLength(2);
  });

  it('lists the models a user can reassign', () => {
    const models: HubPublishedModel[] = [
      { id: 'shared-model', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
      { id: 'claude-sonnet', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
    ];
    expect(switchableModels(models, catalogs, [claudeSource, openAiSource]).map((m) => m.id)).toEqual([
      'shared-model',
    ]);
  });
});

describe('the routable list', () => {
  const conflict: HubConflict = {
    id: 'overlap-1',
    kind: 'model-overlap',
    title: 'shared',
    models: ['shared'],
    candidates: [],
  };
  const models: HubPublishedModel[] = [
    { id: 'shared', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: true },
    { id: 'claude-sonnet', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
  ];

  it('takes a conflicted model out of the vendor list', () => {
    // Otherwise the same model appears under both states at once, and the user
    // can pick an owner in one place and not see it take effect in the other.
    expect(routableModels(models, [conflict]).map((m) => m.id)).toEqual(['claude-sonnet']);
  });

  it('keeps a conflicted model that already has an owner', () => {
    // The conflict list still names it, but `needsChoice` is false, so it is
    // resolved and belongs in the vendor list rather than under Needs a
    // Provider. Only the unresolved half moves.
    const resolved = [{ ...(models[0] as HubPublishedModel), needsChoice: false }];
    expect(routableModels(resolved, [conflict]).map((m) => m.id)).toEqual(['shared']);
  });

  it('lists the models that need an owner', () => {
    expect(needsChoiceModels(models, [conflict]).map((m) => m.id)).toEqual(['shared']);
  });

  it('says so plainly when no conflict names the model', () => {
    expect(needsChoiceModels(models, [])).toEqual([]);
  });
});

describe('vendor groups', () => {
  it('groups by vendor and orders by display name', () => {
    const models: HubPublishedModel[] = [
      { id: 'gpt-4o', ownerLabel: 'Team', ownerProviderId: 'openai', needsChoice: false },
      { id: 'claude-opus', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
    ];
    expect(vendorGroups(models).map((g) => g.vendorName)).toEqual(['Anthropic Claude', 'OpenAI']);
  });

  it('names the serving source when every model has the same one', () => {
    const models: HubPublishedModel[] = [
      { id: 'claude-opus', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
      { id: 'claude-sonnet', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
    ];
    expect(vendorGroups(models)[0]?.footer).toBe('Serving source: Work');
  });

  it('names the provider when several sources serve it', () => {
    const models: HubPublishedModel[] = [
      { id: 'claude-opus', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
      { id: 'claude-sonnet', ownerLabel: 'Personal', ownerProviderId: 'claude', needsChoice: false },
    ];
    expect(vendorGroups(models)[0]?.footer).toBe('Published via Claude');
  });

  it('says nothing when the group spans providers', () => {
    // A footer that guesses is worse than no footer.
    const models: HubPublishedModel[] = [
      { id: 'claude-opus', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
      { id: 'claude-sonnet', ownerLabel: 'Team', ownerProviderId: 'openai', needsChoice: false },
    ];
    expect(vendorGroups(models)[0]?.footer).toBe('');
  });

  it('sorts the models inside a group by id', () => {
    const models: HubPublishedModel[] = [
      { id: 'claude-sonnet', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
      { id: 'claude-opus', ownerLabel: 'Work', ownerProviderId: 'claude', needsChoice: false },
    ];
    expect(vendorGroups(models)[0]?.models.map((m) => m.id)).toEqual(['claude-opus', 'claude-sonnet']);
  });
});

describe('the first-load expansion seed', () => {
  it('expands a short list and collapses a long one', () => {
    // The rule macOS uses (`initializeExpansionIfNeeded`, ProxyFlows.swift), not
    // a preference. Two models behind a collapsed row is a list that says
    // nothing; a hundred rows expanded is a wall.
    expect(seedExpanded(2)).toBe(true);
    expect(seedExpanded(25)).toBe(true);
    expect(seedExpanded(26)).toBe(false);
  });
});
