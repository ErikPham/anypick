import {
  OPENCODE_GO_UPSTREAM,
  OPENCODE_ZEN_UPSTREAM,
  type OpenCodeCatalog as OpenCodeCatalogKind,
  type OpenCodeCredential,
} from './auth';
import {
  parseOpenCodeModelDescriptors,
  parseOpenCodeProviderMetadata,
  type OpenCodeModelDescriptor,
} from './models';
import {
  CATALOG_STALE_MAX_MS,
  CATALOG_TTL_MS,
  DEFAULT_MODEL_METADATA_URL,
  ENTITLEMENT_TTL_MS,
  MODELS_FETCH_MS,
  USER_AGENT,
} from './constants';
import type { EgressTransport } from '../../network/egress/types';
import type { CatalogCache, CatalogModel, OpenCodeCatalogStore } from './types';

export interface OpenCodeCatalogOptions {
  egress: EgressTransport;
  authMode: string;
  forcedUpstream?: string;
  zenUpstream?: string;
  goUpstream?: string;
  metadataUrl?: string | false;
  credential: () => Promise<OpenCodeCredential>;
  log: (line: string) => void;
}

export class CatalogStore implements OpenCodeCatalogStore {
  private cache?: CatalogCache;
  private inflight?: Promise<CatalogCache>;
  /** Definitive Go-entitlement probe result for the current key, if any. */
  private entitlement?: { hasGo: boolean; checkedAt: number };
  private readonly forcedUpstream?: string;
  private readonly zenBase: string;
  private readonly goBase: string;
  private readonly metadataUrl?: string;

  constructor(private readonly opts: OpenCodeCatalogOptions) {
    this.forcedUpstream = strip(opts.forcedUpstream);
    this.zenBase = strip(opts.zenUpstream ?? OPENCODE_ZEN_UPSTREAM)!;
    this.goBase = strip(opts.goUpstream ?? OPENCODE_GO_UPSTREAM)!;
    this.metadataUrl =
      opts.metadataUrl === false
        ? undefined
        : strip(
            opts.metadataUrl ??
              (opts.forcedUpstream || opts.zenUpstream || opts.goUpstream
                ? undefined
                : DEFAULT_MODEL_METADATA_URL),
          );
  }

  baseFor(catalog: OpenCodeCatalogKind): string {
    if (this.forcedUpstream) {
      return this.forcedUpstream;
    }
    return catalog === 'go' ? this.goBase : this.zenBase;
  }

  async route(modelId: string): Promise<{ catalog: OpenCodeCatalogKind; base: string }> {
    const cache = await this.live();
    const scopedDefault = this.entitlement?.hasGo ? 'go' : 'zen';
    const catalog = cache.byModel.get(modelId)?.catalog ?? scopedDefault;
    return { catalog, base: this.baseFor(catalog) };
  }

  /**
   * Drop any cached list so the next `live()` (or an explicit reload) refetches
   * the upstream catalog. Used when a caller asks for a fresh model list instead
   * of one cached up to CATALOG_TTL_MS ago — new models users are entitled to
   * (e.g. a free tier added upstream) otherwise stay hidden until the TTL lapses.
   */
  invalidate(): void {
    this.cache = undefined;
    this.inflight = undefined;
    this.entitlement = undefined;
  }

  async live(): Promise<CatalogCache> {
    const now = Date.now();
    if (this.cache) {
      const age = now - this.cache.fetchedAt;
      if (age < CATALOG_TTL_MS) {
        return this.cache;
      }
      if (age < CATALOG_STALE_MAX_MS) {
        void this.refresh().catch((err) =>
          this.opts.log(
            `catalog refresh failed: ${err instanceof Error ? err.message : String(err)}`,
          ),
        );
        return this.cache;
      }
    }
    return this.refresh();
  }

  private refresh(): Promise<CatalogCache> {
    if (this.inflight) {
      return this.inflight;
    }
    this.inflight = (async () => {
      const cred = await this.opts.credential();
      const [hasGo, metadata] = await Promise.all([
        this.resolveGoEntitlement(cred),
        this.fetchMetadata(),
      ]);
      const lists = await Promise.all(
        this.catalogs(hasGo, cred).map(async (catalog) => ({
          catalog,
          list: await this.fetchModels(catalog, cred.apiKey),
        })),
      );
      const byModel = new Map<string, CatalogModel>();
      const ids: string[] = [];
      const metadataByModel = new Map(
        metadata.map((model) => [`${model.source ?? 'zen'}:${model.id.toLowerCase()}`, model]),
      );
      for (const { catalog, list } of lists) {
        for (const live of list) {
          if (byModel.has(live.id)) {
            continue;
          }
          const extra = metadataByModel.get(`${catalog}:${live.id.toLowerCase()}`);
          byModel.set(live.id, {
            ...extra,
            ...live,
            id: live.id,
            protocol: live.protocol ?? extra?.protocol,
            providerPackage: live.providerPackage ?? extra?.providerPackage,
            catalog,
          });
          ids.push(live.id);
        }
      }
      const next = { byModel, ids, fetchedAt: Date.now() };
      this.cache = next;
      this.opts.log(`models list ← ${ids.length} (${this.catalogs(hasGo, cred).join('+')})`);
      return next;
    })().finally(() => {
      this.inflight = undefined;
    });
    return this.inflight;
  }

  /**
   * Which catalogs this key may serve. The Go usage endpoint doubles as an
   * entitlement probe: 200 means the key holds a Go subscription, 403 means it
   * does not. Only a definitive answer narrows the scope; anything else keeps
   * the legacy both-catalog behavior.
   */
  private catalogs(hasGo: boolean | undefined, cred: OpenCodeCredential): OpenCodeCatalogKind[] {
    // A forced upstream merges everything into one base anyway; public mode has
    // no key to entitle. Unknown entitlement must keep serving both lists
    // rather than hide models.
    if (this.forcedUpstream || cred.mode === 'public') {
      return ['zen'];
    }
    if (hasGo === undefined) {
      return ['zen', 'go'];
    }
    return hasGo ? ['go'] : ['zen'];
  }

  private async resolveGoEntitlement(cred: OpenCodeCredential): Promise<boolean | undefined> {
    if (cred.mode === 'public' || this.forcedUpstream) {
      return undefined;
    }
    if (this.entitlement && Date.now() - this.entitlement.checkedAt < ENTITLEMENT_TTL_MS) {
      return this.entitlement.hasGo;
    }
    let hasGo: boolean | undefined;
    try {
      const response = await this.opts.egress.fetch(
        `${this.goBase}/usage`,
        {
          headers: {
            accept: 'application/json',
            authorization: `Bearer ${cred.apiKey}`,
            'user-agent': USER_AGENT,
          },
          signal: AbortSignal.timeout(MODELS_FETCH_MS),
        },
        { operation: 'catalog' },
      );
      if (response.ok) {
        hasGo = true;
      } else if (response.status === 403) {
        // EntitlementError — the key definitively lacks a Go subscription.
        hasGo = false;
      }
    } catch {
      // Network trouble: leave unknown so both catalogs stay available.
    }
    if (hasGo !== undefined) {
      this.entitlement = { hasGo, checkedAt: Date.now() };
    }
    return hasGo;
  }

  private async fetchModels(
    catalog: OpenCodeCatalogKind,
    apiKey: string,
  ): Promise<OpenCodeModelDescriptor[]> {
    const base = this.baseFor(catalog);
    for (let attempt = 1; attempt <= 2; attempt++) {
      try {
        const response = await this.opts.egress.fetch(
          `${base}/models`,
          {
            headers: {
              accept: 'application/json',
              authorization: `Bearer ${apiKey}`,
              'x-opencode-client': 'desktop',
              'user-agent': USER_AGENT,
            },
            signal: AbortSignal.timeout(MODELS_FETCH_MS),
          },
          { operation: 'catalog' },
        );
        if (response.ok) {
          return parseOpenCodeModelDescriptors(await response.json());
        }
        this.opts.log(
          `models ${catalog} ← ${response.status}${attempt === 1 ? ' (retrying)' : ''}`,
        );
        if (![502, 503, 504].includes(response.status) || attempt === 2) {
          return [];
        }
      } catch (err) {
        this.opts.log(`models ${catalog} ✗ ${err instanceof Error ? err.message : String(err)}`);
        if (attempt === 2) {
          return [];
        }
        await new Promise((resolve) => setTimeout(resolve, 100));
      }
    }
    return [];
  }

  private async fetchMetadata(): Promise<OpenCodeModelDescriptor[]> {
    if (!this.metadataUrl) {
      return [];
    }
    try {
      const response = await this.opts.egress.fetch(
        this.metadataUrl,
        {
          headers: { accept: 'application/json', 'user-agent': USER_AGENT },
          signal: AbortSignal.timeout(MODELS_FETCH_MS),
        },
        { operation: 'catalog' },
      );
      if (!response.ok) {
        this.opts.log(`model metadata ← ${response.status} (using live ids without enrichment)`);
        return [];
      }
      return parseOpenCodeProviderMetadata(await response.json());
    } catch (err) {
      this.opts.log(
        `model metadata ✗ ${err instanceof Error ? err.message : String(err)} (using live ids without enrichment)`,
      );
      return [];
    }
  }
}

function strip(value: string | undefined): string | undefined {
  return value?.replace(/\/$/, '');
}
