/**
 * The desktop UI's data contract, generated from the Go engine.
 *
 * Source of truth is `go/internal/desktop/snapshot.go`. Nothing here is
 * invented and nothing is a fixture: if a field is listed, the engine sends it.
 * The engine speaks this shape over the stdio desktop protocol, the Rust layer
 * forwards it, and this file is the TypeScript mirror of it.
 *
 * Do not add fields "for the UI's convenience" — change the Go struct, or map
 * at the edge. A contract that drifts from the engine is worse than none.
 */

/** Loose engine objects (routes, hub sources, activity, …) stay open maps. */
export type EngineMap = Record<string, unknown>;

export type DegradedCondition = {
  id: string;
  kind: string;
  title: string;
  detail: string;
};

/** One selectable client → source route. */
export type ActionRow = {
  id: string;
  clientId: string;
  sourceId: string;
  client: string;
  label: string;
  detail?: string;
  /** 'native' | 'gateway' */
  kind: string;
  presentation: string;
  selected: boolean;
  enabled: boolean;
  /** Extra engine-supplied fields (badges, warnings, ordering). */
  extras?: EngineMap;
};

export type AccountRow = {
  id: string;
  providerId: string;
  sourceId?: string;
  name: string;
  label: string;
  detail: string;
  active: boolean;
  canRefresh: boolean;
  switchActionId: string;
};

export type ProxyRow = {
  id: string;
  providerId: string;
  label: string;
  detail: string;
  address?: string;
  running: boolean;
  enabled: boolean;
  logsAvailable?: boolean;
  toggleActionId: string;
  restartActionId: string;
};

export type GatewayRow = {
  id: string;
  providerId?: string;
  label?: string;
  detail?: string;
} & EngineMap;

export type UsageSnapshotRow = {
  client: string;
  account: string;
  windows: EngineMap[];
};

export type LogSourceRow = {
  id: string;
  providerId: string;
  name: string;
  label: string;
  detail: string;
};

export type LoginMethodRow = {
  id: string;
  label: string;
  detail?: string;
};

export type AccountProviderRow = {
  id: string;
  providerId: string;
  clientId?: string;
  sourceId?: string;
  label: string;
  detail: string;
  installed: boolean;
  livePresent: boolean;
  liveIdentity?: string;
  loginMethods: LoginMethodRow[];
  canClear?: boolean;
};

export type GatewayProviderRow = {
  id: string;
  label: string;
  detail: string;
  providesEndpoint?: boolean;
  firstPartyClientIds?: string[];
  kind?: string;
};

export type Preferences = {
  launchAtLogin: boolean;
  startEnabledProxies: boolean;
  showQuota: boolean;
  quotaGuardEnabled: boolean;
};

/** The full snapshot the engine pushes after a handshake and on every change. */
export type Snapshot = {
  proxyCount: number;
  revision: number;
  routes: EngineMap[];
  actions: ActionRow[];
  clientModelConfigs: EngineMap[];
  usage: UsageSnapshotRow[];
  proxies: ProxyRow[];
  accounts: AccountRow[];
  hubSources: EngineMap[];
  hubConflicts: EngineMap[];
  logSources: LogSourceRow[];
  gateways: GatewayRow[];
  accountProviders: AccountProviderRow[];
  gatewayProviders: GatewayProviderRow[];
  settings: Preferences;
  activity: EngineMap[];
  degradedConditions: DegradedCondition[];
};

/** Destinations mirror the macOS app's sidebar (`macos/DesignTypes.swift`). */
export const DESTINATIONS = [
  'overview',
  'apps',
  'accounts',
  'proxyHub',
  'proxies',
  'activity',
  'backups',
] as const;

export type Destination = (typeof DESTINATIONS)[number];

export function isDestination(value: string): value is Destination {
  return (DESTINATIONS as readonly string[]).includes(value);
}
