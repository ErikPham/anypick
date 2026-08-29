/** Commands emitted by the native desktop app over its stdout pipe. */
export const TRAY_PROTOCOL_VERSION = 1;
export const MAX_TRAY_COMMAND_BYTES = 16 * 1024;

/** App-owned transport handshake; command/snapshot DTOs remain v1 during cutover. */
export const APP_SERVICE_PROTOCOL_VERSION = 2;
export const MAX_APP_SERVICE_FRAME_BYTES = 4 * 1024 * 1024;

export interface AppServiceEngineHello {
  protocolVersion: 2;
  messageVersion: 1;
  engineVersion: string;
  schemaVersion: number;
  capabilities: string[];
  /** Non-secret diagnostic correlation id. */
  instanceId: string;
  maxFrameBytes: number;
}

export interface AppServiceHostHello {
  protocolVersion: 2;
  appVersion: string;
  platform: 'macos' | 'windows' | 'linux' | 'test';
  capabilities: string[];
  hostState: { launchAtLogin: boolean };
}

export interface AppServiceHostEffect {
  protocolVersion: 2;
  requestId: string;
  effect: { kind: 'launch-at-login'; enabled: boolean };
}

export interface AppServiceHostEffectResult {
  protocolVersion: 2;
  requestId: string;
  status: 'success' | 'error';
  message?: string;
  hostState: { launchAtLogin: boolean };
}

export interface TrayInvokeCommand {
  version: 1;
  requestId: string;
  revision: number;
  actionId: string;
}

export interface TrayApplyModelRolesCommand {
  version: 1;
  requestId: string;
  revision: number;
  clientId: string;
  /** Role id -> opaque model action id from this exact snapshot revision. */
  roleActionIds: Record<string, string>;
}

export interface TrayLogsCommand {
  version: 1;
  requestId: string;
  providerId: string;
  name: string;
  lines: number;
}

export interface TrayBackupInspectCommand {
  version: 1;
  requestId: string;
  path: string;
}

export interface TrayCancelCommand {
  version: 1;
  /** Request to abort. Cancellation is control-plane state, never a new mutation. */
  requestId: string;
}

export type TrayBackupDisposition = 'create' | 'replace' | 'blocked' | 'unavailable';
export type TrayBackupReason = 'identity-conflict' | 'provider-unavailable';
export type TrayBackupInspectionFailure =
  | 'damaged'
  | 'newer-version'
  | 'permission-denied'
  | 'timeout';

export interface TrayBackupPreviewEntry {
  id: string;
  providerId: string;
  name: string;
  label: string;
  disposition: TrayBackupDisposition;
  reason?: TrayBackupReason;
}

export type TrayBackupPreviewResult =
  | {
      version: 1;
      requestId: string;
      status: 'ready';
      inspectionId: string;
      createdAt: string;
      entries: TrayBackupPreviewEntry[];
    }
  | {
      version: 1;
      requestId: string;
      status: 'error';
      error: 'inspection-failed';
      code: 'BACKUP_INSPECT_FAILED';
      reason: TrayBackupInspectionFailure;
      message: string;
    };

export type TrayProxyLogsState = 'ready' | 'empty' | 'not-running' | 'error';

export interface TrayProxyLogsResult {
  version: 1;
  requestId: string;
  proxyId: string;
  state: TrayProxyLogsState;
  text: string;
}

/** Stable identity shared by snapshot rows, log requests, and log responses. */
export function trayLogSourceId(providerId: string, name: string): string {
  return `${providerId}/${name}`;
}

export type TrayMutationOperation =
  | 'account-detect'
  | 'account-import'
  | 'account-export'
  | 'account-backup-all'
  | 'account-backup-export'
  | 'account-backup-import'
  | 'account-login'
  | 'account-save-current'
  | 'account-save'
  | 'account-edit'
  | 'account-refresh'
  | 'account-remove'
  /** Clear the live app login (stash: optional backup + local wipe, no remote logout). */
  | 'account-clear'
  | 'gateway-create'
  | 'gateway-test'
  | 'gateway-edit'
  | 'gateway-refresh'
  | 'gateway-remove'
  | 'client-reset'
  | 'hub-source-toggle'
  | 'proxy-restart-all'
  | 'proxy-stop-all'
  | 'setting-launch-at-login'
  | 'setting-auto-start-proxies'
  | 'setting-show-quota'
  | 'setting-quota-guard';

interface TrayMutationFields {
  version: 1;
  requestId: string;
  providerId?: string;
  sourceId?: string;
  methodId?: string;
  label?: string;
  endpoint?: string;
  apiKey?: string;
  /** Non-secret credential qualifier (e.g. Kiro API region) for account-save. */
  region?: string;
  defaultModel?: string;
  overwrite?: boolean;
  enabled?: boolean;
}

export interface TrayBackupAccountRef {
  provider: string;
  name: string;
}

export type TrayMutationCommand =
  | (TrayMutationFields & {
      operation: 'account-import' | 'account-export';
      providerId: string;
      name: string;
      path: string;
    })
  | (TrayMutationFields & {
      operation: 'account-backup-all';
      path: string;
    })
  | {
      version: 1;
      requestId: string;
      operation: 'account-backup-export';
      path: string;
      accounts: TrayBackupAccountRef[];
      overwrite: boolean;
    }
  | {
      version: 1;
      requestId: string;
      operation: 'account-backup-import';
      inspectionId: string;
      entryIds: string[];
      overwriteConfirmed: boolean;
    }
  | (TrayMutationFields & {
      operation: 'account-login';
      providerId: string;
      methodId: string;
      name?: string;
    })
  | (TrayMutationFields & {
      operation: 'account-save-current';
      providerId: string;
      name?: string;
      path?: never;
    })
  | (TrayMutationFields & {
      operation: Exclude<
        TrayMutationOperation,
        | 'account-import'
        | 'account-export'
        | 'account-backup-all'
        | 'account-backup-export'
        | 'account-backup-import'
        | 'account-login'
        | 'account-save-current'
      >;
      name: string;
      path?: never;
    });

export type TrayCommand =
  | { kind: 'open' | 'refresh' | 'restart' | 'stop' | 'quit' }
  | {
      kind: 'navigate';
      screen: 'accounts' | 'gateways' | 'proxy' | 'proxy-hub' | 'add-account' | 'add-gateway';
    }
  | { kind: 'apply-model-roles'; payload: TrayApplyModelRolesCommand }
  | { kind: 'logs'; payload: TrayLogsCommand }
  | { kind: 'backup-inspect'; payload: TrayBackupInspectCommand }
  | { kind: 'cancel'; payload: TrayCancelCommand }
  | { kind: 'mutate'; payload: TrayMutationCommand }
  | { kind: 'invoke'; payload: TrayInvokeCommand };

type SimpleTrayCommandKind = 'open' | 'refresh' | 'restart' | 'stop' | 'quit';

const SIMPLE_COMMANDS = new Set<SimpleTrayCommandKind>([
  'open',
  'refresh',
  'restart',
  'stop',
  'quit',
]);
const INVOKE_PREFIX = 'invoke\t';
const MODEL_ROLES_PREFIX = 'model-roles\t';
const LOGS_PREFIX = 'logs\t';
const BACKUP_INSPECT_PREFIX = 'backup-inspect\t';
const CANCEL_PREFIX = 'cancel\t';
const MUTATE_PREFIX = 'mutate\t';
const NAVIGATE_PREFIX = 'navigate\t';
const NAVIGATE_SCREENS = new Set([
  'accounts',
  'gateways',
  'proxy',
  'proxy-hub',
  'add-account',
  'add-gateway',
]);
const CANONICAL_BASE64 = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/;

/**
 * Decodes one complete command line from the native desktop app.
 *
 * The helper is a separate process, so malformed input is ignored rather than
 * allowed to disrupt proxy supervision.
 */
export function decodeTrayCommand(line: string): TrayCommand | undefined {
  try {
    if (Buffer.byteLength(line, 'utf8') > MAX_TRAY_COMMAND_BYTES) {
      return undefined;
    }

    if (SIMPLE_COMMANDS.has(line as SimpleTrayCommandKind)) {
      return { kind: line as SimpleTrayCommandKind };
    }

    if (line.startsWith(NAVIGATE_PREFIX)) {
      const screen = line.slice(NAVIGATE_PREFIX.length);
      if (NAVIGATE_SCREENS.has(screen)) {
        return {
          kind: 'navigate',
          screen: screen as
            | 'accounts'
            | 'gateways'
            | 'proxy'
            | 'proxy-hub'
            | 'add-account'
            | 'add-gateway',
        };
      }
      return undefined;
    }

    const encoded = line.startsWith(INVOKE_PREFIX)
      ? line.slice(INVOKE_PREFIX.length)
      : line.startsWith(MODEL_ROLES_PREFIX)
        ? line.slice(MODEL_ROLES_PREFIX.length)
        : line.startsWith(LOGS_PREFIX)
          ? line.slice(LOGS_PREFIX.length)
          : line.startsWith(BACKUP_INSPECT_PREFIX)
            ? line.slice(BACKUP_INSPECT_PREFIX.length)
            : line.startsWith(CANCEL_PREFIX)
              ? line.slice(CANCEL_PREFIX.length)
              : line.startsWith(MUTATE_PREFIX)
                ? line.slice(MUTATE_PREFIX.length)
                : undefined;
    if (encoded === undefined) {
      return undefined;
    }
    if (
      encoded.length === 0 ||
      Buffer.byteLength(encoded, 'utf8') > MAX_TRAY_COMMAND_BYTES ||
      !CANONICAL_BASE64.test(encoded)
    ) {
      return undefined;
    }

    const bytes = Buffer.from(encoded, 'base64');
    const decoded = bytes.toString('utf8');
    if (
      bytes.length > MAX_TRAY_COMMAND_BYTES ||
      bytes.toString('base64') !== encoded ||
      !bytes.equals(Buffer.from(decoded, 'utf8'))
    ) {
      return undefined;
    }

    const value: unknown = JSON.parse(decoded);
    if (line.startsWith(INVOKE_PREFIX)) {
      return isTrayInvokeCommand(value) ? { kind: 'invoke', payload: value } : undefined;
    }
    if (line.startsWith(MODEL_ROLES_PREFIX)) {
      return isTrayApplyModelRolesCommand(value)
        ? { kind: 'apply-model-roles', payload: value }
        : undefined;
    }
    if (line.startsWith(LOGS_PREFIX)) {
      return isTrayLogsCommand(value) ? { kind: 'logs', payload: value } : undefined;
    }
    if (line.startsWith(BACKUP_INSPECT_PREFIX)) {
      return isTrayBackupInspectCommand(value)
        ? { kind: 'backup-inspect', payload: value }
        : undefined;
    }
    if (line.startsWith(CANCEL_PREFIX)) {
      return isTrayCancelCommand(value) ? { kind: 'cancel', payload: value } : undefined;
    }
    return isTrayMutationCommand(value) ? { kind: 'mutate', payload: value } : undefined;
  } catch {
    return undefined;
  }
}

function isTrayCancelCommand(value: unknown): value is TrayCancelCommand {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    Object.keys(candidate).every((key) => ['version', 'requestId'].includes(key)) &&
    Object.keys(candidate).length === 2 &&
    candidate.version === TRAY_PROTOCOL_VERSION &&
    safeTrayString(candidate.requestId, 128, false)
  );
}

function isTrayBackupInspectCommand(value: unknown): value is TrayBackupInspectCommand {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    Object.keys(candidate).every((key) => ['version', 'requestId', 'path'].includes(key)) &&
    Object.keys(candidate).length === 3 &&
    candidate.version === TRAY_PROTOCOL_VERSION &&
    safeTrayString(candidate.requestId, 128, false) &&
    safeTrayPath(candidate.path)
  );
}

function isTrayLogsCommand(value: unknown): value is TrayLogsCommand {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  return (
    Object.keys(candidate).every((key) =>
      ['version', 'requestId', 'providerId', 'name', 'lines'].includes(key),
    ) &&
    candidate.version === TRAY_PROTOCOL_VERSION &&
    safeTrayString(candidate.requestId, 128, false) &&
    safeTrayString(candidate.providerId, 64, false) &&
    safeTrayString(candidate.name, 128, false) &&
    typeof candidate.lines === 'number' &&
    Number.isSafeInteger(candidate.lines) &&
    candidate.lines >= 1 &&
    candidate.lines <= 200
  );
}

function isTrayMutationCommand(value: unknown): value is TrayMutationCommand {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  const allowed = new Set([
    'version',
    'requestId',
    'operation',
    'providerId',
    'sourceId',
    'methodId',
    'name',
    'label',
    'endpoint',
    'apiKey',
    'region',
    'defaultModel',
    'overwrite',
    'enabled',
    'path',
    'accounts',
    'inspectionId',
    'entryIds',
    'overwriteConfirmed',
  ]);
  if (Object.keys(candidate).some((key) => !allowed.has(key))) {
    return false;
  }
  if (
    candidate.version !== TRAY_PROTOCOL_VERSION ||
    !safeTrayString(candidate.requestId, 128, false) ||
    !isMutationOperation(candidate.operation)
  ) {
    return false;
  }
  if (candidate.path !== undefined && !safeTrayPath(candidate.path)) {
    return false;
  }
  if (candidate.operation === 'account-backup-export') {
    return (
      safeTrayPath(candidate.path) &&
      typeof candidate.overwrite === 'boolean' &&
      isBackupAccountRefs(candidate.accounts) &&
      Object.keys(candidate).every((key) =>
        ['version', 'requestId', 'operation', 'path', 'accounts', 'overwrite'].includes(key),
      )
    );
  }
  if (candidate.operation === 'account-backup-import') {
    return (
      safeTrayString(candidate.inspectionId, 128, false) &&
      isUniqueSafeStrings(candidate.entryIds, 64, 16) &&
      typeof candidate.overwriteConfirmed === 'boolean' &&
      Object.keys(candidate).every((key) =>
        [
          'version',
          'requestId',
          'operation',
          'inspectionId',
          'entryIds',
          'overwriteConfirmed',
        ].includes(key),
      )
    );
  }
  if (candidate.operation === 'account-backup-all') {
    return (
      safeTrayPath(candidate.path) &&
      Object.keys(candidate).every((key) =>
        ['version', 'requestId', 'operation', 'path'].includes(key),
      )
    );
  }
  if (candidate.operation === 'account-import' || candidate.operation === 'account-export') {
    const operationFields =
      candidate.operation === 'account-import'
        ? ['version', 'requestId', 'operation', 'providerId', 'name', 'path', 'overwrite']
        : ['version', 'requestId', 'operation', 'providerId', 'name', 'path'];
    return (
      safeTrayString(candidate.providerId, 64, false) &&
      safeTrayString(candidate.name, 128, false) &&
      safeTrayPath(candidate.path) &&
      (candidate.overwrite === undefined || typeof candidate.overwrite === 'boolean') &&
      Object.keys(candidate).every((key) => operationFields.includes(key))
    );
  }
  if (candidate.operation === 'account-login') {
    return (
      safeTrayString(candidate.providerId, 64, false) &&
      safeTrayString(candidate.methodId, 128, false) &&
      (candidate.sourceId === undefined || safeTrayString(candidate.sourceId, 64, false)) &&
      (candidate.name === undefined || safeTrayString(candidate.name, 128, false)) &&
      (candidate.label === undefined || safeTrayString(candidate.label, 160, true)) &&
      Object.keys(candidate).every((key) =>
        [
          'version',
          'requestId',
          'operation',
          'providerId',
          'sourceId',
          'methodId',
          'name',
          'label',
        ].includes(key),
      )
    );
  }
  if (candidate.operation === 'account-save-current') {
    return (
      safeTrayString(candidate.providerId, 64, false) &&
      (candidate.sourceId === undefined || safeTrayString(candidate.sourceId, 64, false)) &&
      (candidate.name === undefined || safeTrayString(candidate.name, 128, false)) &&
      Object.keys(candidate).every((key) =>
        ['version', 'requestId', 'operation', 'providerId', 'sourceId', 'name'].includes(key),
      )
    );
  }
  if (!safeTrayString(candidate.name, 128, false) || candidate.path !== undefined) {
    return false;
  }
  for (const [key, limit] of [
    ['providerId', 64],
    ['sourceId', 64],
    ['label', 160],
    ['endpoint', 2048],
    ['apiKey', 8192],
    ['region', 64],
    ['defaultModel', 256],
  ] as const) {
    if (candidate[key] !== undefined && !safeTrayString(candidate[key], limit, true)) {
      return false;
    }
  }
  if (candidate.overwrite !== undefined && typeof candidate.overwrite !== 'boolean') {
    return false;
  }
  if (candidate.enabled !== undefined && typeof candidate.enabled !== 'boolean') {
    return false;
  }
  const needsProvider =
    candidate.operation === 'account-detect' ||
    candidate.operation === 'account-save' ||
    candidate.operation === 'account-edit' ||
    candidate.operation === 'account-refresh' ||
    candidate.operation === 'account-remove' ||
    candidate.operation === 'account-clear' ||
    candidate.operation === 'gateway-create' ||
    candidate.operation === 'gateway-test' ||
    candidate.operation === 'hub-source-toggle';
  const isSetting = candidate.operation.startsWith('setting-');
  const needsEnabled = isSetting || candidate.operation === 'hub-source-toggle';
  return (
    (!needsProvider || safeTrayString(candidate.providerId, 64, false)) &&
    (!needsEnabled || typeof candidate.enabled === 'boolean')
  );
}

function isMutationOperation(value: unknown): value is TrayMutationOperation {
  return (
    value === 'account-detect' ||
    value === 'account-import' ||
    value === 'account-export' ||
    value === 'account-backup-all' ||
    value === 'account-backup-export' ||
    value === 'account-backup-import' ||
    value === 'account-login' ||
    value === 'account-save-current' ||
    value === 'account-save' ||
    value === 'account-edit' ||
    value === 'account-refresh' ||
    value === 'account-remove' ||
    value === 'account-clear' ||
    value === 'gateway-create' ||
    value === 'gateway-test' ||
    value === 'gateway-edit' ||
    value === 'gateway-refresh' ||
    value === 'gateway-remove' ||
    value === 'client-reset' ||
    value === 'hub-source-toggle' ||
    value === 'proxy-restart-all' ||
    value === 'proxy-stop-all' ||
    value === 'setting-launch-at-login' ||
    value === 'setting-auto-start-proxies' ||
    value === 'setting-show-quota' ||
    value === 'setting-quota-guard'
  );
}

function isBackupAccountRefs(value: unknown): value is TrayBackupAccountRef[] {
  if (!Array.isArray(value) || value.length === 0 || value.length > 64) {
    return false;
  }
  const seen = new Set<string>();
  return value.every((ref: unknown) => {
    if (ref === null || typeof ref !== 'object' || Array.isArray(ref)) {
      return false;
    }
    const candidate = ref as Record<string, unknown>;
    if (
      Object.keys(candidate).length !== 2 ||
      Object.keys(candidate).some((key) => !['provider', 'name'].includes(key)) ||
      !safeTrayString(candidate.provider, 64, false) ||
      !safeTrayString(candidate.name, 128, false)
    ) {
      return false;
    }
    const key = `${candidate.provider}\0${candidate.name}`;
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}

function isUniqueSafeStrings(
  value: unknown,
  maxItems: number,
  maxLength: number,
): value is string[] {
  if (!Array.isArray(value) || value.length === 0 || value.length > maxItems) {
    return false;
  }
  const seen = new Set<string>();
  return value.every((item: unknown) => {
    if (!safeTrayString(item, maxLength, false) || seen.has(item)) {
      return false;
    }
    seen.add(item);
    return true;
  });
}

function safeTrayPath(value: unknown): value is string {
  return safeTrayString(value, 4096, false) && !/\p{Cc}/u.test(value);
}

function safeTrayString(value: unknown, maxLength: number, allowEmpty: boolean): value is string {
  return (
    typeof value === 'string' &&
    (allowEmpty || value.length > 0) &&
    value.length <= maxLength &&
    !value.includes(String.fromCharCode(0)) &&
    !value.includes('\n') &&
    !value.includes('\r')
  );
}

function isTrayInvokeCommand(value: unknown): value is TrayInvokeCommand {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }

  const candidate = value as Record<string, unknown>;
  return (
    candidate.version === TRAY_PROTOCOL_VERSION &&
    typeof candidate.requestId === 'string' &&
    candidate.requestId.length > 0 &&
    candidate.requestId.length <= 128 &&
    typeof candidate.revision === 'number' &&
    Number.isSafeInteger(candidate.revision) &&
    candidate.revision >= 0 &&
    typeof candidate.actionId === 'string' &&
    candidate.actionId.length > 0 &&
    candidate.actionId.length <= 128
  );
}

function isTrayApplyModelRolesCommand(value: unknown): value is TrayApplyModelRolesCommand {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return false;
  }
  const candidate = value as Record<string, unknown>;
  if (
    Object.keys(candidate).some(
      (key) => !['version', 'requestId', 'revision', 'clientId', 'roleActionIds'].includes(key),
    ) ||
    candidate.version !== TRAY_PROTOCOL_VERSION ||
    !safeTrayString(candidate.requestId, 128, false) ||
    !safeTrayString(candidate.clientId, 64, false) ||
    typeof candidate.revision !== 'number' ||
    !Number.isSafeInteger(candidate.revision) ||
    candidate.revision < 0 ||
    candidate.roleActionIds === null ||
    typeof candidate.roleActionIds !== 'object' ||
    Array.isArray(candidate.roleActionIds)
  ) {
    return false;
  }
  const entries = Object.entries(candidate.roleActionIds as Record<string, unknown>);
  return (
    entries.length >= 1 &&
    entries.length <= 16 &&
    entries.some(([roleId]) => roleId === 'default') &&
    entries.every(
      ([roleId, actionId]) =>
        safeTrayString(roleId, 64, false) && safeTrayString(actionId, 128, false),
    )
  );
}
