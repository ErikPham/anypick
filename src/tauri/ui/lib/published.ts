/**
 * Published models: identity, routing candidates, and vendor grouping.
 *
 * Ported from `src/macos/ProxyFlows.swift` (`parseModelIdentity`,
 * `PublishedModelsView`) and `src/macos/HubRouting.swift`
 * (`allRouteCandidates`).
 *
 * The action ids are reconstructed here rather than published by the engine.
 * `registerModelRoutingActions` in `hub_routing.go:488` registers
 * `model-route:<model>:provider/<pid>` and `model-route:<model>:<source id>`
 * in its handler registry, but it does not put them on the wire. macOS
 * rebuilds the same strings from `hubCatalogs` and `hubSources`, which is what
 * this file does. The format is a contract with the engine: if either side's
 * string changes, the pickers invoke an id the registry does not have and every
 * route silently does nothing.
 */

import type { HubCatalog, HubConflict, HubPublishedModel, HubSource } from './contract';
import { isGhost } from './hub';
import { cleanSourceDisplayName, nativeProviderName } from './model';

export type PublishedIdentity = {
  vendorKey: string;
  vendorName: string;
  cleanName: string;
  /** `-preview`, `-reasoning` and friends, stripped off the clean name. */
  tag: string | null;
};

/**
 * The vendor a model belongs to, from its id and failing that its owner.
 *
 * The prefix table is ordered, and the order is load-bearing: `gpt-4` must
 * classify as OpenAI before any generic match, and `claude-…-gpt-…` must not
 * classify by its tail. Both are why this is a chain of `includes` checks
 * rather than a map lookup.
 */
const VENDOR_ALIASES: ReadonlyArray<[RegExp, string]> = [
  [/claude|anthropic/u, 'claude'],
  [/openai|codex|gpt/u, 'codex'],
  [/gemini|google/u, 'gemini'],
  [/deepseek/u, 'deepseek'],
  [/grok|x-ai|xai/u, 'grok'],
  [/kimi|moonshot/u, 'kimi'],
  [/zai|glm|zhipu/u, 'zai'],
  [/qwen|alibaba/u, 'qwen'],
  [/mistral/u, 'mistral'],
  [/cline/u, 'cline'],
];

const VENDOR_NAMES: Record<string, string> = {
  claude: 'Anthropic Claude',
  codex: 'OpenAI',
  gemini: 'Google Gemini',
  deepseek: 'DeepSeek',
  grok: 'xAI Grok',
  kimi: 'Moonshot Kimi',
  zai: 'Zhipu AI (GLM)',
  qwen: 'Qwen',
  mistral: 'Mistral',
  cline: 'Cline',
};

/** Prefixes that identify a vendor without a separator. Checked in order. */
const VENDOR_PREFIXES: ReadonlyArray<[string, string]> = [
  ['claude', 'claude'],
  ['gpt', 'codex'],
  ['o1', 'codex'],
  ['o3', 'codex'],
  ['text-embedding', 'codex'],
  ['gemini', 'gemini'],
  ['deepseek', 'deepseek'],
  ['grok', 'grok'],
  ['kimi', 'kimi'],
  ['glm', 'zai'],
  ['qwen', 'qwen'],
  ['mistral', 'mistral'],
];

/** Suffix tags that are metadata, not part of the model's name. */
const MODEL_TAGS = ['-latest', '-preview', '-reasoning', '-instruct', '-turbo'];

export function parseModelIdentity(modelId: string, fallbackProvider: string): PublishedIdentity {
  const mid = modelId.trim();
  let vendorKey = '';
  let clean = mid;

  if (mid.startsWith('~') && mid.includes('/')) {
    // `~vendor/model`: the hub's namespaced form, where the prefix is the
    // account's own vendor. Only the first separator splits.
    const rest = mid.slice(1);
    const slash = rest.indexOf('/');
    if (slash > 0) {
      const prefix = rest.slice(0, slash).toLowerCase();
      clean = rest.slice(slash + 1);
      vendorKey = matchVendor(prefix) ?? prefix;
    }
  } else {
    const lower = mid.toLowerCase();
    const prefixed = VENDOR_PREFIXES.find(([token]) => lower.startsWith(token));
    vendorKey = prefixed ? prefixed[1] : matchVendor(lower) ?? fallbackProvider.toLowerCase();
  }

  // Normalize a matched vendor back to its canonical key, so `openai` and
  // `gpt-4o` both land on `codex` and group together.
  vendorKey = matchVendor(vendorKey) ?? vendorKey;

  let tag: string | null = null;
  for (const suffix of MODEL_TAGS) {
    if (clean.endsWith(suffix)) {
      tag = suffix.slice(1);
      clean = clean.slice(0, -suffix.length);
      break;
    }
  }

  return {
    vendorKey,
    vendorName: VENDOR_NAMES[vendorKey] ?? hubProviderDisplayName(vendorKey),
    cleanName: clean,
    tag,
  };
}

function matchVendor(value: string): string | null {
  const lower = value.toLowerCase();
  return VENDOR_ALIASES.find(([pattern]) => pattern.test(lower))?.[1] ?? null;
}

/**
 * `hubProviderDisplayName`. The short label a picker uses, so it is not
 * `anypickDisplayName`: a picker offering "Claude Code" beside "OpenAI" reads
 * as a client list, not a provider list.
 */
export function hubProviderDisplayName(providerId: string): string {
  const key = providerId.toLowerCase();
  const short: Record<string, string> = {
    claude: 'Claude',
    anthropic: 'Claude',
    codex: 'Codex',
    openai: 'OpenAI',
    gemini: 'Gemini',
    google: 'Gemini',
    grok: 'Grok',
    xai: 'Grok',
    kiro: 'Kiro',
    workbuddy: 'WorkBuddy',
    cline: 'Cline',
    cloudflare: 'Cloudflare',
  };
  if (short[key]) return short[key];
  const display = nativeProviderName(key);
  return display || '';
}

export type RouteCandidate = {
  id: string;
  label: string;
  detail: string;
  providerId: string;
  actionId: string;
  /** True when choosing pins the whole group to one provider. */
  providerChoice: boolean;
};

/**
 * `allRouteCandidates`. Which accounts can serve one model, and the action id
 * that pins it to each.
 *
 * Only enabled, non-ghost sources count: a ghost is a source the engine cannot
 * honour, so offering it would produce a pick that fails on the first request.
 */
export function allRouteCandidates(
  catalogs: readonly HubCatalog[],
  sources: readonly HubSource[],
): Map<string, RouteCandidate[]> {
  const byId = new Map(sources.map((source) => [source.id, source]));
  const publishing = new Set(sources.filter((source) => source.enabled && !isGhost(source)).map((s) => s.id));

  // model -> provider -> the sources of that provider that carry the model
  const modelProviders = new Map<string, Map<string, Set<string>>>();
  for (const catalog of catalogs) {
    if (!publishing.has(catalog.accountID)) continue;
    const source = byId.get(catalog.accountID);
    if (!source) continue;
    const pid = (source.providerId || source.name).toLowerCase();
    if (!pid) continue;
    for (const model of catalog.models) {
      let byProvider = modelProviders.get(model);
      if (!byProvider) {
        byProvider = new Map();
        modelProviders.set(model, byProvider);
      }
      let accounts = byProvider.get(pid);
      if (!accounts) {
        accounts = new Set();
        byProvider.set(pid, accounts);
      }
      accounts.add(catalog.accountID);
    }
  }

  const result = new Map<string, RouteCandidate[]>();
  for (const [model, byProvider] of modelProviders) {
    // One provider means there is no decision to make. macOS only offers a
    // picker when the count is above one, and so does this.
    if (byProvider.size <= 1) continue;
    result.set(
      model,
      [...byProvider.keys()].sort().map((pid) => ({
        id: `provider/${pid}`,
        label: hubProviderDisplayName(pid),
        detail: `${byProvider.get(pid)?.size ?? 0} accounts`,
        providerId: pid,
        actionId: `model-route:${model}:provider/${pid}`,
        providerChoice: true,
      })),
    );
  }
  return result;
}

export function routeCandidates(
  modelId: string,
  catalogs: readonly HubCatalog[],
  sources: readonly HubSource[],
): RouteCandidate[] {
  return allRouteCandidates(catalogs, sources).get(modelId) ?? [];
}

/** A vendor group with the models under it and the footer macOS shows. */
export type VendorGroup = {
  vendorKey: string;
  vendorName: string;
  models: HubPublishedModel[];
  /** `Serving source: X` or `Published via Y`, or empty when neither applies. */
  footer: string;
};

export function vendorGroups(models: readonly HubPublishedModel[]): VendorGroup[] {
  const grouped = new Map<string, HubPublishedModel[]>();
  for (const model of models) {
    const { vendorKey } = parseModelIdentity(model.id, model.ownerProviderId);
    const bucket = grouped.get(vendorKey);
    if (bucket) bucket.push(model);
    else grouped.set(vendorKey, [model]);
  }

  const groups = [...grouped.entries()].map(([vendorKey, items]): VendorGroup => {
    const sample = items[0] as HubPublishedModel;
    const identity = parseModelIdentity(sample.id, sample.ownerProviderId);
    return {
      vendorKey,
      vendorName: identity.vendorName,
      models: [...items].sort((a, b) => a.id.localeCompare(b.id)),
      footer: groupFooter(items),
    };
  });

  return groups.sort((a, b) => a.vendorName.localeCompare(b.vendorName));
}

/**
 * The group's footer. One serving source names it exactly; several sources
 * under one provider name the provider instead; anything else says nothing,
 * because a footer that guesses is worse than no footer.
 */
function groupFooter(models: readonly HubPublishedModel[]): string {
  const sources = new Set(models.map((model) => model.ownerLabel).filter(Boolean));
  if (sources.size === 1) return `Serving source: ${sources.values().next().value as string}`;
  const providers = new Set(
    models.map((model) => hubProviderDisplayName(model.ownerProviderId)).filter(Boolean),
  );
  if (providers.size === 1) return `Published via ${providers.values().next().value as string}`;
  return '';
}

/** The vendors a filter offers, with counts, ordered by display name. */
export function availableVendors(models: readonly HubPublishedModel[]): VendorGroup[] {
  return vendorGroups(models).map((group) => ({
    ...group,
    models: [],
  }));
}

/**
 * The models that still need an owner. These are excluded from the main list
 * when the conflict group is also shown, so a model is never presented twice
 * under two different states.
 */
export function needsChoiceModels(
  models: readonly HubPublishedModel[],
  conflicts: readonly HubConflict[],
): HubPublishedModel[] {
  const conflicted = new Set(conflicts.flatMap((conflict) => conflict.models ?? []));
  return models.filter((model) => model.needsChoice && conflicted.has(model.id));
}

/**
 * The routable list: everything published, minus the models whose conflict is
 * shown separately. macOS applies the same filter, and it is what stops a
 * model appearing under both "needs a provider" and a vendor group.
 */
export function routableModels(
  models: readonly HubPublishedModel[],
  conflicts: readonly HubConflict[],
): HubPublishedModel[] {
  const conflicted = new Set(conflicts.flatMap((conflict) => conflict.models ?? []));
  return models.filter((model) => !conflicted.has(model.id) || !model.needsChoice);
}

/** The models a user can reassign: more than one provider can serve them. */
export function switchableModels(
  models: readonly HubPublishedModel[],
  catalogs: readonly HubCatalog[],
  sources: readonly HubSource[],
): HubPublishedModel[] {
  const candidates = allRouteCandidates(catalogs, sources);
  return models.filter((model) => (candidates.get(model.id)?.length ?? 0) > 1);
}

/** The owner a model row names, preferring the source's own label. */
export function modelOwnerLabel(model: HubPublishedModel): string {
  if (!model.ownerLabel) return 'No owner';
  return cleanSourceDisplayName(model.ownerLabel);
}

/**
 * `initializeExpansionIfNeeded`. Whether the vendor groups start open.
 *
 * macOS expands all of them when the list is short enough to read and none when
 * it is not. Two models behind a collapsed "2 models" row is a list that says
 * nothing about what it holds, and a hundred rows already expanded is a wall.
 */
export const AUTO_EXPAND_LIMIT = 25;

export function seedExpanded(modelCount: number): boolean {
  return modelCount <= AUTO_EXPAND_LIMIT;
}