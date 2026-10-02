/**
 * The hub's display rules, ported from `src/macos/ProxyHubView.swift` and
 * `DesignDisplay.swift`.
 *
 * They live here rather than in the pane so a routing decision can be tested
 * without a webview. The macOS app's copy is the product's copy, so these are
 * the words a Linux user reads.
 */

import type { HubCatalog, HubConflict, HubPublishedModel, HubSource, ProxyRow } from './contract';
import { cleanModelDisplayName, cleanSourceDisplayName, nativeProviderName } from './model';

/** The hub proxy row, or undefined when the engine published none. */
export function hubProxy(proxies: readonly ProxyRow[]): ProxyRow | undefined {
  return proxies.find((proxy) => proxy.providerId === 'proxy-hub');
}

/**
 * The published-model summary the hub's Service row shows.
 *
 * macOS splits this into a count and a count of models that still need an
 * owner, and says so only when the second number is non-zero. A hub with
 * nothing published is not zero models: it is a hub with no sources switched
 * on, and the empty state has to say which.
 */
export function publishedSummary(
  published: readonly HubPublishedModel[],
  sourceCount: number,
): { summary: string; needsChoice: number } {
  const needsChoice = published.filter((model) => model.needsChoice).length;
  if (published.length === 0) {
    return {
      summary:
        sourceCount === 0
          ? 'No accounts switched on yet'
          : 'No models published. Turn on an account below.',
      needsChoice,
    };
  }
  return {
    summary: `${published.length} model${published.length === 1 ? '' : 's'} published`,
    needsChoice,
  };
}

/**
 * `hubConflictDisplayInfo`. The title names how many models collided, the
 * subtitle names who can serve them.
 */
export function conflictDisplayInfo(
  conflict: HubConflict,
): { title: string; subtitle: string } {
  const models = conflict.models ?? [];
  let title: string;
  if (models.length === 0) {
    title = conflict.title;
  } else if (models.length === 1) {
    title = `${cleanModelDisplayName(models[0])} has two owners`;
  } else if (models.length === 2) {
    title = `${cleanModelDisplayName(models[0])} and ${cleanModelDisplayName(models[1])} need a provider`;
  } else {
    title = `${models.length} models need a provider: ${cleanModelDisplayName(models[0])}, ${cleanModelDisplayName(models[1])}, and ${models.length - 2} more`;
  }
  return { title, subtitle: `Available via ${conflict.candidates.map((c) => candidateLabel(c.providerId, c.label)).join(', ')}` };
}

/**
 * `uniqueProviderCandidates`. Two saved accounts of one provider are one
 * routing decision, not two, so a picker offering both would let the user
 * choose something that changes nothing.
 */
export function uniqueProviderCandidates(conflict: HubConflict): HubConflict['candidates'] {
  const seen = new Set<string>();
  const out: HubConflict['candidates'] = [];
  for (const candidate of conflict.candidates) {
    const key = candidate.providerChoice ? candidate.providerId : `${candidate.providerId}/${candidate.detail}`;
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(candidate);
  }
  return out;
}

function candidateLabel(providerId: string, label: string): string {
  return label || nativeProviderName(providerId);
}

/**
 * `hubSourceRows`. Rotation first because it governs the whole hub, then the
 * sources in provider-then-name order. Ghosts sort with their provider: a
 * ghost is a source the user chose and the engine cannot honour, so hiding it
 * at the bottom would bury the one row that needs attention.
 */
/**
 * The rows that carry a hub-wide meaning, kept apart so a caller cannot pass
 * an account row where a rotation row is required. The two are told apart by
 * `rotates`, and the whole distinction is that rotation carries no account
 * value: it is rendered and toggled through `hub-rotation`, not
 * `hub-source-toggle`.
 */
export type HubRotationRow = HubSource & { rotates: true };

export function hubSourceRows(sources: readonly HubSource[]): {
  rotation: HubRotationRow[];
  accounts: HubSource[];
} {
  const rotation: HubRotationRow[] = [];
  const accounts: HubSource[] = [];
  for (const source of sources) {
    if (isRotation(source)) rotation.push(source);
    else accounts.push(source);
  }
  rotation.sort(byHubThenProvider);
  accounts.sort(byHubThenProvider);
  return { rotation, accounts };
}

function byHubThenProvider(a: HubSource, b: HubSource): number {
  return a.hub.localeCompare(b.hub) || a.providerId.localeCompare(b.providerId) || a.name.localeCompare(b.name);
}

export function isRotation(source: HubSource): source is HubRotationRow {
  return 'rotates' in source && source.rotates === true;
}

export function isGhost(source: HubSource): source is HubSource & { ghost: true; unavailableReason: string } {
  return 'ghost' in source && source.ghost === true;
}

export function isPool(source: HubSource): source is HubSource & { poolSource: true } {
  return 'poolSource' in source && source.poolSource === true;
}

/**
 * The subtitle a hub source carries. macOS shows `identity · N models`, or
 * says the account is excluded when it is switched off.
 */
export function hubSourceDetail(
  source: HubSource,
  catalogs: readonly HubCatalog[],
): string {
  if (isGhost(source)) return 'Excluded from Hub';
  if (isPool(source)) return source.detail;
  if (!source.enabled) return 'Excluded from Hub';
  const identity = cleanSourceDisplayName(source.name);
  const count = catalogModelCount(catalogs, source.id);
  return count > 0 ? `${identity} · ${count} model${count === 1 ? '' : 's'}` : identity;
}

/** How many models one source can publish. A source with no catalog row has none. */
export function catalogModelCount(catalogs: readonly HubCatalog[], sourceId: string): number {
  return catalogs.find((catalog) => catalog.accountID === sourceId)?.models.length ?? 0;
}

/**
 * `serviceFooter`. What the hub is doing, said once under the Service group.
 * Every branch is a state a user can be in; the empty one is the most common
 * on a fresh install and used to be blank.
 */
export function hubServiceFooter(input: {
  running: boolean;
  enabled: boolean;
  sourceCount: number;
  publishedCount: number;
  conflictCount: number;
}): string {
  if (input.conflictCount > 0) {
    return 'Resolve the routing conflicts below before this hub serves every model.';
  }
  if (!input.enabled) {
    return 'Turn the hub on to publish models on one local address.';
  }
  if (!input.running) {
    return input.sourceCount === 0
      ? 'Turn on an account below, then start the hub.'
      : 'The hub is enabled but not serving yet.';
  }
  if (input.publishedCount === 0) {
    return 'The hub is serving, but no source can serve a model yet.';
  }
  return `${input.publishedCount} model${input.publishedCount === 1 ? '' : 's'} on one local address.`;
}

/** The hub's own lifecycle subtitle. Lowercase, because it follows the row title. */
export function hubStatusSubtitle(proxy: ProxyRow | undefined): string {
  if (!proxy) return 'Not configured';
  if (proxy.running) return proxy.address ? `Running · ${proxy.address}` : 'Running';
  if (proxy.enabled) return 'Enabled, not serving yet';
  return 'Stopped';
}

/**
 * Published models grouped by the catalog that owns them, so the picker has a
 * filter that means something. macOS groups by vendor; the engine publishes
 * `catalog` per source, so the vendor is the catalog's first segment.
 */
export function publishedGroups(
  published: readonly HubPublishedModel[],
  catalogs: readonly HubCatalog[],
): Array<{ vendor: string; models: HubPublishedModel[] }> {
  const vendorOf = new Map<string, string>();
  for (const catalog of catalogs) {
    const vendor = catalog.catalog.split(/[/.:]/u)[0] || catalog.catalog;
    for (const model of catalog.models) vendorOf.set(model, vendor);
  }
  const groups = new Map<string, HubPublishedModel[]>();
  for (const model of published) {
    const vendor = vendorOf.get(model.id) ?? model.ownerProviderId ?? 'other';
    const bucket = groups.get(vendor);
    if (bucket) bucket.push(model);
    else groups.set(vendor, [model]);
  }
  return [...groups.entries()]
    .map(([vendor, models]) => ({ vendor, models: [...models].sort((a, b) => a.id.localeCompare(b.id)) }))
    .sort((a, b) => a.vendor.localeCompare(b.vendor));
}

/** Which published models still need a provider chosen. The hub cannot route those. */
export function needsChoiceModels(published: readonly HubPublishedModel[]): HubPublishedModel[] {
  return published.filter((model) => model.needsChoice);
}

/** The routes a client can be pointed at from one published model. */
export function publishedCandidates(
  model: HubPublishedModel,
  conflicts: readonly HubConflict[],
): HubConflict['candidates'] {
  return uniqueProviderCandidates({
    id: model.id,
    kind: 'source-choice',
    title: model.id,
    models: [model.id],
    candidates:
      conflicts.find((conflict) => (conflict.models ?? []).includes(model.id))?.candidates ?? [],
  });
}
