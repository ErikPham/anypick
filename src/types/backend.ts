import type { CredentialClass } from './account';
import type { Protocol, ResourceRef } from './bindings';

/** Versioned, secret-free contract consumed by desktop UI shells. */
export const BACKEND_SCHEMA_VERSION = 1 as const;

export type BackendExecutionPath =
  | 'native_direct'
  | 'provider_direct'
  | 'local_passthrough'
  | 'local_translate'
  | 'proxy_hub'
  | 'unavailable';

export type BackendOperationState =
  | 'planning'
  | 'awaiting_user'
  | 'executing'
  | 'verifying'
  | 'rolling_back'
  | 'completed'
  | 'failed';

export interface BackendClientSnapshot {
  id: string;
  name: string;
  protocols: Protocol[];
  supportsPersistentConfig: boolean;
  supportsIsolatedHome: boolean;
}

export interface BackendProviderSnapshot {
  id: string;
  name: string;
  supportsProxy: boolean;
  supportsPool: boolean;
  supportsBrowserLogin: boolean;
  supportsCredentialInput: boolean;
}

export interface BackendAccountSnapshot {
  providerId: string;
  name: string;
  label?: string;
  identity?: string;
  credentialClass: CredentialClass;
  active: boolean;
  live: boolean;
  proxy: { enabled: boolean; running: boolean };
  /** Cached, best-effort quota for this provider's currently live account. */
  usage?: { windows: Array<{ label: string; remainingPercent: number; resetsAtMs?: number }> };
}

export interface BackendGatewaySnapshot {
  name: string;
  providerId: string;
  label?: string;
  endpoint?: string;
  defaultModel?: string;
}

/** Complete, secret-free pool configuration returned to desktop clients. */
export interface BackendPoolDetails {
  providerId: string;
  mode: 'single' | 'multi';
  enabled: boolean;
  host?: string;
  port?: number;
  strategy: 'failover' | 'round-robin';
  members: Array<{ account: string; enabled: boolean }>;
  updatedAt: string;
}

/** Complete, secret-free Hub configuration plus its public runtime status. */
export interface BackendHubDetails {
  name: string;
  enabled: boolean;
  host: string;
  port: number;
  sources: Array<{
    ref:
      | { kind: 'account'; provider: string; name: string }
      | { kind: 'account-pool'; provider: string };
    enabled: boolean;
  }>;
  modelOwners: Array<{
    model: string;
    source:
      | { kind: 'account'; provider: string; name: string }
      | { kind: 'account-pool'; provider: string };
  }>;
  revision: number;
  createdAt: string;
  updatedAt: string;
  running: boolean;
  endpoint?: string;
  sourceCount: number;
  modelCount: number;
  conflictCount: number;
  detail?: string;
}

export interface BackendBindingSnapshot {
  id: string;
  scope: 'global' | 'project';
  projectRoot?: string;
  clientId: string;
  source: ResourceRef;
  transportPolicy: 'auto' | 'direct' | 'proxy';
  executionPath: BackendExecutionPath;
  clientSurface: 'cli' | 'desktop';
  requiresRestart: boolean;
  canResume: boolean;
  /** True only when the backend can prove an owner process is running. */
  ownerRunning: boolean;
  availableActions: string[];
  availability: 'ready' | 'unavailable';
  error?: BackendError;
}

export interface BackendRunningOperation {
  operationId: string;
  type: string;
  state: BackendOperationState;
  startedAt: string;
  updatedAt: string;
}

export interface BackendNotice {
  id: string;
  severity: 'info' | 'warn' | 'error';
  code: string;
  message: string;
  timestamp: string;
  operationId?: string;
}

export interface BackendSnapshot {
  schemaVersion: typeof BACKEND_SCHEMA_VERSION;
  revision: number;
  clients: BackendClientSnapshot[];
  providers: BackendProviderSnapshot[];
  accounts: BackendAccountSnapshot[];
  gateways: BackendGatewaySnapshot[];
  pools: Array<{
    providerId: string;
    mode: 'single' | 'multi';
    enabled: boolean;
    memberCount: number;
  }>;
  hubs: Array<{
    name: string;
    enabled: boolean;
    running: boolean;
    sourceCount: number;
  }>;
  bindings: BackendBindingSnapshot[];
  runningOperations: BackendRunningOperation[];
  notices: BackendNotice[];
}

export interface BackendError {
  code: string;
  message: string;
  resource?: string;
  operationId?: string;
  recoverable: boolean;
  recoveryActions: string[];
}

/** Stable wire command names. Unsupported lifecycle/log commands are not
 * advertised: subscription is the event transport. */
export type BackendCommand =
  | 'snapshot.get'
  | 'account.add'
  | 'account.login'
  | 'account.save'
  | 'account.switch'
  | 'account.remove'
  | 'account.export'
  | 'account.import'
  | 'gateway.add'
  | 'gateway.edit'
  | 'gateway.remove'
  | 'gateway.test'
  | 'activation.plan'
  | 'activation.execute'
  | 'activation.recover'
  | 'proxy.start'
  | 'proxy.stop'
  | 'proxy.restart'
  | 'proxy.status'
  | 'pool.get'
  | 'hub.get'
  | 'binding.use'
  | 'binding.link'
  | 'binding.reset'
  | 'doctor.run';

type AccountSavePayload = {
  providerId: string;
  name: string;
  label?: string;
  notes?: string;
  force?: boolean;
  source?: string;
  input?: { kind: string; secret: string; options?: Record<string, string> };
};

/** Command→payload mapping; this is the porting contract for UI bridges. */
export interface BackendCommandPayloadMap {
  'snapshot.get': undefined;
  'account.add': AccountSavePayload;
  'account.login': {
    providerId: string;
    methodId?: string;
    source?: string;
    name?: string;
    label?: string;
  };
  'account.save': AccountSavePayload;
  'account.switch': { providerId: string; name: string; noRefresh?: boolean; noProxy?: boolean };
  'account.remove': { providerId: string; name: string };
  'account.export': { providerId: string; name: string; path: string };
  'account.import': { providerId: string; name: string; path: string; force?: boolean };
  'gateway.add': {
    name: string;
    providerId: string;
    endpoint?: string;
    apiKey?: string;
    label?: string;
    notes?: string;
    force?: boolean;
  };
  'gateway.edit': {
    name: string;
    endpoint?: string;
    apiKey?: string;
    label?: string;
    notes?: string;
    clearApiKey?: boolean;
  };
  'gateway.remove': { name: string };
  'gateway.test': { name: string };
  'activation.plan': {
    clientId: string;
    source: ResourceRef;
    mode?: 'persistent' | 'project';
    /** Required when mode is project. Never inferred from the UI process cwd. */
    projectRoot?: string;
    model?: string;
    transportPolicy?: 'auto' | 'direct' | 'proxy';
  };
  'activation.execute': { planId: string; dryRun?: boolean };
  'activation.recover': undefined;
  'proxy.start': { providerId: string; name?: string; port?: number; host?: string };
  'proxy.stop': { providerId: string; name?: string };
  'proxy.restart': { providerId: string; name?: string; port?: number; host?: string };
  'proxy.status': { providerId: string; name?: string };
  'pool.get': { providerId: string };
  'hub.get': { name?: string };
  'binding.use': {
    clientId: string;
    with?: string;
    current?: boolean;
    model?: string;
    dryRun?: boolean;
  };
  'binding.link': {
    clientId: string;
    with?: string;
    model?: string;
    dryRun?: boolean;
    cwd?: string;
  };
  'binding.reset': { clientId: string; dryRun?: boolean };
  'doctor.run': { target?: string };
}

export type BackendCommandPayload<C extends BackendCommand = BackendCommand> =
  BackendCommandPayloadMap[C];

/** Command→non-secret response mapping. Other state is returned in BackendSnapshot. */
export interface BackendCommandDataMap {
  'snapshot.get': undefined;
  'account.add': undefined;
  'account.login': { account: string; methodId: string };
  'account.save': undefined;
  'account.switch': undefined;
  'account.remove': undefined;
  'account.export': undefined;
  'account.import': undefined;
  'gateway.add': undefined;
  'gateway.edit': undefined;
  'gateway.remove': undefined;
  'gateway.test': { reachable: 'live'; modelCount: number };
  'activation.plan': {
    planId: string;
    mode: 'persistent' | 'project';
    clientId: string;
    source: ResourceRef;
    transport: string;
    steps: Array<{ kind: string; detail?: string }>;
  };
  'activation.execute': { journalId: string };
  'activation.recover': { recovered: number; failed: string[]; refused: string[] };
  'proxy.start': undefined;
  'proxy.stop': undefined;
  'proxy.restart': undefined;
  'proxy.status': { enabled: boolean; running: boolean; endpoint?: string };
  'pool.get': BackendPoolDetails;
  'hub.get': BackendHubDetails;
  'binding.use': undefined;
  'binding.link': undefined;
  'binding.reset': undefined;
  'doctor.run': { checkCount: number; failedCount: number };
}

export type BackendCommandData<C extends BackendCommand = BackendCommand> =
  BackendCommandDataMap[C];

/** Options shared by all backend commands. Abort is best-effort: only a
 * provider login can currently observe it after it has started. */
export interface BackendCommandOptions {
  signal?: AbortSignal;
}

export type BackendCommandResult<C extends BackendCommand = BackendCommand> =
  | ({
      ok: true;
      command: C;
      operationId?: string;
      snapshot?: BackendSnapshot;
      /** Explicitly redacted, command-specific non-secret data. */
    } & (undefined extends BackendCommandData<C>
      ? { data?: BackendCommandData<C> }
      : { data: BackendCommandData<C> }))
  | { ok: false; command: C; operationId?: string; error: BackendError };

export interface OperationEvent {
  sequence: number;
  operationId: string;
  phase: BackendOperationState;
  status: 'progress' | 'completed' | 'failed';
  message: string;
  recoverable: boolean;
  errorCode?: string;
}

export type BackendEvent =
  | { type: 'snapshot'; snapshot: BackendSnapshot }
  | { type: 'operation'; operation: OperationEvent };
