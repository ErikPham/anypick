import type { AnyPickApp } from './app';
import type { AnyPickEvent } from './events';
import { providerCapabilities } from './capabilities';
import { protocolForClient } from './activation-protocol';
import { materializeResolvedSource } from './resolve-source';
import { resolveTransportPolicy } from './transport-resolver';
import { planActivation } from './activation-planner';
import { executeActivation, recoverIncompleteOperations } from './activation-executor';
import { randomUUID } from 'node:crypto';
import type {
  BackendAccountSnapshot,
  BackendBindingSnapshot,
  BackendCommand,
  BackendCommandData,
  BackendCommandOptions,
  BackendCommandPayload,
  BackendCommandResult,
  BackendError,
  BackendEvent,
  BackendExecutionPath,
  BackendOperationState,
  BackendSnapshot,
  OperationEvent,
  Protocol,
  ResourceRef,
  ActivationPlan,
} from '../types';
import { BACKEND_SCHEMA_VERSION } from '../types';
import { isAnyPickError } from '../utils/errors';

/**
 * Explicit names for the UI RPC surface. Implementations can add handlers over
 * time without leaking a store, SQLite handle, or provider-specific branch to
 * SwiftUI/Tauri.
 */
export const BACKEND_COMMANDS = [
  'snapshot.get',
  'account.add',
  'account.login',
  'account.save',
  'account.switch',
  'account.remove',
  'account.export',
  'account.import',
  'gateway.add',
  'gateway.edit',
  'gateway.remove',
  'gateway.test',
  'activation.plan',
  'activation.execute',
  'activation.recover',
  'proxy.start',
  'proxy.stop',
  'proxy.restart',
  'proxy.status',
  'pool.get',
  'hub.get',
  'binding.use',
  'binding.link',
  'binding.reset',
  'doctor.run',
] as const satisfies readonly BackendCommand[];

export interface BackendApi {
  snapshot(): Promise<BackendSnapshot>;
  /**
   * Execute a validated UI command without exposing services, stores, or
   * provider credentials. Expected failures are returned as DTOs, not thrown.
   */
  execute<C extends BackendCommand>(
    command: C,
    ...args: undefined extends BackendCommandPayload<C>
      ? [payload?: BackendCommandPayload<C>, options?: BackendCommandOptions]
      : [payload: BackendCommandPayload<C>, options?: BackendCommandOptions]
  ): Promise<BackendCommandResult<C>>;
  /** Emits an initial snapshot, then only semantic state changes. */
  subscribe(listener: (event: BackendEvent) => void): () => void;
  close(): void;
}

export interface BackendApiOptions {
  /** Re-check external state at this cadence while a UI is subscribed. */
  refreshIntervalMs?: number;
  /** Lifetime for opaque activation handles. Defaults to five minutes. */
  planTtlMs?: number;
}

type BackendUsage = BackendAccountSnapshot['usage'];
type BackendUsageWindow = NonNullable<BackendUsage>['windows'][number];

interface BackendUsageCacheEntry {
  expiresAt: number;
  usage: BackendUsage;
  refresh?: Promise<void>;
}

type BackendUsageCache = Map<string, BackendUsageCacheEntry>;

function noopBackendUsageUpdated(): void {}

/**
 * The thin-client boundary. It deliberately captures the composition root
 * internally and only exposes redacted DTOs; UI code has no Store/SQLite path
 * through this object.
 */
export function createBackendApi(app: AnyPickApp, options: BackendApiOptions = {}): BackendApi {
  let revision = 0;
  let previous = '';
  let sequence = 0;
  let closed = false;
  const subscribers = new Set<(event: BackendEvent) => void>();
  let timer: ReturnType<typeof setInterval> | undefined;
  const knownOperations = new Map<string, BackendOperationState>();
  const terminalOperations = new Set<string>();
  let lastPublishedRevision = -1;
  let cached: BackendSnapshot | undefined;
  let cachedAt = 0;
  let snapshotInFlight: Promise<BackendSnapshot> | undefined;
  let publishing = false;
  let usageRepublishRequested = false;
  let notifyUsageUpdated = noopBackendUsageUpdated;
  const refreshIntervalMs = Math.max(options.refreshIntervalMs ?? 30_000, 15_000);
  const planTtlMs = Math.max(options.planTtlMs ?? 5 * 60_000, 1_000);
  const plannedActivations = new Map<string, { plan: ActivationPlan; expiresAt: number }>();
  const usageCache: BackendUsageCache = new Map();

  const snapshot = async (force = false): Promise<BackendSnapshot> => {
    if (!force && cached && Date.now() - cachedAt < refreshIntervalMs) {
      return cached;
    }
    if (snapshotInFlight) {
      return snapshotInFlight;
    }
    snapshotInFlight = (async () => {
      const next = await buildBackendSnapshot(app, revision, usageCache, notifyUsageUpdated);
      const comparable = JSON.stringify({ ...next, revision: 0 });
      if (comparable !== previous) {
        previous = comparable;
        revision += 1;
      }
      cached = { ...next, revision };
      cachedAt = Date.now();
      return cached;
    })();
    try {
      return await snapshotInFlight;
    } finally {
      snapshotInFlight = undefined;
    }
  };

  const invalidate = (): void => {
    cached = undefined;
    cachedAt = 0;
  };

  const emit = (event: BackendEvent): void => {
    for (const listener of subscribers) {
      listener(event);
    }
  };

  const emitOperation = (
    operationId: string,
    phase: BackendOperationState,
    status: OperationEvent['status'],
    message: string,
    error?: BackendError,
  ): void => {
    if (terminalOperations.has(operationId)) {
      return;
    }
    if (status !== 'progress') {
      terminalOperations.add(operationId);
    }
    emit({
      type: 'operation',
      operation: {
        sequence: ++sequence,
        operationId,
        phase,
        status,
        message,
        recoverable: error?.recoverable ?? phase === 'rolling_back',
        errorCode: error?.code,
      },
    });
  };

  const publishJournalTransitions = (): void => {
    // `listRecent` is cheap SQLite metadata and lets an external activation
    // reach a terminal event even when it completed between UI refreshes.
    for (const item of app.journal.listRecent(100)) {
      const state = backendOperationState(item.state);
      const before = knownOperations.get(item.id);
      knownOperations.set(item.id, state);
      if (before === state) {
        continue;
      }
      const terminal = state === 'completed' || state === 'failed';
      emitOperation(
        item.id,
        state,
        terminal ? (state === 'completed' ? 'completed' : 'failed') : 'progress',
        `Operation ${item.type} is ${state.replaceAll('_', ' ')}.`,
      );
    }
  };

  const publish = async (force = false): Promise<void> => {
    if (closed || subscribers.size === 0 || publishing) {
      return;
    }
    publishing = true;
    try {
      publishJournalTransitions();
      const next = await snapshot(force);
      if (next.revision !== lastPublishedRevision) {
        lastPublishedRevision = next.revision;
        emit({ type: 'snapshot', snapshot: next });
      }
    } finally {
      publishing = false;
      if (usageRepublishRequested) {
        usageRepublishRequested = false;
        void publish(true);
      }
    }
  };

  const execute = async <C extends BackendCommand>(
    command: C,
    payload: BackendCommandPayload<C> = {} as BackendCommandPayload<C>,
    commandOptions: BackendCommandOptions = {},
  ): Promise<BackendCommandResult<C>> => {
    if (commandOptions.signal?.aborted) {
      return {
        ok: false,
        command,
        error: {
          code: 'OPERATION_CANCELLED',
          message: 'Operation was cancelled.',
          recoverable: true,
          recoveryActions: ['retry'],
        },
      };
    }
    if (command === 'snapshot.get') {
      return { ok: true, command, snapshot: await snapshot() } as BackendCommandResult<C>;
    }
    const operationId = randomUUID();
    emitOperation(operationId, 'executing', 'progress', `Running ${command}.`);
    try {
      const data = await dispatchBackendCommand(
        app,
        command,
        payload as Record<string, unknown>,
        commandOptions,
        operationId,
        plannedActivations,
        planTtlMs,
      );
      if (invalidatesBackendUsage(command)) {
        usageCache.clear();
      }
      const mutated = isBackendMutation(command);
      const next = mutated ? (invalidate(), await snapshot(true)) : undefined;
      emitOperation(operationId, 'completed', 'completed', `${command} completed.`);
      if (mutated) {
        void publish();
      }
      return { ok: true, command, operationId, snapshot: next, data } as BackendCommandResult<C>;
    } catch (error) {
      const dto = toBackendError(error);
      emitOperation(operationId, 'failed', 'failed', `${command} failed.`, dto);
      return { ok: false, command, operationId, error: dto };
    }
  };

  notifyUsageUpdated = (): void => {
    if (closed) {
      return;
    }
    const activeSnapshot = snapshotInFlight;
    void (async () => {
      // A fast quota response can arrive while its initiating snapshot is still
      // assembling. Wait until that snapshot has committed its cache before
      // invalidating it, otherwise the stale result can overwrite this refresh.
      await activeSnapshot?.catch(() => undefined);
      if (closed) {
        return;
      }
      invalidate();
      if (publishing) {
        usageRepublishRequested = true;
        return;
      }
      await publish(true);
    })().catch(() => {});
  };

  return {
    snapshot: () => snapshot(),
    execute,
    subscribe(listener): () => void {
      if (closed) {
        return () => {};
      }
      subscribers.add(listener);
      // Establish this baseline before asynchronous liveness work. An
      // operation that completes while the initial snapshot is resolving is a
      // new lifecycle transition, not historical journal data.
      for (const item of app.journal.listRecent(100)) {
        knownOperations.set(item.id, backendOperationState(item.state));
        if (
          backendOperationState(item.state) === 'completed' ||
          backendOperationState(item.state) === 'failed'
        ) {
          terminalOperations.add(item.id);
        }
      }
      void snapshot().then((value) => {
        lastPublishedRevision = value.revision;
        listener({ type: 'snapshot', snapshot: value });
        publishJournalTransitions();
      });
      if (!timer) {
        timer = setInterval(() => void publish(true), refreshIntervalMs);
        timer.unref();
      }
      return () => {
        subscribers.delete(listener);
        if (subscribers.size === 0 && timer) {
          clearInterval(timer);
          timer = undefined;
        }
      };
    },
    close(): void {
      closed = true;
      subscribers.clear();
      if (timer) {
        clearInterval(timer);
        timer = undefined;
      }
      plannedActivations.clear();
      usageCache.clear();
    },
  };
}

export async function buildBackendSnapshot(
  app: AnyPickApp,
  revision = 0,
  usageCache: BackendUsageCache = new Map(),
  onUsageUpdated: () => void = () => {},
): Promise<BackendSnapshot> {
  const accounts = await app.accounts.list();
  const usageByAccount = new Map<string, BackendUsage>();
  for (const account of accounts.filter((candidate) => candidate.isLiveMatch)) {
    const key = backendUsageKey(account);
    const cached = usageCache.get(key);
    if (cached?.usage) {
      usageByAccount.set(key, cached.usage);
    }
    scheduleBackendUsageRefresh(app, account.provider, key, usageCache, onUsageUpdated);
  }
  const accountRows: BackendAccountSnapshot[] = [];
  for (const account of accounts) {
    const stored = await app.accounts.get(account.provider, account.name);
    accountRows.push({
      providerId: account.provider,
      name: account.name,
      label: account.label,
      identity: account.identity,
      credentialClass:
        stored?.meta.credentialClass ??
        (stored?.meta.credentialKind === 'proxy-only' ? 'proxy_only' : 'native_subscription'),
      active: account.active,
      live: account.isLiveMatch,
      // AccountService.list already checks each provider's live state once and
      // includes proxy status. Calling it again here doubled provider/keychain
      // work for every row.
      proxy: { enabled: account.proxyEnabled, running: account.proxyRunning },
      ...(account.isLiveMatch && usageByAccount.get(backendUsageKey(account))
        ? { usage: usageByAccount.get(backendUsageKey(account)) }
        : {}),
    });
  }

  const bindings = await Promise.all(
    [...app.bindings.listGlobal(), ...app.bindings.listAllProjects()].map((binding) =>
      backendBinding(app, binding, 'projectRoot' in binding ? binding.projectRoot : undefined),
    ),
  );
  const providers = app.accountRegistry.list();
  const pools = await Promise.all(
    providers.map(async (provider) => {
      const pool = await app.pools.get(provider.id);
      return pool
        ? {
            providerId: provider.id,
            mode: pool.mode,
            enabled: pool.enabled,
            memberCount: pool.members.length,
          }
        : null;
    }),
  );
  const hub = await app.hub.status().catch(() => null);

  return {
    schemaVersion: BACKEND_SCHEMA_VERSION,
    revision,
    clients: app.clients.list().map((client) => ({
      id: client.id,
      name: client.name,
      protocols: [...(client.capabilities?.acceptedProtocols ?? [])],
      supportsPersistentConfig: client.capabilities?.supportsPersistentConfig === true,
      supportsIsolatedHome: client.capabilities?.supportsIsolatedHome === true,
    })),
    providers: providers.map((provider) => {
      const capabilities = providerCapabilities(provider);
      return {
        id: provider.id,
        name: provider.name,
        supportsProxy: capabilities.canProxy,
        supportsPool: capabilities.canPool,
        supportsBrowserLogin: typeof provider.login === 'function',
        supportsCredentialInput: capabilities.canBackupInput,
      };
    }),
    accounts: accountRows,
    gateways: (await app.profiles.list()).map((profile) => ({
      name: profile.meta.name,
      providerId: profile.meta.provider,
      label: profile.meta.label,
      endpoint: profile.meta.endpoint,
      defaultModel: profile.meta.defaultModel,
    })),
    pools: pools.filter((pool): pool is NonNullable<typeof pool> => pool !== null),
    hubs: hub
      ? [
          {
            name: hub.name,
            enabled: hub.enabled,
            running: hub.running,
            sourceCount: hub.sourceCount,
          },
        ]
      : [],
    bindings,
    runningOperations: app.journal.listIncomplete().map((operation) => ({
      operationId: operation.id,
      type: operation.type,
      state: backendOperationState(operation.state),
      startedAt: operation.startedAt,
      updatedAt: operation.updatedAt,
    })),
    notices: app.events.list().map(backendNotice),
  };
}

type BackendListedAccount = Awaited<ReturnType<AnyPickApp['accounts']['list']>>[number];

function backendUsageKey(account: BackendListedAccount): string {
  // Account name prevents one provider's quota from following a native switch;
  // identity also separates a saved slot that was overwritten with another login.
  return JSON.stringify([account.provider, account.name, account.identity ?? null]);
}

function scheduleBackendUsageRefresh(
  app: AnyPickApp,
  providerId: string,
  key: string,
  cache: BackendUsageCache,
  onUsageUpdated: () => void,
): void {
  const existing = cache.get(key);
  if (existing?.refresh || (existing && existing.expiresAt > Date.now())) {
    return;
  }

  const entry: BackendUsageCacheEntry = existing ?? { expiresAt: 0, usage: undefined };
  const previous = JSON.stringify(entry.usage);
  entry.refresh = app.accounts
    .liveUsage(providerId)
    .catch(() => null)
    .then((live) => {
      // A credential mutation may clear this entry while its old request is in
      // flight. Never let that completion repopulate the new account's state.
      if (cache.get(key) !== entry) {
        return;
      }
      entry.usage = backendUsage(live);
      entry.expiresAt = Date.now() + 60_000;
      if (JSON.stringify(entry.usage) !== previous) {
        onUsageUpdated();
      }
    })
    .finally(() => {
      entry.refresh = undefined;
    });
  cache.set(key, entry);
}

function backendUsage(
  live: Awaited<ReturnType<AnyPickApp['accounts']['liveUsage']>>,
): BackendUsage {
  const windows = (live?.windows ?? [])
    .filter((window) => window.label.trim())
    .map((window): BackendUsageWindow => {
      const normalized: BackendUsageWindow = {
        label: window.label.trim().slice(0, 24),
        remainingPercent: Math.max(0, Math.min(100, Math.round(window.remainingPercent))),
      };
      if (window.resetsAtMs != null && Number.isFinite(window.resetsAtMs)) {
        normalized.resetsAtMs = window.resetsAtMs;
      }
      return normalized;
    });
  return windows.length > 0 ? { windows } : undefined;
}

export function toBackendError(error: unknown): BackendError {
  if (isAnyPickError(error)) {
    return {
      code: error.code ?? 'ERROR',
      message: redactBackendMessage(error.message),
      recoverable: !error.mutated || error.suggestions.length > 0,
      recoveryActions: error.suggestions.map(recoveryAction),
    };
  }
  return {
    code:
      error && typeof error === 'object' && typeof (error as { code?: unknown }).code === 'string'
        ? (error as { code: string }).code
        : 'ERROR',
    message: redactBackendMessage(error instanceof Error ? error.message : String(error)),
    recoverable: false,
    recoveryActions: [],
  };
}

function redactBackendMessage(message: string): string {
  // DTOs must remain safe if a provider includes a bearer/API key in an
  // upstream exception. Keep the diagnostic useful without treating messages
  // as a credential transport.
  return message
    .replace(/\b(?:sk|rk|pk|AIza)[-_a-zA-Z0-9]{12,}\b/g, '[redacted]')
    .replace(/(bearer\s+)[^\s,]+/gi, '$1[redacted]');
}

async function backendBinding(
  app: AnyPickApp,
  binding: {
    client: string;
    spec: { source: ResourceRef; transportPolicy: 'auto' | 'direct' | 'proxy' };
  },
  projectRoot?: string,
): Promise<BackendBindingSnapshot> {
  const client = app.clients.get(binding.client);
  try {
    const source = await materializeResolvedSource(binding.spec.source, app);
    const capability = resolveTransportPolicy(
      source.adapter,
      binding.client,
      binding.spec.transportPolicy,
    );
    const protocol = protocolForClient(client.capabilities, source.adapter.capabilities.protocols);
    const executionPath = executionPathFor(
      source.ref,
      capability,
      protocol,
      source.adapter.capabilities,
    );
    return {
      id: projectRoot ? `project/${projectRoot}/${binding.client}` : `global/${binding.client}`,
      scope: projectRoot ? 'project' : 'global',
      projectRoot,
      clientId: binding.client,
      source: binding.spec.source,
      transportPolicy: binding.spec.transportPolicy,
      executionPath,
      // The managed clients here are CLI configurations. Codex Desktop keeps
      // its own signed-in account and is intentionally never reported as
      // switchable by this backend.
      clientSurface: 'cli',
      requiresRestart: executionPath === 'native_direct',
      canResume: false,
      ownerRunning: false,
      availableActions: ['binding.use', 'binding.reset'],
      availability: 'ready',
    };
  } catch (error) {
    const dto = toBackendError(error);
    return {
      id: projectRoot ? `project/${projectRoot}/${binding.client}` : `global/${binding.client}`,
      scope: projectRoot ? 'project' : 'global',
      projectRoot,
      clientId: binding.client,
      source: binding.spec.source,
      transportPolicy: binding.spec.transportPolicy,
      executionPath: 'unavailable',
      clientSurface: 'cli',
      requiresRestart: false,
      canResume: false,
      ownerRunning: false,
      availableActions: dto.recoveryActions,
      availability: 'unavailable',
      error: dto,
    };
  }
}

function executionPathFor(
  ref: ResourceRef,
  capability: string,
  protocol: Protocol,
  source: { protocols: Protocol[]; requiresNativeAuthWrite?: boolean },
): BackendExecutionPath {
  if (ref.kind === 'proxy-hub') {
    return 'proxy_hub';
  }
  if (capability === 'direct') {
    return ref.kind === 'account' && source.requiresNativeAuthWrite
      ? 'native_direct'
      : 'provider_direct';
  }
  return source.protocols.includes(protocol) ? 'local_passthrough' : 'local_translate';
}

function backendOperationState(state: string): BackendOperationState {
  switch (state) {
    case 'planned':
      return 'planning';
    case 'executing':
      return 'executing';
    case 'verifying':
      return 'verifying';
    case 'rolling_back':
      return 'rolling_back';
    case 'committed':
      return 'completed';
    case 'failed':
    case 'rolled_back':
      return 'failed';
    default:
      return 'failed';
  }
}

function backendNotice(event: AnyPickEvent) {
  return {
    id: `${event.timestamp}/${event.code}`,
    severity: event.severity,
    code: event.code,
    message: event.message,
    timestamp: event.timestamp,
    operationId: event.opId,
  };
}

function recoveryAction(suggestion: string): string {
  const value = suggestion.toLowerCase();
  if (value.includes('restart')) {
    return 'restart_client';
  }
  if (value.includes('unlock')) {
    return 'unlock_keychain';
  }
  if (value.includes('file')) {
    return 'switch_to_file_store';
  }
  if (value.includes('login')) {
    return 'open_login';
  }
  if (value.includes('restore')) {
    return 'restore_previous';
  }
  return 'retry';
}

async function dispatchBackendCommand(
  app: AnyPickApp,
  command: BackendCommand,
  payload: Record<string, unknown>,
  options: BackendCommandOptions,
  operationId: string,
  plannedActivations: Map<string, { plan: ActivationPlan; expiresAt: number }>,
  planTtlMs: number,
): Promise<BackendCommandData | undefined> {
  const providerId = optionalString(payload.providerId);
  const name = optionalString(payload.name);
  const clientId = optionalString(payload.clientId);

  switch (command) {
    case 'account.add':
    case 'account.save': {
      requireText(providerId, 'providerId');
      requireText(name, 'name');
      const input = record(payload.input);
      await app.accounts.save(providerId, name, {
        label: optionalString(payload.label),
        notes: optionalString(payload.notes),
        force: payload.force === true,
        source: optionalString(payload.source),
        input: input
          ? {
              kind: requiredString(input.kind, 'input.kind'),
              secret: requiredString(input.secret, 'input.secret'),
              options: stringRecord(input.options),
            }
          : undefined,
      });
      return;
    }
    case 'account.login': {
      requireText(providerId, 'providerId');
      const result = await app.accounts.login(providerId, {
        methodId: optionalString(payload.methodId),
        source: optionalString(payload.source),
        name,
        label: optionalString(payload.label),
        signal: options.signal,
      });
      return { account: `${result.provider}/${result.account.name}`, methodId: result.method.id };
    }
    case 'account.switch':
      requireText(providerId, 'providerId');
      requireText(name, 'name');
      await app.accounts.use(providerId, name, {
        noRefresh: payload.noRefresh === true,
        noProxy: payload.noProxy === true,
      });
      return;
    case 'account.remove':
      requireText(providerId, 'providerId');
      requireText(name, 'name');
      await app.accounts.delete(providerId, name);
      return;
    case 'account.export':
      requireText(providerId, 'providerId');
      requireText(name, 'name');
      await app.accounts.exportAccount(providerId, name, requiredString(payload.path, 'path'));
      return;
    case 'account.import':
      requireText(providerId, 'providerId');
      requireText(name, 'name');
      await app.accounts.importAccount(providerId, name, requiredString(payload.path, 'path'), {
        force: payload.force === true,
      });
      return;
    case 'gateway.add':
      requireText(name, 'name');
      await app.profiles.create(name, profileCreatePayload(payload));
      return;
    case 'gateway.edit':
      requireText(name, 'name');
      await app.profiles.edit(name, profileEditPayload(payload));
      return;
    case 'gateway.remove':
      requireText(name, 'name');
      await app.profiles.delete(name);
      return;
    case 'gateway.test': {
      requireText(name, 'name');
      const profile = await app.profiles.get(name);
      const result = await app.modelDiscovery.list({
        providerId: profile.meta.provider,
        endpoint: profile.meta.endpoint,
        apiKey: profile.secrets.apiKey,
        refresh: true,
      });
      if (result.source !== 'live') {
        throw Object.assign(new Error('The gateway did not return a live model catalog.'), {
          code: 'GATEWAY_TEST_FAILED',
        });
      }
      return { reachable: 'live', modelCount: result.models.length };
    }
    case 'activation.plan': {
      requireText(clientId, 'clientId');
      if (payload.mode === 'ephemeral') {
        throw Object.assign(
          new Error(
            'Ephemeral activation requires a child/session lifecycle and is unavailable through this backend.',
          ),
          { code: 'BACKEND_COMMAND_UNSUPPORTED' },
        );
      }
      const source = resourceRef(payload.source);
      const plan = await planActivation(
        {
          mode: payload.mode === 'project' ? 'project' : 'persistent',
          client: clientId,
          source,
          model: optionalString(payload.model),
          transportPolicy: transportPolicy(payload.transportPolicy),
        },
        app,
        {
          projectRoot:
            payload.mode === 'project'
              ? requiredString(payload.projectRoot, 'projectRoot')
              : undefined,
        },
      );
      // A plan carries routes/tokens for execution. Expose only a transparent,
      // non-secret summary at this output boundary.
      pruneExpiredPlans(plannedActivations);
      const planId = randomUUID();
      plannedActivations.set(planId, { plan, expiresAt: Date.now() + planTtlMs });
      return {
        planId,
        mode: plan.mode === 'project' ? 'project' : 'persistent',
        clientId: plan.client,
        source: plan.resolvedSource.ref,
        transport: plan.transport.capability,
        steps: plan.steps.map((step) => ({ kind: step.kind, detail: step.detail })),
      };
    }
    case 'activation.execute': {
      const planId = requiredString(payload.planId, 'planId');
      pruneExpiredPlans(plannedActivations);
      const planned = plannedActivations.get(planId);
      if (!planned) {
        throw Object.assign(
          new Error('Activation plan is missing or has expired. Plan again and retry.'),
          {
            code: 'ACTIVATION_PLAN_EXPIRED',
          },
        );
      }
      // A plan is single-use. Its journal is now the durable recovery record.
      plannedActivations.delete(planId);
      const result = await executeActivation(planned.plan, app, {
        dryRun: payload.dryRun === true,
        operationId,
      });
      return { journalId: result.journalId ?? operationId };
    }
    case 'binding.use':
      requireText(clientId, 'clientId');
      await app.bindingService.use(clientId, {
        with: optionalString(payload.with),
        current: payload.current === true,
        model: optionalString(payload.model),
        dryRun: payload.dryRun === true,
      });
      return;
    case 'binding.link':
      requireText(clientId, 'clientId');
      await app.bindingService.link(clientId, {
        with: optionalString(payload.with),
        model: optionalString(payload.model),
        dryRun: payload.dryRun === true,
        cwd: optionalString(payload.cwd),
      });
      return;
    case 'binding.reset':
      requireText(clientId, 'clientId');
      await app.bindingService.reset(clientId, { dryRun: payload.dryRun === true });
      return;
    case 'activation.recover': {
      const result = await recoverIncompleteOperations({
        journal: app.journal,
        root: app.root,
        events: app.events.sink,
        resolve: app,
      });
      return { recovered: result.recovered, failed: result.failed, refused: result.refused };
    }
    case 'proxy.start':
      requireText(providerId, 'providerId');
      await app.proxy.startProxy(providerId, name, proxyOptions(payload));
      return;
    case 'proxy.stop':
      requireText(providerId, 'providerId');
      await app.proxy.stopProxy(providerId, name);
      return;
    case 'proxy.restart':
      requireText(providerId, 'providerId');
      await app.proxy.stopProxy(providerId, name);
      await app.proxy.startProxy(providerId, name, proxyOptions(payload));
      return;
    case 'proxy.status': {
      requireText(providerId, 'providerId');
      const status = await app.proxy.proxyStatus(providerId, name);
      return { enabled: status.enabled, running: status.running, endpoint: status.endpoint };
    }
    case 'pool.get':
      requireText(providerId, 'providerId');
      {
        const pool = await app.pools.getOrDefault(providerId);
        return {
          providerId: pool.provider,
          mode: pool.mode,
          enabled: pool.enabled,
          host: pool.host,
          port: pool.port,
          strategy: pool.strategy,
          members: pool.members.map((member) => ({ ...member })),
          updatedAt: pool.updatedAt,
        };
      }
    case 'hub.get': {
      const hubName = optionalString(payload.name);
      const [config, status] = await Promise.all([app.hub.get(hubName), app.hub.status(hubName)]);
      return {
        name: config.name,
        enabled: config.enabled,
        host: config.host,
        port: config.port,
        sources: config.sources.map((source) => ({
          ref: { ...source.ref },
          enabled: source.enabled,
        })),
        modelOwners: config.modelOwners.map((owner) => ({
          model: owner.model,
          source: { ...owner.source },
        })),
        revision: config.revision,
        createdAt: config.createdAt,
        updatedAt: config.updatedAt,
        running: status.running,
        endpoint: status.endpoint,
        sourceCount: status.sourceCount,
        modelCount: status.modelCount,
        conflictCount: status.conflictCount,
        detail: status.detail,
      };
    }
    case 'doctor.run': {
      const report = await app.doctor.run(optionalString(payload.target));
      return {
        checkCount: report.checks.length,
        failedCount: report.checks.filter((check) => !check.ok).length,
      };
    }
    default:
      throw backendUnsupported(`Unknown backend command "${command}".`);
  }
}

function pruneExpiredPlans(plans: Map<string, { plan: ActivationPlan; expiresAt: number }>): void {
  const now = Date.now();
  for (const [id, planned] of plans) {
    if (planned.expiresAt <= now) {
      plans.delete(id);
    }
  }
}

function isBackendMutation(command: BackendCommand): boolean {
  return !new Set<BackendCommand>([
    'snapshot.get',
    'activation.plan',
    'gateway.test',
    'proxy.status',
    'pool.get',
    'hub.get',
    'doctor.run',
  ]).has(command);
}

function invalidatesBackendUsage(command: BackendCommand): boolean {
  return new Set<BackendCommand>([
    'account.add',
    'account.login',
    'account.save',
    'account.switch',
    'account.remove',
    'account.import',
    'activation.execute',
    'binding.use',
    'binding.link',
    'binding.reset',
  ]).has(command);
}

function backendUnsupported(message: string): Error {
  return Object.assign(new Error(message), { code: 'BACKEND_COMMAND_UNSUPPORTED' });
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}
function optionalString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined;
}
function requiredString(value: unknown, field: string): string {
  const result = optionalString(value);
  if (!result) {
    throw Object.assign(new Error(`${field} is required.`), { code: 'INVALID_USAGE' });
  }
  return result;
}
function requireText(value: string | undefined, field: string): asserts value is string {
  requiredString(value, field);
}
function stringRecord(value: unknown): Record<string, string> | undefined {
  const values = record(value);
  if (!values) {
    return undefined;
  }
  const out: Record<string, string> = {};
  for (const [key, item] of Object.entries(values)) {
    out[key] = requiredString(item, `options.${key}`);
  }
  return out;
}
function transportPolicy(value: unknown): 'auto' | 'direct' | 'proxy' | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (value === 'auto' || value === 'direct' || value === 'proxy') {
    return value;
  }
  throw Object.assign(new Error('transportPolicy must be auto, direct, or proxy.'), {
    code: 'INVALID_USAGE',
  });
}
function resourceRef(value: unknown): ResourceRef {
  const ref = record(value);
  if (!ref || typeof ref.kind !== 'string') {
    throw Object.assign(new Error('source is required.'), { code: 'INVALID_USAGE' });
  }
  if (ref.kind === 'account') {
    return {
      kind: 'account',
      provider: requiredString(ref.provider, 'source.provider'),
      name: requiredString(ref.name, 'source.name'),
    };
  }
  if (ref.kind === 'gateway') {
    return { kind: 'gateway', name: requiredString(ref.name, 'source.name') };
  }
  if (ref.kind === 'proxy-hub') {
    return { kind: 'proxy-hub', name: requiredString(ref.name, 'source.name') };
  }
  if (ref.kind === 'account-pool') {
    return { kind: 'account-pool', provider: requiredString(ref.provider, 'source.provider') };
  }
  throw Object.assign(new Error('source is not a valid resource reference.'), {
    code: 'INVALID_USAGE',
  });
}
function proxyOptions(payload: Record<string, unknown>): { port?: number; host?: string } {
  return {
    port: typeof payload.port === 'number' ? payload.port : undefined,
    host: optionalString(payload.host),
  };
}
function profileCreatePayload(payload: Record<string, unknown>) {
  return {
    provider: requiredString(payload.providerId, 'providerId'),
    endpoint: optionalString(payload.endpoint),
    apiKey: optionalString(payload.apiKey),
    label: optionalString(payload.label),
    notes: optionalString(payload.notes),
    force: payload.force === true,
  };
}
function profileEditPayload(payload: Record<string, unknown>) {
  return {
    endpoint: optionalString(payload.endpoint),
    apiKey: optionalString(payload.apiKey),
    label: optionalString(payload.label),
    notes: optionalString(payload.notes),
    clearApiKey: payload.clearApiKey === true,
  };
}
